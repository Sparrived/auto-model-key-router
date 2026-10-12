package unifiedmodel

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
	"github.com/Sparrived/auto-model-key-router/internal/configops"
)

// mustValue 解析 JSON 字面量。
func mustValue(t *testing.T, raw string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(raw)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", raw, err)
	}
	return value
}

// writeBaseline 写出与 Python 探测脚本等价的基线配置。
//
// 刻意用 config.EmptyConfigDict() 在 Go 侧现场构造，而**不**内联 Python 探测脚本
// dump 出来的那串 JSON：那份 dump 里含 empty_config_dict() 生成的机器相关绝对路径
// （Windows 的 AppData 用户目录），内联进仓库既不可移植，也等于把本地 profile 路径
// 提交进去。两侧只要用同一组 API 调用，结果就一致。
func writeBaseline(t *testing.T, path string) {
	t.Helper()
	data, err := config.EmptyConfigDict()
	if err != nil {
		t.Fatalf("构造空配置失败: %v", err)
	}
	if _, err := configops.CreateProvider(data, "p1", "https://up.example", configops.CreateProviderOptions{}); err != nil {
		t.Fatalf("建供应商失败: %v", err)
	}
	if _, err := configops.CreateProviderKey(data, "p1", "k1", "s1", configops.CreateProviderKeyOptions{}); err != nil {
		t.Fatalf("建 provider key 失败: %v", err)
	}
	for _, item := range []struct{ modelID, keyName, apiKey, upstream string }{
		{"m1", "k1", "s1", "upstream-1"},
		{"m2", "k2", "s2", "upstream-2"},
	} {
		key := mustValue(t, `{"name":"`+item.keyName+`","api_key":"`+item.apiKey+`","upstream_model":"`+item.upstream+`"}`)
		if err := configops.CreateModelWithKeys(data, item.modelID, configops.CreateModelOptions{}, []*canonical.Value{key}); err != nil {
			t.Fatalf("建模型 %s 失败: %v", item.modelID, err)
		}
	}
	data.Obj.Set("local_api_key", canonical.NewString("k"))
	if err := config.SaveConfigData(path, data); err != nil {
		t.Fatalf("写基线失败: %v", err)
	}
}

// unifiedModelJSON 读回磁盘配置里的 unified_model 字段。
//
// 用 LoadConfigData 而不是 Load：要的是**磁盘上的原始字段**，与 Python 探测脚本的
// load_config_data(p)["unified_model"] 同一视角。
func unifiedModelJSON(t *testing.T, path string) string {
	t.Helper()
	data, err := config.LoadConfigData(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	return canonical.DumpsOrdered(data.Lookup("unified_model"))
}

// newPath 返回临时目录下的配置路径并写好基线。
func newPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "router-config.json")
	writeBaseline(t, path)
	return path
}

func ptr(value string) *string { return &value }

// requireOperationError 断言 err 是 ConfigOperationError 且文案与状态码相符。
func requireOperationError(t *testing.T, err error, wantStatus int, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("应报错（期望 %q）", wantMessage)
	}
	var opErr *configops.ConfigOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("应为 ConfigOperationError，实际 %T: %v", err, err)
	}
	if opErr.StatusCode != wantStatus {
		t.Errorf("状态码应为 %d，实际 %d", wantStatus, opErr.StatusCode)
	}
	if opErr.Error() != wantMessage {
		t.Errorf("错误文案不符\n实际 %q\n期望 %q", opErr.Error(), wantMessage)
	}
}

// TestSwitchUnifiedTargetPersists 锁定单目标切换的落盘结果。
//
// 期望值来自真实 Python：unified_model.switch_unified_target(p,"default.primary","m1","mk1")
// 之后 load_config_data(p)["unified_model"] == {"default":{"primary":{"model":"m1"}}}。
// 注意**没有** key 字段——update_key 默认 False 时不写入 key，即使传了 key_name。
func TestSwitchUnifiedTargetPersists(t *testing.T) {
	path := newPath(t)
	if _, err := SwitchUnifiedTarget(path, "default.primary", ptr("m1"), ptr("k1"), false); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	want := `{"default":{"primary":{"model":"m1"}}}`
	if got := unifiedModelJSON(t, path); got != want {
		t.Fatalf("unified_model 不符\n实际 %s\n期望 %s", got, want)
	}
}

// TestSwitchUnifiedTargetUpdateKeyWritesKey 锁定 update_key=true 时确实写入 key。
//
// 实测 Python：switch_unified_model(..., update_key=True) 后
// unified_model == {"default":{"primary":{"model":"m1","key":"k1"}}}。
// 字段顺序是 model 在前、key 在后。
func TestSwitchUnifiedTargetUpdateKeyWritesKey(t *testing.T) {
	path := newPath(t)
	if _, err := SwitchUnifiedTarget(path, "default.primary", ptr("m1"), ptr("k1"), true); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	want := `{"default":{"primary":{"model":"m1","key":"k1"}}}`
	if got := unifiedModelJSON(t, path); got != want {
		t.Fatalf("unified_model 不符\n实际 %s\n期望 %s", got, want)
	}
}

// TestSwitchUnifiedTargetRejectsInvalidTarget 锁定非法目标的 422。
//
// 实测 Python 文案：ConfigOperationError("无效 unified 目标: bogus.primary",
// status_code=422)。
func TestSwitchUnifiedTargetRejectsInvalidTarget(t *testing.T) {
	path := newPath(t)
	_, err := SwitchUnifiedTarget(path, "bogus.primary", ptr("m1"), ptr("k1"), false)
	requireOperationError(t, err, 422, "无效 unified 目标: bogus.primary")
	if got := unifiedModelJSON(t, path); got != "null" {
		t.Fatalf("失败时 unified_model 应保持 null，实际 %s", got)
	}
}

// TestSwitchUnifiedModelLeavesImageUntouched 锁定"未传 image 就不动 image 槽位"。
//
// 实测 Python：只传 default 参数时 unified_model 为
// {"default":{"primary":{"model":"m1"}}}，image 槽位不存在。
func TestSwitchUnifiedModelLeavesImageUntouched(t *testing.T) {
	path := newPath(t)
	if _, err := SwitchUnifiedModel(path, ptr("m1"), ptr("k1"), false, nil, nil, false); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	want := `{"default":{"primary":{"model":"m1"}}}`
	if got := unifiedModelJSON(t, path); got != want {
		t.Fatalf("unified_model 不符\n实际 %s\n期望 %s", got, want)
	}
}

// TestSwitchUnifiedModelUpdatesBothSlots 锁定两个槽位在同一次提交里更新。
//
// 实测 Python：传 image_model_name/image_key_name 后 unified_model 为
// {"default":{"primary":{"model":"m1"}},"image":{"primary":{"model":"m2"}}}。
//
// 两个槽位必须在**同一次 mutation** 内应用（参照实现只调一次 ConfigService.update），
// 否则会出现「default 改了、image 失败」的半成品状态。这里通过断言最终状态同时包含
// 两者来锁定。
func TestSwitchUnifiedModelUpdatesBothSlots(t *testing.T) {
	path := newPath(t)
	if _, err := SwitchUnifiedModel(path, ptr("m1"), ptr("k1"), false, ptr("m2"), ptr("k2"), false); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	want := `{"default":{"primary":{"model":"m1"}},"image":{"primary":{"model":"m2"}}}`
	if got := unifiedModelJSON(t, path); got != want {
		t.Fatalf("unified_model 不符\n实际 %s\n期望 %s", got, want)
	}
}

// TestSwitchUnifiedModelImageModelOnlyIsEnough 锁定只给 image 模型也会更新 image 槽位。
//
// 实测 Python：image_model_name="m2"、image_key_name=None 时成功，且 image 槽位为
// {"primary":{"model":"m2"}}（无 key，因 update_image_key 默认 False）。这条证明判空
// 条件确实是「任一非 nil」而不是「两者都非 nil」。
func TestSwitchUnifiedModelImageModelOnlyIsEnough(t *testing.T) {
	path := newPath(t)
	if _, err := SwitchUnifiedModel(path, ptr("m1"), ptr("k1"), false, ptr("m2"), nil, false); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	want := `{"default":{"primary":{"model":"m1"}},"image":{"primary":{"model":"m2"}}}`
	if got := unifiedModelJSON(t, path); got != want {
		t.Fatalf("unified_model 不符\n实际 %s\n期望 %s", got, want)
	}
}

// TestSwitchUnifiedModelImageKeyOnlyFailsWhenImageSlotMissing 锁定只给 image key 的行为。
//
// 实测 Python：image_key_name="k2"、image_model_name=None 时报
// ConfigOperationError("尚未配置 image.primary，请先选择模型")，且磁盘完全未改动。
//
// 这条同时证明**确实进入了 image 分支**（否则不会出现与 image 相关的报错），以及
// 「任一槽位失败则整次提交中止、default 也不落盘」。
func TestSwitchUnifiedModelImageKeyOnlyFailsWhenImageSlotMissing(t *testing.T) {
	path := newPath(t)
	_, err := SwitchUnifiedModel(path, ptr("m1"), ptr("k1"), false, nil, ptr("k2"), false)
	requireOperationError(t, err, 422, "尚未配置 image.primary，请先选择模型")
	if got := unifiedModelJSON(t, path); got != "null" {
		t.Fatalf("任一槽位失败时整次提交应中止，unified_model 实际 %s", got)
	}
}

// TestSwitchUnifiedModelImageEmptyModelFails 锁定 image 模型传空串会真的尝试更新。
//
// 实测 Python：image_model_name="" （非 None）报
// ConfigOperationError("未配置模型或别名: ")，磁盘未改动。
//
// 意义在于判空用的是 `is not None` 而不是真值判断：空串表示"要改"，nil 表示"不要改"。
// 若误用真值判断，本意是「清空/不改 image」的调用会静默什么都不做。
func TestSwitchUnifiedModelImageEmptyModelFails(t *testing.T) {
	path := newPath(t)
	_, err := SwitchUnifiedModel(path, ptr("m1"), ptr("k1"), false, ptr(""), nil, false)
	requireOperationError(t, err, 404, "未配置模型或别名: ")
	if got := unifiedModelJSON(t, path); got != "null" {
		t.Fatalf("失败时不应落盘任何槽位，实际 %s", got)
	}
}

// TestSwitchUnifiedTargetReturnsNewConfig 验证返回值是落盘后的新配置。
func TestSwitchUnifiedTargetReturnsNewConfig(t *testing.T) {
	path := newPath(t)
	routerConfig, err := SwitchUnifiedTarget(path, "default.primary", ptr("m1"), ptr("k1"), false)
	if err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if routerConfig == nil || routerConfig.UnifiedModel == nil {
		t.Fatal("应返回带 unified model 的变更后配置")
	}
}
