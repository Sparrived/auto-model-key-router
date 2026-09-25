package metrics

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// recordSource 写入一行带请求来源的指标。
func recordSource(t *testing.T, store *Store, workspace, clientAddr, userAgent string) {
	t.Helper()
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(2)},
	)
	// StatusCode 非 nil 且 <400，这一行才是 success=1：下面的筛选用例要用到它。
	statusCode := int64(200)
	if err := store.Record(RecordParams{
		ModelID:    "gpt-4o",
		KeyName:    "key-a",
		StatusCode: &statusCode,
		Usage:      usage,
		CallerType: "local",
		Workspace:  workspace,
		ClientAddr: clientAddr,
		UserAgent:  userAgent,
	}); err != nil {
		t.Fatalf("写入指标: %v", err)
	}
}

// historyItems 取 /metrics/requests 形态的明细条目。
func historyItems(t *testing.T, store *Store, params RequestHistoryParams) []*canonical.Value {
	t.Helper()
	if params.Limit == 0 {
		params.Limit = 10
	}
	value, err := store.RequestHistory(params)
	if err != nil {
		t.Fatalf("读取明细: %v", err)
	}
	items, _ := value.Obj.Get("items")
	return items.Arr
}

// historyItem 取唯一一条明细。
func historyItem(t *testing.T, store *Store) *canonical.Value {
	t.Helper()
	items := historyItems(t, store, RequestHistoryParams{})
	if len(items) != 1 {
		t.Fatalf("明细条数 = %d，期望 1", len(items))
	}
	return items[0]
}

// TestRequestHistoryCarriesSource 断言明细里带上请求来源三项。
//
// 来源不进 request_metrics 的列（见 schema.go 的旁挂表理由），因此这条用例同时守着
// 两件事：写入端真的写了 request_source，读取端的 LEFT JOIN 真的把它取出来了。
func TestRequestHistoryCarriesSource(t *testing.T) {
	store := tempStore(t)
	recordSource(t, store, "teamA", "127.0.0.1:50874", "claude-cli/1.0")

	item := historyItem(t, store)
	workspace, _ := item.Obj.Get("workspace")
	clientAddr, _ := item.Obj.Get("client_addr")
	userAgent, _ := item.Obj.Get("user_agent")

	if workspace.Str != "teamA" {
		t.Errorf("workspace = %q，期望 teamA", workspace.Str)
	}
	if clientAddr.Str != "127.0.0.1:50874" {
		t.Errorf("client_addr = %q，期望 127.0.0.1:50874", clientAddr.Str)
	}
	if userAgent.Str != "claude-cli/1.0" {
		t.Errorf("user_agent = %q，期望 claude-cli/1.0", userAgent.Str)
	}

	// 过滤条件与两条 JOIN 拼在同一个查询里（whereSQL 用的是裸列名，接入 JOIN 后
	// 不能出现列名歧义或把来源行筛掉），因此这里各跑一次命中与不命中的筛选。
	local, success := "local", true
	matched := historyItems(t, store, RequestHistoryParams{CallerType: &local, Success: &success})
	if len(matched) != 1 {
		t.Fatalf("按 caller_type=local & success=true 筛选 = %d 条，期望 1", len(matched))
	}
	workspace, _ = matched[0].Obj.Get("workspace")
	if workspace.Str != "teamA" {
		t.Errorf("筛选后的 workspace = %q，期望 teamA", workspace.Str)
	}
	other := "workspace"
	if items := historyItems(t, store, RequestHistoryParams{CallerType: &other}); len(items) != 0 {
		t.Errorf("按 caller_type=workspace 筛选应无结果，实得 %d 条", len(items))
	}
}

// TestRequestHistorySourceIsNullWithoutAttribution 断言没有来源的行渲染成 null。
//
// 历史行（升级前写入）与不走 HTTP 的写入路径都没有来源。它们必须照常出现在明细里
// （LEFT JOIN 而不是 INNER），且来源是 null——**不能**兜底成空串或某个默认地址：
// 那会让看板把"不知道从哪来"显示成一个具体的来源。
func TestRequestHistorySourceIsNullWithoutAttribution(t *testing.T) {
	store := tempStore(t)
	recordSource(t, store, "", "", "")

	item := historyItem(t, store)
	for _, key := range []string{"workspace", "client_addr", "user_agent"} {
		value, ok := item.Obj.Get(key)
		if !ok {
			t.Fatalf("明细缺少字段 %s", key)
		}
		if value.Kind != canonical.KindNull {
			t.Errorf("%s 应为 null，实得 %s", key, compact(t, value))
		}
	}
}

// TestRequestSourceEmptyUserAgentStaysNull 断言空 User-Agent 落库为 NULL。
//
// 「没带这个头」与「带了一个空串的头」在看板上没有区别，因此库里只留一种表示；
// 顺带确认 client_addr 存在时那一行照写（user_agent 空不该让整行丢掉）。
func TestRequestSourceEmptyUserAgentStaysNull(t *testing.T) {
	store := tempStore(t)
	recordSource(t, store, "teamA", "[::1]:5555", "")

	item := historyItem(t, store)
	clientAddr, _ := item.Obj.Get("client_addr")
	userAgent, _ := item.Obj.Get("user_agent")
	if clientAddr.Str != "[::1]:5555" {
		t.Errorf("client_addr = %q，期望 [::1]:5555", clientAddr.Str)
	}
	if userAgent.Kind != canonical.KindNull {
		t.Errorf("空 user_agent 应为 null，实得 %s", compact(t, userAgent))
	}
}
