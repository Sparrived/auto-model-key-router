package configops

import (
	"errors"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// 本文件固化「参照实现的怪异行为」与「刻意不移植的部分」，每条都有名字。
//
// 这里只写三类断言：
//   - 不容易用别的方式表达的结构性事实（键顺序、指针身份、内部 helper 的语义差异）；
//   - 参照实现里看起来像 bug 但必须保留的行为（防止后人「顺手修好」）；
//   - Go 侧有意的偏离（必须显式记录，否则就是静默漂移）。

// TestNormalizeBaseURLMatchesURLSplit 固化 base_url 校验对 urllib.parse 的复刻。
//
// 这些期望值全部由本机 Python 3.12 实测得出（urlparse 的 scheme/netloc）。最值得
// 注意的是几个「Python 接受、net/url 会拒绝」的输入：Go 的 url.Parse 对
// `http://host:bad` 报 invalid port、对 `https://exa mple.com` 报 invalid character
// in host，而参照实现照收不误。为了逐字兼容，Go 侧手写了 scheme/netloc 提取。
func TestNormalizeBaseURLMatchesURLSplit(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "plain", input: "https://api.openai.com", want: "https://api.openai.com"},
		{name: "trailing_slashes", input: "https://api.openai.com///", want: "https://api.openai.com"},
		{name: "scheme_not_lowercased", input: "HTTPS://API.OpenAI.COM", want: "HTTPS://API.OpenAI.COM"},
		{name: "bad_port_text_accepted", input: "http://host:bad", want: "http://host:bad"},
		{name: "space_in_host_accepted", input: "https://exa mple.com", want: "https://exa mple.com"},
		{name: "ipv6_accepted", input: "http://[::1]", want: "http://[::1]"},
		{name: "userinfo_accepted", input: "https://user:pw@h.example", want: "https://user:pw@h.example"},
		{name: "query_cut_from_netloc", input: "https://x.example?y", want: "https://x.example?y"},
		{name: "leading_control_kept_in_result", input: "\x00https://x.example", want: "\x00https://x.example"},
		{name: "empty", input: "", wantErr: "base_url不能为空"},
		{name: "blank", input: "   ", wantErr: "base_url不能为空"},
		{name: "slash_only", input: "/", wantErr: "base_url 必须是 http 或 https URL"},
		{name: "no_scheme", input: "api.openai.com", wantErr: "base_url 必须是 http 或 https URL"},
		{name: "scheme_only", input: "https://", wantErr: "base_url 必须是 http 或 https URL"},
		{name: "ftp_rejected", input: "ftp://x.example", wantErr: "base_url 必须是 http 或 https URL"},
		{name: "opaque_rejected", input: "http:example.com", wantErr: "base_url 必须是 http 或 https URL"},
		{name: "unbalanced_bracket_is_value_error", input: "http://[::1", wantErr: "Invalid IPv6 URL"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(canonical.NewString(testCase.input))
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("期望报错 %q，实际返回 %q", testCase.wantErr, got)
				}
				if err.Error() != testCase.wantErr {
					t.Fatalf("错误文本期望 %q，实际 %q", testCase.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功，实际报错: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("期望 %q，实际 %q", testCase.want, got)
			}
		})
	}
}

// TestUnbalancedBracketIsNotConfigOperationError 固化「IPv6 括号不平衡」的错误分类。
//
// 参照实现抛的是裸 ValueError（urlsplit 内部），不是 ConfigOperationError，因此
// 对外是 500 而不是 400。Go 侧同样不能折叠成 ConfigOperationError。
func TestUnbalancedBracketIsNotConfigOperationError(t *testing.T) {
	_, err := NormalizeBaseURL(canonical.NewString("http://[::1"))
	if err == nil {
		t.Fatal("期望报错")
	}
	var opErr *ConfigOperationError
	if errors.As(err, &opErr) {
		t.Fatal("unbalanced bracket 不该是 ConfigOperationError")
	}
	var pyErr *PyError
	if !errors.As(err, &pyErr) || pyErr.TypeName != "ValueError" {
		t.Fatalf("期望 ValueError 语义，实际 %T: %v", err, err)
	}
}

// TestProviderRenameMovesItToTheEnd 固化 `d[new] = d.pop(old)` 的键顺序副作用。
//
// config_operations.py:133 用 pop 再赋值实现改名，于是被改名的供应商在 providers
// 里移到**末尾**。落盘字节因此与「原地改 ID」不同，必须保留。
func TestProviderRenameMovesItToTheEnd(t *testing.T) {
	data := mustParse(t, `{"providers":{"a":{"base_url":"https://a.example","keys":{}},`+
		`"b":{"base_url":"https://b.example","keys":{}}}}`)
	if _, err := UpdateProvider(data, "a", UpdateProviderOptions{NewID: StringPtr("c")}); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	providers, err := Providers(data)
	if err != nil {
		t.Fatalf("取 providers 失败: %v", err)
	}
	got := strings.Join(providers.Obj.Keys(), ",")
	if got != "b,c" {
		t.Fatalf("改名后键顺序期望 b,c，实际 %s", got)
	}
}

// TestUpdateModelKeyQualifiedNameRenamesProviderKey 固化「传带前缀的名字会改名」。
//
// config_operations.py:861 的 `actual = ... if new_name is not None else key_name`
// 用的是**调用方传入**的 key_name（可能是 `{provider}-{key}` 形式），不是 provider
// 里的裸名。于是「什么都不改」的一次调用会把 provider key 真的改名成
// `openai-k1`，并把 target 也改成同一个名字。看起来是 bug，但管理 API 依赖它来
// 区分「按带前缀名字操作」与「按裸名操作」。
func TestUpdateModelKeyQualifiedNameRenamesProviderKey(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"providers":{"openai":{"base_url":"https://api.openai.com",`+
		`"keys":{"k1":{"api_key":"s1"}}}},"models":{"m1":{"targets":[`+
		`{"provider":"openai","key":"k1","upstream_model":"gpt-4o"}]}}}`)
	actual, err := UpdateModelKey(data, "m1", "openai-k1", UpdateModelKeyOptions{})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if actual != "openai-k1" {
		t.Fatalf("期望返回 openai-k1，实际 %q", actual)
	}
	provider, err := RequireProvider(data, "openai")
	if err != nil {
		t.Fatalf("取供应商失败: %v", err)
	}
	keys, err := ProviderKeys(provider)
	if err != nil {
		t.Fatalf("取 keys 失败: %v", err)
	}
	if keys.Obj.Has("k1") || !keys.Obj.Has("openai-k1") {
		t.Fatalf("期望 provider key 被改名成 openai-k1，实际 %v", keys.Obj.Keys())
	}
}

// TestCreateProviderFailureStillCreatesProvidersKey 固化失败路径的非原子性。
//
// config_operations.py:91 先 setdefault providers 再校验 base_url，因此 base_url
// 非法时配置里已经多了一个空 providers 对象。管理 API 会把它存下来。
func TestCreateProviderFailureStillCreatesProvidersKey(t *testing.T) {
	data := mustParse(t, `{}`)
	_, err := CreateProvider(data, "p", "not-a-url", CreateProviderOptions{})
	if err == nil {
		t.Fatal("期望报错")
	}
	if !data.Obj.Has("providers") {
		t.Fatal("期望失败后 data 里已有 providers")
	}
	providers, _ := Providers(data)
	if providers.Obj.Len() != 0 {
		t.Fatalf("期望 providers 为空对象，实际 %v", providers.Obj.Keys())
	}
}

// TestUpdateProviderMissingBaseURLIsBareValueError 固化一个 500 而不是 400 的分支。
//
// config_operations.py:114 在改 base_url 时先读旧值并要求非空。旧值缺失时
// normalize_upstream_base_url 抛裸 ValueError，**没有**被包成
// ConfigOperationError，因此管理 API 会按 500 处理。这是可观察行为，不能「顺手
// 修好」成 422。
func TestUpdateProviderMissingBaseURLIsBareValueError(t *testing.T) {
	data := mustParse(t, `{"providers":{"p":{"keys":{}}}}`)
	_, err := UpdateProvider(data, "p", UpdateProviderOptions{BaseURL: StringPtr("https://x.example")})
	if err == nil {
		t.Fatal("期望报错")
	}
	if err.Error() != "upstream_routes 上游 URL 不能为空" {
		t.Fatalf("错误文本不符: %q", err.Error())
	}
	var opErr *ConfigOperationError
	if errors.As(err, &opErr) {
		t.Fatal("这条例外不该是 ConfigOperationError")
	}
}

// TestConfigOperationErrorExposesStatusCode 固化对外契约：errors.As 能取到状态码。
func TestConfigOperationErrorExposesStatusCode(t *testing.T) {
	data := mustParse(t, `{"providers":{"p":{"base_url":"https://a.example","keys":{}}}}`)
	_, err := CreateProvider(data, "p", "https://b.example", CreateProviderOptions{})
	var opErr *ConfigOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("期望 ConfigOperationError，实际 %T", err)
	}
	if opErr.StatusCode != 409 {
		t.Fatalf("状态码期望 409，实际 %d", opErr.StatusCode)
	}
	if opErr.Error() != "供应商已存在: p" {
		t.Fatalf("错误文本不符: %q", opErr.Error())
	}
	// 缺省状态码是 400（providers 必须是对象）。
	if _, err := Providers(mustParse(t, `{"providers":5}`)); err == nil {
		t.Fatal("期望报错")
	} else if !errors.As(err, &opErr) || opErr.StatusCode != 400 {
		t.Fatalf("期望 400，实际 %v", err)
	}
}

// TestTargetIdentityKeepsNoneWhileMergeIdentityFoldsIt 固化两处 str() 归一的差异。
//
// 两处看着都是「三元组去重」，但表达式不同：
//
//	models.go:534     str(t.get("upstream_model"))            → None 变成 "None"
//	transfer.go:1010  str(t.get("upstream_model") or model_id) → None 变成 model_id
//
// 用错会让去重结果漂移（例如两个都缺 upstream_model 的 target 本该视为同一条）。
func TestTargetIdentityKeepsNoneWhileMergeIdentityFoldsIt(t *testing.T) {
	target := mustParse(t, `{"provider":"p","key":"k"}`)
	if got := targetIdentity(target); !strings.Contains(got, "None") {
		t.Fatalf("targetIdentity 期望保留 \"None\"，实际 %q", got)
	}
	if got := mergeIdentity(target, "m"); strings.Contains(got, "None") {
		t.Fatalf("mergeIdentity 期望回落到模型 ID，实际 %q", got)
	} else if got != "p\x00k\x00m" {
		t.Fatalf("mergeIdentity 期望 p\\x00k\\x00m，实际 %q", got)
	}
}

// TestContainsVsIterationErrorTexts 固化两种 TypeError 文本。
//
// Python 里 `"x" in None` 与 `for x in None` 报的是**不同**的消息，参照实现两处
// 都用了，因此 Go 侧必须有两个 helper、不能互相替代。
func TestContainsVsIterationErrorTexts(t *testing.T) {
	if _, err := pyContains(nil, "x"); err == nil {
		t.Fatal("期望报错")
	} else if err.Error() != "argument of type 'NoneType' is not iterable" {
		t.Fatalf("in 的报错不符: %q", err.Error())
	}
	if _, err := canonical.PyIterate(nil); err == nil {
		t.Fatal("期望报错")
	} else if err.Error() != "'NoneType' object is not iterable" {
		t.Fatalf("for 的报错不符: %q", err.Error())
	}
	if _, err := pyContains(canonical.NewInt("5"), "x"); err == nil {
		t.Fatal("期望报错")
	} else if err.Error() != "argument of type 'int' is not iterable" {
		t.Fatalf("数字容器的报错不符: %q", err.Error())
	}
}

// TestUpdateModelDistinguishesAbsentFromEmptyAliases 固化 nil 与空切片的语义差别。
//
// 参照实现用 `aliases is not None` 判断，所以「不传」与「传空数组」不同：前者
// 保留原别称，后者真的清空。Go 侧用 nil / 非 nil 空切片复刻，绝不能都当成 nil。
func TestUpdateModelDistinguishesAbsentFromEmptyAliases(t *testing.T) {
	const fixture = `{"config_version":4,"models":{"m":{"aliases":["a"],"targets":[]}}}`

	kept := mustParse(t, fixture)
	if _, err := UpdateModel(kept, "m", UpdateModelOptions{}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if model := lookup(mustLookup(t, kept, "models"), "m"); canonical.DumpsOrdered(model) != `{"aliases":["a"],"targets":[]}` {
		t.Fatalf("nil 应保留别称，实际 %s", canonical.DumpsOrdered(model))
	}

	cleared := mustParse(t, fixture)
	if _, err := UpdateModel(cleared, "m", UpdateModelOptions{Aliases: []string{}}); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if model := lookup(mustLookup(t, cleared, "models"), "m"); canonical.DumpsOrdered(model) != `{"aliases":[],"targets":[]}` {
		t.Fatalf("空切片应清空别称，实际 %s", canonical.DumpsOrdered(model))
	}
}

// TestSetKeyServiceModelsCreatesModelsInCallerOrder 记录一处**有意偏离**参照实现。
//
// config_operations.py:412 的 desired 是 Python set，一次新增多个模型时写入
// models 的顺序由字符串哈希决定（进程间随机）。Go 侧按调用方给出的顺序去重后
// 依次创建——更稳定，但顺序可能与某一次 Python 运行不同。这条测试负责把偏离
// 钉住并说清原因。
func TestSetKeyServiceModelsCreatesModelsInCallerOrder(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"providers":{"p":{"base_url":"https://a.example",`+
		`"keys":{"k":{"api_key":"1"}}}},"models":{}}`)
	result, err := SetKeyServiceModels(data, "p", "k", []string{"z", "a", "m"})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if strings.Join(result.Added, ",") != "a,m,z" {
		t.Fatalf("added 期望升序 a,m,z，实际 %v", result.Added)
	}
	models, _ := Models(data)
	if got := strings.Join(models.Obj.Keys(), ","); got != "z,a,m" {
		t.Fatalf("models 键序期望按调用方顺序 z,a,m，实际 %s", got)
	}
}

// TestTransferableConfigOmitsMachineSettings 固化导出范围。
//
// 迁移出去的只有 providers / models / tasks：local_api_key、监听地址、超时都是
// 「本机」设置，跟着走会把对端实例的本地鉴权 key 改掉。
func TestTransferableConfigOmitsMachineSettings(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"local_api_key":"sk-local","host":"0.0.0.0","port":9000,`+
		`"providers":{"p":{"base_url":"https://a.example","_amkr_model_key_clone":true,`+
		`"keys":{"k":{"api_key":"1","capabilities":{"models":["x"]}}}}},`+
		`"models":{"m":{"targets":[{"provider":"p","key":"k","upstream_model":"u"}]}}}`)
	exported, err := TransferableConfig(data)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if got := strings.Join(exported.Obj.Keys(), ","); got != "config_version,providers,models" {
		t.Fatalf("导出键期望 config_version,providers,models，实际 %s", got)
	}
	text := canonical.DumpsOrdered(exported)
	for _, forbidden := range []string{"sk-local", "0.0.0.0", "9000", "_amkr_model_key_clone", "capabilities"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("导出内容不应包含 %q: %s", forbidden, text)
		}
	}
}

// TestTransferableConfigStripsWorkspaceCredentials 固化：配置导出摘掉**两把**空间凭据。
//
// 这条通道的导出文件会被贴进工单、聊天记录与文档，因此工作空间随它迁移，但凭据不能
// 跟着走。新增 inference_key 时最容易漏掉它——只摘 api_key 会让「导出不含凭据」这个
// 承诺在加字段后悄悄失效，而 inference_key 比 api_key 更敏感：面板 key 只开一个空间的
// 面板，推理 key 能直接消耗上游额度。
//
// 想搬凭据请走工作空间自己的整包迁移（那条通道**有意**带上 api_key）。
func TestTransferableConfigStripsWorkspaceCredentials(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"local_api_key":"sk-local",`+
		`"providers":{"p":{"base_url":"https://a.example","keys":{"k":{"api_key":"1"}}}},`+
		`"models":{"m":{"targets":[{"provider":"p","key":"k"}]}},`+
		`"workspaces":{"teamA":{"api_key":"amkr_ws_secret","inference_key":"amkr_ik_secret",`+
		`"models":["m"],"tasks":{"t":{"model":"m"}}}}}`)
	exported, err := TransferableConfig(data)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	text := canonical.DumpsOrdered(exported)
	for _, forbidden := range []string{"amkr_ws_secret", "amkr_ik_secret"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("导出内容不应包含凭据 %q: %s", forbidden, text)
		}
	}
	// 但空间本身与它的 models 授权要留下：它们不是凭据，丢掉会让导入方少一个空间。
	for _, wanted := range []string{"teamA", `"models"`} {
		if !strings.Contains(text, wanted) {
			t.Errorf("导出应保留 %q: %s", wanted, text)
		}
	}
}

// TestProviderRoutesKeepInputOrder 固化 routes 的键顺序。
//
// config.NormalizeUpstreamRoutes 返回 Go map（顺序丢失），而 provider["routes"]
// 会原样落盘；Go 侧因此重写了一份保序实现。顺序必须在「输入顺序」上，且被
// 跳过的条目（null / 空白）不占位。
func TestProviderRoutesKeepInputOrder(t *testing.T) {
	data := mustParse(t, `{"providers":{"p":{"base_url":"https://a.example","keys":{}}}}`)
	err := UpdateProvider2Routes(data, mustParse(t, `{"images":"v1/images","openai":null,"anthropic":"anthropic/v1/messages","embeddings":"   "}`))
	if err != nil {
		t.Fatalf("设置路由失败: %v", err)
	}
	provider, _ := RequireProvider(data, "p")
	routes := lookup(provider, "routes")
	if got := strings.Join(routes.Obj.Keys(), ","); got != "images,anthropic" {
		t.Fatalf("routes 键序期望 images,anthropic，实际 %s", got)
	}
}

// TestSwitchUnifiedTargetBlankKeyBecomesAbsentKey 记录一处**有意的归一化**。
//
// 参照实现里空白 key_name 会先变成空串再写进中间结构，最后由
// set_unified_model 规范成「没有 key」。Go 侧直接折成 nil，落盘结果一致；这条
// 测试保证「空白 key」不会写进配置。
func TestSwitchUnifiedTargetBlankKeyBecomesAbsentKey(t *testing.T) {
	data := mustParse(t, `{"config_version":4,"providers":{"p":{"base_url":"https://a.example",`+
		`"keys":{"k":{"api_key":"1"}}}},"models":{"m":{"targets":[`+
		`{"provider":"p","key":"k","upstream_model":"u"}]}}}`)
	if err := SwitchUnifiedTarget(data, "default.primary", StringPtr("m"), StringPtr("   "), true); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	unified := lookup(data, "unified_model")
	target := lookup(lookup(unified, "default"), "primary")
	if target.Obj.Has("key") {
		t.Fatalf("空白 key 不应落盘，实际 %s", canonical.DumpsOrdered(unified))
	}
}

// TestPyStrOrEmptyDiffersForFalsyValues 固化 `str(x)` 与 `str(x or "")` 的差别。
//
// 两个 helper 分别复刻这两种表达式，混用会让 identity 去重与错误文本漂移。
func TestPyStrOrEmptyDiffersForFalsyValues(t *testing.T) {
	cases := []struct {
		text      string
		wantPyStr string
		wantEmpty string
	}{
		{text: "null", wantPyStr: "None", wantEmpty: ""},
		{text: "0", wantPyStr: "0", wantEmpty: ""},
		{text: "false", wantPyStr: "False", wantEmpty: ""},
		{text: `""`, wantPyStr: "", wantEmpty: ""},
		{text: `"x"`, wantPyStr: "x", wantEmpty: "x"},
		{text: `[1]`, wantPyStr: "[1]", wantEmpty: "[1]"},
	}
	for _, testCase := range cases {
		value := mustParse(t, testCase.text)
		if got := pyStrOf(value); got != testCase.wantPyStr {
			t.Errorf("str(%s) 期望 %q，实际 %q", testCase.text, testCase.wantPyStr, got)
		}
		if got := value.StringValue(); got != testCase.wantEmpty {
			t.Errorf("str(%s or \"\") 期望 %q，实际 %q", testCase.text, testCase.wantEmpty, got)
		}
	}
}

// mustParse 解析一段 JSON，失败即终止测试。
func mustParse(t *testing.T, text string) *canonical.Value {
	t.Helper()
	value, err := canonical.ParseString(text)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", text, err)
	}
	return value
}

// mustLookup 取对象的成员，缺失即终止测试。
func mustLookup(t *testing.T, value *canonical.Value, key string) *canonical.Value {
	t.Helper()
	child, ok := value.LookupOK(key)
	if !ok {
		t.Fatalf("缺少成员 %s", key)
	}
	return child
}

// UpdateProvider2Routes 只是把「设置 routes」这一步写短一点，供键序测试使用。
func UpdateProvider2Routes(data, routes *canonical.Value) error {
	_, err := UpdateProvider(data, "p", UpdateProviderOptions{Routes: routes, UpdateRoutes: true})
	return err
}
