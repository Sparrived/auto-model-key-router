package metrics

import (
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// beijingTime 解析一个固定时刻。
func beijingTime(t *testing.T, text string) time.Time {
	t.Helper()
	return parsePythonISO(t, text)
}

// recordWorkspace 写入一行带工作空间归属的指标。
//
// Usage 传 canonical 对象（record() 只接受对象，非对象按空字典处理），
// total_tokens 由 normalizeUsage 从 prompt+completion 推出，因此这里只给 prompt。
func recordWorkspace(t *testing.T, store *Store, workspace, modelID, requestedModelID string,
	provider, upstream *string, promptTokens int64) {
	t.Helper()
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(promptTokens)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
	)
	params := RecordParams{
		ModelID:          modelID,
		KeyName:          "key-a",
		Usage:            usage,
		CallerType:       "local",
		RequestedModelID: &requestedModelID,
		ProviderID:       provider,
		UpstreamModelID:  upstream,
		Workspace:        workspace,
	}
	if err := store.Record(params); err != nil {
		t.Fatalf("写入指标 (%s/%s): %v", workspace, modelID, err)
	}
}

// intOf 取一个数字值的 int64，用 AsInt 而非 Num 字段（Num 是字面量文本）。
func intOf(t *testing.T, value *canonical.Value) int64 {
	t.Helper()
	parsed, ok := value.AsInt()
	if !ok {
		t.Fatalf("值不是整数: %+v", value)
	}
	return parsed
}

// statInt 从 UsageStats 字典里取一个整数字段。
func statInt(t *testing.T, stats *canonical.Value, key string) int64 {
	t.Helper()
	value, ok := stats.Obj.Get(key)
	if !ok {
		t.Fatalf("统计字典缺少字段 %s", key)
	}
	return intOf(t, value)
}

// TestWorkspaceUsageGroupsByWorkspace 断言按工作空间分组与流向连边的形状。
//
// 这是新增读接口的**唯一一条可运行检查**：覆盖分组计数、token 汇总、未归属行不
// 落入任何工作空间，以及六层连边的相邻关系。
func TestWorkspaceUsageGroupsByWorkspace(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)

	openai, gpt4o := "openai", "gpt-4o"
	deepseek, v3 := "deepseek", "deepseek-v3"

	// teamA：两条，落在 openai/gpt-4o。
	recordWorkspace(t, store, "teamA", "gpt-4o", "TASK_000001", &openai, &gpt4o, 100)
	recordWorkspace(t, store, "teamA", "gpt-4o", "TASK_000001", &openai, &gpt4o, 50)
	// teamB：一条，落在 deepseek/deepseek-v3。
	recordWorkspace(t, store, "teamB", "deepseek-v3", "TASK_000002", &deepseek, &v3, 30)
	// 没有归属的一条（历史行语义）：不能出现在任何工作空间里。
	recordWorkspace(t, store, "", "gpt-4o", "gpt-4o", &openai, &gpt4o, 7)

	value, err := store.WorkspaceUsage(WorkspaceUsageParams{})
	if err != nil {
		t.Fatalf("WorkspaceUsage: %v", err)
	}

	workspaces, _ := value.Obj.Get("workspaces")
	if len(workspaces.Arr) != 2 {
		t.Fatalf("工作空间数 = %d, 期望 2", len(workspaces.Arr))
	}
	first := workspaces.Arr[0]
	name, _ := first.Obj.Get("name")
	if name.Str != "teamA" {
		t.Errorf("第一个工作空间 = %q, 期望 teamA（按名字排序）", name.Str)
	}
	stats, _ := first.Obj.Get("stats")
	if got := statInt(t, stats, "requests"); got != 2 {
		t.Errorf("teamA requests = %d, 期望 2", got)
	}
	if got := statInt(t, stats, "total_tokens"); got != 150 {
		t.Errorf("teamA total_tokens = %d, 期望 150", got)
	}

	// 未归属行必须被单独统计。
	unattributed, _ := value.Obj.Get("unattributed")
	if got := statInt(t, unattributed, "requests"); got != 1 {
		t.Errorf("unattributed requests = %d, 期望 1", got)
	}
	if got := statInt(t, unattributed, "total_tokens"); got != 7 {
		t.Errorf("unattributed total_tokens = %d, 期望 7", got)
	}

	layers, _ := value.Obj.Get("layers")
	if len(layers.Arr) != 6 {
		t.Fatalf("层数 = %d, 期望 6", len(layers.Arr))
	}
	// Key 这一层必须在供应商与上游模型之间：它是「同一家供应商的哪把 Key」，
	// 放到别的位置（例如上游模型之后）就成了两个互不相干的问题。
	if layers.Arr[3].Str != "provider_id" || layers.Arr[4].Str != "key_name" || layers.Arr[5].Str != "upstream_model_id" {
		t.Errorf("末三层 = %s/%s/%s, 期望 provider_id/key_name/upstream_model_id",
			layers.Arr[3].Str, layers.Arr[4].Str, layers.Arr[5].Str)
	}

	// 连边只来自有归属的行：teamA/teamB。
	// 每层相邻关系各 2 条（teamA 与 teamB 各贡献一条），共 5 段 × 2 = 10 条。
	links, _ := value.Obj.Get("links")
	if len(links.Arr) != 10 {
		t.Errorf("连边数 = %d, 期望 10", len(links.Arr))
	}
	// 第一条连边应是 teamA -> TASK_000001，宽度 2。
	if len(links.Arr) > 0 {
		link := links.Arr[0]
		source, _ := link.Obj.Get("source")
		target, _ := link.Obj.Get("target")
		if source.Str != "teamA" || target.Str != "TASK_000001" {
			t.Errorf("首条连边 = %s -> %s, 期望 teamA -> TASK_000001", source.Str, target.Str)
		}
		if got := statInt(t, link, "requests"); got != 2 {
			t.Errorf("首条连边 requests = %d, 期望 2", got)
		}
	}
	for _, link := range links.Arr {
		sourceLayer, _ := link.Obj.Get("source_layer")
		targetLayer, _ := link.Obj.Get("target_layer")
		if intOf(t, targetLayer) != intOf(t, sourceLayer)+1 {
			t.Errorf("连边不是相邻层: %d -> %d", intOf(t, sourceLayer), intOf(t, targetLayer))
		}
	}
}

// TestWorkspaceUsageWindowScopesFlows 断言时间窗口对统计与流向同时生效。
func TestWorkspaceUsageWindowScopesFlows(t *testing.T) {
	now := beijingTime(t, "2026-07-14T12:00:00+08:00")
	installClock(t, now)
	store := tempStore(t)

	openai, gpt4o := "openai", "gpt-4o"
	recordWorkspace(t, store, "teamA", "gpt-4o", "TASK_000001", &openai, &gpt4o, 100)

	// 把时钟拨到 2 小时后：1 小时窗口内应当什么都看不到。
	installClock(t, now.Add(2*time.Hour))
	hours := 1.0
	value, err := store.WorkspaceUsage(WorkspaceUsageParams{Hours: &hours})
	if err != nil {
		t.Fatalf("WorkspaceUsage: %v", err)
	}
	workspaces, _ := value.Obj.Get("workspaces")
	if len(workspaces.Arr) != 0 {
		t.Errorf("窗口外的工作空间数 = %d, 期望 0", len(workspaces.Arr))
	}
	links, _ := value.Obj.Get("links")
	if len(links.Arr) != 0 {
		t.Errorf("窗口外的连边数 = %d, 期望 0", len(links.Arr))
	}
	unattributed, _ := value.Obj.Get("unattributed")
	if got := statInt(t, unattributed, "requests"); got != 0 {
		t.Errorf("窗口外的 unattributed requests = %d, 期望 0", got)
	}
}

// TestWorkspaceUsageRejectsNonPositiveHours 断言参数校验沿用同一套错误文本。
func TestWorkspaceUsageRejectsNonPositiveHours(t *testing.T) {
	store := tempStore(t)
	hours := 0.0
	if _, err := store.WorkspaceUsage(WorkspaceUsageParams{Hours: &hours}); err == nil {
		t.Fatal("hours=0 应当报错")
	} else if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("错误类型 = %T, 期望 *ValidationError", err)
	}
}

// TestRecordKeepsWorkspaceCallerType 固化 caller_type 的第三档 'workspace' 能落库。
//
// Go 侧新增（参照实现只有 local / visitor，后者已随访客模式删除）。store.Record 里有一个白名单，未列的取值
// 会被**静默改写成 'local'**——那会让工作空间流量在看板上算成本机（权限最高的一档），
// 归因彻底失真。因此这一档必须显式被接受。
func TestRecordKeepsWorkspaceCallerType(t *testing.T) {
	store := tempStore(t)
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
	)
	if err := store.Record(RecordParams{
		ModelID:    "model-a",
		KeyName:    "key-a",
		Usage:      usage,
		CallerType: "workspace",
		Workspace:  "teamA",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	hours := 24.0
	value, err := store.Snapshot(&hours, nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	callerTypes, _ := value.Obj.Get("caller_types")
	if _, ok := callerTypes.Obj.Get("workspace"); !ok {
		t.Fatalf("snapshot 应含 workspace 档，实得 %v", callerTypes.Obj.Keys())
	}
	// 且不得被算成 local。
	workspaceStats, _ := callerTypes.Obj.Get("workspace")
	if got := statInt(t, workspaceStats, "requests"); got != 1 {
		t.Errorf("workspace 档请求数 = %d，期望 1", got)
	}
	localStats, _ := callerTypes.Obj.Get("local")
	if got := statInt(t, localStats, "requests"); got != 0 {
		t.Errorf("local 档请求数 = %d，期望 0（不得把 workspace 归进 local）", got)
	}
}

// TestRecordRejectsUnknownCallerType 固化：未知 caller_type 仍然收敛到 'local'。
//
// 这是既有行为，与新档无关——写在这里是为了让「加档」与「兜底」两个方向同时被锁住：
// 放宽白名单时不能顺手把兜底也取消掉。
func TestRecordRejectsUnknownCallerType(t *testing.T) {
	store := tempStore(t)
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(0)},
	)
	if err := store.Record(RecordParams{
		ModelID:    "model-a",
		KeyName:    "key-a",
		Usage:      usage,
		CallerType: "bogus",
		Workspace:  "teamA",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	hours := 24.0
	value, err := store.Snapshot(&hours, nil)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	callerTypes, _ := value.Obj.Get("caller_types")
	localStats, _ := callerTypes.Obj.Get("local")
	if got := statInt(t, localStats, "requests"); got != 1 {
		t.Errorf("未知 caller_type 应收敛到 local，local 请求数 = %d，期望 1", got)
	}
}
