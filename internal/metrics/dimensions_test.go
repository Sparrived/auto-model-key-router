package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件固化 Dimensions（/ui 读数的七个多值筛选维度）的两件事：
//
//   - SQL 形状：条件的种类、顺序与参数顺序（新维度一律追加在最早两个维度之后），
//     全部为空时一个字节都不多——/metrics 系列走的就是零值；
//   - 行为：维度内 OR、维度间 AND，在 FilteredMetrics / KeyUsage / StatusMonitor 三条
//     读数上同一口径。上游 Key 是成对条件，必须不是「供应商 IN × Key 名 IN」的笛卡尔积。

// TestDimensionConditionsSQLOrder 锁定全部七个维度同时给值时的 SQL 文本与参数顺序。
//
// 前两个维度的位置与 TestMetricFilterDimensionConditionsAppendedLast 锁的一致；其后依次
// 是模型、上游 Key、工作空间、调用方档位、成败。参数逐个比对：一处顺序抄错就会把模型名
// 绑到工作空间的占位符上，而那种错误在 SQL 文本里看不出来。
func TestDimensionConditionsSQLOrder(t *testing.T) {
	since := "2026-07-14T00:00:00+08:00"
	successful := true
	filter := metricFilter{
		SinceCreatedAt: &since,
		Dimensions: Dimensions{
			ProviderIDs:  []string{"openai"},
			AccessKeyIDs: []string{"ak1"},
			ModelIDs:     []string{"m1", "m2"},
			UpstreamKeys: []UpstreamKey{{"openai", "main"}, {"azure", "backup"}},
			Workspaces:   []string{"teamA"},
			CallerTypes:  []string{"local", "access_key"},
			Success:      &successful,
		},
	}
	whereSQL, parameters := filter.sql()
	want := " WHERE created_at >= ?" +
		" AND provider_id IN (?)" +
		" AND id IN (SELECT request_id FROM request_access_key WHERE access_key_id IN (?))" +
		" AND model_id IN (?, ?)" +
		" AND ((provider_id = ? AND key_name = ?) OR (provider_id = ? AND key_name = ?))" +
		" AND id IN (SELECT request_id FROM request_workspace WHERE workspace IN (?))" +
		" AND caller_type IN (?, ?)" +
		" AND success = ?"
	if whereSQL != want {
		t.Errorf("七维度 SQL =\n %q\n期望\n %q", whereSQL, want)
	}
	wantParams := []any{
		since, "openai", "ak1", "m1", "m2",
		"openai", "main", "azure", "backup",
		"teamA", "local", "access_key", int64(1),
	}
	if fmt.Sprint(parameters) != fmt.Sprint(wantParams) || len(parameters) != len(wantParams) {
		t.Errorf("七维度参数 = %v，期望 %v", parameters, wantParams)
	}
	if parameters[len(parameters)-1] != int64(1) {
		t.Errorf("success 参数 = %#v，期望 int64(1)", parameters[len(parameters)-1])
	}

	// 单对上游 Key 也有外层括号：它与其它条件之间的 AND 不能被 OR 的优先级拆开。
	single := metricFilter{Dimensions: Dimensions{UpstreamKeys: []UpstreamKey{{"openai", "main"}}}}
	if got, _ := single.sql(); got != " WHERE ((provider_id = ? AND key_name = ?))" {
		t.Errorf("单对上游 Key SQL = %q", got)
	}

	// 失败筛选给 0；与参照实现的单值 Success 并存时两个条件都在（单值在前、维度在后）。
	failed := false
	both := metricFilter{Success: &failed, Dimensions: Dimensions{Success: &successful}}
	gotSQL, gotParams := both.sql()
	if gotSQL != " WHERE success = ? AND success = ?" {
		t.Errorf("单值 Success 与 Dimensions.Success 并存的 SQL = %q", gotSQL)
	}
	if len(gotParams) != 2 || gotParams[0] != int64(0) || gotParams[1] != int64(1) {
		t.Errorf("并存时参数 = %v，期望 [0 1]", gotParams)
	}

	// 全部为空：一个字节都不多（零值、空切片都一样）。
	empty := metricFilter{SinceCreatedAt: &since, Dimensions: Dimensions{
		ProviderIDs: []string{}, ModelIDs: []string{}, UpstreamKeys: []UpstreamKey{},
		Workspaces: []string{}, CallerTypes: []string{},
	}}
	if got, params := empty.sql(); got != " WHERE created_at >= ?" || len(params) != 1 {
		t.Errorf("空维度 SQL = %q（参数 %d 个），期望与不加维度时相同", got, len(params))
	}

	// 新维度同样不进 /metrics/* 的 filters 回显。
	if pairs := (queryFilter{Dimensions: filter.Dimensions}).filterPairs(); len(pairs) != 0 {
		t.Errorf("filterPairs 不应回显多值维度，实得 %v", pairs)
	}
}

// TestKeyUsageDimensionConditionsSQL 锁定 KeyUsage 那份别名写法：同样的种类与顺序，只是
// 列带 m. 前缀、访问密钥直接过滤 LEFT JOIN 进来的 a.access_key_id、工作空间子查询的外层
// 写 m.id。
func TestKeyUsageDimensionConditionsSQL(t *testing.T) {
	failed := false
	filters, parameters := appendKeyUsageDimensionFilters(nil, nil, Dimensions{
		ProviderIDs:  []string{"openai"},
		AccessKeyIDs: []string{"ak1", "ak2"},
		ModelIDs:     []string{"m1"},
		UpstreamKeys: []UpstreamKey{{"openai", "main"}},
		Workspaces:   []string{"teamA"},
		CallerTypes:  []string{"access_key"},
		Success:      &failed,
	})
	want := []string{
		"m.provider_id IN (?)",
		"a.access_key_id IN (?, ?)",
		"m.model_id IN (?)",
		"((m.provider_id = ? AND m.key_name = ?))",
		"m.id IN (SELECT request_id FROM request_workspace WHERE workspace IN (?))",
		"m.caller_type IN (?)",
		"m.success = ?",
	}
	if strings.Join(filters, " AND ") != strings.Join(want, " AND ") {
		t.Errorf("KeyUsage 维度条件 =\n %v\n期望\n %v", filters, want)
	}
	if len(parameters) != 9 || parameters[8] != int64(0) {
		t.Errorf("KeyUsage 维度参数 = %v，期望 9 个且末位为 0", parameters)
	}
	if extra, params := appendKeyUsageDimensionFilters(nil, nil, Dimensions{}); len(extra) != 0 || len(params) != 0 {
		t.Errorf("空维度不应追加条件，实得 %v / %v", extra, params)
	}
}

// seedDimensions 写入一组让七个维度彼此可区分的样本（6 行，都落在 T）。
//
//	#  模型  供应商  上游模型     上游 Key  调用方          工作空间  状态  API 路径
//	1  m1    openai  gpt-4o       main      local           teamA     200   chat/completions
//	2  m1    openai  gpt-4o       backup    access_key/ak1  teamA     200   messages
//	3  m2    azure   gpt-4o       main      access_key/ak2  teamB     500   chat/completions
//	4  m2    azure   gpt-35       backup    workspace       teamB     200   responses
//	5  m3    NULL    NULL         legacy    local           —         NULL  —
//	6  m1    openai  gpt-4o-mini  main      access_key/ak1  —         429   chat/completions
//
// 上游 Key 的四个组合 (openai|azure) × (main|backup) 都出现过：筛
// `(openai,main) + (azure,backup)` 时若被实现成笛卡尔积，会把 2、3 两行也带进来。
// pool_name 全为 NULL（v4 配置下的真实写入就是这样）：snapshot 的 upstream_models 因此
// 是空的，而 UsageBreakdown 的 upstream_model 分组照样有数。
func seedDimensions(t *testing.T, store *Store) {
	t.Helper()
	openai, azure := "openai", "azure"
	gpt4o, gpt35, gpt4oMini := "gpt-4o", "gpt-35", "gpt-4o-mini"
	requested := "alias-1"
	ok, failed, limited := int64(200), int64(500), int64(429)
	usage := func(prompt, completion int64) *canonical.Value {
		return canonical.NewObjectOf(
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(completion)},
		)
	}
	records := []RecordParams{
		{ModelID: "m1", RequestedModelID: &requested, KeyName: "main", ProviderID: &openai,
			UpstreamModelID: &gpt4o, CallerType: "local", Workspace: "teamA", StatusCode: &ok, APIFormat: "chat/completions",
			Usage: usage(100, 10), DurationMS: 100, FirstTokenMS: 10},
		{ModelID: "m1", KeyName: "backup", ProviderID: &openai, UpstreamModelID: &gpt4o,
			CallerType: "access_key", AccessKeyID: "ak1", Workspace: "teamA", StatusCode: &ok,
			APIFormat: "messages", Usage: usage(50, 0), DurationMS: 50, FirstTokenMS: 5},
		{ModelID: "m2", KeyName: "main", ProviderID: &azure, UpstreamModelID: &gpt4o,
			CallerType: "access_key", AccessKeyID: "ak2", Workspace: "teamB", StatusCode: &failed,
			APIFormat: "chat/completions", Usage: usage(30, 0), DurationMS: 30},
		{ModelID: "m2", KeyName: "backup", ProviderID: &azure, UpstreamModelID: &gpt35,
			CallerType: "workspace", Workspace: "teamB", StatusCode: &ok,
			APIFormat: "responses", Usage: usage(20, 0), DurationMS: 20, FirstTokenMS: 2},
		{ModelID: "m3", KeyName: "legacy", CallerType: "local", Usage: usage(7, 0), DurationMS: 7},
		{ModelID: "m1", KeyName: "main", ProviderID: &openai, UpstreamModelID: &gpt4oMini,
			CallerType: "access_key", AccessKeyID: "ak1", StatusCode: &limited,
			APIFormat: "chat/completions", Usage: usage(5, 0), DurationMS: 5},
	}
	for _, record := range records {
		recordKeyUsage(t, store, record)
	}
}

// TestDimensionFiltersOrAndAcrossReadings 断言每个新维度的筛选语义，以及它们与旧维度、
// 彼此之间的 AND。
//
// 每个用例同时校验 FilteredMetrics 的 snapshot / series / requests 三份读数（同窗口，合计
// 必须相等）与 filters 回显——回显必须原样带回调用方给的条件。
func TestDimensionFiltersOrAndAcrossReadings(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	installClock(t, beijingTime(t, moment))
	store := tempStore(t)
	seedDimensions(t, store)

	yes, no := true, false
	hours := 24.0
	cases := []struct {
		name string
		dims Dimensions
		want int64
	}{
		{"不筛", Dimensions{}, 6},
		{"模型单值", Dimensions{ModelIDs: []string{"m1"}}, 3},
		{"模型多值 OR", Dimensions{ModelIDs: []string{"m1", "m2"}}, 5},
		{"上游 Key 单对", Dimensions{UpstreamKeys: []UpstreamKey{{"openai", "main"}}}, 2},
		{"上游 Key 多对是成对 OR 而非笛卡尔积",
			Dimensions{UpstreamKeys: []UpstreamKey{{"openai", "main"}, {"azure", "backup"}}}, 3},
		{"上游 Key 不跨供应商匹配同名 Key", Dimensions{UpstreamKeys: []UpstreamKey{{"azure", "legacy"}}}, 0},
		{"工作空间单值", Dimensions{Workspaces: []string{"teamA"}}, 2},
		{"工作空间多值 OR（无归属的行不算进任何空间）", Dimensions{Workspaces: []string{"teamA", "teamB"}}, 4},
		{"调用方档位单值", Dimensions{CallerTypes: []string{"access_key"}}, 3},
		{"调用方档位多值 OR", Dimensions{CallerTypes: []string{"local", "workspace"}}, 3},
		{"只看成功", Dimensions{Success: &yes}, 3},
		{"只看失败（NULL 状态码也是失败）", Dimensions{Success: &no}, 3},
		{"模型 AND 失败", Dimensions{ModelIDs: []string{"m1"}, Success: &no}, 1},
		{"工作空间 AND 调用方档位", Dimensions{Workspaces: []string{"teamA"}, CallerTypes: []string{"access_key"}}, 1},
		{"上游 Key AND 访问密钥",
			Dimensions{UpstreamKeys: []UpstreamKey{{"openai", "main"}}, AccessKeyIDs: []string{"ak1"}}, 1},
		{"七个维度同时给值", Dimensions{
			ProviderIDs: []string{"openai"}, AccessKeyIDs: []string{"ak1"}, ModelIDs: []string{"m1"},
			UpstreamKeys: []UpstreamKey{{"openai", "backup"}}, Workspaces: []string{"teamA"},
			CallerTypes: []string{"access_key"}, Success: &yes,
		}, 1},
		{"相互矛盾的条件给空", Dimensions{CallerTypes: []string{"local"}, AccessKeyIDs: []string{"ak1"}}, 0},
	}
	for _, testCase := range cases {
		value, err := store.FilteredMetrics(FilteredMetricsParams{
			Hours: &hours, BucketSeconds: 3600, Limit: 200, Dimensions: testCase.dims,
		})
		if err != nil {
			t.Fatalf("%s: FilteredMetrics: %v", testCase.name, err)
		}
		if got := snapshotTotalRequests(t, value); got != testCase.want {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := seriesRequests(t, value); got != testCase.want {
			t.Errorf("%s: series 合计 = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got := requestItems(t, value); int64(got) != testCase.want {
			t.Errorf("%s: requests.items = %d，期望 %d", testCase.name, got, testCase.want)
		}
		if got, want := compact(t, value.Lookup("filters")), compact(t, dimensionsEcho(testCase.dims)); got != want {
			t.Errorf("%s: filters 回显 = %s，期望 %s", testCase.name, got, want)
		}
	}
}

// TestDimensionsEchoShape 锁定 filters 回显的键序与未设时的取值。
func TestDimensionsEchoShape(t *testing.T) {
	if got := compact(t, dimensionsEcho(Dimensions{})); got !=
		`{"provider_ids":[],"access_key_ids":[],"model_ids":[],"upstream_keys":[],`+
			`"workspaces":[],"caller_types":[],"success":null}` {
		t.Errorf("空回显 = %s", got)
	}
	no := false
	got := compact(t, dimensionsEcho(Dimensions{
		ProviderIDs: []string{"openai"}, UpstreamKeys: []UpstreamKey{{"openai", "a/b|c"}},
		CallerTypes: []string{"workspace"}, Success: &no,
	}))
	want := `{"provider_ids":["openai"],"access_key_ids":[],"model_ids":[],` +
		`"upstream_keys":[{"provider_id":"openai","key_name":"a/b|c"}],` +
		`"workspaces":[],"caller_types":["workspace"],"success":false}`
	if got != want {
		t.Errorf("回显 = %s\n期望 %s", got, want)
	}
}

// TestKeyUsageNewDimensions 断言 KeyUsage 的别名写法与其它读数同一口径。
func TestKeyUsageNewDimensions(t *testing.T) {
	installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
	store := tempStore(t)
	seedDimensions(t, store)

	no := false
	cases := []struct {
		name         string
		dims         Dimensions
		wantUpstream []string
		wantAccess   []string
		wantUnattrib int64
	}{
		{"模型", Dimensions{ModelIDs: []string{"m2"}}, []string{"azure/backup", "azure/main"}, []string{"ak2"}, 0},
		{"工作空间", Dimensions{Workspaces: []string{"teamA"}}, []string{"openai/backup", "openai/main"}, []string{"ak1"}, 0},
		{"上游 Key", Dimensions{UpstreamKeys: []UpstreamKey{{"openai", "main"}}}, []string{"openai/main"}, []string{"ak1"}, 0},
		{"调用方档位", Dimensions{CallerTypes: []string{"local"}}, []string{"openai/main"}, nil, 1},
		{"失败", Dimensions{Success: &no}, []string{"azure/main", "openai/main"}, []string{"ak1", "ak2"}, 1},
	}
	for _, testCase := range cases {
		value, err := store.KeyUsage(KeyUsageParams{Dimensions: testCase.dims})
		if err != nil {
			t.Fatalf("%s: KeyUsage: %v", testCase.name, err)
		}
		upstream := make([]string, 0)
		for _, row := range value.Lookup("upstream_keys").Arr {
			upstream = append(upstream, fieldOf(t, row, "provider_id")+"/"+fieldOf(t, row, "key_name"))
		}
		if strings.Join(upstream, ",") != strings.Join(testCase.wantUpstream, ",") {
			t.Errorf("%s: upstream_keys = %v，期望 %v", testCase.name, upstream, testCase.wantUpstream)
		}
		access := make([]string, 0)
		for _, row := range value.Lookup("access_keys").Arr {
			access = append(access, fieldOf(t, row, "access_key_id"))
		}
		if strings.Join(access, ",") != strings.Join(testCase.wantAccess, ",") {
			t.Errorf("%s: access_keys = %v，期望 %v", testCase.name, access, testCase.wantAccess)
		}
		if got := statRequests(t, value.Lookup("unattributed")); got != testCase.wantUnattrib {
			t.Errorf("%s: unattributed.requests = %d，期望 %d", testCase.name, got, testCase.wantUnattrib)
		}
	}
}

// TestStatusMonitorNewDimensions 断言状态监控的四份读数一并按新维度收窄，且失败状态码
// 与 success 维度的并存是「成功里没有失败状态码」而不是报错或忽略。
func TestStatusMonitorNewDimensions(t *testing.T) {
	installClock(t, beijingTime(t, statusMonitorMoment))
	store := tempStore(t)
	seedDimensions(t, store)

	yes, no := true, false
	cases := []struct {
		name         string
		dims         Dimensions
		wantTotal    int64
		wantFailures string
	}{
		{"不筛", Dimensions{}, 6, `{"none":1,"429":1,"500":1}`},
		{"只看成功", Dimensions{Success: &yes}, 3, `{}`},
		{"只看失败", Dimensions{Success: &no}, 3, `{"none":1,"429":1,"500":1}`},
		{"模型", Dimensions{ModelIDs: []string{"m1"}}, 3, `{"429":1}`},
		{"工作空间", Dimensions{Workspaces: []string{"teamB"}}, 2, `{"500":1}`},
	}
	for _, testCase := range cases {
		value := statusMonitorValue(t, store, StatusMonitorParams{Hours: 24, Dimensions: testCase.dims})
		if got := snapshotTotalRequests(t, value); got != testCase.wantTotal {
			t.Errorf("%s: snapshot.total.requests = %d，期望 %d", testCase.name, got, testCase.wantTotal)
		}
		if got := statusMatrixRequests(t, value); got != testCase.wantTotal {
			t.Errorf("%s: 矩阵合计 = %d，期望 %d", testCase.name, got, testCase.wantTotal)
		}
		if got := compact(t, value.Lookup("failure_status_codes")); got != testCase.wantFailures {
			t.Errorf("%s: failure_status_codes = %s，期望 %s", testCase.name, got, testCase.wantFailures)
		}
		if got, want := compact(t, value.Lookup("filters")), compact(t, dimensionsEcho(testCase.dims)); got != want {
			t.Errorf("%s: filters 回显 = %s，期望 %s", testCase.name, got, want)
		}
	}
}

// TestWorkspaceIndexCreated 断言工作空间旁挂表的查找索引存在，而 request_metrics 的索引
// 仍是参照实现那 7 条（旁挂表可以加对象，主表不行）。
func TestWorkspaceIndexCreated(t *testing.T) {
	store := tempStore(t)
	var count int64
	if err := store.db().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_request_workspace'" +
			" AND tbl_name = 'request_workspace'",
	).Scan(&count); err != nil {
		t.Fatalf("查询 sqlite_master: %v", err)
	}
	if count != 1 {
		t.Errorf("idx_request_workspace 条目数 = %d，期望 1", count)
	}
	if err := store.db().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'request_metrics'" +
			" AND sql IS NOT NULL",
	).Scan(&count); err != nil {
		t.Fatalf("查询 sqlite_master: %v", err)
	}
	if count != int64(len(createIndexStatements)) {
		t.Errorf("request_metrics 的显式索引 = %d 条，期望 %d 条（不得新增）", count, len(createIndexStatements))
	}
}

// TestMetricDimensionsOptions 断言筛选器选项的形状、排序、NULL 处理与窗口。
func TestMetricDimensionsOptions(t *testing.T) {
	const moment = "2026-07-14T12:00:00+08:00"
	now := beijingTime(t, moment)
	store := tempStore(t)
	// 一行落在 2 小时前：1 小时窗口里不该出现它的取值。
	installClock(t, now.Add(-2*time.Hour))
	ok := int64(200)
	old := "old-provider"
	recordKeyUsage(t, store, RecordParams{
		ModelID: "m-old", KeyName: "k-old", ProviderID: &old, StatusCode: &ok,
		CallerType: "access_key", AccessKeyID: "ak-old", Workspace: "teamOld",
	})
	installClock(t, now)
	seedDimensions(t, store)

	value, err := store.MetricDimensions(MetricDimensionsParams{})
	if err != nil {
		t.Fatalf("MetricDimensions: %v", err)
	}
	keys := value.Obj.Keys()
	wantKeys := []string{"window", "providers", "models", "requested_models", "upstream_keys",
		"access_keys", "workspaces", "caller_types"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("顶层键 = %v，期望 %v", keys, wantKeys)
	}
	if from := value.Lookup("window").Lookup("from"); from.Kind != canonical.KindNull {
		t.Errorf("all_history 的 window.from = %s，期望 null", compact(t, from))
	}
	expect := map[string]string{
		// requests 降序、同量按 id 升序；NULL 供应商（m3 那行）不出现。
		"providers": `[{"id":"openai","requests":3},{"id":"azure","requests":2},{"id":"old-provider","requests":1}]`,
		"models":    `[{"id":"m1","requests":3},{"id":"m2","requests":2},{"id":"m-old","requests":1},{"id":"m3","requests":1}]`,
		// 第 1 行请求的是别名 alias-1，其余行的 requested_model_id 回填成 model_id。
		"requested_models": `[{"id":"m1","requests":2},{"id":"m2","requests":2},{"id":"alias-1","requests":1},` +
			`{"id":"m-old","requests":1},{"id":"m3","requests":1}]`,
		"upstream_keys": `[{"provider_id":"openai","key_name":"main","requests":2},` +
			`{"provider_id":"azure","key_name":"backup","requests":1},` +
			`{"provider_id":"azure","key_name":"main","requests":1},` +
			`{"provider_id":"old-provider","key_name":"k-old","requests":1},` +
			`{"provider_id":"openai","key_name":"backup","requests":1}]`,
		"access_keys": `[{"id":"ak1","name":null,"requests":2},{"id":"ak-old","name":null,"requests":1},` +
			`{"id":"ak2","name":null,"requests":1}]`,
		"workspaces": `[{"name":"teamA","requests":2},{"name":"teamB","requests":2},{"name":"teamOld","requests":1}]`,
		// 三档恒在；同量按 id 升序。
		"caller_types": `[{"id":"access_key","requests":4},{"id":"local","requests":2},{"id":"workspace","requests":1}]`,
	}
	for _, key := range wantKeys[1:] {
		if got := compact(t, value.Lookup(key)); got != expect[key] {
			t.Errorf("%s =\n %s\n期望\n %s", key, got, expect[key])
		}
	}

	// 1 小时窗口：2 小时前那行的取值全部消失。
	hours := 1.0
	windowed, err := store.MetricDimensions(MetricDimensionsParams{Hours: &hours})
	if err != nil {
		t.Fatalf("MetricDimensions（1 小时）: %v", err)
	}
	if got := compact(t, windowed.Lookup("providers")); got != `[{"id":"openai","requests":3},{"id":"azure","requests":2}]` {
		t.Errorf("1 小时窗口 providers = %s", got)
	}
	if hoursValue := windowed.Lookup("window").Lookup("hours"); compact(t, hoursValue) != "1.0" {
		t.Errorf("window.hours = %s，期望 1.0", compact(t, hoursValue))
	}

	// 空库：caller_types 仍是三档零值，其余为空数组。
	empty, err := tempStore(t).MetricDimensions(MetricDimensionsParams{Hours: &hours})
	if err != nil {
		t.Fatalf("MetricDimensions（空库）: %v", err)
	}
	if got := compact(t, empty.Lookup("caller_types")); got !=
		`[{"id":"access_key","requests":0},{"id":"local","requests":0},{"id":"workspace","requests":0}]` {
		t.Errorf("空库 caller_types = %s", got)
	}
	if got := compact(t, empty.Lookup("models")); got != `[]` {
		t.Errorf("空库 models = %s，期望 []", got)
	}

	zero := 0.0
	if _, err := store.MetricDimensions(MetricDimensionsParams{Hours: &zero}); err == nil {
		t.Error("hours=0 应当报错")
	}
}
