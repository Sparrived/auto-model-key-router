package proxy

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/Sparrived/auto-model-key-router/internal/config"
)

// 本文件固化 multipart 表单的 **model 改写**：表单里的 model 只用于本地路由，
// 发往上游前必须换成选中 target 的上游模型名——与 JSON 路径一致。除此之外请求体
// 逐字节透传（图片内容对 AMKR 不透明，必须原样送达）。
//
// 缺陷背景：线上图像编辑用任务名（image_generate）走表单，上游收到的是任务名而不是
// gpt-image-2.5-sunburst，回 400 `Model image_generate is not supported`；同一路由
// 换成 JSON 就正常。

// pngBytes 是一段带二进制与 CRLF 的假图片，用来确认图片内容逐字节保留。
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDRfake-image\r\nend")

// buildImageEditForm 构造一个图像编辑表单：model + prompt + image（文件 part）。
//
// modelLast 决定 model 的位置，用来覆盖「model 不是第一个 part」的情形。
func buildImageEditForm(t *testing.T, model string, image []byte, modelLast bool) (string, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	writeModel := func() {
		if err := writer.WriteField("model", model); err != nil {
			t.Fatalf("写 model 字段失败: %v", err)
		}
	}
	writeOthers := func() {
		if err := writer.WriteField("prompt", "a cat on a mat"); err != nil {
			t.Fatalf("写 prompt 字段失败: %v", err)
		}
		part, err := writer.CreateFormFile("image", "cat.png")
		if err != nil {
			t.Fatalf("创建 image part 失败: %v", err)
		}
		if _, err := part.Write(image); err != nil {
			t.Fatalf("写 image part 失败: %v", err)
		}
	}
	if modelLast {
		writeOthers()
		writeModel()
	} else {
		writeModel()
		writeOthers()
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	return buffer.String(), writer.FormDataContentType()
}

// formFields 用给定的 Content-Type 解析表单，返回「字段名 -> 值」。
func formFields(t *testing.T, contentType string, body []byte) map[string][]byte {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("解析 Content-Type 失败: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	fields := map[string][]byte{}
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			return fields
		}
		if nextErr != nil {
			t.Fatalf("解析表单失败: %v", nextErr)
		}
		value, readErr := io.ReadAll(part)
		_ = part.Close()
		if readErr != nil {
			t.Fatalf("读取 part 失败: %v", readErr)
		}
		fields[part.FormName()] = value
	}
}

// TestMultipartWithModelReplacesOnlyModelValue 固化改写粒度：只换 model 的值，
// 其余字节（含 boundary、图片内容、其他字段）一个不动。
func TestMultipartWithModelReplacesOnlyModelValue(t *testing.T) {
	const local = "image_generate"
	const upstream = "gpt-image-2.5-sunburst"
	body, contentType := buildImageEditForm(t, local, pngBytes, false)

	rewritten, err := multipartWithModel(contentType, []byte(body), upstream)
	if err != nil {
		t.Fatalf("改写 model 失败: %v", err)
	}
	// 字节级断言：把 body 里唯一那处 model 值替换掉，应当与改写结果完全相同。
	want := strings.Replace(body, local, upstream, 1)
	if string(rewritten) != want {
		t.Fatalf("改写结果不是「只换 model 值」:\n got=%q\nwant=%q", rewritten, want)
	}
	// 用**原始** Content-Type 解析改写后的体：boundary 没变才解析得出来。
	fields := formFields(t, contentType, rewritten)
	if got := string(fields["model"]); got != upstream {
		t.Fatalf("上游模型名: got=%q want=%q", got, upstream)
	}
	if got := string(fields["prompt"]); got != "a cat on a mat" {
		t.Fatalf("prompt 被改写: got=%q", got)
	}
	if !bytes.Equal(fields["image"], pngBytes) {
		t.Fatalf("图片字节被改写:\n got=%q\nwant=%q", fields["image"], pngBytes)
	}
}

// TestMultipartWithModelEdgeCases 覆盖「model 不在第一个 part」与「表单没有 model」。
func TestMultipartWithModelEdgeCases(t *testing.T) {
	const upstream = "upstream-model"

	t.Run("model 在最后一个 part", func(t *testing.T) {
		body, contentType := buildImageEditForm(t, "local-model", pngBytes, true)
		rewritten, err := multipartWithModel(contentType, []byte(body), upstream)
		if err != nil {
			t.Fatalf("改写 model 失败: %v", err)
		}
		fields := formFields(t, contentType, rewritten)
		if got := string(fields["model"]); got != upstream {
			t.Fatalf("上游模型名: got=%q want=%q", got, upstream)
		}
		if !bytes.Equal(fields["image"], pngBytes) {
			t.Fatalf("图片字节被改写")
		}
	})

	t.Run("表单没有 model 字段", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		if err := writer.WriteField("prompt", "x"); err != nil {
			t.Fatalf("写 prompt 字段失败: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("关闭 multipart writer 失败: %v", err)
		}
		// 宁可报错让调用方看到「构造上游请求体失败」，也不要把错的模型名发出去。
		if _, err := multipartWithModel(writer.FormDataContentType(), buffer.Bytes(), upstream); err == nil {
			t.Fatal("没有 model 字段时应返回错误")
		}
	})
}

// TestImagesEditsMultipartSendsUpstreamModel 是这条缺陷的回归用例：任务名走表单时，
// 上游必须收到真实模型名；而直接写真实模型名时请求体必须逐字节透传。
func TestImagesEditsMultipartSendsUpstreamModel(t *testing.T) {
	const task = "image_generate"
	const model = "gpt-image-2.5-sunburst"
	cfg := &config.RouterConfig{
		Models: []config.ModelConfig{
			testModel(model, []config.KeyConfig{testKey("gpt-plus", "https://upstream.test")}),
		},
		Tasks: []config.TaskConfig{{Name: task, Model: model}},
	}
	env := newTestEnv(t, cfg, Options{})
	env.route("/v1/images/edits", jsonStep(200, `{"created":1,"data":[{"b64_json":"AA=="}]}`))

	// 1) 任务名 + 表单：发往上游的 model 必须是解析后的真实模型名。
	body, contentType := buildImageEditForm(t, task, pngBytes, false)
	recorder := env.request(http.MethodPost, "images/edits", body,
		map[string]string{"Content-Type": contentType})
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码: got %d want 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	if len(env.transport.calls) != 1 {
		t.Fatalf("上游调用数: got %d want 1（%v）", len(env.transport.calls), describeUpstreams(env.transport.calls))
	}
	call := env.transport.calls[0]
	if call.path != "/v1/images/edits" {
		t.Fatalf("上游路径: got %q", call.path)
	}
	if got := call.headers["content-type"]; got != contentType {
		t.Fatalf("Content-Type 必须保留原始 boundary: got=%q want=%q", got, contentType)
	}
	fields := formFields(t, contentType, []byte(call.body))
	if got := string(fields["model"]); got != model {
		t.Fatalf("上游收到的 model: got=%q want=%q（任务名不得透传到上游）", got, model)
	}
	if !bytes.Equal(fields["image"], pngBytes) {
		t.Fatalf("图片字节被改写:\n got=%q\nwant=%q", fields["image"], pngBytes)
	}
	if got := string(fields["prompt"]); got != "a cat on a mat" {
		t.Fatalf("prompt 被改写: got=%q", got)
	}

	// 2) 直接写真实模型名：无需改写，请求体必须逐字节透传。
	env.route("/v1/images/edits", jsonStep(200, `{"created":1,"data":[]}`))
	direct, directContentType := buildImageEditForm(t, model, pngBytes, false)
	if got := env.request(http.MethodPost, "images/edits", direct,
		map[string]string{"Content-Type": directContentType}); got.Code != http.StatusOK {
		t.Fatalf("直呼模型名状态码: got %d want 200（body=%s）", got.Code, got.Body.String())
	}
	if len(env.transport.calls) != 2 {
		t.Fatalf("上游调用数: got %d want 2", len(env.transport.calls))
	}
	if got := env.transport.calls[1].body; got != direct {
		t.Fatalf("模型名无需改写时必须逐字节透传:\n got=%q\nwant=%q", got, direct)
	}
}
