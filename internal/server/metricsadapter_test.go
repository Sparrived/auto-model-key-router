package server

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/metrics"
	"github.com/Sparrived/auto-model-key-router/internal/proxy"
	"github.com/Sparrived/auto-model-key-router/internal/runtime"
)

// openTestStore 打开一个临时指标库。
func openTestStore(t *testing.T) *metrics.Store {
	t.Helper()
	store, err := metrics.Open(t.TempDir() + "/metrics.sqlite3")
	if err != nil {
		t.Fatalf("打开指标库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestMetricsAdapterRecordFieldMapping 逐字段断言 proxy.MetricRecord ->
// metrics.RecordParams 的搬运。
//
// 为什么必须逐字段（而不是只跑一遍落库）：这个适配层没有任何业务逻辑，全部风险都在
// 「同名字段串位」与「nil 与零值混淆」上。落库后的字节对比能发现串位，但读不出是哪
// 一个字段错了；而 nil/零值的差异（StatusCode nil vs 0）会被 metrics 层的
// `failed = ... || status_code is None || status_code >= 400` 掩盖成同一个「失败」，
// 只有直接比对参数才能钉住。
func TestMetricsAdapterRecordFieldMapping(t *testing.T) {
	statusCode := 503
	providerID := "prov-a"
	poolName := "pool-x"
	upstreamModel := "gpt-upstream"
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewInt("7")},
	)
	record := proxy.MetricRecord{
		ModelID:          "model-a",
		KeyName:          "key-a",
		StatusCode:       &statusCode,
		Usage:            usage,
		Retried:          true,
		Failed:           true,
		DurationMS:       1234,
		FirstTokenMS:     56,
		RequestedModelID: "alias-a",
		CallerType:       "access_key",
		ProviderID:       &providerID,
		PoolName:         &poolName,
		UpstreamModelID:  &upstreamModel,
		ClientAddr:       "127.0.0.1:50874",
		UserAgent:        "claude-cli/1.0",
	}

	params := recordParams(record)

	if params.ModelID != "model-a" {
		t.Errorf("ModelID = %q", params.ModelID)
	}
	if params.KeyName != "key-a" {
		t.Errorf("KeyName = %q", params.KeyName)
	}
	if params.StatusCode == nil || *params.StatusCode != 503 {
		t.Errorf("StatusCode = %v，期望 503", params.StatusCode)
	}
	if params.Usage != usage {
		t.Errorf("Usage 未原样透传")
	}
	if !params.Retried || !params.Failed {
		t.Errorf("Retried/Failed = %v/%v，期望 true/true", params.Retried, params.Failed)
	}
	if params.DurationMS != 1234 || params.FirstTokenMS != 56 {
		t.Errorf("DurationMS/FirstTokenMS = %d/%d", params.DurationMS, params.FirstTokenMS)
	}
	if params.RequestedModelID == nil || *params.RequestedModelID != "alias-a" {
		t.Errorf("RequestedModelID = %v，期望 alias-a", params.RequestedModelID)
	}
	if params.CallerType != "access_key" {
		t.Errorf("CallerType = %q", params.CallerType)
	}
	if params.ProviderID == nil || *params.ProviderID != "prov-a" {
		t.Errorf("ProviderID = %v", params.ProviderID)
	}
	if params.PoolName == nil || *params.PoolName != "pool-x" {
		t.Errorf("PoolName = %v（v4 下恒为 nil，但接缝必须原样搬运）", params.PoolName)
	}
	if params.UpstreamModelID == nil || *params.UpstreamModelID != "gpt-upstream" {
		t.Errorf("UpstreamModelID = %v", params.UpstreamModelID)
	}
	if params.ClientAddr != "127.0.0.1:50874" {
		t.Errorf("ClientAddr = %q", params.ClientAddr)
	}
	if params.UserAgent != "claude-cli/1.0" {
		t.Errorf("UserAgent = %q", params.UserAgent)
	}
}

// TestMetricsAdapterPreservesNilOptionals 断言可选项的 nil 语义。
//
// StatusCode 为 nil 在参照实现里表示「上游请求直接失败、没有响应」，落库时
// status_code 必须是 NULL 而不是 0；空串形态的可选字段（RequestedModelID 等）在
// metrics.Store.Record 里与 nil 等价（它只在非空时覆盖），因此统一折叠成 nil。
func TestMetricsAdapterPreservesNilOptionals(t *testing.T) {
	params := recordParams(proxy.MetricRecord{ModelID: "model-a", KeyName: "key-a"})

	if params.StatusCode != nil {
		t.Errorf("StatusCode = %v，期望 nil", params.StatusCode)
	}
	if params.RequestedModelID != nil {
		t.Errorf("RequestedModelID = %v，期望 nil（空串折叠）", params.RequestedModelID)
	}
	if params.ProviderID != nil || params.PoolName != nil || params.UpstreamModelID != nil {
		t.Errorf("归属字段应保持 nil: %v/%v/%v",
			params.ProviderID, params.PoolName, params.UpstreamModelID)
	}
}

// TestMetricsAdapterRecordLandsInDatabase 走一遍真实落库，确认 nil StatusCode 写成
// NULL、非 nil 写成数值，并且 usage 真的被解析进了 token 列。
func TestMetricsAdapterRecordLandsInDatabase(t *testing.T) {
	store := openTestStore(t)
	adapter := newMetricsAdapter(store)

	statusCode := 200
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewInt("3")},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewInt("4")},
		canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewInt("7")},
	)
	if err := adapter.Record(context.Background(), proxy.MetricRecord{
		ModelID: "model-a", KeyName: "key-a", StatusCode: &statusCode,
		Usage: usage, DurationMS: 10, FirstTokenMS: 5,
		RequestedModelID: "alias-a", CallerType: "local",
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := adapter.Record(context.Background(), proxy.MetricRecord{
		ModelID: "model-a", KeyName: "key-b", Failed: true, DurationMS: 20,
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	db, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("打开校验连接失败: %v", err)
	}
	defer db.Close()

	var (
		statusCodeValue sql.NullInt64
		totalTokens     int64
		requestModel    string
	)
	if err := db.QueryRow(
		"SELECT status_code, total_tokens, requested_model_id FROM request_metrics ORDER BY id LIMIT 1",
	).Scan(&statusCodeValue, &totalTokens, &requestModel); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !statusCodeValue.Valid || statusCodeValue.Int64 != 200 {
		t.Errorf("status_code = %v，期望 200", statusCodeValue)
	}
	if totalTokens != 7 {
		t.Errorf("total_tokens = %d，期望 7", totalTokens)
	}
	if requestModel != "alias-a" {
		t.Errorf("requested_model_id = %q，期望 alias-a", requestModel)
	}

	var nilStatus sql.NullInt64
	if err := db.QueryRow(
		"SELECT status_code FROM request_metrics ORDER BY id DESC LIMIT 1",
	).Scan(&nilStatus); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if nilStatus.Valid {
		t.Errorf("没有响应时 status_code 应为 NULL，实际 %v", nilStatus.Int64)
	}
}

// TestMetricsAdapterRecordStreamMapping 断言 runtime.StreamOutcome 的搬运。
//
// 这条路径在生产上由 proxy 自己的 streamLifecycle 承担（proxy/lifecycle.go），但
// runtime.MetricsSink 接缝要求实现它，因此同样逐字段钉住。
func TestMetricsAdapterRecordStreamMapping(t *testing.T) {
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewInt("9")},
	)
	params := streamRecordParams(runtime.StreamOutcome{
		ModelID:          "model-a",
		KeyName:          "key-a",
		StatusCode:       200,
		Usage:            usage,
		Failed:           true,
		DurationMS:       120,
		FirstTokenMS:     30,
		RequestedModelID: "alias-a",
		CallerType:       "access_key",
		ProviderID:       "prov-a",
		PoolName:         "",
		UpstreamModelID:  "upstream-a",
	})

	if params.StatusCode == nil || *params.StatusCode != 200 {
		t.Errorf("StatusCode = %v，期望 200", params.StatusCode)
	}
	if params.Usage != usage {
		t.Errorf("Usage 未原样透传")
	}
	if !params.Failed {
		t.Errorf("Failed = false，期望 true")
	}
	if params.DurationMS != 120 || params.FirstTokenMS != 30 {
		t.Errorf("耗时 = %d/%d", params.DurationMS, params.FirstTokenMS)
	}
	if params.RequestedModelID == nil || *params.RequestedModelID != "alias-a" {
		t.Errorf("RequestedModelID = %v", params.RequestedModelID)
	}
	if params.CallerType != "access_key" {
		t.Errorf("CallerType = %q", params.CallerType)
	}
	if params.ProviderID == nil || *params.ProviderID != "prov-a" {
		t.Errorf("ProviderID = %v", params.ProviderID)
	}
	// 空串折叠成 nil（Python 的 None）：两者在 record() 里落库结果相同。
	if params.PoolName != nil {
		t.Errorf("PoolName = %v，期望 nil", params.PoolName)
	}
	if params.UpstreamModelID == nil || *params.UpstreamModelID != "upstream-a" {
		t.Errorf("UpstreamModelID = %v", params.UpstreamModelID)
	}
}

// TestCloseReleasesMetricsStore 断言关停真的释放了指标库（sqlite 句柄）。
//
// 对应 lifespan 的 finally（app.py:69-78）：`await runtime_manager.close()` 会逐代
// 关闭「没有被其它代共享」的连接池与指标库（runtime/manager.go 的 closeUnused）。
// 装配层的责任是让每一代持有**自己的**适配器——若所有代共用一个 sink，closeUnused
// 的共享检测会永远认为「还有人用」，指标库就永远不会关闭。
//
// 判据选「关停后再写一行必须失败」：metrics.Store.Close 幂等，光看返回值区分不出
// 「关过」与「没关过」。
func TestCloseReleasesMetricsStore(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, true, false)
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("载入配置失败: %v", err)
	}
	app, err := New(Options{ConfigPath: path, Config: loaded, Version: testVersion})
	if err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	adapter := app.currentMetricsAdapter()
	if adapter == nil {
		t.Fatal("当前代没有指标适配器")
	}
	if err := adapter.Record(context.Background(), proxy.MetricRecord{ModelID: "m", KeyName: "k"}); err != nil {
		t.Fatalf("关停前的写入应成功: %v", err)
	}

	if err := app.Close(); err != nil {
		t.Fatalf("关停失败: %v", err)
	}
	if err := adapter.Record(context.Background(), proxy.MetricRecord{ModelID: "m", KeyName: "k"}); err == nil {
		t.Error("关停后指标库应已关闭，写入必须失败")
	}
	// Close 幂等：再次调用不应 panic 或返回错误。
	if err := app.Close(); err != nil {
		t.Errorf("重复关停应无错误，实际 %v", err)
	}
}

// TestMetricsAdapterDelegatesActiveCounter 断言活跃计数落在**同一个** store 上：
// /v1/ 请求期间加一、结束后归零，且 /metrics 的 active_requests 能读到它。
//
// 对应 app.py:354 的 acquire_active 与 app.py:359/367 的 release_active。
func TestMetricsAdapterDelegatesActiveCounter(t *testing.T) {
	store := openTestStore(t)
	adapter := newMetricsAdapter(store)
	if got := store.ActiveCount(); got != 0 {
		t.Fatalf("初始活跃数 = %d，期望 0", got)
	}
	adapter.AcquireActive()
	if got := store.ActiveCount(); got != 1 {
		t.Errorf("AcquireActive 后活跃数 = %d，期望 1", got)
	}
	adapter.ReleaseActive()
	adapter.ReleaseActive() // 多释放一次不应变成负数（metrics.Store 已钳位）
	if got := store.ActiveCount(); got != 0 {
		t.Errorf("ReleaseActive 后活跃数 = %d，期望 0", got)
	}
}
