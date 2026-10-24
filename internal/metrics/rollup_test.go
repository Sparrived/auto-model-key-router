package metrics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// compact 把 canonical 值渲染成键序敏感的一行，用于逐字段比对。
func compact(t *testing.T, value *canonical.Value) string {
	t.Helper()
	return canonical.DumpsOrdered(value)
}

// parsePythonISO 解析 Python isoformat 的文本。
//
// Go 的 time.RFC3339 恰好能吃下 Python 的 "2026-07-14T12:00:00+08:00" 与带微秒的
// 形式。只用它解析（写入），不用它产出（产出必须走 formatISO）。
func parsePythonISO(t *testing.T, text string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatalf("解析时间 %q 失败: %v", text, err)
	}
	return parsed
}

// installClock 固定 nowBeijing 并在测试结束时还原。
func installClock(t *testing.T, moment time.Time) {
	t.Helper()
	previous := nowBeijing
	nowBeijing = func() time.Time { return moment }
	t.Cleanup(func() { nowBeijing = previous })
}

// tempStore 在一个临时目录里打开 store。
func tempStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "metrics.sqlite3"))
	if err != nil {
		t.Fatalf("打开 store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestSnapshotRollupMatchesGroupedQueries 用参照实现那条取数路径（_query_stats 的逐
// 分组查询 + _query_filtered_stats）校验上卷结果。
//
// 这里就地把两种取数路径的每个分组逐字段（含键序，用 DumpsOrdered）比对。上卷若在
// 合并口径、NULL 处理或键序上有偏差，都会在这里暴露：
//
//   - 合并口径：MIN/MAX 必须取极值而不是相加，status_codes 必须按状态码累加；
//   - NULL 处理：状态码为 NULL 的行要进主聚合但不进 status_codes；
//   - 键序：每个分组的键序必须与各自的 `ORDER BY dims` 一致（上卷只有一次扫描，
//     拿不到各分组自己的顺序，只能收尾重排）。
func TestSnapshotRollupMatchesGroupedQueries(t *testing.T) {
	// assertMatchesGrouped 比对一次窗口取数。
	assertMatchesGrouped := func(t *testing.T, store *Store, since, until *string) {
		t.Helper()
		groups, err := store.snapshotRollup(since, until, Dimensions{})
		if err != nil {
			t.Fatalf("上卷失败: %v", err)
		}

		cases := []struct {
			name        string
			dims        []string
			includeNull bool
			render      func(*statsResult) *canonical.Value
			got         *canonical.Value
		}{
			{"total", nil, true,
				func(result *statsResult) *canonical.Value { return result.get(dimKey{}).dict() },
				groups.total.get(dimKey{}).dict()},
			{"caller_types", []string{"caller_type"}, true, flatStats,
				flatStats(groups.callerTypes)},
			{"models", []string{"model_id"}, true, flatStats,
				flatStats(groups.models)},
			{"requested_models", []string{"requested_model_id"}, true, flatStats,
				flatStats(groups.requestedModels)},
			{"model_requested_models", []string{"model_id", "requested_model_id"}, true, nestedStats,
				nestedStats(groups.modelRequested)},
			{"keys", []string{"model_id", "key_name"}, true, nestedStats,
				nestedStats(groups.keys)},
			{"providers", []string{"provider_id"}, false, flatStats,
				flatStats(groups.providers)},
			{"provider_pools", []string{"provider_id", "pool_name"}, false, nestedStats,
				nestedStats(groups.providerPools)},
			{"upstream_models", []string{"upstream_model_id"}, false, flatStats,
				flatStats(groups.upstreamModels)},
		}
		for _, testCase := range cases {
			reference, err := store.queryStats(testCase.dims, since, until, testCase.includeNull, Dimensions{})
			if err != nil {
				t.Fatalf("%s: 逐分组查询失败: %v", testCase.name, err)
			}
			got, want := compact(t, testCase.got), compact(t, testCase.render(reference))
			if got != want {
				t.Errorf("%s 的上卷结果与逐分组查询不同:\n 上卷=%s\n 逐组=%s", testCase.name, got, want)
			}
		}

		// unattributed 走的是 _query_filtered_stats（attributed=false），单独比一次。
		attributedFalse := false
		filter := metricFilter{SinceCreatedAt: since, UntilCreatedAt: until, Attributed: &attributedFalse}
		unattributedWhere, unattributedParams := filter.sql()
		reference, err := store.queryFilteredStats(unattributedWhere, unattributedParams)
		if err != nil {
			t.Fatalf("unattributed: 逐分组查询失败: %v", err)
		}
		got := compact(t, groups.unattributed.get(dimKey{}).dict())
		if want := compact(t, reference.dict()); got != want {
			t.Errorf("unattributed 的上卷结果与逐分组查询不同:\n 上卷=%s\n 逐组=%s", got, want)
		}
	}

	t.Run("edge_cases", func(t *testing.T) {
		installClock(t, beijingTime(t, "2026-07-14T12:00:00+08:00"))
		store := tempStore(t)

		openai, anthropic := "openai", "anthropic"
		poolA, poolB := "pool-a", "pool-b"
		gpt4o, sonnet := "gpt-4o", "claude-3-5-sonnet"
		ok, throttled := int64(200), int64(429)

		usage := func(prompt int64) *canonical.Value {
			return canonical.NewObjectOf(
				canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
				canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(prompt / 2)},
			)
		}
		record := func(modelID, requested, key, caller string,
			provider, pool, upstream *string, status *int64, prompt, durationMS int64) {
			t.Helper()
			params := RecordParams{
				ModelID: modelID, KeyName: key, StatusCode: status,
				Usage: usage(prompt), CallerType: caller,
				RequestedModelID: &requested, ProviderID: provider, PoolName: pool,
				UpstreamModelID: upstream, DurationMS: durationMS, FirstTokenMS: durationMS / 10,
				Workspace: "teamA",
			}
			if err := store.Record(params); err != nil {
				t.Fatalf("写入指标 (%s/%s): %v", modelID, key, err)
			}
		}

		// 归属完整（三个归属列都非空）：进 providers / provider_pools / upstream_models。
		record("gpt-4o", "TASK_A", "k1", "local", &openai, &poolA, &gpt4o, &ok, 100, 900)
		record("gpt-4o", "TASK_A", "k1", "local", &openai, &poolA, &gpt4o, &throttled, 200, 30)
		record("gpt-4o", "TASK_B", "k2", "access_key", &openai, &poolB, &gpt4o, &ok, 300, 600)
		record("claude-3-5-sonnet", "TASK_A", "k1", "workspace", &anthropic, &poolA, &sonnet, &ok, 400, 120)
		// 归属不全（pool 为空）：三个归属列的口径要求全非空，因此只算未归属。
		record("claude-3-5-sonnet", "TASK_B", "k2", "access_key", &anthropic, nil, &sonnet, &ok, 500, 240)
		// 完全没有归属：只算未归属。
		record("gpt-4o", "TASK_B", "k3", "local", nil, nil, nil, &ok, 600, 60)
		// status_code 为 NULL：要进主聚合（requests/successes/failures），但不进
		// status_codes 字典。
		record("gpt-4o", "TASK_A", "k3", "local", &openai, &poolA, &gpt4o, nil, 700, 45)

		now := nowBeijing()
		dayAgo := formatISO(addHours(now, 24))
		nowText := formatISO(now)
		future := formatISO(addHours(now, -1))
		assertMatchesGrouped(t, store, &dayAgo, &nowText)
		assertMatchesGrouped(t, store, nil, &nowText)
		assertMatchesGrouped(t, store, &future, &nowText)
	})
}
