package configops

import (
	"errors"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// cpaErrStatus 取 ConfigOperationError 的状态码；不是这个类型即失败。
func cpaErrStatus(t *testing.T, err error) int {
	t.Helper()
	var opErr *ConfigOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("期望 ConfigOperationError，实际 %v", err)
	}
	return opErr.StatusCode
}

// TestReplaceCPAInstancesNormalizes 钉住写入前的规范化：base_url 去尾斜杠、
// management_key 去空白、缺 label 时用 ID 兜底。
func TestReplaceCPAInstancesNormalizes(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"local_api_key":"k"}`)
	incoming := mustParse(t, `{"cpa-a":{"base_url":"http://127.0.0.1:8317/","management_key":"  mk-1  "}}`)

	if err := ReplaceCPAInstances(data, incoming); err != nil {
		t.Fatalf("替换失败: %v", err)
	}

	stored, err := CPAInstances(data)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	entry := mustLookup(t, stored, "cpa-a")
	if got := mustLookup(t, entry, "base_url").StringValue(); got != "http://127.0.0.1:8317" {
		t.Errorf("base_url 未去尾斜杠: %q", got)
	}
	if got := mustLookup(t, entry, "management_key").StringValue(); got != "mk-1" {
		t.Errorf("management_key 未去空白: %q", got)
	}
	if got := mustLookup(t, entry, "label").StringValue(); got != "cpa-a" {
		t.Errorf("缺 label 时应用 ID 兜底，实际 %q", got)
	}
}

// TestReplaceCPAInstancesKeepsUnknownFields 钉住「不丢字段」：以后给实例加字段时，
// 旧版本保存一次配置不该把它抹掉。
func TestReplaceCPAInstancesKeepsUnknownFields(t *testing.T) {
	data := mustParse(t, `{"config_version":4}`)
	incoming := mustParse(t, `{"cpa-a":{"base_url":"https://cpa.example","management_key":"mk","future_field":{"n":1}}}`)

	if err := ReplaceCPAInstances(data, incoming); err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	stored, err := CPAInstances(data)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	entry := mustLookup(t, stored, "cpa-a")
	if got := canonical.Dumps(mustLookup(t, entry, "future_field")); got != `{"n":1}` {
		t.Errorf("未知字段丢失或改变: %s", got)
	}
}

// TestReplaceCPAInstancesRejectsBadInput 覆盖信任边界上的校验：客户端能提交什么，
// 这里就必须挡住什么。
func TestReplaceCPAInstancesRejectsBadInput(t *testing.T) {
	cases := []struct {
		name       string
		incoming   string
		wantStatus int
	}{
		{"不是对象", `[]`, 422},
		{"实例不是对象", `{"a":"http://x"}`, 422},
		{"base_url 不是字符串", `{"a":{"base_url":1,"management_key":"mk"}}`, 422},
		{"base_url 缺 scheme", `{"a":{"base_url":"cpa.example","management_key":"mk"}}`, 422},
		{"base_url 是非法子协议", `{"a":{"base_url":"ftp://cpa.example","management_key":"mk"}}`, 422},
		{"management_key 缺失", `{"a":{"base_url":"https://cpa.example"}}`, 422},
		{"management_key 是空白", `{"a":{"base_url":"https://cpa.example","management_key":"   "}}`, 422},
		{"去空白后 ID 重复", `{"b":{"base_url":"https://x.example","management_key":"mk"}," b ":{}}`, 409},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := mustParse(t, `{"config_version":4}`)
			err := ReplaceCPAInstances(data, mustParse(t, testCase.incoming))
			if err == nil {
				t.Fatal("期望失败，实际成功")
			}
			if got := cpaErrStatus(t, err); got != testCase.wantStatus {
				t.Errorf("期望状态码 %d，实际 %d（%v）", testCase.wantStatus, got, err)
			}
		})
	}
}

// TestReplaceCPAInstancesIsAtomicOnFailure 钉住「校验不过就不改配置」：失败后
// cpa_instances 要么保持原样、要么根本不存在，不能留下半份清单。
func TestReplaceCPAInstancesIsAtomicOnFailure(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"cpa_instances":{"old":{"base_url":"https://old.example","management_key":"mk"}}}`)
	incoming := mustParse(t, `{"ok":{"base_url":"https://ok.example","management_key":"mk"},"bad":{"base_url":"nope","management_key":"mk"}}`)

	if err := ReplaceCPAInstances(data, incoming); err == nil {
		t.Fatal("期望失败，实际成功")
	}
	stored, err := CPAInstances(data)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !stored.Obj.Has("old") || stored.Obj.Has("ok") {
		t.Errorf("失败时不应改动已有清单: %s", canonical.Dumps(stored))
	}
}

// TestCPAInstancesMissingKeyDoesNotCreateKey 钉住读路径不写配置：只看一眼账号资源
// 不该改动配置文件。
func TestCPAInstancesMissingKeyDoesNotCreateKey(t *testing.T) {
	data := mustParse(t, `{"config_version":4}`)
	instances, err := CPAInstances(data)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if instances.Obj.Len() != 0 {
		t.Errorf("缺失时应为空对象，实际 %s", canonical.Dumps(instances))
	}
	if data.Obj.Has(cpaInstancesKey) {
		t.Error("读路径不应写入 cpa_instances")
	}
}

// TestParseCPAInstancesSortedAndLenient 钉住扇出用的解析：顺序稳定（按 ID 排序），
// 且单个手改坏的实例不会把整个看板打掉。
func TestParseCPAInstancesSortedAndLenient(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"cpa_instances":{
		"zeta":{"base_url":"https://z.example/","management_key":" mk-z "},
		"alpha":{"label":"主力","base_url":"https://a.example","management_key":"mk-a"},
		"broken":"手改坏了"
	}}`)

	items, err := ParseCPAInstances(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("期望 3 个实例，实际 %d", len(items))
	}
	ids := []string{items[0].ID, items[1].ID, items[2].ID}
	want := []string{"alpha", "broken", "zeta"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("实例顺序应为 %v，实际 %v", want, ids)
		}
	}
	if items[0].Label != "主力" || items[0].BaseURL != "https://a.example" || items[0].ManagementKey != "mk-a" {
		t.Errorf("alpha 解析错误: %+v", items[0])
	}
	if items[1].BaseURL != "" || items[1].ManagementKey != "" {
		t.Errorf("坏实例应解析成空字段交给调用方报错: %+v", items[1])
	}
	if items[2].BaseURL != "https://z.example" || items[2].ManagementKey != "mk-z" {
		t.Errorf("zeta 未去空白或尾斜杠: %+v", items[2])
	}
}
