package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	"github.com/Sparrived/auto-model-key-router/internal/modelkind"
	"github.com/Sparrived/auto-model-key-router/internal/pricing"
)

// modelKindsPath 是模型分类读数在 WebUI 挂载下的文件名。
//
// **为什么挂在 /ui/ 而不是新开一条 /api/ 路由**：管理面那 47 条是有对外承诺的已发布
// 接口（Python 参照实现留下的形状），运维面 7 条同理。新增一条 /api 路由就得手写
// 一条对它负责的用例，而那些用例只能断言"新写的代码是新写的"——给不出兼容性证据。
// 这与 pricing.go / update.go / key_usage.go 选择的出路相同。
//
// 与 pricing.json 的**相同点**：同样是 models.dev 的公开派生数据，同样不鉴权。
const modelKindsPath = "/model-kinds.json"

// modelKindsDocumentVersion 是 /ui/model-kinds.json 的载荷版本。前端据此判断能否
// 解析；字段形状有破坏性变化时 +1（与 pricing.DocumentVersion 同一个约定）。
const modelKindsDocumentVersion = 1

// modelKindsMaxIDs 是单次查询允许的模型名个数上限。
//
// 取值与 WebUI 的实际用法对齐：供应商页一次要标注的是一个 Key 探测到的模型清单，
// 上游 /v1/models 常常一次返回几百个，所以给到 200；再大就不是"渲染前问一句"，
// 而是把这份读数当批量接口用了。
const modelKindsMaxIDs = 200

// modelKindsMaxIDLength 是单个模型名的最大长度（字节）。
//
// 与 internal/server/query.go 对字符串参数的 512 一致：模型名没有这么长的，超过就是
// 调用方出了问题，早点拒掉比让它进归一化更清楚。
const modelKindsMaxIDLength = 512

// handleModelKinds 按请求给出的模型名逐个分类（`GET /ui/model-kinds.json?model=...`）。
//
// 四条语义：
//
//   - **分类在服务端做**，而不是把目录发给浏览器让前端自己判。规则表与目录合并口径
//     只有一份实现（internal/modelkind），前端复刻一份必然漂移；代价是界面要为它
//     手上那一串名字问一次，这比下载一份 7800 项的目录便宜得多。
//   - **目录不可用时仍然 200**。这里与 pricing.json 的 503 **刻意不同**：没有价格时
//     算出来的成本是错的（会把一切算成免费），所以必须响亮失败；而没有目录时还有
//     名字规则，判定仍然有价值，只是精度下降。载荷里的 `catalog_available` 说明当前
//     是哪种情况，界面据此提示"分类依据仅名字规则"。
//   - **未分类是合法结果**（`kinds` 为空、`primary` 为 "unknown"）。自建网关、中转站
//     改名的模型必然落到这里，界面要显示"未分类"而不是把 unknown 当成文本。
//   - **响应键是请求里的原始模型名**，不是归一化后的名字：调用方拿它直接对号入座，
//     归一化（去前缀、去日期）是服务端内部的事。
func (a *App) handleModelKinds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// 与 /ui/ 下的静态资源一致（internal/webui 的 serve）。
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "方法不被允许", http.StatusMethodNotAllowed)
		return
	}

	ids := requestedModelIDs(r)
	if len(ids) > modelKindsMaxIDs {
		writeErrorEnvelope(w, http.StatusBadRequest,
			"一次最多查询 "+strconv.Itoa(modelKindsMaxIDs)+" 个模型")
		return
	}
	for _, id := range ids {
		if len(id) > modelKindsMaxIDLength {
			writeErrorEnvelope(w, http.StatusBadRequest,
				"模型名过长（上限 "+strconv.Itoa(modelKindsMaxIDLength)+" 字节）")
			return
		}
	}

	catalog, available := a.pricing.ModelKinds()
	rules := modelkind.DefaultRules()
	models := canonical.NewObject()
	for _, id := range ids {
		models.SetKey(id, modelKindDocument(modelkind.Resolve(id, catalog, rules)))
	}

	writeJSON(w, http.StatusOK, canonical.NewObjectOf(
		canonical.ObjectPair{Key: "version", Value: canonical.NewIntValue(modelKindsDocumentVersion)},
		canonical.ObjectPair{Key: "source", Value: canonical.NewString(pricing.SourceURL)},
		canonical.ObjectPair{Key: "catalog_available", Value: canonical.NewBool(available)},
		canonical.ObjectPair{Key: "models", Value: models},
	))
}

// requestedModelIDs 取出 `model` 查询参数并去重排序。
//
// 空值直接丢掉而不是报 400：`?model=a&model=` 这种拼串在浏览器里很常见，它表达的是
// "没有更多名字"，不是"我要查一个空名字"。排序是为了让响应可复现——同一次请求的
// 输出不该因为 URL 里参数的先后而不一样。
func requestedModelIDs(r *http.Request) []string {
	values := r.URL.Query()["model"]
	seen := make(map[string]bool, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// modelKindDocument 把一个判定渲染成响应里的一个条目。
//
// 带上 grounds 是为了让界面能解释"这条标签是谁给的"：只有名字规则命中的结论该让用户
// 有机会改，而目录给出的可以信。丢掉来源的判定在排障时无法解释（用户只会看到模型
// 被分到了错的组里）。
func modelKindDocument(info modelkind.Info) *canonical.Value {
	pairs := []canonical.ObjectPair{
		{Key: "kinds", Value: kindsValue(info.Kinds)},
		{Key: "primary", Value: canonical.NewString(string(info.Primary()))},
		{Key: "endpoints", Value: endpointsValue(info.Endpoints)},
	}
	// 模态只在真有证据时出现：零值渲染成空对象会让人以为"上游说了它没有模态"。
	if !info.Modalities.IsZero() {
		pairs = append(pairs, canonical.ObjectPair{Key: "modalities", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "input", Value: canonical.NewStringArray(info.Modalities.Input)},
			canonical.ObjectPair{Key: "output", Value: canonical.NewStringArray(info.Modalities.Output)},
		)})
	}
	grounds := canonical.NewArray()
	for _, ground := range info.Grounds {
		grounds.Arr = append(grounds.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "source", Value: canonical.NewString(ground.Source)},
			canonical.ObjectPair{Key: "detail", Value: canonical.NewString(ground.Detail)},
			canonical.ObjectPair{Key: "kinds", Value: kindsValue(ground.Kinds)},
		))
	}
	pairs = append(pairs, canonical.ObjectPair{Key: "grounds", Value: grounds})
	return canonical.NewObjectOf(pairs...)
}

// kindsValue 把类型切片渲染成字符串数组（空切片渲染成空数组，不是 null）。
func kindsValue(kinds []modelkind.Kind) *canonical.Value {
	items := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		items = append(items, string(kind))
	}
	return canonical.NewStringArray(items)
}

// endpointsValue 把端点切片渲染成字符串数组。
func endpointsValue(endpoints []modelkind.Endpoint) *canonical.Value {
	items := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		items = append(items, string(endpoint))
	}
	return canonical.NewStringArray(items)
}
