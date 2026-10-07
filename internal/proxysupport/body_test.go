package proxysupport

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// upBodyCase 是一条请求体构造用例。
//
// 所有 want 值都来自对参照实现 _upstream_body 的实测调用（脚本 _gp3.py），逐字节
// 粘贴，不是推导出来的。请求体构造是上游真正看到的东西，差一个字段就是静默的行为
// 差异。
type upBodyCase struct {
	name             string
	body             string
	payload          string
	modelID          string
	stream           bool
	native           bool
	reasoningModelID string
	taskParams       string
	path             string
	want             string
}

// upstreamBodyCases 覆盖参照实现实测矩阵。
func upstreamBodyCases() []upBodyCase {
	return []upBodyCase{
		// --- native 分支：按方言改名任务参数 ---
		{
			// Anthropic 的 messages 体只认 stop_sequences。
			name: "native 里 stop 改名为 stop_sequences", body: `{"model":"o","messages":[]}`,
			payload: `{"model":"o","messages":[]}`, modelID: "m1", native: true,
			taskParams: `{"stop":["a"]}`, path: "messages",
			want: `{"model":"m1","messages":[],"stop_sequences":["a"]}`,
		},
		{
			// 没有 messages 键时不做改名（不是 Anthropic 体）。
			name: "native 无 messages 时不改名", body: `{"model":"o"}`,
			payload: `{"model":"o"}`, modelID: "m1", native: true,
			taskParams: `{"stop":["a"]}`, path: "messages",
			want: `{"model":"m1","stop":["a"]}`,
		},
		{
			// 目标键已存在时只更新值，**不移动位置**。
			name: "native 覆盖已有 stop_sequences 且保留位置", body: `{"model":"o","messages":[],"stop_sequences":["z"]}`,
			payload: `{"model":"o","messages":[],"stop_sequences":["z"]}`, modelID: "m1", native: true,
			taskParams: `{"stop":["a"]}`, path: "messages",
			want: `{"model":"m1","messages":[],"stop_sequences":["a"]}`,
		},
		{
			// reasoning_effort 在原生体里没有对应概念，直接丢掉。
			name: "native 丢弃 reasoning_effort", body: `{"model":"o","messages":[]}`,
			payload: `{"model":"o","messages":[]}`, modelID: "m1", native: true,
			taskParams: `{"stop":["a"],"temperature":0.5,"reasoning_effort":"low"}`, path: "messages",
			want: `{"model":"m1","messages":[],"stop_sequences":["a"],"temperature":0.5}`,
		},

		// --- reasoning_effort 优先级 ---
		{
			// 模型级设置**覆盖**载荷里已有的值。
			name: "模型级 reasoning_effort 覆盖载荷", body: `{"model":"o"}`,
			payload: `{"model":"o","reasoning_effort":"low"}`, modelID: "m1", path: "chat/completions",
			want: `{"model":"m1","reasoning_effort":"medium"}`,
		},
		{
			// 模型级设置优先于 reasoning.effort。
			name: "模型级优先于 reasoning.effort", body: `{"model":"o"}`,
			payload: `{"model":"o","reasoning":{"effort":"high"}}`, modelID: "m1", path: "chat/completions",
			want: `{"model":"m1","reasoning_effort":"medium"}`,
		},
		{
			// reasoningModelID 覆盖了实际模型 ID，于是用另一个模型的设置。
			name: "reasoningModelID 决定取哪份设置", body: `{"model":"o"}`,
			payload: `{"model":"o","reasoning":{"effort":"high"}}`, modelID: "m1",
			reasoningModelID: "other", path: "chat/completions",
			want: `{"model":"m1","reasoning_effort":"high"}`,
		},

		// --- 任务参数覆盖 ---
		{
			name: "任务参数覆盖载荷同名字段", body: `{"model":"o","temperature":0.1}`,
			payload: `{"model":"o","temperature":0.1}`, modelID: "m1",
			taskParams: `{"temperature":0.9}`, path: "chat/completions",
			want: `{"model":"m1","temperature":0.9,"reasoning_effort":"medium"}`,
		},

		// --- 直通路径 ---
		{
			// 没有 model 字段：不是要代理的对话请求，原样返回。
			name: "无 model 字段时原样返回", body: `{"raw":1}`,
			payload: `{"raw":1}`, modelID: "m1", path: "chat/completions",
			want: `{"raw":1}`,
		},
		{
			name: "空载荷原样返回空体", body: ``, payload: `{}`, modelID: "m1", path: "chat/completions",
			want: ``,
		},
		{
			// model 为 null 时**仍会被改写**：参照实现的早退条件是
			// `not payload or "model" not in payload`，非空 dict 为真且键存在，
			// 因此继续走改写路径。只有**缺键**才直通。
			name: "model 为 null 时仍改写", body: `{"model":null}`,
			payload: `{"model":null}`, modelID: "m1", path: "chat/completions",
			want: `{"model":"m1","reasoning_effort":"medium"}`,
		},
		{
			// 缺 model 键才是真正的直通。
			name: "缺 model 键时原样返回", body: `{"prompt":"cat"}`,
			payload: `{"prompt":"cat"}`, modelID: "m1", path: "chat/completions",
			want: `{"prompt":"cat"}`,
		},

		// --- 流式 ---
		{
			name: "流式补 include_usage", body: `{"model":"o"}`,
			payload: `{"model":"o"}`, modelID: "m1", stream: true, path: "chat/completions",
			want: `{"model":"m1","reasoning_effort":"medium","stream_options":{"include_usage":true}}`,
		},
		{
			// 已有 stream_options 时**合并**（保留原字段）。
			name: "流式合并已有 stream_options", body: `{"model":"o"}`,
			payload: `{"model":"o","stream_options":{"x":1}}`, modelID: "m1", stream: true,
			path: "chat/completions",
			want: `{"model":"m1","stream_options":{"x":1,"include_usage":true},"reasoning_effort":"medium"}`,
		},
		{
			// 已有 include_usage:false 会被覆盖为 true。
			name: "流式覆盖 include_usage=false", body: `{"model":"o"}`,
			payload: `{"model":"o","stream_options":{"include_usage":false}}`, modelID: "m1", stream: true,
			path: "chat/completions",
			want: `{"model":"m1","stream_options":{"include_usage":true},"reasoning_effort":"medium"}`,
		},

		// --- embeddings：只换 model，不做方言转换 ---
		{
			// 走 AdaptMessagePayload 会把 input 当成 Responses 的 input 改写成
			// messages，上游于是收到没有 input 的 chat 请求。
			name: "embeddings 保留 input 不做转换", body: `{"model":"o","input":["a"]}`,
			payload: `{"model":"o","input":["a"],"encoding_format":"float"}`, modelID: "m1",
			path: "embeddings",
			want: `{"model":"m1","input":["a"],"encoding_format":"float"}`,
		},
		{
			name: "embeddings 字符串 input", body: `{"model":"o","input":"hi"}`,
			payload: `{"model":"o","input":"hi"}`, modelID: "m1", path: "embeddings",
			want: `{"model":"m1","input":"hi"}`,
		},

		// --- 透传端点族：只换 model，字节不动 ---
		{
			// 图像、语音、视频、重排的请求体是它们自己的规范形态，AMKR 只替换 model。
			// 这里**与本次改动前有意分叉**：旧实现把 images/generations 当对话路径处理，
			// 会凭空注入模型级的 reasoning_effort，并删掉 metadata/store/include 这类
			// 字段。图像端点不认识 reasoning_effort，上游可能直接 400。
			name: "images 只换 model 不注入 reasoning_effort", body: `{"model":"o","prompt":"cat"}`,
			payload: `{"model":"o","prompt":"cat"}`, modelID: "m1", path: "images/generations",
			want: `{"model":"m1","prompt":"cat"}`,
		},
		{
			// 语音合成（TTS）：`input` 是待朗读的文本。旧实现会把带 input 的体当成
			// Responses 请求改写成 messages，上游于是收到一个没有 input 的 chat 请求，
			// 用户看到的是「明明调 tts 却像在调对话」。这条用例就是那个缺陷的回归锁。
			name: "audio/speech 保留 input 不做方言转换", body: `{"model":"o","input":"hello","voice":"alloy"}`,
			payload: `{"model":"o","input":"hello","voice":"alloy","response_format":"mp3"}`,
			modelID: "m1", path: "audio/speech",
			want: `{"model":"m1","input":"hello","voice":"alloy","response_format":"mp3"}`,
		},
		{
			// 视频生成：prompt 与对话无关，且要保留 metadata 这类上游偶有使用的字段。
			name: "videos 保留 metadata", body: `{"model":"o","prompt":"a cat"}`,
			payload: `{"model":"o","prompt":"a cat","metadata":{"trace":"t1"}}`,
			modelID: "m1", path: "videos",
			want: `{"model":"m1","prompt":"a cat","metadata":{"trace":"t1"}}`,
		},
		{
			name: "rerank 保留 query/documents", body: `{"model":"o","query":"q"}`,
			payload: `{"model":"o","query":"q","documents":["a","b"]}`, modelID: "m1", path: "rerank",
			want: `{"model":"m1","query":"q","documents":["a","b"]}`,
		},
		{
			// 未登记的路径不算透传：decide / classify 这类自有端点沿用既有改写行为
			// （含模型级 reasoning_effort 注入）。
			name: "未登记路径仍按对话处理", body: `{"model":"o","input":"x"}`,
			payload: `{"model":"o","input":"x"}`, modelID: "m1", path: "decide",
			want: `{"model":"m1","reasoning_effort":"medium","messages":[{"role":"user","content":"x"}]}`,
		},
	}
}

// TestUpstreamBodyMatchesPython 逐字节对齐请求体构造。
func TestUpstreamBodyMatchesPython(t *testing.T) {
	cfg := pythonConfig(t)
	for _, item := range upstreamBodyCases() {
		t.Run(item.name, func(t *testing.T) {
			var taskParams *canonical.Value
			if item.taskParams != "" {
				taskParams = mustValue(t, item.taskParams)
			}
			payload := mustValue(t, item.payload)
			got, err := UpstreamBody(
				[]byte(item.body), payload, item.modelID, cfg,
				item.stream, item.native, item.reasoningModelID, taskParams, item.path,
			)
			if err != nil {
				t.Fatalf("报错: %v", err)
			}
			if string(got) != item.want {
				t.Errorf("请求体不符\n 实际 %s\n 期望 %s", got, item.want)
			}
		})
	}
}

// TestUpstreamBodyDoesNotMutateInput 验证请求体构造不修改调用方的载荷。
//
// 载荷在重试循环里会被反复使用（同一请求换 key 重放），若被就地改写，第二次重试
// 就会拿到已被污染的 body——而 model 已被换成上游名，可能导致路由错乱。
func TestUpstreamBodyDoesNotMutateInput(t *testing.T) {
	cfg := pythonConfig(t)
	payload := mustValue(t, `{"model":"o","messages":[]}`)
	before := canonical.DumpsOrdered(payload)
	if _, err := UpstreamBody(
		[]byte(`{"model":"o","messages":[]}`), payload, "m1", cfg,
		false, false, "", mustValue(t, `{"temperature":0.5}`), "messages",
	); err != nil {
		t.Fatalf("报错: %v", err)
	}
	if after := canonical.DumpsOrdered(payload); after != before {
		t.Fatalf("入参被修改\n 之前 %s\n 之后 %s", before, after)
	}
}

// TestUpstreamBodyNilConfigIsSafe 验证配置为 nil 时不 panic。
//
// 探测原生端点等场景会拿不到配置；参照实现用 `if config is not None` 保护。
func TestUpstreamBodyNilConfigIsSafe(t *testing.T) {
	got, err := UpstreamBody(
		[]byte(`{"model":"o"}`), mustValue(t, `{"model":"o"}`), "m1", nil,
		false, false, "", nil, "chat/completions",
	)
	if err != nil {
		t.Fatalf("报错: %v", err)
	}
	if string(got) != `{"model":"m1"}` {
		t.Fatalf("无配置时结果不符: %s", got)
	}
}

// TestUpstreamBodyWithFilteredToolsMatchesPython 对齐「过滤工具后重试」的请求体。
func TestUpstreamBodyWithFilteredToolsMatchesPython(t *testing.T) {
	cfg := pythonConfig(t)
	toolsPayload := `{"model":"o","tools":[
		{"type":"function","function":{"name":"keep"}},
		{"type":"other","function":{"name":"drop"}},
		{"type":"function","function":{"name":""}}
	]}`
	cases := []struct {
		name    string
		body    string
		payload string
		stream  bool
		want    string
	}{
		{
			name: "过滤非 function 工具", body: `{}`, payload: toolsPayload,
			want: `{"model":"m1","tools":[{"type":"function","function":{"name":"keep"}}],"reasoning_effort":"medium"}`,
		},
		{
			name: "过滤后再补 include_usage", body: `{}`, payload: toolsPayload, stream: true,
			want: `{"model":"m1","tools":[{"type":"function","function":{"name":"keep"}}],"reasoning_effort":"medium","stream_options":{"include_usage":true}}`,
		},
		{
			name: "无 model 时原样返回", body: `{"raw":1}`, payload: `{"raw":1}`,
			want: `{"raw":1}`,
		},
		{
			name: "无 tools 字段", body: `{}`, payload: `{"model":"o"}`,
			want: `{"model":"m1","reasoning_effort":"medium"}`,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := UpstreamBodyWithFilteredTools(
				[]byte(item.body), mustValue(t, item.payload), "m1", cfg, item.stream, "", nil,
			)
			if err != nil {
				t.Fatalf("报错: %v", err)
			}
			if string(got) != item.want {
				t.Errorf("请求体不符\n 实际 %s\n 期望 %s", got, item.want)
			}
		})
	}
}

// TestApplyReasoningEffortPrecedence 单独锁定 reasoning_effort 的三级优先级。
func TestApplyReasoningEffortPrecedence(t *testing.T) {
	cfg := pythonConfig(t)
	cases := []struct {
		name    string
		payload string
		modelID string
		want    string
	}{
		// 1. 模型级设置存在：覆盖已有 reasoning_effort 的值，并保留 reasoning 字段。
		//    （reasoning 只在 UpstreamBody 里被 AdaptMessagePayload 剥掉；本函数
		//    单独调用时不动它。实测确认。）
		{"模型级覆盖载荷已有值", `{"reasoning_effort":"low"}`, "m1", `{"reasoning_effort":"medium"}`},
		{"模型级覆盖 reasoning.effort", `{"reasoning":{"effort":"high"}}`, "m1", `{"reasoning":{"effort":"high"},"reasoning_effort":"medium"}`},
		// 2. 载荷已有 reasoning_effort 且模型无设置：不动。
		{"载荷值不被 reasoning.effort 覆盖", `{"reasoning_effort":"x","reasoning":{"effort":"low"}}`, "other", `{"reasoning_effort":"x","reasoning":{"effort":"low"}}`},
		// 3. 从 reasoning.effort 提升。
		{"从 reasoning.effort 提升", `{"reasoning":{"effort":"low"}}`, "other", `{"reasoning":{"effort":"low"},"reasoning_effort":"low"}`},
		// effort 为空串是假值，不提升。
		{"空的 reasoning.effort 不提升", `{"reasoning":{"effort":""}}`, "other", `{"reasoning":{"effort":""}}`},
		// reasoning 不是对象则忽略。
		{"reasoning 非对象被忽略", `{"reasoning":"notadict"}`, "other", `{"reasoning":"notadict"}`},
		{"无 reasoning 字段则不动", `{"a":1}`, "other", `{"a":1}`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := canonical.DumpsOrdered(ApplyReasoningEffort(mustValue(t, item.payload), item.modelID, cfg))
			if got != item.want {
				t.Errorf("不符\n 实际 %s\n 期望 %s", got, item.want)
			}
		})
	}
	// nil 配置时不 panic 且不改动。
	got := canonical.DumpsOrdered(ApplyReasoningEffort(mustValue(t, `{"a":1}`), "m1", nil))
	if got != `{"a":1}` {
		t.Fatalf("nil 配置时不应改动: %s", got)
	}
}

// TestApplyReasoningEffortDoesNotMutate 验证不修改入参。
func TestApplyReasoningEffortDoesNotMutate(t *testing.T) {
	cfg := pythonConfig(t)
	payload := mustValue(t, `{"a":1}`)
	ApplyReasoningEffort(payload, "m1", cfg)
	if got := canonical.DumpsOrdered(payload); got != `{"a":1}` {
		t.Fatalf("入参被修改: %s", got)
	}
}
