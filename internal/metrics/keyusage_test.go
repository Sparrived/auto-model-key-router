package metrics

import (
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// recordKeyUsage 写入一行可控上游 Key / 访问密钥归属的指标。
//
// 与 recordWorkspace 的差别：那份固定了 KeyName 且不带访问密钥，本文件要断言的正是
// 「哪把上游 Key + 哪把访问密钥」两个身份，因此两者都必须可指定。
func recordKeyUsage(t *testing.T, store *Store, params RecordParams) {
	t.Helper()
	if params.Usage == nil {
		params.Usage = canonical.NewObjectOf(
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(0)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
		)
	}
	if err := store.Record(params); err != nil {
		t.Fatalf("写入指标 (%s/%s): %v", params.ModelID, params.KeyName, err)
	}
}

// keyUsagePrompt 是只给 prompt 的用量对象（total_tokens 由 normalizeUsage 推出）。
func keyUsagePrompt(tokens int64) *canonical.Value {
	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(tokens)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
	)
}

// rowAt 按序号取数组里的一行，越界时直接失败。
func rowAt(t *testing.T, array *canonical.Value, index int) *canonical.Value {
	t.Helper()
	if array == nil || index >= len(array.Arr) {
		t.Fatalf("数组第 %d 行不存在（共 %d 行）", index, len(array.Arr))
	}
	return array.Arr[index]
}

// fieldOf 取一个对象字段的字符串值。
func fieldOf(t *testing.T, value *canonical.Value, key string) string {
	t.Helper()
	field, ok := value.Obj.Get(key)
	if !ok {
		t.Fatalf("对象缺少字段 %s", key)
	}
	return field.Str
}

// statsAt 取一行里的 stats 子对象。
func statsAt(t *testing.T, row *canonical.Value) *canonical.Value {
	t.Helper()
	stats, ok := row.Obj.Get("stats")
	if !ok {
		t.Fatal("行缺少 stats 字段")
	}
	return stats
}

// seedKeyUsage 写入一组覆盖全部分支的样本。
//
// 五行的分工：
//   - 第 1 行：完整权限（无访问密钥归属），openai/main；
//   - 第 2 行：访问密钥 ak1，与第 1 行**同一把上游 Key**——两份拆分必须各归各的；
//   - 第 3 行：访问密钥 ak2，openai/backup，状态码 500（失败要计入）；
//   - 第 4 行：azure/main —— 与第 1/2 行**同名但不同供应商**的 Key。这正是
//     /metrics 的 keys（只按模型 × Key 名分组）会并成一行的那种数据；
//   - 第 5 行：provider_id 为 NULL 的历史行，状态码也是 NULL——它必须整个进
//     unattributed，且**不能**出现在按 Key 的两份拆分里。
func seedKeyUsage(t *testing.T, store *Store) {
	t.Helper()
	openai, azure := "openai", "azure"
	requested := "m1"
	ok := int64(200)
	failed := int64(500)

	// 第 1 行：本机完整权限。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m1", KeyName: "main", Usage: keyUsagePrompt(100),
		StatusCode: &ok, CallerType: "local",
		RequestedModelID: &requested, ProviderID: &openai, Workspace: "teamA",
	})
	// 第 2 行：访问密钥 ak1 打同一把上游 Key。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m1", KeyName: "main", Usage: keyUsagePrompt(50),
		StatusCode: &ok, CallerType: "access_key", AccessKeyID: "ak1",
		ProviderID: &openai, Workspace: "teamA",
	})
	// 第 3 行：访问密钥 ak2，另一把上游 Key，失败。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m2", KeyName: "backup", Usage: keyUsagePrompt(30),
		StatusCode: &failed, CallerType: "access_key", AccessKeyID: "ak2",
		ProviderID: &openai, Workspace: "teamA",
	})
	// 第 4 行：另一家的同名 Key。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m2", KeyName: "main", Usage: keyUsagePrompt(20),
		StatusCode: &ok, CallerType: "local", ProviderID: &azure, Workspace: "teamB",
	})
	// 第 5 行：没有供应商归因、也没有状态码的历史行。
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m3", KeyName: "legacy", Usage: keyUsagePrompt(7), CallerType: "local",
	})
}

// TestKeyUsageSplitsByUpstreamKeyAndAccessKey 断言三份拆分与未归属的口径。
//
// 这是新增读接口的**唯一一条可运行检查**，锁四件事：
//   - 同名 Key 分属两家时必须各成一行（provider_id 是分组键的一部分）；
//   - 同一把上游 Key 的流量要跨调用方合并（两份拆分互不嵌套）；
//   - 访问密钥那一份只统计有归属的行，且与上游 Key 各自独立；
//   - provider_id 为 NULL 的行整个进 unattributed，不得混进任何按 Key 的行。
func TestKeyUsageSplitsByUpstreamKeyAndAccessKey(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedKeyUsage(t, store)

	value, err := store.KeyUsage(KeyUsageParams{})
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}

	upstream, _ := value.Obj.Get("upstream_keys")
	// 三家组合：azure/main、openai/backup、openai/main。m3 那一行没有供应商，不在这里。
	if len(upstream.Arr) != 3 {
		t.Fatalf("upstream_keys 行数 = %d，期望 3（body=%s）", len(upstream.Arr), compact(t, value))
	}
	// 按 (供应商, Key 名) 升序：azure 在前。
	first := rowAt(t, upstream, 0)
	if got := fieldOf(t, first, "provider_id"); got != "azure" {
		t.Errorf("首行 provider_id = %q，期望 azure（按供应商升序）", got)
	}
	if got := fieldOf(t, first, "key_name"); got != "main" {
		t.Errorf("首行 key_name = %q，期望 main", got)
	}
	if got := statInt(t, statsAt(t, first), "requests"); got != 1 {
		t.Errorf("azure/main requests = %d，期望 1", got)
	}

	// openai/main 必须是两行合并后的 2 次请求、150 token——它跨了两种调用方。
	var openaiMain *canonical.Value
	for _, row := range upstream.Arr {
		if fieldOf(t, row, "provider_id") == "openai" && fieldOf(t, row, "key_name") == "main" {
			openaiMain = row
		}
	}
	if openaiMain == nil {
		t.Fatal("upstream_keys 缺少 openai/main（同名 Key 可能被并掉了）")
	}
	openaiStats := statsAt(t, openaiMain)
	if got := statInt(t, openaiStats, "requests"); got != 2 {
		t.Errorf("openai/main requests = %d，期望 2（跨调用方合并）", got)
	}
	if got := statInt(t, openaiStats, "total_tokens"); got != 150 {
		t.Errorf("openai/main total_tokens = %d，期望 150", got)
	}
	if got := statInt(t, openaiStats, "successes"); got != 2 {
		t.Errorf("openai/main successes = %d，期望 2", got)
	}

	// 模型 × Key：m1/openai/main（两行合并）、m2/openai/backup、m2/azure/main。
	modelKeys, _ := value.Obj.Get("model_keys")
	if len(modelKeys.Arr) != 3 {
		t.Fatalf("model_keys 行数 = %d，期望 3（body=%s）", len(modelKeys.Arr), compact(t, value))
	}
	// 升序后首行应是 m1/openai/main。
	firstModel := rowAt(t, modelKeys, 0)
	if got := fieldOf(t, firstModel, "model_id"); got != "m1" {
		t.Errorf("model_keys 首行 model_id = %q，期望 m1", got)
	}
	if got := statInt(t, statsAt(t, firstModel), "requests"); got != 2 {
		t.Errorf("m1/openai/main requests = %d，期望 2", got)
	}

	// 访问密钥：只有 ak1 / ak2 两把，且各自独立于上游 Key。
	access, _ := value.Obj.Get("access_keys")
	if len(access.Arr) != 2 {
		t.Fatalf("access_keys 行数 = %d，期望 2（body=%s）", len(access.Arr), compact(t, value))
	}
	ak1 := rowAt(t, access, 0)
	if got := fieldOf(t, ak1, "access_key_id"); got != "ak1" {
		t.Errorf("访问密钥首行 = %q，期望 ak1（按 id 升序）", got)
	}
	if got := statInt(t, statsAt(t, ak1), "requests"); got != 1 {
		t.Errorf("ak1 requests = %d，期望 1", got)
	}
	if got := statInt(t, statsAt(t, ak1), "total_tokens"); got != 50 {
		t.Errorf("ak1 total_tokens = %d，期望 50", got)
	}
	// 显示名由服务端补，指标层**不得**编造一个：这里只应有 id 与 stats 两个字段。
	if _, ok := ak1.Obj.Get("access_key_name"); ok {
		t.Error("指标层不应返回 access_key_name（显示名在配置里，由服务端补）")
	}

	// 未归属：m3 那一行（1 次请求、7 token）。
	unattributed, _ := value.Obj.Get("unattributed")
	if got := statInt(t, unattributed, "requests"); got != 1 {
		t.Errorf("unattributed requests = %d，期望 1", got)
	}
	if got := statInt(t, unattributed, "total_tokens"); got != 7 {
		t.Errorf("unattributed total_tokens = %d，期望 7", got)
	}
	// 该行的 status_code 是 NULL：计数口径是全部请求，但不得进 status_codes。
	unattributedCodes, _ := unattributed.Obj.Get("status_codes")
	if len(unattributedCodes.Obj.Keys()) != 0 {
		t.Errorf("unattributed.status_codes = %v，期望空（NULL 状态码不进分布）", unattributedCodes.Obj.Keys())
	}
	// 反向：有状态码的行必须带上分布。
	openaiCodes, _ := openaiStats.Obj.Get("status_codes")
	if got := statInt(t, openaiCodes, "200"); got != 2 {
		t.Errorf("openai/main status_codes[200] = %d，期望 2", got)
	}
	for _, row := range upstream.Arr {
		if fieldOf(t, row, "key_name") != "backup" {
			continue
		}
		codes, _ := statsAt(t, row).Obj.Get("status_codes")
		if got := statInt(t, codes, "500"); got != 1 {
			t.Errorf("openai/backup status_codes[500] = %d，期望 1", got)
		}
	}

	// 归属守恒：按 Key 三份拆分的请求数各自应等于「总量 - 未归属」。
	totalRequests := int64(0)
	for _, row := range upstream.Arr {
		totalRequests += statInt(t, statsAt(t, row), "requests")
	}
	if totalRequests != 4 {
		t.Errorf("upstream_keys 请求数合计 = %d，期望 4（全部行减去未归属的 1 行）", totalRequests)
	}
}

// TestKeyUsageWindowScopesBreakdowns 断言时间窗口对三份拆分同时生效。
func TestKeyUsageWindowScopesBreakdowns(t *testing.T) {
	now := beijingTime(t, "2026-07-14T12:00:00+08:00")
	installClock(t, now)
	store := tempStore(t)
	seedKeyUsage(t, store)

	// 把时钟拨到 2 小时后：1 小时窗口内应当什么都看不到。
	installClock(t, now.Add(2*time.Hour))
	hours := 1.0
	value, err := store.KeyUsage(KeyUsageParams{Hours: &hours})
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	for _, name := range []string{"upstream_keys", "model_keys", "access_keys"} {
		array, _ := value.Obj.Get(name)
		if len(array.Arr) != 0 {
			t.Errorf("窗口外 %s 行数 = %d，期望 0", name, len(array.Arr))
		}
	}
	unattributed, _ := value.Obj.Get("unattributed")
	if got := statInt(t, unattributed, "requests"); got != 0 {
		t.Errorf("窗口外 unattributed requests = %d，期望 0", got)
	}
	// 空窗口也要给出零值对象，而不是缺字段——界面因此不必为「暂时是空的」写分支。
	if _, ok := unattributed.Obj.Get("total_tokens"); !ok {
		t.Error("空窗口的 unattributed 缺少 total_tokens 字段")
	}
}

// TestKeyUsageRejectsNonPositiveHours 断言参数校验沿用同一套错误文本。
func TestKeyUsageRejectsNonPositiveHours(t *testing.T) {
	store := tempStore(t)
	hours := 0.0
	if _, err := store.KeyUsage(KeyUsageParams{Hours: &hours}); err == nil {
		t.Fatal("hours=0 应当报错")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("错误类型 = %T，期望 *ValidationError", err)
	}
}

// TestKeyUsageEmptyDatabaseHasZeroUnattributed 固化空库（无任何行）时的形状。
//
// 与 TestKeyUsageWindowScopesBreakdowns 的差别：那次是「有行但都在窗口外」，这里是
// 「库是空的」。两条路径在 SQL 层不同（前者分组为空但表非空），都必须给出零值。
func TestKeyUsageEmptyDatabaseHasZeroUnattributed(t *testing.T) {
	store := tempStore(t)
	value, err := store.KeyUsage(KeyUsageParams{})
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	unattributed, _ := value.Obj.Get("unattributed")
	if got := statInt(t, unattributed, "requests"); got != 0 {
		t.Errorf("空库 unattributed requests = %d，期望 0", got)
	}
	upstream, _ := value.Obj.Get("upstream_keys")
	if upstream == nil || len(upstream.Arr) != 0 {
		t.Errorf("空库 upstream_keys = %v，期望空数组", upstream)
	}
}
