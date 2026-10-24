package metrics

import (
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// recordAccessKey 写入一行带访问密钥归属的指标。
//
// 与 recordWorkspace 的差别有两点，都是访问密钥的真实形态：
//   - CallerType 是 "access_key"（这才是会被写进 request_metrics 的档位）；
//   - 归属走 AccessKeyID 而不是 Workspace，且两者**同时**给出——访问密钥不绑定
//     工作空间，它的请求照常带 X-AMKR-Workspace（归一化后通常是 default）。
func recordAccessKey(t *testing.T, store *Store, accessKeyID, workspace, modelID string,
	provider *string, promptTokens int64) {
	t.Helper()
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(promptTokens)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
	)
	if err := store.Record(RecordParams{
		ModelID:     modelID,
		KeyName:     "upstream-key-a",
		Usage:       usage,
		CallerType:  "access_key",
		ProviderID:  provider,
		Workspace:   workspace,
		AccessKeyID: accessKeyID,
	}); err != nil {
		t.Fatalf("写入访问密钥指标 (%s/%s): %v", accessKeyID, modelID, err)
	}
}

func TestAccessKeyLastUsed(t *testing.T) {
	store := tempStore(t)
	lastUsed, err := store.AccessKeyLastUsed()
	if err != nil || len(lastUsed) != 0 {
		t.Fatalf("空库最近调用 = %v, err=%v", lastUsed, err)
	}

	start := beijingTime(t, "2026-10-12T12:00:00+08:00")
	for _, entry := range []struct {
		id     string
		offset time.Duration
		failed bool
	}{
		{"trial", 0, false},
		{"other", time.Hour, false},
		{"trial", 2 * time.Hour, false},
		{"trial", 2*time.Hour + time.Microsecond, true},
		{"", 3 * time.Hour, false},
	} {
		installClock(t, start.Add(entry.offset))
		if err := store.Record(RecordParams{
			ModelID: "model", KeyName: "key", AccessKeyID: entry.id, Failed: entry.failed,
		}); err != nil {
			t.Fatal(err)
		}
	}
	installClock(t, start.Add(48*time.Hour))
	lastUsed, err = store.AccessKeyLastUsed()
	if err != nil {
		t.Fatal(err)
	}
	if len(lastUsed) != 2 || lastUsed["trial"] != formatISO(start.Add(2*time.Hour+time.Microsecond)) ||
		lastUsed["other"] != formatISO(start.Add(time.Hour)) {
		t.Fatalf("最近调用 = %v，必须隔离密钥、包含失败与窗口外记录，并取同秒内最后一次写入", lastUsed)
	}
}

// TestAccessKeyUsageIsolatesKeys 是本功能的**核心断言**：两把访问密钥的用量必须
// 互不串味。
//
// 这一条锁住的正是 request_access_key 旁挂表存在的理由：caller_type 只能说明
// 「来自访问密钥」，说不出哪一把，而访问密钥是按人分发的。若归属丢了（或 JOIN 写错
// 成按 workspace），两把 key 会显示同一个数字——看板因此会告诉这个人别人用了多少。
// 两把 key 在这里刻意用**同一个工作空间**：这是访问密钥的常态（都不带工作空间头，
// 全都归一化成 default），也正因如此工作空间归属替代不了它。
func TestAccessKeyUsageIsolatesKeys(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)

	openai, gpt4o := "openai", "gpt-4o"
	deepseek := "deepseek"

	// trial：两条，同一工作空间 default，落在 openai/gpt-4o。
	recordAccessKey(t, store, "trial", "default", "gpt-4o", &openai, 100)
	recordAccessKey(t, store, "trial", "default", "gpt-4o", &openai, 50)
	// other：一条，**同一个工作空间**，落在 deepseek/deepseek-v3。
	recordAccessKey(t, store, "other", "default", "deepseek-v3", &deepseek, 30)
	// 完整权限的一条（无访问密钥归属）：不能出现在任一访问密钥里。
	recordWorkspace(t, store, "default", "gpt-4o", "gpt-4o", &openai, &gpt4o, 7)

	value, err := store.AccessKeyUsage(AccessKeyUsageParams{AccessKeyID: "trial"})
	if err != nil {
		t.Fatalf("AccessKeyUsage: %v", err)
	}
	if id, _ := value.Obj.Get("access_key_id"); id.Str != "trial" {
		t.Errorf("access_key_id = %q, 期望 trial", id.Str)
	}
	stats, _ := value.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 2 {
		t.Errorf("trial requests = %d, 期望 2（不得把 other 或完整权限算进来）", got)
	}
	if got := statInt(t, stats, "total_tokens"); got != 150 {
		t.Errorf("trial total_tokens = %d, 期望 150", got)
	}

	// 按模型拆分：只有 gpt-4o 一项，且没有别的 key 的模型混进来。
	// dimensions 的键是原始列名（model_id / provider_id），与 workspace.go 的 layers 同一约定。
	dimensions, _ := value.Obj.Get("dimensions")
	models, _ := dimensions.Obj.Get("model_id")
	if keys := models.Obj.Keys(); len(keys) != 1 || keys[0] != "gpt-4o" {
		t.Fatalf("trial 的模型拆分 = %v, 期望仅 [gpt-4o]", keys)
	}
	modelStats, _ := models.Obj.Get("gpt-4o")
	if got := statInt(t, modelStats, "requests"); got != 2 {
		t.Errorf("gpt-4o requests = %d, 期望 2", got)
	}
	providers, _ := dimensions.Obj.Get("provider_id")
	if keys := providers.Obj.Keys(); len(keys) != 1 || keys[0] != "openai" {
		t.Fatalf("trial 的供应商拆分 = %v, 期望仅 [openai]", keys)
	}

	// 最近明细只含 trial 的两条。
	recent, _ := value.Obj.Get("recent_requests")
	if len(recent.Arr) != 2 {
		t.Errorf("trial 最近明细 = %d 条, 期望 2", len(recent.Arr))
	}

	// 反向确认：另一把 key 只看到自己的那一条。
	otherValue, err := store.AccessKeyUsage(AccessKeyUsageParams{AccessKeyID: "other"})
	if err != nil {
		t.Fatalf("AccessKeyUsage(other): %v", err)
	}
	otherStats, _ := otherValue.Obj.Get("stats")
	if got := statInt(t, otherStats, "requests"); got != 1 {
		t.Errorf("other requests = %d, 期望 1", got)
	}
	if got := statInt(t, otherStats, "total_tokens"); got != 30 {
		t.Errorf("other total_tokens = %d, 期望 30", got)
	}
}

// TestAccessKeyUsageEmptyForUnknownKey 固化：没有流量的 key 拿到零值而不是报错。
//
// 「刚创建、还没人用」是访问密钥最常见的初始状态，界面必须能显示一个 0 而不是
// 一片错误。同时确认零值统计里的字段齐全（前端直接读字段，缺键会渲染成 undefined）。
func TestAccessKeyUsageEmptyForUnknownKey(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	provider := "openai"
	recordAccessKey(t, store, "trial", "default", "gpt-4o", &provider, 10)

	value, err := store.AccessKeyUsage(AccessKeyUsageParams{AccessKeyID: "brand-new"})
	if err != nil {
		t.Fatalf("AccessKeyUsage: %v", err)
	}
	stats, _ := value.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 0 {
		t.Errorf("未知 key 的 requests = %d, 期望 0", got)
	}
	if got := statInt(t, stats, "total_tokens"); got != 0 {
		t.Errorf("未知 key 的 total_tokens = %d, 期望 0", got)
	}
	dimensions, _ := value.Obj.Get("dimensions")
	models, _ := dimensions.Obj.Get("model_id")
	if len(models.Obj.Keys()) != 0 {
		t.Errorf("未知 key 的模型拆分 = %v, 期望空", models.Obj.Keys())
	}
	recent, _ := value.Obj.Get("recent_requests")
	if len(recent.Arr) != 0 {
		t.Errorf("未知 key 的最近明细 = %d 条, 期望 0", len(recent.Arr))
	}
	// 窗口必须回显，界面靠它渲染「统计范围」。
	if _, ok := value.Obj.Get("window"); !ok {
		t.Error("响应缺少 window")
	}
}

// TestAccessKeyUsageWindowScopesEverything 断言时间窗口对统计、拆分与明细同时生效。
//
// 三者共用同一个窗口：分开取会让界面上的合计与拆分后的行在窗口边界处对不上。
func TestAccessKeyUsageWindowScopesEverything(t *testing.T) {
	now := beijingTime(t, "2026-07-14T12:00:00+08:00")
	installClock(t, now)
	store := tempStore(t)
	provider := "openai"
	recordAccessKey(t, store, "trial", "default", "gpt-4o", &provider, 100)

	// 把时钟拨到 2 小时后：1 小时窗口内应当什么都看不到。
	installClock(t, now.Add(2*time.Hour))
	hours := 1.0
	value, err := store.AccessKeyUsage(AccessKeyUsageParams{Hours: &hours, AccessKeyID: "trial"})
	if err != nil {
		t.Fatalf("AccessKeyUsage: %v", err)
	}
	stats, _ := value.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 0 {
		t.Errorf("窗口外 requests = %d, 期望 0", got)
	}
	dimensions, _ := value.Obj.Get("dimensions")
	models, _ := dimensions.Obj.Get("model_id")
	if len(models.Obj.Keys()) != 0 {
		t.Errorf("窗口外模型拆分 = %v, 期望空", models.Obj.Keys())
	}
	recent, _ := value.Obj.Get("recent_requests")
	if len(recent.Arr) != 0 {
		t.Errorf("窗口外最近明细 = %d 条, 期望 0", len(recent.Arr))
	}

	// 24 小时窗口能重新看到它，确认不是数据被删了而是窗口在起作用。
	day := 24.0
	value, err = store.AccessKeyUsage(AccessKeyUsageParams{Hours: &day, AccessKeyID: "trial"})
	if err != nil {
		t.Fatalf("AccessKeyUsage(24h): %v", err)
	}
	stats, _ = value.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 1 {
		t.Errorf("24 小时窗口内 requests = %d, 期望 1", got)
	}
}

// TestAccessKeyUsageRejectsNonPositiveHours 断言参数校验沿用同一套错误文本。
func TestAccessKeyUsageRejectsNonPositiveHours(t *testing.T) {
	store := tempStore(t)
	hours := 0.0
	if _, err := store.AccessKeyUsage(AccessKeyUsageParams{Hours: &hours, AccessKeyID: "trial"}); err == nil {
		t.Fatal("hours=0 应当报错")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("错误类型 = %T, 期望 *ValidationError", err)
	}
}

// TestAccessKeyRecentRespectsLimit 断言最近明细的条数上限被真正应用。
//
// 默认 50 是为了「一眼扫过」，上限 200 与 request_history 对齐。若 LIMIT 根本没拼进
// SQL（或参数没传），这里会拿到全部 5 条而不是 2 条。
func TestAccessKeyRecentRespectsLimit(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	provider := "openai"
	for index := 0; index < 5; index++ {
		recordAccessKey(t, store, "trial", "default", "gpt-4o", &provider, 10)
	}
	value, err := store.AccessKeyUsage(AccessKeyUsageParams{AccessKeyID: "trial", RecentLimit: 2})
	if err != nil {
		t.Fatalf("AccessKeyUsage: %v", err)
	}
	recent, _ := value.Obj.Get("recent_requests")
	if len(recent.Arr) != 2 {
		t.Fatalf("最近明细 = %d 条, 期望 2（limit 未生效）", len(recent.Arr))
	}
	// 统计不受 limit 影响：它是窗口内的全量。
	stats, _ := value.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 5 {
		t.Errorf("requests = %d, 期望 5（limit 不应影响统计）", got)
	}
	// 明细按 id 倒序（最新在前），因此第一条的 total_tokens 应是最后写入的那条。
	// 这里全部相同，改验字段齐全与类型。
	first := recent.Arr[0]
	for _, key := range []string{"created_at", "model_id", "provider_id", "upstream_model_id",
		"status_code", "success", "retried", "prompt_tokens", "completion_tokens",
		"total_tokens", "cached_tokens", "first_token_ms", "duration_ms"} {
		if _, ok := first.Obj.Get(key); !ok {
			t.Errorf("明细缺少字段 %s", key)
		}
	}
	// success 是**布尔**而不是整数（与 request_history 的 items 一致）。
	success, _ := first.Obj.Get("success")
	if _, ok := success.AsBool(); !ok {
		t.Errorf("success 应是布尔，实得 %+v", success)
	}
}

// TestRequestHistoryCarriesAccessKey 断言请求明细带上**发起请求的那把**访问密钥。
//
// 概览页的请求流要回答「这次请求是谁发的」，而 caller_type=access_key 只说得出档位：
// 访问密钥一人一把，说不出哪一把就等于没答（与 AccessKeyUsage 的理由同源）。这一条
// 同时守着写入端（request_access_key 真的写了行）与读取端的 LEFT JOIN。
//
// 没有归属的行必须是 **null**：兜底成空串或某一把 key，会把完整权限的用量记到某个人
// 头上——那正是这张旁挂表要避免的事。
func TestRequestHistoryCarriesAccessKey(t *testing.T) {
	store := tempStore(t)
	provider := "openai"
	recordAccessKey(t, store, "trial", "default", "gpt-4o", &provider, 10)
	// 完整权限的一条：没有访问密钥归属，但必须照常出现在明细里（LEFT 而不是 INNER）。
	recordWorkspace(t, store, "default", "gpt-4o-mini", "gpt-4o-mini", &provider, &provider, 5)

	byModel := map[string]*canonical.Value{}
	for _, item := range historyItems(t, store, RequestHistoryParams{}) {
		model, _ := item.Obj.Get("model_id")
		byModel[model.Str] = item
	}
	if len(byModel) != 2 {
		t.Fatalf("明细条数 = %d, 期望 2（无归属的行也要在）", len(byModel))
	}
	attributed, _ := byModel["gpt-4o"].Obj.Get("access_key_id")
	if attributed.Str != "trial" {
		t.Errorf("access_key_id = %q, 期望 trial", attributed.Str)
	}
	// 显示名不在指标层：id 是稳定标识符，名字可改、属于配置（由服务端补）。
	if _, ok := byModel["gpt-4o"].Obj.Get("access_key_name"); ok {
		t.Error("指标层不应返回 access_key_name（显示名在配置里，由服务端补）")
	}
	unattributed, ok := byModel["gpt-4o-mini"].Obj.Get("access_key_id")
	if !ok {
		t.Fatal("明细缺少 access_key_id 字段")
	}
	if unattributed.Kind != canonical.KindNull {
		t.Errorf("无归属行的 access_key_id 应为 null, 实得 %s", compact(t, unattributed))
	}
}
