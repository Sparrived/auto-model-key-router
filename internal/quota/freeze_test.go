package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 本文件钉住冻结表的行为契约：到期自动解除、首次 FrozenAt 保留、手工解冻的抑制窗口、
// 失效清理、落盘与重载、损坏文件按空表、以及并发访问安全。

// testClock 是可控时钟；带锁以便并发用例直接共用（-race 下也干净）。
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

func testNow() time.Time {
	return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
}

// TestStoreUpsertGetExpiry 钉住「观测到冻结 → 到期自动解除」这条主路径。
func TestStoreUpsertGetExpiry(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)

	changed, err := store.Upsert(Freeze{
		ProviderID:  "cpa-claude",
		KeyName:     "claude-pro",
		Vendor:      "Claude",
		Scope:       "cpa_instance",
		Source:      "account_resource",
		Account:     "ops@example.com",
		InstanceID:  "jp-cpa-claude",
		WindowKey:   "claude/5h",
		WindowLabel: "5 小时",
		Remaining:   0,
		Status:      "rejected",
		ResetAt:     now.Add(3 * time.Hour),
		ExpiresAt:   now.Add(3*time.Hour + time.Minute),
		Reason:      "5 小时窗口已用尽（剩余 0%）",
	})
	if err != nil || !changed {
		t.Fatalf("首次 Upsert 应新增记录: changed=%v err=%v", changed, err)
	}

	freeze, found := store.Get("cpa-claude", "claude-pro")
	if !found {
		t.Fatal("刚写入的冻结应可读取")
	}
	if !freeze.FrozenAt.Equal(now) || freeze.Reason == "" {
		t.Errorf("FrozenAt 应自动取当前时刻: %+v", freeze)
	}
	if !store.IsFrozen("cpa-claude", "claude-pro") || store.FrozenReason("cpa-claude", "claude-pro") == "" {
		t.Error("IsFrozen / FrozenReason 与 Get 应一致")
	}
	if got := store.Snapshot(); len(got) != 1 || got[0].KeyName != "claude-pro" {
		t.Errorf("Snapshot 应返回 1 条: %+v", got)
	}

	// 到期即解除：ExpiresAt 是边界，恰好到点就不再算冻结。
	clock.Advance(3*time.Hour + time.Minute)
	if _, found := store.Get("cpa-claude", "claude-pro"); found {
		t.Error("到期后 Get 不应再返回冻结")
	}
	if store.IsFrozen("cpa-claude", "claude-pro") {
		t.Error("到期后 IsFrozen 应为 false")
	}
	if got := store.Snapshot(); len(got) != 0 {
		t.Errorf("到期后 Snapshot 应为空: %+v", got)
	}
	if got := store.FrozenReason("cpa-claude", "claude-pro"); got != "" {
		t.Errorf("到期后 FrozenReason 应为空，实际 %q", got)
	}
}

// TestStoreUpsertRefreshesAndPreservesFrozenAt 钉住「只保留一条 + 保留首次 FrozenAt」。
func TestStoreUpsertRefreshesAndPreservesFrozenAt(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)

	if _, err := store.Upsert(Freeze{
		ProviderID: "p", KeyName: "k",
		ExpiresAt: now.Add(10 * time.Minute),
		Remaining: 0,
		Reason:    "首次",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	clock.Advance(5 * time.Minute)

	changed, err := store.Upsert(Freeze{
		ProviderID: "p", KeyName: "k",
		ExpiresAt: clock.Now().Add(30 * time.Minute),
		Remaining: 0.1,
		Reason:    "刷新",
	})
	if err != nil || !changed {
		t.Fatalf("窗口刷新应算变化: changed=%v err=%v", changed, err)
	}
	freeze, _ := store.Get("p", "k")
	if !freeze.FrozenAt.Equal(now) {
		t.Errorf("刷新必须保留首次 FrozenAt=%v，实际 %v", now, freeze.FrozenAt)
	}
	if freeze.Reason != "刷新" || freeze.Remaining != 0.1 {
		t.Errorf("窗口字段应随刷新更新: %+v", freeze)
	}
	if !freeze.ExpiresAt.Equal(clock.Now().Add(30 * time.Minute)) {
		t.Errorf("ExpiresAt 应随刷新更新: %+v", freeze)
	}

	// 完全相同的记录再写一次不产生变化（调用方据此决定是否打日志）。
	changed, err = store.Upsert(freeze)
	if err != nil || changed {
		t.Errorf("内容未变时 Upsert 应返回 false: changed=%v err=%v", changed, err)
	}
}

// TestStoreUpsertFrozenAtAcrossEpisodes 钉住「自 X 起冻结」的时间口径。
//
// 仍生效的旧记录沿用首次 FrozenAt（每轮巡检刷新不该把起始时间往后推）；旧记录已
// 过期后再冻上属于新一次冻结，FrozenAt 必须从这次算起——评估链路每轮末尾才 Prune，
// 过期记录可能还留在表里，不区分就会显示出很久以前的起始时间。
func TestStoreUpsertFrozenAtAcrossEpisodes(t *testing.T) {
	t.Run("未过期刷新保留首次时刻", func(t *testing.T) {
		now := testNow()
		clock := newTestClock(now)
		store := NewStoreWithClock("", clock.Now)
		if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(10 * time.Minute)}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		clock.Advance(5 * time.Minute)
		if _, err := store.Upsert(Freeze{
			ProviderID: "p", KeyName: "k",
			ExpiresAt: clock.Now().Add(10 * time.Minute),
			Reason:    "刷新",
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		freeze, found := store.Get("p", "k")
		if !found {
			t.Fatal("应仍处于冻结")
		}
		if !freeze.FrozenAt.Equal(now) {
			t.Errorf("未过期刷新应保留首次 FrozenAt=%v，实际 %v", now, freeze.FrozenAt)
		}
	})

	t.Run("过期重冻从新时刻算起", func(t *testing.T) {
		now := testNow()
		clock := newTestClock(now)
		store := NewStoreWithClock("", clock.Now)
		if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(time.Minute)}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		clock.Advance(2 * time.Minute)
		retriggered := clock.Now()
		changed, err := store.Upsert(Freeze{
			ProviderID: "p", KeyName: "k",
			ExpiresAt: retriggered.Add(time.Hour),
			Reason:    "再次耗尽",
		})
		if err != nil || !changed {
			t.Fatalf("过期后重冻应算变化: changed=%v err=%v", changed, err)
		}
		freeze, found := store.Get("p", "k")
		if !found {
			t.Fatal("应重新冻结")
		}
		if !freeze.FrozenAt.Equal(retriggered) {
			t.Errorf("过期重冻的 FrozenAt 应为本次时刻 %v，实际 %v", retriggered, freeze.FrozenAt)
		}
		if !freeze.ExpiresAt.Equal(retriggered.Add(time.Hour)) {
			t.Errorf("过期重冻的 ExpiresAt 应使用新记录: %+v", freeze)
		}
	})

	t.Run("过期重冻保留调用方给的 FrozenAt", func(t *testing.T) {
		now := testNow()
		clock := newTestClock(now)
		store := NewStoreWithClock("", clock.Now)
		if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(time.Minute)}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		clock.Advance(2 * time.Minute)
		explicit := clock.Now().Add(-30 * time.Second)
		if _, err := store.Upsert(Freeze{
			ProviderID: "p", KeyName: "k", FrozenAt: explicit,
			ExpiresAt: clock.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		freeze, found := store.Get("p", "k")
		if !found {
			t.Fatal("应重新冻结")
		}
		if !freeze.FrozenAt.Equal(explicit) {
			t.Errorf("过期重冻应使用调用方给的 FrozenAt=%v，实际 %v", explicit, freeze.FrozenAt)
		}
	})

	t.Run("到期边界视为过期", func(t *testing.T) {
		now := testNow()
		clock := newTestClock(now)
		store := NewStoreWithClock("", clock.Now)
		expires := now.Add(time.Minute)
		if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: expires}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// now == ExpiresAt：Get/IsFrozen 已视为不冻结，重冻必须是一次新冻结。
		clock.Advance(time.Minute)
		if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: clock.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		freeze, found := store.Get("p", "k")
		if !found {
			t.Fatal("应重新冻结")
		}
		if !freeze.FrozenAt.Equal(expires) {
			t.Errorf("恰好到期后重冻的 FrozenAt 应为当前时刻 %v，实际 %v", expires, freeze.FrozenAt)
		}
	})
}

// TestStoreUpsertValidation 钉住缺主键与去空白。
func TestStoreUpsertValidation(t *testing.T) {
	store := NewStore("")
	if _, err := store.Upsert(Freeze{KeyName: "k"}); err == nil {
		t.Error("缺 provider_id 应报错")
	}
	if _, err := store.Upsert(Freeze{ProviderID: "p"}); err == nil {
		t.Error("缺 key_name 应报错")
	}
	if _, err := store.Upsert(Freeze{ProviderID: " p ", KeyName: " k "}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !store.IsFrozen("p", "k") {
		t.Error("主键两侧空白应被去掉")
	}
}

// TestStoreFallbackExpiry 钉住「拿不到 reset_at 时 10 分钟自动解除」。
func TestStoreFallbackExpiry(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)

	if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	freeze, found := store.Get("p", "k")
	if !found {
		t.Fatal("应已冻结")
	}
	wantExpires := now.Add(fallbackFreezeTTL)
	if !freeze.ExpiresAt.Equal(wantExpires) || !freeze.FrozenAt.Equal(now) {
		t.Errorf("缺省到期时间应为 now+10m: %+v", freeze)
	}
	clock.Advance(fallbackFreezeTTL)
	if store.IsFrozen("p", "k") {
		t.Error("10 分钟兜底到期后应自动解除")
	}
	if got := SuppressionFloor(); got != 10*time.Minute {
		t.Errorf("手工解冻抑制下限应为 10 分钟，实际 %v", got)
	}
}

// TestStoreReleaseSuppressesRefreeze 钉住手工解冻的抑制窗口：窗口内观测到耗尽也不冻。
func TestStoreReleaseSuppressesRefreeze(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)
	if _, err := store.Upsert(Freeze{
		ProviderID: "p", KeyName: "k",
		ExpiresAt: now.Add(3 * time.Hour),
		Reason:    "5 小时窗口已用尽",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	suppressUntil := now.Add(10 * time.Minute)
	released, found, err := store.Release("p", "k", suppressUntil)
	if err != nil || !found {
		t.Fatalf("Release: found=%v err=%v", found, err)
	}
	if released.Reason != "5 小时窗口已用尽" {
		t.Errorf("Release 应返回被解除的记录: %+v", released)
	}
	if store.IsFrozen("p", "k") {
		t.Error("解冻后不应再冻结")
	}
	if !store.Suppressed("p", "k") {
		t.Error("解冻后应进入抑制窗口")
	}
	if until, ok := store.SuppressionUntil("p", "k"); !ok || !until.Equal(suppressUntil) {
		t.Errorf("抑制截止时间不对: %v ok=%v", until, ok)
	}

	// 抑制窗口内即使又观测到耗尽也不冻。
	changed, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(3 * time.Hour)})
	if err != nil || changed {
		t.Errorf("抑制窗口内 Upsert 应被忽略: changed=%v err=%v", changed, err)
	}
	if store.IsFrozen("p", "k") {
		t.Error("抑制窗口内不应冻结")
	}

	// 抑制窗口到期后恢复自动冻结。
	clock.Advance(10 * time.Minute)
	if store.Suppressed("p", "k") {
		t.Error("抑制窗口到期后 Suppressed 应为 false")
	}
	changed, err = store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: clock.Now().Add(3 * time.Hour)})
	if err != nil || !changed {
		t.Errorf("抑制窗口到期后应能重新冻结: changed=%v err=%v", changed, err)
	}
	if !store.IsFrozen("p", "k") {
		t.Error("抑制窗口到期后应重新冻结")
	}
}

// TestStoreReleaseUnknownHasNoSideEffects 钉住「不存在的冻结不写任何状态」。
//
// 曾经的实现会无条件写抑制窗口：对没冻结的 Key 点一次解冻，就会把接下来 10 分钟里
// 每一次真实的额度耗尽都压掉；第二次 DELETE 还会把已有的抑制窗口改长。
func TestStoreReleaseUnknownHasNoSideEffects(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)

	if _, found, err := store.Release("p", "k", now.Add(10*time.Minute)); found || err != nil {
		t.Fatalf("不存在的冻结应返回 found=false: found=%v err=%v", found, err)
	}
	if store.Suppressed("p", "k") {
		t.Fatal("不存在的冻结不能留下抑制窗口")
	}
	if changed, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(time.Hour)}); err != nil || !changed {
		t.Errorf("之后观测到耗尽应能正常冻结: changed=%v err=%v", changed, err)
	}

	// 同一把 Key 的第二次 DELETE（此时已无冻结）不得改写已有的抑制窗口。
	first := now.Add(10 * time.Minute)
	if _, found, _ := store.Release("p", "k", first); !found {
		t.Fatal("第一次 DELETE 应成功")
	}
	clock.Advance(time.Minute)
	if _, found, _ := store.Release("p", "k", clock.Now().Add(20*time.Minute)); found {
		t.Fatal("第二次 DELETE 应返回 found=false")
	}
	if until, ok := store.SuppressionUntil("p", "k"); !ok || !until.Equal(first) {
		t.Errorf("第二次 DELETE 不应改写抑制窗口: until=%v ok=%v，期望 %v", until, ok, first)
	}
}

// TestStoreReleaseExpiredFreezeIsNotFound 钉住「已过期的冻结不算当前冻结」。
func TestStoreReleaseExpiredFreezeIsNotFound(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)
	if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, found, err := store.Release("p", "k", clock.Now().Add(10*time.Minute)); found || err != nil {
		t.Errorf("过期冻结不算当前冻结: found=%v err=%v", found, err)
	}
	if store.Suppressed("p", "k") {
		t.Error("对过期冻结解冻不应留下抑制窗口")
	}
}

// TestStorePrune 钉住失效清理：到期、配置里已不存在、已过期的抑制。
func TestStorePrune(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	store := NewStoreWithClock("", clock.Now)
	for _, freeze := range []Freeze{
		{ProviderID: "p", KeyName: "a", ExpiresAt: now.Add(-time.Minute)},
		{ProviderID: "p", KeyName: "b", ExpiresAt: now.Add(time.Hour)},
		{ProviderID: "p", KeyName: "c", ExpiresAt: now.Add(time.Hour)},
	} {
		if _, err := store.Upsert(freeze); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	removed, err := store.Prune(func(providerID, keyName string) bool { return keyName != "c" })
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 || removed[0].KeyName != "a" || removed[1].KeyName != "c" {
		t.Errorf("应清掉到期与失效的冻结（按 provider/key 排序）: %+v", removed)
	}
	if snapshot := store.Snapshot(); len(snapshot) != 1 || snapshot[0].KeyName != "b" {
		t.Errorf("清理后只应剩 b: %+v", snapshot)
	}
	if removed, err := store.Prune(nil); err != nil || len(removed) != 0 {
		t.Errorf("没有可清理项时应返回空: removed=%+v err=%v", removed, err)
	}
}

// TestStorePrunePersistsSuppressionCleanup 钉住「只清过期抑制时也要落盘」。
//
// 否则重启后那条本该失效的抑制会从文件里复活，把下一次合法冻结又压住。
func TestStorePrunePersistsSuppressionCleanup(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	path := filepath.Join(t.TempDir(), "quota-freezes.json")
	store := NewStoreWithClock(path, clock.Now)
	if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "k", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, found, err := store.Release("p", "k", now.Add(10*time.Minute)); err != nil || !found {
		t.Fatalf("Release: found=%v err=%v", found, err)
	}

	clock.Advance(11 * time.Minute)
	removed, err := store.Prune(nil)
	if err != nil || len(removed) != 0 {
		t.Fatalf("只剩过期抑制可清: removed=%+v err=%v", removed, err)
	}

	// 关键：文件里也不能再有这条过期抑制（否则重载后它会从文件里复活）。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var file freezeFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("状态文件不是合法 JSON: %v", err)
	}
	if len(file.Suppressions) != 0 {
		t.Errorf("过期抑制必须已落盘清理，文件里仍有 %+v", file.Suppressions)
	}

	reloaded := NewStoreWithClock(path, clock.Now)
	if reloaded.Suppressed("p", "k") {
		t.Error("过期抑制清理后重载不应再处于抑制窗口")
	}
}

// TestStorePersistenceRoundTrip 钉住落盘格式与跨实例重载。
func TestStorePersistenceRoundTrip(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "quota-freezes.json")
	store := NewStoreWithClock(path, clock.Now)

	records := []Freeze{
		{
			ProviderID: "z-provider", KeyName: "a-key", Vendor: "Claude", Scope: "cpa_instance",
			Source: "account_resource", Account: "ops@example.com", InstanceID: "jp-1",
			WindowKey: "claude/5h", WindowLabel: "5 小时", Remaining: 0, Status: "rejected",
			ResetAt:   now.Add(3 * time.Hour),
			ExpiresAt: now.Add(3*time.Hour + time.Minute),
			Reason:    "5 小时窗口已用尽（剩余 0%）",
		},
		{ProviderID: "a-provider", KeyName: "z-key", WindowKey: "codex/primary", WindowLabel: "主要窗口", Remaining: 0, ExpiresAt: now.Add(time.Hour), Reason: "主要窗口已用尽"},
		{ProviderID: "a-provider", KeyName: "a-key", WindowKey: "codex/secondary", WindowLabel: "次要窗口", Remaining: 0, ExpiresAt: now.Add(2 * time.Hour), Reason: "次要窗口已用尽"},
	}
	for _, freeze := range records {
		if _, err := store.Upsert(freeze); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	// 另造一条手工解冻的抑制记录，一并验证持久化。
	if _, err := store.Upsert(Freeze{ProviderID: "p", KeyName: "released", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	suppressUntil := now.Add(10 * time.Minute)
	if _, found, err := store.Release("p", "released", suppressUntil); err != nil || !found {
		t.Fatalf("Release: found=%v err=%v", found, err)
	}

	reloaded := NewStoreWithClock(path, clock.Now)
	snapshot := reloaded.Snapshot()
	if len(snapshot) != 3 {
		t.Fatalf("重载后应有 3 条冻结，实际 %d（%+v）", len(snapshot), snapshot)
	}
	wantOrder := []string{"a-provider/a-key", "a-provider/z-key", "z-provider/a-key"}
	for index, freeze := range snapshot {
		if got := freeze.ProviderID + "/" + freeze.KeyName; got != wantOrder[index] {
			t.Errorf("Snapshot 排序不对: 第 %d 条是 %s，期望 %s", index, got, wantOrder[index])
		}
	}
	got, found := reloaded.Get("z-provider", "a-key")
	if !found {
		t.Fatal("重载后应能按主键读取")
	}
	if got.Vendor != "Claude" || got.Scope != "cpa_instance" || got.Account != "ops@example.com" ||
		got.InstanceID != "jp-1" || got.WindowKey != "claude/5h" || got.WindowLabel != "5 小时" ||
		got.Status != "rejected" || got.Reason != "5 小时窗口已用尽（剩余 0%）" {
		t.Errorf("重载后字段丢失: %+v", got)
	}
	if !got.ResetAt.Equal(now.Add(3*time.Hour)) || !got.FrozenAt.Equal(now) || !got.ExpiresAt.Equal(now.Add(3*time.Hour+time.Minute)) {
		t.Errorf("重载后时间字段不对: %+v", got)
	}
	if reloaded.IsFrozen("p", "released") {
		t.Error("已解冻的 Key 不应被加载成冻结")
	}
	if until, ok := reloaded.SuppressionUntil("p", "released"); !ok || !until.Equal(suppressUntil) {
		t.Errorf("抑制窗口未持久化: until=%v ok=%v", until, ok)
	}

	// 落盘是一次原子 rename：目录里不该留下临时文件。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "quota-freezes.json" {
		t.Errorf("状态目录里应只有一个文件，实际 %v", entries)
	}

	// 文件形状：version=1，冻结与抑制都在。
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var file freezeFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("状态文件不是合法 JSON: %v", err)
	}
	if file.Version != freezeVersion || len(file.Freezes) != 3 || len(file.Suppressions) != 1 {
		t.Errorf("状态文件形状不对: %+v", file)
	}
	if file.UpdatedAt == "" {
		t.Error("状态文件应带 updated_at")
	}
}

// TestStoreLoadCorruptAndMissing 钉住「文件缺失 / 损坏 / 版本不认识 一律按空表」。
func TestStoreLoadCorruptAndMissing(t *testing.T) {
	now := testNow()
	clock := newTestClock(now)
	dir := t.TempDir()

	missing := NewStoreWithClock(filepath.Join(dir, "missing.json"), clock.Now)
	if snapshot := missing.Snapshot(); len(snapshot) != 0 {
		t.Errorf("文件缺失应按空表: %+v", snapshot)
	}

	corruptPath := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	corrupt := NewStoreWithClock(corruptPath, clock.Now)
	if snapshot := corrupt.Snapshot(); len(snapshot) != 0 {
		t.Errorf("损坏文件应按空表: %+v", snapshot)
	}

	versionPath := filepath.Join(dir, "version.json")
	if err := os.WriteFile(versionPath, []byte(`{"version":99,"freezes":[{"provider_id":"p","key_name":"k","expires_at":"2999-01-01T00:00:00Z"}],"suppressions":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	versioned := NewStoreWithClock(versionPath, clock.Now)
	if snapshot := versioned.Snapshot(); len(snapshot) != 0 {
		t.Errorf("不认识的版本应按空表: %+v", snapshot)
	}

	// 单条记录坏掉只丢那一条，不连累同文件里正常的记录。
	partialPath := filepath.Join(dir, "partial.json")
	partial := `{"version":1,"freezes":[` +
		`{"provider_id":"p","key_name":"k","expires_at":"2999-01-01T00:00:00Z"},` +
		`{"provider_id":"p2","expires_at":"2999-01-01T00:00:00Z"}],` +
		`"suppressions":[` +
		`{"provider_id":"p","key_name":"k","until":"not-a-time"},` +
		`{"provider_id":"p3","key_name":"k3","until":"2999-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(partialPath, []byte(partial), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded := NewStoreWithClock(partialPath, clock.Now)
	if snapshot := loaded.Snapshot(); len(snapshot) != 1 || snapshot[0].KeyName != "k" {
		t.Errorf("坏记录应被跳过、好记录应保留: %+v", snapshot)
	}
	if loaded.Suppressed("p", "k") {
		t.Error("非法抑制时间应被跳过")
	}
	if !loaded.Suppressed("p3", "k3") {
		t.Error("合法抑制记录应被加载")
	}
}

// TestStoreConcurrentAccess 用并发读写把 Store 钉在 -race 下跑一遍。
//
// 落盘与清理同时发生也不能写出半个 JSON、不能留下临时文件；最终文件必须能重载。
func TestStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "quota-freezes.json")
	store := NewStore(path)

	const workers = 8
	const rounds = 25
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			key := fmt.Sprintf("key-%d", worker)
			for round := 0; round < rounds; round++ {
				if _, err := store.Upsert(Freeze{
					ProviderID: "p", KeyName: key,
					ExpiresAt: time.Now().Add(time.Hour),
					Reason:    "并发用例",
				}); err != nil {
					t.Errorf("并发 Upsert 失败: %v", err)
					return
				}
				_ = store.IsFrozen("p", key)
				_ = store.FrozenReason("p", key)
				_ = store.Suppressed("p", key)
				_ = store.Snapshot()
				if round%5 == 0 {
					_, _, _ = store.Release("p", key, time.Now().Add(time.Minute))
				}
				if round%7 == 0 {
					_, _ = store.Prune(nil)
				}
			}
		}(worker)
	}
	wait.Wait()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("并发落盘后文件应存在: %v", err)
	}
	var file freezeFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("并发落盘写出了坏 JSON: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("并发落盘不应留下临时文件: %v", entries)
	}
	reloaded := NewStore(path)
	if snapshot := reloaded.Snapshot(); len(snapshot) > workers {
		t.Errorf("重载后冻结条数异常: %d", len(snapshot))
	}
}
