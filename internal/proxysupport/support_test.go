package proxysupport

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// pythonConfigJSON 与 _gp3.py 里喂给参照实现的配置一致。
const pythonConfigJSON = `{
  "config_version": 4, "local_api_key": "x",
  "providers": {"p": {"base_url": "https://up.example", "api_key": "k",
                      "keys": [{"name": "k1", "api_key": "s"}]}},
  "models": {"m1": {"provider": "p", "upstream_model": "u", "reasoning_effort": "medium"}}
}`

// mustValue 解析 JSON 字面量，失败即终止。
func mustValue(t *testing.T, raw string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(raw)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", raw, err)
	}
	return value
}

// pythonConfig 构造与参照实现等价的配置。
func pythonConfig(t *testing.T) *config.RouterConfig {
	t.Helper()
	routerConfig, err := config.FromDict(mustValue(t, pythonConfigJSON))
	if err != nil {
		t.Fatalf("构造配置失败: %v", err)
	}
	return routerConfig
}

// TestRequestRouteKindMatchesPython 锁定路径分类。
func TestRequestRouteKindMatchesPython(t *testing.T) {
	cases := map[string]string{
		"chat/completions":   "default",
		"messages":           "default",
		"responses":          "default",
		"images/generations": "image",
		"images/edits":       "image",
		"embeddings":         "embeddings",
		"models":             "default",
		"":                   "default",
		"foo":                "default",
		// 大小写敏感：路径来自 URL，不会自动规范化。
		"IMAGES/GENERATIONS": "default",
	}
	for path, want := range cases {
		if got := RequestRouteKind(path); got != want {
			t.Errorf("RequestRouteKind(%q) = %q，期望 %q", path, got, want)
		}
	}
}

// TestUpstreamModeMatchesPython 锁定路径到上游方言的映射。
func TestUpstreamModeMatchesPython(t *testing.T) {
	cases := map[string]string{
		"chat/completions":   "openai",
		"messages":           "anthropic",
		"responses":          "responses",
		"images/generations": "images",
		"images/edits":       "images",
		"embeddings":         "embeddings",
		"models":             "",
		"":                   "",
	}
	for path, want := range cases {
		if got := UpstreamMode(path); got != want {
			t.Errorf("UpstreamMode(%q) = %q，期望 %q", path, got, want)
		}
	}
}

// TestUpstreamPathMatchesPython 逐条对齐路径选择（无自定义路由）。
//
// 期望值来自对 _upstream_path 的实测调用。分支顺序敏感：`messages` 是否带 model
// 字段会决定上游是 Anthropic 原生端点还是 OpenAI 兼容端点。
func TestUpstreamPathMatchesPython(t *testing.T) {
	cases := []struct {
		path     string
		native   bool
		hasModel bool
		want     string
	}{
		{"chat/completions", false, false, "v1/chat/completions"},
		{"chat/completions", false, true, "v1/chat/completions"},
		{"chat/completions", true, false, "v1/chat/completions"},
		{"chat/completions", true, true, "v1/chat/completions"},
		{"messages", false, false, "v1/messages"},
		// 带 model 说明要转换，上游是 OpenAI 兼容端点。
		{"messages", false, true, "v1/chat/completions"},
		{"messages", true, false, "v1/messages"},
		{"messages", true, true, "v1/messages"},
		{"responses", false, false, "v1/responses"},
		{"responses", false, true, "v1/chat/completions"},
		{"responses", true, false, "v1/responses"},
		{"responses", true, true, "v1/responses"},
		{"images/generations", false, true, "v1/images/generations"},
		{"images/generations", true, true, "v1/images/generations"},
		{"images/edits", false, true, "v1/images/edits"},
		{"images/edits", true, true, "v1/images/generations"},
		{"embeddings", false, true, "v1/embeddings"},
		{"embeddings", true, true, "v1/embeddings"},
		{"models", false, false, "v1/models"},
		{"models", true, true, "v1/models"},
	}
	for _, item := range cases {
		payload := mustValue(t, `{}`)
		if item.hasModel {
			payload = mustValue(t, `{"model":"m"}`)
		}
		got, err := UpstreamPath(item.path, payload, item.native, map[string]string{})
		if err != nil {
			t.Fatalf("UpstreamPath(%q) 报错: %v", item.path, err)
		}
		if got != item.want {
			t.Errorf("UpstreamPath(%q, native=%v, model=%v) = %q，期望 %q",
				item.path, item.native, item.hasModel, got, item.want)
		}
	}
}

// TestUpstreamPathUsesConfiguredRoutes 对齐配置了自定义路由时的结果。
func TestUpstreamPathUsesConfiguredRoutes(t *testing.T) {
	routes := map[string]string{
		"openai":     "v1/custom/chat",
		"anthropic":  "v1/custom/msg",
		"responses":  "v1/custom/resp",
		"images":     "v1/custom/img",
		"embeddings": "v1/custom/emb",
	}
	cases := []struct {
		path     string
		native   bool
		hasModel bool
		want     string
	}{
		{"chat/completions", false, true, "v1/custom/chat"},
		{"messages", false, false, "v1/messages"},
		{"messages", false, true, "v1/custom/chat"},
		{"messages", true, false, "v1/custom/msg"},
		{"responses", false, false, "v1/responses"},
		{"responses", false, true, "v1/custom/chat"},
		{"responses", true, false, "v1/custom/resp"},
		{"images/generations", false, true, "v1/custom/img"},
		{"embeddings", false, true, "v1/custom/emb"},
	}
	for _, item := range cases {
		payload := mustValue(t, `{}`)
		if item.hasModel {
			payload = mustValue(t, `{"model":"m"}`)
		}
		got, err := UpstreamPath(item.path, payload, item.native, routes)
		if err != nil {
			t.Fatalf("UpstreamPath(%q) 报错: %v", item.path, err)
		}
		if got != item.want {
			t.Errorf("UpstreamPath(%q, native=%v, model=%v) = %q，期望 %q",
				item.path, item.native, item.hasModel, got, item.want)
		}
	}
}

// TestUpstreamPathImagesEditsHonorsConfiguredRoute 锁定修复后的 images/edits 路由行为。
//
// 这是**与参照实现有意分叉**的一处（产品决策：修掉真实缺陷）。参照实现在非 native
// 时不查配置路由、直接拼 "v1/images/edits"，使 upstream_routes 里配的 images 路径
// 对 /v1/images/edits 静默无效。
//
// 修复后的语义分两种情形：
//  1. 显式配置了 images 路由 -> 用它（这就是用户期望的"配置生效"）；
//  2. **没有**配置 -> 仍是 "v1/images/edits"，而不是套用 images 的默认路由
//     "v1/images/generations"。第 2 条尤其重要：若照搬默认值，未配置路由的用户会把自己
//     的图片**编辑**请求发到图片**生成**端点——那比原来的缺陷更危险。
func TestUpstreamPathImagesEditsHonorsConfiguredRoute(t *testing.T) {
	payload := mustValue(t, `{"model":"m"}`)

	// 情形 1：配置了 images 路由 -> 生效。
	configured := map[string]string{"images": "v1/custom/img"}
	got, err := UpstreamPath("images/edits", payload, false, configured)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/custom/img" {
		t.Fatalf("非 native 的 images/edits 应使用已配置的 images 路由，实际 %q", got)
	}

	// 情形 2：未配置 -> 保持 edits 自己的默认路径，**不能**变成 generations。
	got, err = UpstreamPath("images/edits", payload, false, map[string]string{})
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/images/edits" {
		t.Fatalf("未配置路由时 images/edits 应保持 v1/images/edits，实际 %q", got)
	}
	if got == "v1/images/generations" {
		t.Fatal("未配置路由时绝不能把 edits 改道到 generations 端点")
	}

	// 空串配置视为未配置（与 config.UpstreamRoutePath 对空值的处理一致）。
	got, err = UpstreamPath("images/edits", payload, false, map[string]string{"images": ""})
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/images/edits" {
		t.Fatalf("images 路由为空串时应退回 v1/images/edits，实际 %q", got)
	}

	// native 形态不受影响：它本来就走查配置的分支，未配置时落到 images 默认路由。
	got, err = UpstreamPath("images/edits", payload, true, configured)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/custom/img" {
		t.Fatalf("native 的 images/edits 应使用已配置的 images 路由，实际 %q", got)
	}
	got, err = UpstreamPath("images/edits", payload, true, map[string]string{})
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/images/generations" {
		t.Fatalf("native 的 images/edits 未配置时应落到 images 默认路由，实际 %q", got)
	}

	// generations 的行为不受本次修复影响。
	got, err = UpstreamPath("images/generations", payload, false, configured)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if got != "v1/custom/img" {
		t.Fatalf("images/generations 应使用已配置的 images 路由，实际 %q", got)
	}
}

// TestJoinURLMatchesPython 锁定 URL 拼接（两侧斜杠数量都不敏感）。
func TestJoinURLMatchesPython(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"https://api.openai.com", "v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com///", "///v1/x", "https://api.openai.com/v1/x"},
		// 空 base 保留前导斜杠，不是朴素拼接。
		{"", "v1/x", "/v1/x"},
		{"https://x.com", "", "https://x.com/"},
	}
	for _, item := range cases {
		if got := JoinURL(item.base, item.path); got != item.want {
			t.Errorf("JoinURL(%q, %q) = %q，期望 %q", item.base, item.path, got, item.want)
		}
	}
}

// TestUpstreamHeadersMatchesPython 锁定请求头过滤与补充。
//
// 与响应侧方向**相反**（见 TestResponseHeadersMatchesPython）：这里保留原始大小写，
// 且同大小写下重复出现时**后者胜**。依据是对参照实现的实测——Starlette 的
// Headers.items() 逐个产出原始对（不合并），参照实现用 dict 推导式收集，于是
// 同大小写覆盖、不同大小写各自成为独立的键：
//
//	raw=[(X-Multi,first),(x-multi,second)]
//	  -> {'X-Multi':'first','x-multi':'second'}   （两个键都存活）
//
// 把请求侧与响应侧当成同一种行为，正是本包最初的错误来源，故两处各有一个测试。
func TestUpstreamHeadersMatchesPython(t *testing.T) {
	client := map[string][]string{
		"Authorization":     {"Bearer client"},
		"Host":              {"amkr.local"},
		"Content-Length":    {"10"},
		"Destination-Addr":  {"x"},
		"Accept-Encoding":   {"gzip"},
		"X-Api-Key":         {"ck"},
		"Anthropic-Version": {"2023-06-01"},
		"Anthropic-Beta":    {"b"},
		"Content-Type":      {"application/json"},
		"X-Custom":          {"keep"},
		"User-Agent":        {"ua"},
		// AMKR 自己的路由头，不该外泄（Go 侧新增，参照实现没有）。
		"X-Amkr-Workspace": {"teamA"},
	}
	got := UpstreamHeaders(client, "up-key")
	want := map[string]string{
		"Content-Type":    "application/json",
		"X-Custom":        "keep",
		"User-Agent":      "ua",
		"Authorization":   "Bearer up-key",
		"Accept-Encoding": "identity",
	}
	if len(got) != len(want) {
		t.Fatalf("头部数量不符: 期望 %d，实际 %d (%v)", len(want), len(got), got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("头部 %q = %q，期望 %q", key, got[key], value)
		}
	}
	// 剔除是大小写不敏感的。
	for _, blocked := range []string{"Host", "Content-Length", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta", "Destination-Addr", "X-Amkr-Workspace"} {
		if _, exists := got[blocked]; exists {
			t.Errorf("头部 %q 应被剔除", blocked)
		}
	}
	// 键保留原始大小写。
	if _, exists := got["Content-Type"]; !exists {
		t.Error("请求头应保留原始大小写（Content-Type 不应被小写化）")
	}
	// 同大小写的重复头后者胜（不是第一个）。
	dup := UpstreamHeaders(map[string][]string{"X-Multi": {"first", "second", "third"}}, "k")
	if dup["X-Multi"] != "third" {
		t.Fatalf("同大小写的重复请求头应后者胜，实际 %q", dup["X-Multi"])
	}
	// 不同大小写是**两个独立的键**，各自存活（对齐参照实现的 dict 推导式）。
	mixed := UpstreamHeaders(map[string][]string{"X-Multi": {"first"}, "x-multi": {"second"}}, "k")
	if mixed["X-Multi"] != "first" || mixed["x-multi"] != "second" {
		t.Fatalf("不同大小写的同名头应各自存活，实际 %v", mixed)
	}
	// 空客户端头也要补齐两个必需头。
	empty := UpstreamHeaders(nil, "k")
	if len(empty) != 2 || empty["Authorization"] != "Bearer k" || empty["Accept-Encoding"] != "identity" {
		t.Fatalf("空客户端头应只补两个必需头，实际 %v", empty)
	}
}

// TestJSONBodyMatchesPython 锁定请求体解析的宽容策略。
func TestJSONBodyMatchesPython(t *testing.T) {
	empty := JSONBody(nil)
	if !empty.IsObject() || empty.Len() != 0 {
		t.Fatal("空体应返回空对象")
	}
	// 非对象与坏 JSON 都退化为空对象，而不是报错。
	for _, raw := range []string{`[1,2]`, `notjson`, `"str"`, `null`, `123`} {
		value := JSONBody([]byte(raw))
		if !value.IsObject() || value.Len() != 0 {
			t.Errorf("输入 %q 应返回空对象，实际 %v", raw, value)
		}
	}
	// 合法对象原样解析。
	value := JSONBody([]byte(`{"a":1}`))
	if value.Lookup("a") == nil {
		t.Fatal("合法对象应保留字段")
	}
}

// TestResolveModelIDMatchesPython 锁定模型 ID 提取，重点区分「空串」与「不存在」。
func TestResolveModelIDMatchesPython(t *testing.T) {
	cases := []struct {
		path    string
		payload string
		want    string
		wantOK  bool
	}{
		// models 路径表示"列出模型"，无需模型，返回存在的空串。
		{"models", `{}`, "", true},
		{"models", `{"model":"x"}`, "", true},
		{"chat/completions", `{"model":"gpt-4"}`, "gpt-4", true},
		// 以下都是"不存在"，走默认路由。
		{"chat/completions", `{}`, "", false},
		{"chat/completions", `{"model":""}`, "", false},
		{"chat/completions", `{"model":null}`, "", false},
		{"chat/completions", `{"model":0}`, "", false},
		{"chat/completions", `{"model":[]}`, "", false},
		// 非空非字符串会被 str() 化。
		{"chat/completions", `{"model":123}`, "123", true},
	}
	for _, item := range cases {
		got, ok := ResolveModelID(item.path, mustValue(t, item.payload))
		if got != item.want || ok != item.wantOK {
			t.Errorf("ResolveModelID(%q, %s) = (%q, %v)，期望 (%q, %v)",
				item.path, item.payload, got, ok, item.want, item.wantOK)
		}
	}
}

// TestResolveModelIDDefaultsDecisionEndpoints 锁定 Go 侧新增的那处分叉：decide /
// classify 没有可用的 model 时回落到默认模型名，而不是"没有模型"。
//
// 为什么不并进 TestResolveModelIDMatchesPython：那个表的价值就在于**逐条对齐 Python**，
// 往里塞 Go 侧新增行为会让"对齐"这个断言失去意义——下一个人照着 Python 核对这张表时，
// 会把这几行读成"参照实现也这样"。因此这里单独一张表，名字本身说明它是分叉。
func TestResolveModelIDDefaultsDecisionEndpoints(t *testing.T) {
	cases := []struct {
		path    string
		payload string
		want    string
		wantOK  bool
	}{
		// 规范调用：这两个端点不带 model。
		{"decide", `{}`, decisionEndpointModel, true},
		{"classify", `{}`, decisionEndpointModel, true},
		// 假值（空串 / null / 0 / 空数组）与"没有 model"在 Python 语义下等价，同样回落。
		{"decide", `{"model":""}`, decisionEndpointModel, true},
		{"decide", `{"model":null}`, decisionEndpointModel, true},
		{"classify", `{"model":[]}`, decisionEndpointModel, true},
		// 显式给了 model 就按它走，默认名不参与——这是"带 model 时按指定模型路由"。
		{"decide", `{"model":"custom-laya"}`, "custom-laya", true},
		{"classify", `{"model":123}`, "123", true},
		// 其它路径不受影响：仍然报"没有模型"，调用方照旧收到 400。
		{"chat/completions", `{}`, "", false},
		{"messages", `{"model":null}`, "", false},
		{"embeddings", `{}`, "", false},
		// 前缀相近但不是这两个端点：不能靠 strings.HasPrefix 之类的宽松判断放行。
		{"decide/extra", `{}`, "", false},
		{"v1/decide", `{}`, "", false},
	}
	for _, item := range cases {
		got, ok := ResolveModelID(item.path, mustValue(t, item.payload))
		if got != item.want || ok != item.wantOK {
			t.Errorf("ResolveModelID(%q, %s) = (%q, %v)，期望 (%q, %v)",
				item.path, item.payload, got, ok, item.want, item.wantOK)
		}
	}
}

// TestSplitRequestedModelKeyMatchesPython 锁定 `model[key]` 拆解。
func TestSplitRequestedModelKeyMatchesPython(t *testing.T) {
	cases := []struct {
		input   string
		want    string
		wantKey string
		wantOK  bool
	}{
		{"gpt-4", "gpt-4", "", false},
		{"gpt-4[key1]", "gpt-4", "key1", true},
		{"gpt-4[ key1 ]", "gpt-4", "key1", true},
		// 贪婪匹配：取**最后**一个方括号组。
		{"a[b][c]", "a[b]", "c", true},
		// 空方括号不匹配（内层要求 1 个以上字符）。
		{"a[]", "a[]", "", false},
		{"[k]", "[k]", "", false},
		{"gpt-4[ключ]", "gpt-4", "ключ", true},
		// 首尾空白**不**被 strip：`.` 能匹配空格，整体匹配成功但模型名带空格。
		{"  gpt-4[x]  ", "  gpt-4[x]  ", "", false},
		// 嵌套方括号不匹配。
		{"a[[b]]", "a[[b]]", "", false},
		{"a[ b c ]", "a", "b c", true},
		{"模型[名字]", "模型", "名字", true},
	}
	for _, item := range cases {
		got, key, ok := SplitRequestedModelKey(item.input)
		if got != item.want || key != item.wantKey || ok != item.wantOK {
			t.Errorf("SplitRequestedModelKey(%q) = (%q, %q, %v)，期望 (%q, %q, %v)",
				item.input, got, key, ok, item.want, item.wantKey, item.wantOK)
		}
	}
}

// TestIsStreamRequestMatchesPython 锁定流式判断的严格性。
//
// 用 `is True` 而非真值判断：1、"true" 都**不是**流式。静默当成流式会让下游拿到
// SSE 却按 JSON 解析。
func TestIsStreamRequestMatchesPython(t *testing.T) {
	cases := map[string]bool{
		`{}`:                false,
		`{"stream":true}`:   true,
		`{"stream":false}`:  false,
		`{"stream":1}`:      false,
		`{"stream":"true"}`: false,
		`{"stream":null}`:   false,
		`{"stream":[]}`:     false,
	}
	for payload, want := range cases {
		if got := IsStreamRequest(mustValue(t, payload)); got != want {
			t.Errorf("IsStreamRequest(%s) = %v，期望 %v", payload, got, want)
		}
	}
}

// TestApplyTaskParamsMatchesPython 锁定任务参数覆盖（任务总是赢）。
func TestApplyTaskParamsMatchesPython(t *testing.T) {
	got := ApplyTaskParams(mustValue(t, `{"a":1,"b":2}`), mustValue(t, `{"b":9,"c":3}`))
	if text := canonical.DumpsOrdered(got); text != `{"a":1,"b":9,"c":3}` {
		t.Fatalf("合并结果不符: %s", text)
	}
	// nil / 空任务参数返回原对象。
	original := mustValue(t, `{"a":1}`)
	if ApplyTaskParams(original, canonical.NewNull()) != original {
		t.Fatal("无条件参数时应返回原对象")
	}
	if ApplyTaskParams(original, mustValue(t, `{}`)) != original {
		t.Fatal("空任务参数时应返回原对象")
	}
	// 不修改入参。
	payload := mustValue(t, `{"a":1}`)
	ApplyTaskParams(payload, mustValue(t, `{"a":99}`))
	if text := canonical.DumpsOrdered(payload); text != `{"a":1}` {
		t.Fatalf("不应修改入参，实际 %s", text)
	}
}

// TestTaskParamConflicts 锁定冲突检测的规则（reasoning_effort 的差异单列，见下）。
func TestTaskParamConflicts(t *testing.T) {
	cases := []struct {
		payload string
		task    string
		want    []string
	}{
		{`{"temperature":0.5}`, `{"temperature":1.0}`, []string{"temperature"}},
		{`{"temperature":0.5}`, `{"top_p":0.9}`, nil},
		{`{}`, `{"temperature":1.0}`, nil},
		{`{"temperature":0.5}`, `null`, nil},
		// Anthropic 方言：任务的 stop 对应载荷的 stop_sequences。
		{`{"stop_sequences":["a"]}`, `{"stop":["b"]}`, []string{"stop_sequences"}},
		{`{"stop":["a"]}`, `{"stop":["b"]}`, []string{"stop"}},
		// 不在任务里的载荷字段不算冲突。
		{`{"max_tokens":5}`, `{"temperature":1}`, nil},
		{`{"temperature":0.5}`, `{"temperature":1,"stop":["b"]}`, []string{"temperature"}},
		{`{"stop_sequences":["a"]}`, `{"stop":["b"],"top_p":0.5}`, []string{"stop_sequences"}},
		// max_tokens 与 stop 同理：任务固定了就拒绝调用方传。
		{`{"max_tokens":5}`, `{"max_tokens":32}`, []string{"max_tokens"}},
		// Responses 方言里输出上限叫 max_output_tokens，视为同一个参数。
		// 检查必须发生在归一化之前：归一化后 max_output_tokens 已变成 max_tokens，
		// 那时再看载荷就什么都看不到了。
		{`{"max_output_tokens":5}`, `{"max_tokens":32}`, []string{"max_output_tokens"}},
		// 两个都传也只报告一次冲突（键不同，报告各自的那个）。
		{`{"max_tokens":5,"max_output_tokens":6}`, `{"max_tokens":32}`, []string{"max_tokens"}},
		// 任务没固定 max_tokens 时，调用方随便传。
		{`{"max_output_tokens":5}`, `{"temperature":1}`, nil},
	}
	for _, item := range cases {
		got := TaskParamConflicts(mustValue(t, item.payload), mustValue(t, item.task))
		if len(got) != len(item.want) {
			t.Errorf("TaskParamConflicts(%s, %s) = %v，期望 %v", item.payload, item.task, got, item.want)
			continue
		}
		for i := range got {
			if got[i] != item.want[i] {
				t.Errorf("TaskParamConflicts(%s, %s) = %v，期望 %v", item.payload, item.task, got, item.want)
				break
			}
		}
	}
}

// TestTaskParamConflictsRejectsCallerReasoningEffort 记录一处**与参照实现的有意差异**。
//
// 参照实现（proxy_support.py:147）把 reasoning_effort 排除在冲突之外：客户端框架常自动
// 带上它，静默覆盖即可。Go 侧刻意不例外——任务路由是给别的 AI 服务用的，不是给 Agent
// 用的：调用方显式传了任务已固定的 reasoning_effort 时必须和 temperature 一样被明确拒绝
// （400），否则「我传的值没生效」这种沉默的错配会一直藏着。
//
// 与既有用例不冲突：原先录制的 handler 与上游夹具里没有任何一条任务用例由
// 调用方传 reasoning_effort（唯一涉及它的那条是 Python 侧用例，Go 侧从未回放），
// 因此这是纯语义增补，不是行为回归。
func TestTaskParamConflictsRejectsCallerReasoningEffort(t *testing.T) {
	cases := []struct {
		payload string
		task    string
	}{
		{`{"reasoning_effort":"low"}`, `{"reasoning_effort":"high"}`},
		{`{"reasoning_effort":"high"}`, `{"reasoning_effort":"high"}`},
		// 值与任务完全相同也算冲突：判断的是「调用方有没有传」，不是「值是否一致」。
		{`{"reasoning_effort":"minimal"}`, `{"reasoning_effort":"minimal"}`},
	}
	for _, item := range cases {
		got := TaskParamConflicts(mustValue(t, item.payload), mustValue(t, item.task))
		if len(got) != 1 || got[0] != "reasoning_effort" {
			t.Errorf("TaskParamConflicts(%s, %s) = %v，期望 [reasoning_effort]",
				item.payload, item.task, got)
		}
	}

	// 任务没固定 reasoning_effort 时，调用方照常透传。
	if got := TaskParamConflicts(
		mustValue(t, `{"reasoning_effort":"low"}`), mustValue(t, `{"temperature":1}`)); got != nil {
		t.Errorf("任务未固定 reasoning_effort 时不应冲突，实得 %v", got)
	}
}

// TestIsToolErrorMatchesPython 锁定工具相关错误检测。
func TestIsToolErrorMatchesPython(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{`{"error":{"message":"tool not supported"}}`, true},
		{`{"error":{"message":"Function calling failed"}}`, true},
		{`{"error":{"param":"tools"}}`, true},
		{`{"error":{"param":"functions[0]"}}`, true},
		{`{"error":{"message":"unrelated"}}`, false},
		{`{"error":"notadict"}`, false},
		{`notjson`, false},
		{`[1,2]`, false},
		{``, false},
		// 大小写不敏感。
		{`{"error":{"message":"TOOL ERROR"}}`, true},
	}
	for _, item := range cases {
		if got := IsToolError([]byte(item.content)); got != item.want {
			t.Errorf("IsToolError(%q) = %v，期望 %v", item.content, got, item.want)
		}
	}
}

// TestFilterFunctionToolsMatchesPython 锁定工具过滤。
func TestFilterFunctionToolsMatchesPython(t *testing.T) {
	cases := []struct{ input, want string }{
		// 只有 function 类型且函数名非空的保留。
		{`{"tools":[{"type":"function","function":{"name":"a"}}]}`,
			`{"tools":[{"type":"function","function":{"name":"a"}}]}`},
		{`{"tools":[{"type":"function","function":{"name":""}}]}`, `{"tools":[]}`},
		{`{"tools":[{"type":"function","function":{}}]}`, `{"tools":[]}`},
		{`{"tools":[{"type":"function"}]}`, `{"tools":[]}`},
		{`{"tools":[{"type":"other","function":{"name":"a"}}]}`, `{"tools":[]}`},
		{`{"tools":["notadict",{"type":"function","function":{"name":"b"}}]}`,
			`{"tools":[{"type":"function","function":{"name":"b"}}]}`},
		{`{"tools":[]}`, `{"tools":[]}`},
		// 非列表原样保留：上游可能接受别的形态，擅自改写会改变语义。
		{`{"tools":"notalist"}`, `{"tools":"notalist"}`},
		{`{}`, `{}`},
		// 其它字段不动。
		{`{"tools":[{"type":"function","function":{"name":"a"}}],"model":"m"}`,
			`{"tools":[{"type":"function","function":{"name":"a"}}],"model":"m"}`},
	}
	for _, item := range cases {
		got := canonical.DumpsOrdered(FilterFunctionTools(mustValue(t, item.input)))
		if got != item.want {
			t.Errorf("FilterFunctionTools(%s)\n 实际 %s\n 期望 %s", item.input, got, item.want)
		}
	}
	// 不修改入参。
	original := mustValue(t, `{"tools":[{"type":"other","function":{"name":"a"}}]}`)
	FilterFunctionTools(original)
	if text := canonical.DumpsOrdered(original); text != `{"tools":[{"type":"other","function":{"name":"a"}}]}` {
		t.Fatalf("不应修改入参，实际 %s", text)
	}
}

// TestUnsupportedEndpointStatusCodes 锁定"端点不支持"的状态码集合。
func TestUnsupportedEndpointStatusCodes(t *testing.T) {
	got := map[int]bool{}
	for _, code := range UnsupportedEndpointStatusCodes {
		got[code] = true
	}
	for _, want := range []int{404, 405, 501} {
		if !got[want] {
			t.Errorf("状态码 %d 应被标记为不支持", want)
		}
	}
	if len(UnsupportedEndpointStatusCodes) != 3 {
		t.Fatalf("应恰好 3 个状态码，实际 %v", UnsupportedEndpointStatusCodes)
	}
}

// TestNativeEndpointSupportedMatchesPython 锁定探测判定。
//
// 只有 404/405/501 说明端点不存在；认证错误、参数错误都说明端点**存在**。
func TestNativeEndpointSupportedMatchesPython(t *testing.T) {
	for _, code := range []int{404, 405, 501} {
		supported, reason := NativeEndpointSupported(code)
		if supported || reason != "unsupported" {
			t.Errorf("状态码 %d 应判为不支持，实际 (%v, %q)", code, supported, reason)
		}
	}
	for _, code := range []int{200, 400, 401, 403, 429, 500, 502} {
		supported, reason := NativeEndpointSupported(code)
		if !supported || reason != "ok" {
			t.Errorf("状态码 %d 应判为支持（端点存在），实际 (%v, %q)", code, supported, reason)
		}
	}
}

// TestNativeEndpointProbeMatchesPython 锁定探测请求体与头部。
func TestNativeEndpointProbeMatchesPython(t *testing.T) {
	anthropic := canonical.DumpsOrdered(NativeEndpointProbeBody("anthropic", "m1"))
	want := `{"model":"m1","max_tokens":1,"messages":[{"role":"user","content":"test"}]}`
	if anthropic != want {
		t.Errorf("Anthropic 探测体不符\n 实际 %s\n 期望 %s", anthropic, want)
	}
	responses := canonical.DumpsOrdered(NativeEndpointProbeBody("responses", "m1"))
	wantResponses := `{"model":"m1","input":"test","max_output_tokens":1}`
	if responses != wantResponses {
		t.Errorf("Responses 探测体不符\n 实际 %s\n 期望 %s", responses, wantResponses)
	}
	// Anthropic 需要 anthropic-version；Responses 不需要。
	headers := NativeEndpointProbeHeaders("anthropic", "k")
	if headers["anthropic-version"] != "2023-06-01" || headers["Authorization"] != "Bearer k" {
		t.Errorf("Anthropic 探测头不符: %v", headers)
	}
	if _, exists := NativeEndpointProbeHeaders("responses", "k")["anthropic-version"]; exists {
		t.Error("Responses 探测头不应带 anthropic-version")
	}
}
