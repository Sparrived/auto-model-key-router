// 模型类型（文本 / 图像 / 视频 / 语音合成 / 语音识别 / 嵌入 / 重排）的展示口径。
//
// **判定在服务端做**：internal/modelkind 是唯一一份「models.dev 目录 + 名字规则」的
// 合并实现，这里只负责把 `/ui/model-kinds.json` 的读数变成界面能用的东西——中文标签、
// 分组顺序、端点族提示、请求切块。刻意不在前端复刻一份规则表：两份实现必然漂移，而
// 漂移的表现是「同一个模型在两个页面被分到不同的组」，那种 bug 一眼看不出来。
//
// 本模块是**纯函数**（不碰 DOM、不发请求），因此可以被 node 探针直接驱动
// （见 webui/probes/webui_model_kinds_probe.mjs）。

// UNKNOWN_KIND 是"没有任何证据"的取值。
//
// 它是**合法结果**：自建网关、中转站改名的模型都在这里，上游也从来不会告诉你它是
// 什么。界面必须把它显示成「未分类」而不是把它当成文本模型——那是在猜。
export const UNKNOWN_KIND = "unknown";

// KIND_LABELS 是类型的中文名。
export const KIND_LABELS = {
  text: "文本",
  image: "图像",
  video: "视频",
  tts: "语音合成",
  stt: "语音识别",
  embedding: "嵌入",
  rerank: "重排",
  [UNKNOWN_KIND]: "未分类",
};

// KIND_ORDER 是分组与筛选的展示顺序，与后端 modelkind.kindOrder 一致：
// 文本在前（用户最常关心"能不能当聊天模型用"），未分类在最后（它是待办的信号）。
export const KIND_ORDER = ["text", "image", "video", "tts", "stt", "embedding", "rerank", UNKNOWN_KIND];

// kindLabel 返回类型的中文名；未知取值原样显示（宁可看到生词，也不要被悄悄归进某一档）。
export function kindLabel(kind) {
  return KIND_LABELS[kind] || String(kind ?? "");
}

// normalizeModelKinds 把读数折成 { available, models: Map<名字, 条目> }。
//
// 形状不对时返回 null：界面据此**不显示任何类型标记**，而不是把一切当"未分类"——
// 后者会把"服务端没答上来"和"服务端说它没证据"画成同一个样子。
export function normalizeModelKinds(document) {
  if (!document || typeof document !== "object") return null;
  const models = document.models;
  if (!models || typeof models !== "object") return null;
  const index = new Map();
  for (const [name, entry] of Object.entries(models)) {
    index.set(String(name), {
      kinds: Array.isArray(entry?.kinds) ? entry.kinds.map(String) : [],
      primary: typeof entry?.primary === "string" && entry.primary ? entry.primary : UNKNOWN_KIND,
      endpoints: Array.isArray(entry?.endpoints) ? entry.endpoints.map(String) : [],
    });
  }
  // catalog_available 为假表示目录还没取回来，当前结论只来自名字规则：界面据此
  // 提示"分类依据仅名字规则"，而不是让用户以为这就是全部证据。
  return { available: document.catalog_available === true, models: index };
}

// kindOf 查一个模型名的主标签；查不到一律回 UNKNOWN_KIND。
//
// 只取 primary 而不是 kinds 全集：模型列表里一个名字只能出现在一个分组里，否则
// 「取消勾选」会在两份卡片上各点一次，而它们背后是同一个模型绑定。
//
// 原样返回 primary（不校验它是否在 KIND_ORDER 里）：后端将来加了新类型而前端还没跟上
// 时，正确行为是把它显示成一个生词组，而不是把它折进「未分类」——后者会让"新类型"
// 和"没有证据"看起来一样。
export function kindOf(index, name) {
  const entry = index?.models?.get(String(name));
  if (!entry) return UNKNOWN_KIND;
  return entry.primary;
}

// groupByKind 按主标签分组（顺序 = KIND_ORDER，空组不出现）。
//
// 返回的 groups 里每项是 { kind, label, names }，界面据此画"分组标题 + 该组的卡片"。
export function groupByKind(names, index) {
  const buckets = new Map();
  for (const name of names) {
    const kind = kindOf(index, name);
    if (!buckets.has(kind)) buckets.set(kind, []);
    buckets.get(kind).push(name);
  }
  const rank = (kind) => {
    // 未分类永远排在最后：它是"待办"而不是一种类型。
    if (kind === UNKNOWN_KIND) return KIND_ORDER.length;
    const at = KIND_ORDER.indexOf(kind);
    // 生词（后端将来加了新类型而前端还没跟上）排在已知类型之后、未分类之前：
    // 它至少是被识别出来的东西，比"没有证据"更具体。
    return at < 0 ? KIND_ORDER.length - 0.5 : at;
  };
  return [...buckets.keys()]
    .sort((left, right) => rank(left) - rank(right) || String(left).localeCompare(String(right)))
    .map((kind) => ({ kind, label: kindLabel(kind), names: buckets.get(kind) }));
}

// chunkModelIDs 把模型名列表切块。
//
// 服务端一次最多接受 200 个名字，但这里取更保守的 100：一个模型名 URL 编码后可能有
// 三十多个字符，200 个就能把请求行撑到 7 KB 以上，而中间那层反向代理的请求行上限
// 不由我们决定。切块是**静默失败**的典型位置（少问一段就少标一段），因此单独成函数
// 并由探针钉住。
export function chunkModelIDs(names, size = 100) {
  const list = Array.isArray(names) ? names : [];
  const limit = Number.isFinite(size) && size > 0 ? Math.floor(size) : 100;
  const chunks = [];
  for (let start = 0; start < list.length; start += limit) {
    chunks.push(list.slice(start, start + limit));
  }
  return chunks;
}

// kindKinds 返回条目支持的全部类型（筛选/提示用），未分类时回空数组。
export function kindsOf(index, name) {
  const entry = index?.models?.get(String(name));
  return entry ? entry.kinds : [];
}

// —— 端点族 ——
//
// 「类型」回答"这是什么模型"，「端点族」回答"该用哪条路径调它"。两者不是同义词：
// 对话模型可以走三种上游模式（openai / anthropic / responses），而语音转写与语音
// 翻译虽然入站路径不同，用的却是同一类模型（共一条 unified 计划）。
//
// 端点 id 就是 `/ui/model-kinds.json` 里 `endpoints` 的取值（服务端
// internal/modelkind 的 Endpoint 常量），这里只做展示：加中文名、定顺序、给标准路径。

// ENDPOINT_LABELS 是端点族的中文名。
export const ENDPOINT_LABELS = {
  chat: "对话",
  images: "图像生成",
  embeddings: "文本嵌入",
  speech: "语音合成",
  transcriptions: "语音识别",
  video: "视频生成",
  rerank: "重排",
};

// ENDPOINT_ORDER 是展示顺序，与服务端 endpointsForKind 的族顺序一致。
export const ENDPOINT_ORDER = ["chat", "images", "embeddings", "speech", "transcriptions", "video", "rerank"];

// ENDPOINT_PATHS 是各端点族的标准入站路径（带 `/v1/`，只用于展示）。
//
// 与服务端 internal/config 的 upstreamRouteDefaultPaths 同源，但**这里只是提示**：
// 真正生效的默认路径由服务端决定（供应商的「高级路径设置」可以逐模式改写）。
export const ENDPOINT_PATHS = {
  chat: "/v1/chat/completions",
  images: "/v1/images/generations",
  embeddings: "/v1/embeddings",
  speech: "/v1/audio/speech",
  transcriptions: "/v1/audio/transcriptions",
  video: "/v1/videos",
  rerank: "/v1/rerank",
};

// ENDPOINT_FAMILY 是「模型类型 → 服务它的上游路由模式（upstream_routes 的 mode）」。
//
// 给界面用来说清"要让这类模型可用，供应商那边至少要配哪条路径"。stt 有两条：语音转写
// 与语音翻译是两个独立入站路径、两个模式，但共用同一条 unified 计划，因此一起列出。
// 未分类为空数组：没有证据时不该暗示任何一条路径能用。
export const ENDPOINT_FAMILY = {
  text: ["openai", "anthropic", "responses"],
  image: ["images"],
  embedding: ["embeddings"],
  tts: ["speech"],
  stt: ["transcriptions", "translations"],
  video: ["video"],
  rerank: ["rerank"],
  [UNKNOWN_KIND]: [],
};

// endpointLabel 返回端点族的中文名；未知取值原样显示（同 kindLabel 的取舍）。
export function endpointLabel(endpoint) {
  return ENDPOINT_LABELS[endpoint] || String(endpoint ?? "");
}

// endpointPath 返回端点族的标准入站路径；没有对应路径时回空串（不编一个出来）。
export function endpointPath(endpoint) {
  return ENDPOINT_PATHS[endpoint] || "";
}

// endpointsOf 返回一个模型支持的端点族（去重、按 ENDPOINT_ORDER 排序）。
//
// 判据是服务端给的 `endpoints`（由 Kinds 推出），**不是**前端的类型映射：多能力模型
// （如 chat + tts + stt）在服务端就是三条端点，界面照抄即可。
//
// 生词端点（后端将来加了新族而前端还没跟上）排在已知族之后、原样保留：它与 kindLabel
// 的取舍相同——宁可看到生词，也不要被悄悄归进某一档。
export function endpointsOf(index, name) {
  const entry = index?.models?.get(String(name));
  const list = Array.isArray(entry?.endpoints) ? entry.endpoints : [];
  const unique = [...new Set(list.map(String).filter(Boolean))];
  const rank = (id) => {
    const at = ENDPOINT_ORDER.indexOf(id);
    return at < 0 ? ENDPOINT_ORDER.length : at;
  };
  return unique.sort((left, right) => rank(left) - rank(right) || left.localeCompare(right));
}

// endpointLabelsOf 返回一个模型的端点族中文名；没有读数时回空数组。
export function endpointLabelsOf(index, name) {
  return endpointsOf(index, name).map(endpointLabel);
}

// groupCounts 统计每个类型的模型数（筛选条上的数字）。
export function groupCounts(names, index) {
  const counts = new Map();
  for (const name of names) {
    const kind = kindOf(index, name);
    counts.set(kind, (counts.get(kind) || 0) + 1);
  }
  return counts;
}
