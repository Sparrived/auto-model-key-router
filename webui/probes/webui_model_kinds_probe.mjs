// 模型类型展示口径探针：直接驱动 webui/model-kinds.js 的纯函数。
// 用法：node webui/probes/webui_model_kinds_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么值得单独锁：**判定在服务端**（internal/modelkind），前端只做三件事——把读数
// 折成索引、按主标签分组、把名字切块问回去（外加把 endpoints 折成"该走哪条路径"的
// 提示）。这三件每一件错了都表现为"看起来正常但分错了"：
//   1. 形状不对时若不返回 null，界面会把"服务端没答上来"画成"全部未分类"，
//      而那正是一个**看起来很正常**的页面；
//   2. 分组用的若是 kinds 全集，一个多能力模型会出现在两个组里，用户对同一个模型绑定
//      会勾两次（其中一次是反的）；
//   3. 切块少问一段，就只有一段没有类型——没有任何报错，也没人会发现。
//
// 这里刻意不断言后端规则表的内容（那是 internal/modelkind 的 go 用例的事）：前端探针
// 只钉住"服务端给什么就画什么"，规则变了不该让前端探针跟着红。

import { pathToFileURL } from "node:url";
import path from "node:path";

const MODULE = path.resolve(import.meta.dirname, "../model-kinds.js");
const m = await import(pathToFileURL(MODULE).href);

const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};

// —— 载荷归一化 ——
const document = {
  version: 1,
  source: "https://models.dev/api.json",
  catalog_available: true,
  models: {
    "gpt-4o": { kinds: ["text"], primary: "text", endpoints: ["chat"], grounds: [] },
    "gpt-image-1": { kinds: ["image"], primary: "image", endpoints: ["images"], grounds: [] },
    "gpt-4o-audio-preview": { kinds: ["text", "tts", "stt"], primary: "text", endpoints: ["chat", "speech", "transcriptions"], grounds: [] },
    "gateway-renamed-model": { kinds: [], primary: "unknown", endpoints: [], grounds: [] },
  },
};

const index = m.normalizeModelKinds(document);
check("normalize_returns_index", index !== null);
check("normalize_keeps_all_models", index?.models.size === 4, String(index?.models.size));
check("normalize_reports_catalog_available", index?.available === true);
check("normalize_keeps_primary", index?.models.get("gpt-image-1")?.primary === "image");

// 形状不对时必须返回 null：界面据此**不画任何类型标记**，而不是把一切当"未分类"。
check("normalize_rejects_null", m.normalizeModelKinds(null) === null);
check("normalize_rejects_missing_models", m.normalizeModelKinds({ version: 1 }) === null);
check("normalize_rejects_non_object_models", m.normalizeModelKinds({ models: "nope" }) === null);
check("normalize_accepts_empty_models", m.normalizeModelKinds({ models: {} })?.models.size === 0);
// catalog_available 缺失时按"目录不可用"处理（只有名字规则的结论）。
check("normalize_defaults_catalog_unavailable",
  m.normalizeModelKinds({ models: {} })?.available === false);

// —— 主标签 ——
check("kind_known", m.kindOf(index, "gpt-image-1") === "image");
// 多能力模型只归一组：分组用 primary，不是 kinds 全集，否则同一个模型会在两个组里
// 各出现一次，而它们背后是同一个绑定。
check("kind_uses_primary_only", m.kindOf(index, "gpt-4o-audio-preview") === "text",
  m.kindOf(index, "gpt-4o-audio-preview"));
check("kind_unknown_model", m.kindOf(index, "gateway-renamed-model") === m.UNKNOWN_KIND);
check("kind_missing_model_is_unknown", m.kindOf(index, "never-asked") === m.UNKNOWN_KIND);
check("kind_without_index_is_unknown", m.kindOf(null, "gpt-4o") === m.UNKNOWN_KIND);
check("kind_null_name_is_unknown", m.kindOf(index, null) === m.UNKNOWN_KIND);

// 后端将来加了新类型而前端还没跟上：显示成生词组，而不是折进「未分类」——后者会让
// "新类型"和"没有证据"看起来一样。
const futureIndex = m.normalizeModelKinds({ models: { "x-model": { kinds: ["hologram"], primary: "hologram" } } });
check("kind_passes_through_unknown_kind", m.kindOf(futureIndex, "x-model") === "hologram");

// —— 分组 ——
const names = ["gpt-4o", "gpt-image-1", "gpt-4o-audio-preview", "gateway-renamed-model", "never-asked"];
const groups = m.groupByKind(names, index);
check("groups_cover_every_name",
  groups.reduce((total, group) => total + group.names.length, 0) === names.length,
  JSON.stringify(groups.map((group) => [group.kind, group.names.length])));
check("groups_no_duplicate_names",
  new Set(groups.flatMap((group) => group.names)).size === names.length);
check("groups_order_follows_kind_order",
  groups.map((group) => group.kind).join(",") === "text,image,unknown",
  groups.map((group) => group.kind).join(","));
check("groups_have_labels", groups[0].label === "文本" && groups[2].label === "未分类",
  groups.map((group) => group.label).join(","));
check("groups_unknown_last", groups[groups.length - 1].kind === m.UNKNOWN_KIND);
check("groups_empty_input", m.groupByKind([], index).length === 0);
check("groups_without_index_all_unknown",
  m.groupByKind(["a", "b"], null).length === 1 && m.groupByKind(["a", "b"], null)[0].kind === m.UNKNOWN_KIND);
// 生词类型排在已知类型之后、未分类之前。
const mixed = m.groupByKind(["x-model", "gateway-renamed-model", "gpt-4o"], m.normalizeModelKinds({
  models: {
    "x-model": { kinds: ["hologram"], primary: "hologram" },
    "gateway-renamed-model": { kinds: [], primary: "unknown" },
    "gpt-4o": { kinds: ["text"], primary: "text" },
  },
}));
check("groups_word_kinds_before_unknown",
  mixed.map((group) => group.kind).join(",") === "text,hologram,unknown",
  mixed.map((group) => group.kind).join(","));

// —— 计数 ——
const counts = m.groupCounts(names, index);
check("counts_text", counts.get("text") === 2, String(counts.get("text")));
check("counts_image", counts.get("image") === 1, String(counts.get("image")));
check("counts_unknown", counts.get("unknown") === 2, String(counts.get("unknown")));

// —— 请求切块 ——
const long = Array.from({ length: 250 }, (_, index) => `model-${index}`);
const chunks = m.chunkModelIDs(long);
check("chunk_default_size_is_100", chunks.length === 3, String(chunks.length));
check("chunk_first_two_full", chunks[0].length === 100 && chunks[1].length === 100);
check("chunk_last_remainder", chunks[2].length === 50, String(chunks[2].length));
check("chunk_preserves_order", chunks.flat().join(",") === long.join(","));
check("chunk_exact_multiple_has_no_empty_tail", m.chunkModelIDs(["a", "b"], 2).length === 1);
check("chunk_empty_input", m.chunkModelIDs([]).length === 0);
check("chunk_non_array_input", m.chunkModelIDs(null).length === 0);
check("chunk_invalid_size_falls_back", m.chunkModelIDs(["a", "b"], 0)[0].length === 2);

// —— 标签 ——
check("label_known", m.kindLabel("tts") === "语音合成", m.kindLabel("tts"));
check("label_unknown", m.kindLabel(m.UNKNOWN_KIND) === "未分类");
// 生词原样显示：宁可看到 "hologram"，也不要被悄悄归进某一档。
check("label_word_kind_passthrough", m.kindLabel("hologram") === "hologram");
check("label_null_is_empty", m.kindLabel(null) === "");

// —— 端点族 ——
//
// 端点提示回答的是"该往哪条路径调这个模型"，它与类型一样**只从服务端读数派生**：
// 前端只负责加中文名、定顺序、给标准路径。这里锁的是三件会静默出错的事：
//   1. 生词端点原样显示（后端加了新族而前端还没跟上时，宁可看到生词）；
//   2. 空读数**不编造**端点（未分类模型不能显示成"走对话端点"，那会让人按错的路径
//      去配上游，而现象只是"这个模型调不通"）；
//   3. 去重与排序（多能力模型会在 endpoints 里给出多条，顺序即展示顺序）。
check("endpoint_labels_known",
  m.ENDPOINT_LABELS.chat === "对话" && m.ENDPOINT_LABELS.images === "图像生成"
  && m.ENDPOINT_LABELS.embeddings === "文本嵌入" && m.ENDPOINT_LABELS.speech === "语音合成"
  && m.ENDPOINT_LABELS.transcriptions === "语音识别" && m.ENDPOINT_LABELS.video === "视频生成"
  && m.ENDPOINT_LABELS.rerank === "重排",
  JSON.stringify(m.ENDPOINT_LABELS));
check("endpoint_order_covers_all_labels",
  m.ENDPOINT_ORDER.every((id) => Boolean(m.ENDPOINT_LABELS[id]))
  && Object.keys(m.ENDPOINT_LABELS).length === m.ENDPOINT_ORDER.length,
  m.ENDPOINT_ORDER.join(","));
// 路径只用于展示，但必须是完整的入站路径（带 /v1/），否则提示会指向一个不存在的地址。
check("endpoint_paths_cover_order",
  m.ENDPOINT_ORDER.every((id) => String(m.ENDPOINT_PATHS[id] || "").startsWith("/v1/")),
  JSON.stringify(m.ENDPOINT_PATHS));
check("endpoint_label_passthrough", m.endpointLabel("hologram") === "hologram", m.endpointLabel("hologram"));
check("endpoint_label_null_is_empty", m.endpointLabel(null) === "");
check("endpoint_path_unknown_is_empty", m.endpointPath("hologram") === "");

// ENDPOINT_FAMILY：类型 → 服务它的上游路由模式。stt 必须是**两条**：语音转写与语音
// 翻译是两个独立模式，却共用同一条 unified 计划——少列一条，用户会以为漏配了。
check("endpoint_family_image", m.ENDPOINT_FAMILY.image.join(",") === "images", JSON.stringify(m.ENDPOINT_FAMILY.image));
check("endpoint_family_stt_covers_both_modes",
  m.ENDPOINT_FAMILY.stt.join(",") === "transcriptions,translations", JSON.stringify(m.ENDPOINT_FAMILY.stt));
check("endpoint_family_text_covers_chat_dialects",
  m.ENDPOINT_FAMILY.text.join(",") === "openai,anthropic,responses", JSON.stringify(m.ENDPOINT_FAMILY.text));
check("endpoint_family_unknown_is_empty", m.ENDPOINT_FAMILY[m.UNKNOWN_KIND].length === 0);

// —— 逐模型的端点 ——
check("endpoints_single", m.endpointsOf(index, "gpt-image-1").join(",") === "images",
  m.endpointsOf(index, "gpt-image-1").join(","));
check("endpoints_multi_sorted",
  m.endpointsOf(index, "gpt-4o-audio-preview").join(",") === "chat,speech,transcriptions",
  m.endpointsOf(index, "gpt-4o-audio-preview").join(","));
check("endpoints_labels_multi",
  m.endpointLabelsOf(index, "gpt-4o-audio-preview").join(",") === "对话,语音合成,语音识别",
  m.endpointLabelsOf(index, "gpt-4o-audio-preview").join(","));
// 未分类模型没有任何端点证据：回空数组，界面据此不画提示（而不是编一条）。
check("endpoints_unknown_model_empty", m.endpointsOf(index, "gateway-renamed-model").length === 0);
check("endpoints_missing_model_empty", m.endpointsOf(index, "never-asked").length === 0);
check("endpoints_without_index_empty", m.endpointsOf(null, "gpt-4o").length === 0);
check("endpoints_null_name_empty", m.endpointsOf(index, null).length === 0);
check("endpoint_labels_unknown_model_empty", m.endpointLabelsOf(index, "gateway-renamed-model").length === 0);
// 去重 + 按 ENDPOINT_ORDER 排序 + 生词排最后：endpoints 是服务端给的集合，
// 重复项会让同一行提示里出现两个"对话"。
const endpointIndex = m.normalizeModelKinds({
  models: {
    "shuffled-model": {
      kinds: ["rerank"], primary: "rerank",
      endpoints: ["rerank", "chat", "rerank", "images", "hologram"],
    },
  },
});
check("endpoints_dedupe_and_order",
  m.endpointsOf(endpointIndex, "shuffled-model").join(",") === "chat,images,rerank,hologram",
  m.endpointsOf(endpointIndex, "shuffled-model").join(","));
// 形状不对（endpoints 不是数组）时回空数组，而不是把字符串当数组用。
const brokenEndpointIndex = m.normalizeModelKinds({ models: { "odd-model": { kinds: ["text"], primary: "text", endpoints: "chat" } } });
check("endpoints_non_array_is_empty", m.endpointsOf(brokenEndpointIndex, "odd-model").length === 0);
check("normalize_keeps_endpoints", index?.models.get("gpt-4o-audio-preview")?.endpoints.length === 3);

const failures = Object.values(checks).filter((value) => value !== true);
console.log(JSON.stringify({ module: "webui/model-kinds.js", checks, failures: failures.length }, null, 2));
if (failures.length) {
  console.error(`\n${failures.length} 项模型类型口径断言失败`);
  process.exit(1);
}
