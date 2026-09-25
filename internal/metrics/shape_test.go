package metrics

import (
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
)

// recordShape 写入一行带请求形态的指标。
func recordShape(t *testing.T, store *Store, stream bool, apiFormat, reasoningEffort string) {
	t.Helper()
	usage := canonical.NewObjectOf(
		canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(10)},
		canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(2)},
	)
	statusCode := int64(200)
	if err := store.Record(RecordParams{
		ModelID:         "gpt-4o",
		KeyName:         "key-a",
		StatusCode:      &statusCode,
		Usage:           usage,
		CallerType:      "local",
		Stream:          stream,
		APIFormat:       apiFormat,
		ReasoningEffort: reasoningEffort,
	}); err != nil {
		t.Fatalf("写入指标: %v", err)
	}
}

// TestRequestHistoryCarriesShape 断言明细里带上请求形态三项。
//
// 形态不进 request_metrics 的列（见 schema.go 的旁挂表理由），因此这条用例同时守着
// 两件事：写入端真的写了 request_shape，读取端的 LEFT JOIN 真的把它取出来了。流式与否
// 在库里是 0/1 整数，读出来必须是**真正的布尔**（与 success/retried 同一约定）。
func TestRequestHistoryCarriesShape(t *testing.T) {
	store := tempStore(t)
	recordShape(t, store, true, "messages", "high")

	item := historyItem(t, store)
	stream, _ := item.Obj.Get("stream")
	apiFormat, _ := item.Obj.Get("api_format")
	reasoningEffort, _ := item.Obj.Get("reasoning_effort")

	if !stream.IsBool() || !stream.Bool {
		t.Errorf("stream 应为布尔 true，实得 %s", compact(t, stream))
	}
	if apiFormat.Str != "messages" {
		t.Errorf("api_format = %q，期望 messages", apiFormat.Str)
	}
	if reasoningEffort.Str != "high" {
		t.Errorf("reasoning_effort = %q，期望 high", reasoningEffort.Str)
	}
}

// TestRequestHistoryShapeNonStreamIsFalse 断言非流式请求读回来是 false 而不是 null。
//
// 「非流式」与「没有形态记录」是两件事：写入端确实知道它不是流式，库里存的是 0。
// 若读取端用 `stream != 0` 之外的写法（或漏了 NOT NULL 语义），看板会把绝大多数请求
// 显示成"未知"，这条用例就是钉住这一点的。
func TestRequestHistoryShapeNonStreamIsFalse(t *testing.T) {
	store := tempStore(t)
	recordShape(t, store, false, "chat/completions", "")

	item := historyItem(t, store)
	stream, _ := item.Obj.Get("stream")
	reasoningEffort, _ := item.Obj.Get("reasoning_effort")
	if !stream.IsBool() || stream.Bool {
		t.Errorf("stream 应为布尔 false，实得 %s", compact(t, stream))
	}
	// 没有生效的强度就是 NULL，不能兜底成 "none"/"medium" 这类并不存在的默认档。
	if reasoningEffort.Kind != canonical.KindNull {
		t.Errorf("空 reasoning_effort 应为 null，实得 %s", compact(t, reasoningEffort))
	}
}

// TestRequestHistoryShapeIsNullWithoutShape 断言没有形态记录的行渲染成 null。
//
// 历史行（升级前写入）与不走 proxy 的写入路径都没有形态。它们必须照常出现在明细里
// （LEFT JOIN 而不是 INNER），且三项都是 null——尤其是 stream 不能兜底成 false，
// 那会把「不知道」显示成「非流式」。
func TestRequestHistoryShapeIsNullWithoutShape(t *testing.T) {
	store := tempStore(t)
	recordShape(t, store, false, "", "")

	item := historyItem(t, store)
	for _, key := range []string{"stream", "api_format", "reasoning_effort"} {
		value, ok := item.Obj.Get(key)
		if !ok {
			t.Fatalf("明细缺少字段 %s", key)
		}
		if !value.IsNull() {
			t.Errorf("%s 应为 null，实得 %s", key, compact(t, value))
		}
	}
}

// TestRequestShapeUnknownFormatIsKept 断言未知的入站路径照原样记下。
//
// 不折算成方言名、也不把未知路径静默归并到某一类：将来接一条新路径时，看板应当显示
// 那个真实的路径，而不是一个看起来像已知分类的值。
func TestRequestShapeUnknownFormatIsKept(t *testing.T) {
	store := tempStore(t)
	recordShape(t, store, false, "audio/transcriptions", "")

	item := historyItem(t, store)
	apiFormat, _ := item.Obj.Get("api_format")
	if apiFormat.Str != "audio/transcriptions" {
		t.Errorf("api_format = %q，期望原样保留 audio/transcriptions", apiFormat.Str)
	}
}
