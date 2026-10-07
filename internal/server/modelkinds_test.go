package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/pricing"
)

// modelKindCatalog 是一份同时带价格与 modalities 的 models.dev 形状目录。
//
// 必须带价格：pricing.Refresh 把"目录里没有任何带价格的模型"当作取回失败（防止把
// Cloudflare 的 SPA 兜底页当成目录），因此只给 modalities 的夹具会让整个快照不存在，
// 也就测不到目录层。
const modelKindCatalog = `{
  "p": {"models": {
    "gpt-image-1": {"cost": {"input": 1, "output": 2}, "modalities": {"input": ["text"], "output": ["image"]}},
    "gpt-4o-mini-tts": {"cost": {"input": 1, "output": 2}, "modalities": {"input": ["text"], "output": ["audio"]}},
    "shared-text": {"cost": {"input": 1, "output": 2}, "modalities": {"input": ["text"], "output": ["text"]}}
  }}
}`

// modelKindsPayload 是 /ui/model-kinds.json 的解码形状（只声明断言用得到的字段）。
type modelKindsPayload struct {
	Version          int    `json:"version"`
	Source           string `json:"source"`
	CatalogAvailable bool   `json:"catalog_available"`
	Models           map[string]struct {
		Kinds      []string `json:"kinds"`
		Primary    string   `json:"primary"`
		Endpoints  []string `json:"endpoints"`
		Modalities *struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		} `json:"modalities"`
		Grounds []struct {
			Source string   `json:"source"`
			Detail string   `json:"detail"`
			Kinds  []string `json:"kinds"`
		} `json:"grounds"`
	} `json:"models"`
}

// decodeModelKinds 请求分类读数并解码。
func decodeModelKinds(t *testing.T, app *App, query string) modelKindsPayload {
	t.Helper()
	recorder := serve(app, http.MethodGet, "/ui/model-kinds.json"+query, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /ui/model-kinds.json%s 状态码 = %d，期望 200（body=%s）",
			query, recorder.Code, recorder.Body.String())
	}
	var payload modelKindsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, recorder.Body.String())
	}
	return payload
}

// TestModelKindsEndpointClassifiesRequestedModels 是这条读数的主用例。
//
// 它同时钉住四件事：目录与规则两层合并、端点族推导、未分类是合法结果，以及响应键是
// **请求里的原始模型名**（不是归一化之后的名字）。
func TestModelKindsEndpointClassifiesRequestedModels(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newPricingApp(t, fetch)
	waitForPricing(t, app)

	payload := decodeModelKinds(t, app,
		"?model=gpt-image-1&model=gpt-4o-mini-tts&model=shared-text&model=my-gateway-model-x")

	if payload.Version != 1 {
		t.Errorf("version = %d，期望 1", payload.Version)
	}
	if payload.Source != pricing.SourceURL {
		t.Errorf("source = %q，期望 %q", payload.Source, pricing.SourceURL)
	}
	if !payload.CatalogAvailable {
		t.Error("目录已预热，catalog_available 应为 true")
	}

	image := payload.Models["gpt-image-1"]
	if len(image.Kinds) != 1 || image.Kinds[0] != "image" {
		t.Errorf("gpt-image-1 的 kinds = %v，期望 [image]", image.Kinds)
	}
	if image.Primary != "image" {
		t.Errorf("gpt-image-1 的 primary = %q，期望 image", image.Primary)
	}
	if len(image.Endpoints) != 1 || image.Endpoints[0] != "images" {
		t.Errorf("gpt-image-1 的 endpoints = %v，期望 [images]", image.Endpoints)
	}
	// 两条证据都要在：目录说它是图像模型（模态 text → image），规则也说。
	if len(image.Grounds) != 2 {
		t.Fatalf("gpt-image-1 的 grounds 有 %d 条，期望 2 条（目录 + 规则）", len(image.Grounds))
	}
	if image.Grounds[0].Source != "catalog" || image.Grounds[1].Detail != "image" {
		t.Errorf("gpt-image-1 的 grounds = %+v，期望 catalog 与 image 规则各一条", image.Grounds)
	}
	// 模态只在有证据时出现，且来自目录。
	if image.Modalities == nil || len(image.Modalities.Output) != 1 || image.Modalities.Output[0] != "image" {
		t.Errorf("gpt-image-1 的 modalities = %+v，期望 output 为 [image]", image.Modalities)
	}

	if got := payload.Models["gpt-4o-mini-tts"]; len(got.Kinds) != 1 || got.Kinds[0] != "tts" {
		t.Errorf("gpt-4o-mini-tts 的 kinds = %v，期望 [tts]", got.Kinds)
	} else if len(got.Endpoints) != 1 || got.Endpoints[0] != "speech" {
		t.Errorf("gpt-4o-mini-tts 的 endpoints = %v，期望 [speech]", got.Endpoints)
	}

	// 只有目录证据（规则表里没有这个名字）：文本模型走 chat 端点。
	if got := payload.Models["shared-text"]; len(got.Kinds) != 1 || got.Kinds[0] != "text" {
		t.Errorf("shared-text 的 kinds = %v，期望 [text]", got.Kinds)
	} else if len(got.Endpoints) != 1 || got.Endpoints[0] != "chat" {
		t.Errorf("shared-text 的 endpoints = %v，期望 [chat]", got.Endpoints)
	}

	// 未分类必须是**合法结果**：自建网关/中转站改名的模型都在这里，界面要显示
	// "未分类"而不是把它当成文本模型。
	unknown, ok := payload.Models["my-gateway-model-x"]
	if !ok {
		t.Fatal("未分类的模型名必须出现在响应里（否则界面无法区分'未分类'与'没问到'）")
	}
	if len(unknown.Kinds) != 0 {
		t.Errorf("未分类模型的 kinds = %v，期望空", unknown.Kinds)
	}
	if unknown.Primary != "unknown" {
		t.Errorf("未分类模型的 primary = %q，期望 unknown", unknown.Primary)
	}
	if unknown.Modalities != nil {
		t.Errorf("未分类模型不该有 modalities，实得 %+v", unknown.Modalities)
	}
}

// TestModelKindsEndpointKeysByOriginalName 钉住"查归一化、回原名"。
//
// 调用方拿响应直接对号入座，因此键必须是它给的那个名字；去前缀/去日期是服务端内部
// 的匹配手段。这条同时保证带供应商前缀、带日期后缀的名字也能命中目录。
func TestModelKindsEndpointKeysByOriginalName(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newPricingApp(t, fetch)
	waitForPricing(t, app)

	payload := decodeModelKinds(t, app, "?model=openai/gpt-image-1&model=gpt-image-1-20250101")

	for _, key := range []string{"openai/gpt-image-1", "gpt-image-1-20250101"} {
		entry, ok := payload.Models[key]
		if !ok {
			t.Fatalf("响应里没有原始名 %q 这个键（键必须是请求里的原始模型名）", key)
		}
		if len(entry.Kinds) != 1 || entry.Kinds[0] != "image" {
			t.Errorf("%s 的 kinds = %v，期望 [image]（归一化后应命中目录）", key, entry.Kinds)
		}
	}
}

// TestModelKindsEndpointDegradesWithoutCatalog 钉住目录不可用时的**降级而不是失败**。
//
// 与 /ui/pricing.json 的 503 刻意不同：没有价格时算出的成本是错的（会把一切算成免费），
// 必须响亮失败；而没有目录时名字规则仍然能给出判定，只是精度下降。载荷里的
// catalog_available 说明当前是哪种情况。
func TestModelKindsEndpointDegradesWithoutCatalog(t *testing.T) {
	failing := func(url, etag string, timeout time.Duration) (pricing.FetchResult, error) {
		return pricing.FetchResult{}, http.ErrHandlerTimeout
	}
	app := newPricingApp(t, failing)

	payload := decodeModelKinds(t, app, "?model=gpt-image-1&model=shared-text")
	if payload.CatalogAvailable {
		t.Error("目录取回失败时 catalog_available 应为 false")
	}
	if got := payload.Models["gpt-image-1"]; len(got.Kinds) != 1 || got.Kinds[0] != "image" {
		t.Errorf("没有目录时 gpt-image-1 的 kinds = %v，期望仍由规则给出 [image]", got.Kinds)
	}
	// 只有目录层认识的名字在此时落进未分类——这正是"降级"与"失败"的区别。
	if got := payload.Models["shared-text"]; len(got.Kinds) != 0 {
		t.Errorf("没有目录时 shared-text 的 kinds = %v，期望空（只有目录认识它）", got.Kinds)
	}
}

// TestModelKindsEndpointDropsEmptyAndSorts 钉住空值与去重，并让响应可复现。
func TestModelKindsEndpointDropsEmptyAndSorts(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newPricingApp(t, fetch)
	waitForPricing(t, app)

	// 空值不报 400（浏览器拼串很常见），重复值只算一次。
	recorder := serve(app, http.MethodGet,
		"/ui/model-kinds.json?model=&model=gpt-image-1&model=gpt-image-1&model=shared-text", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body=%s）", recorder.Code, recorder.Body.String())
	}
	var payload modelKindsPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(payload.Models) != 2 {
		t.Errorf("models 里有 %d 项，期望 2 项（空值丢弃、重复去重）", len(payload.Models))
	}
	// 键按码点排序：同一次查询的输出不该因为 URL 里参数的先后而不同。
	body := recorder.Body.String()
	imageIndex := strings.Index(body, `"gpt-image-1":{`)
	textIndex := strings.Index(body, `"shared-text":{`)
	if imageIndex < 0 || textIndex < 0 || imageIndex > textIndex {
		t.Errorf("响应键未按序输出: %s", body)
	}
}

// TestModelKindsEndpointRejectsOversizedRequests 钉住两条上限（个数与长度）。
func TestModelKindsEndpointRejectsOversizedRequests(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newPricingApp(t, fetch)

	parts := make([]string, 0, modelKindsMaxIDs+1)
	for index := 0; index <= modelKindsMaxIDs; index++ {
		parts = append(parts, "model=m"+strconv.Itoa(index))
	}
	recorder := serve(app, http.MethodGet, "/ui/model-kinds.json?"+strings.Join(parts, "&"), "")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("超过 %d 个模型时状态码 = %d，期望 400", modelKindsMaxIDs, recorder.Code)
	}

	tooLong := "/ui/model-kinds.json?model=" + strings.Repeat("x", modelKindsMaxIDLength+1)
	recorder = serve(app, http.MethodGet, tooLong, "")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("超长模型名时状态码 = %d，期望 400", recorder.Code)
	}

	// 恰好到上限必须放行：边界写错一格就会把正常的供应商页请求拒掉。
	parts = parts[:modelKindsMaxIDs]
	recorder = serve(app, http.MethodGet, "/ui/model-kinds.json?"+strings.Join(parts, "&"), "")
	if recorder.Code != http.StatusOK {
		t.Errorf("恰好 %d 个模型时状态码 = %d，期望 200", modelKindsMaxIDs, recorder.Code)
	}
}

// TestModelKindsEndpointRejectsNonGet 断言只有 GET/HEAD 被接受。
func TestModelKindsEndpointRejectsNonGet(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newPricingApp(t, fetch)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := serve(app, method, "/ui/model-kinds.json?model=gpt-4o", "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s 状态码 = %d，期望 405", method, recorder.Code)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s 的 Allow = %q，期望 GET, HEAD", method, got)
		}
	}
}

// TestModelKindsEndpointAbsentWithoutWebUI 断言 WebUI 未挂载时这条读数也不存在。
//
// 与价格目录同生共死是刻意的取舍（见 modelkinds.go）；这条把它钉住，避免有人日后
// 误以为它是一条独立于 WebUI 的路由。
func TestModelKindsEndpointAbsentWithoutWebUI(t *testing.T) {
	app := newTestApp(t, t.TempDir(), nil)
	recorder := serve(app, http.MethodGet, "/ui/model-kinds.json?model=gpt-4o", "")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("未挂载 WebUI 时状态码 = %d，期望 404", recorder.Code)
	}
	if recorder.Body.String() != notFoundBody {
		t.Errorf("响应体 = %s，期望兜底 404 %s", recorder.Body.String(), notFoundBody)
	}
}

// TestModelKindsEndpointFollowsMountPrefix 断言嵌入宿主时读数跟着挂载前缀走。
//
// 路由用 webui.Path(MountPrefix) 拼前缀（嵌入后是 /amkr/ui），而 WebUI 自己的请求是按
// location.pathname 反推前缀的（webui/api.js 的 apiBase）——两边必须一致。
func TestModelKindsEndpointFollowsMountPrefix(t *testing.T) {
	fetch, _ := stubPricingFetch(t, modelKindCatalog)
	app := newTestAppWith(t, t.TempDir(), appFixture{
		webUIEnabled: true,
		mutate: func(options *Options) {
			options.MountPrefix = "/amkr"
			options.WebUIAssets = fs.FS(fstest.MapFS{
				"index.html": &fstest.MapFile{Data: []byte("<html>amkr</html>")},
			})
			options.PricingFetch = fetch
		},
	})
	waitForPricing(t, app)

	recorder := serve(app, http.MethodGet, "/amkr/ui/model-kinds.json?model=gpt-image-1", "")
	if recorder.Code != http.StatusOK {
		t.Errorf("GET /amkr/ui/model-kinds.json 状态码 = %d，期望 200（body=%s）",
			recorder.Code, recorder.Body.String())
	}
	plain := serve(app, http.MethodGet, "/ui/model-kinds.json?model=gpt-image-1", "")
	if plain.Code != http.StatusNotFound {
		t.Errorf("无前缀的路径状态码 = %d，期望 404", plain.Code)
	}
}
