// 统一模型页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/unified.js。
// 用法：node webui/probes/webui_unified_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么值得单独锁：这一页的保存失败**完全没有声音**。它在提交前会重画整张表单，而错误
// 提示原本是写进重画前的那个节点——那个节点已经脱离文档，于是「保存」点下去只会看到按钮从
// 「正在保存」闪回「保存」，页面上不留任何痕迹；同时表单里的模型、路由方式又是就地改的内存
// 状态，改完立刻重画，看起来像"改上了"。两件事叠在一起就是「所有的保存只在前端显示、保存
// 按钮无效」。这一页还有三条同类的静默故障，四条都锁在这里：
//
//   1. saving 复位 —— 它同时控制着整张表单的 disabled 与保存按钮的文案。成功路径上漏掉
//      复位，下一次点「编辑」拿到的是一整个禁用、按钮写着「正在保存」的表单，从此再也存不进
//      任何改动（直到刷新浏览器）；
//   2. 失败原因可见 —— 必须画在**当前**这棵 DOM 里；
//   3. 每次进入页面重新取数 —— 模型与 config_revision 会被别的页面改掉（在供应商页删掉一个
//      供应商会连同它的模型一起删掉），缓存下来的旧模型名选不中、旧版本号必然撞 409；
//   4. 失效模型回落 —— unified 指向已随供应商一起消失的模型时，界面必须把它当成「没选」，
//      否则下拉显示空值、保存却仍把那个名字发给服务端，被「引用了未配置的模型」顶回来；
//   5. 分族计划 —— unified-model 按入站端点族分派（default + 图像/嵌入/语音合成/语音识别/
//      视频/重排）。漏掉一族的表现是"这一族的请求永远回落到默认模型"：页面看起来完全正常，
//      只有调用方会发现图像请求打到了聊天模型上。提交体少一个键更隐蔽——服务端把"没传"
//      读成"这次不动它"，用户以为清掉的旧计划还在配置里；
//   6. 下拉不筛掉任何模型 —— 类型只用于排序与分组：自定义模型名在服务端没有档案
//      （未分类），把它们挡在下拉外，用户就再也选不回自己的模型；
//   7. api.updateUnified 的透传契约 —— 老调用方只传 default/image/embeddings，新的传七个
//      计划键；接口层必须"给什么发什么"，替调用方补 null 会把没打算动的族清空。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

// —— 最小 DOM 垫片（与 webui_accesskeys_probe.mjs 同一套）——
class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.value = "";
    this.checked = false;
    this.disabled = false;
    this.parent = null;
    this.documentRoot = false;
  }
  append(...nodes) {
    for (const node of nodes.flat()) {
      if (node === null || node === undefined) continue;
      if (typeof node === "object") node.parent = this;
      this.children.push(node);
    }
  }
  appendChild(node) { this.append(node); return node; }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) { return this.attrs[key]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((item) => item !== fn);
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
  }
  querySelector() { return null; }
  focus() {}
  select() {}
  get isConnected() {
    for (let node = this; node; node = node.parent) if (node.documentRoot) return true;
    return false;
  }
  get textContent() {
    return this.children.map((c) => (c.textContent === undefined ? String(c) : c.textContent)).join(" ");
  }
}
class FakeText extends FakeNode {
  constructor(text) { super("#text"); this.data = text; }
  get textContent() { return this.data; }
}

const define = (name, value) =>
  Object.defineProperty(global, name, { value, writable: true, configurable: true });

const location = { hash: "#/unified", pathname: "/ui/" };
define("location", location);
define("window", { addEventListener() {}, isSecureContext: true, location });
define("navigator", { clipboard: null });
global.Node = FakeNode;
const body = new FakeNode("body");
define("document", {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  body,
  addEventListener() {},
  removeEventListener() {},
  execCommand: () => true,
});
// 提示条（toast）与自动复位定时器在本探针里都不需要跑，直接吞掉：
// 断言看的是**页面节点**里有没有错误提示，而不是弹窗。
define("setTimeout", () => 0);
define("localStorage", { getItem: () => null, setItem() {}, removeItem() {} });

// —— 假取数层 ——
// routes 按「方法 + 路径」预置响应，可在场景之间改写，用来模拟"别的页面改了配置"。
const requests = [];
let routes = {};
const respond = (status, payload) => ({
  ok: status >= 200 && status < 300,
  status,
  async text() { return payload === undefined ? "" : JSON.stringify(payload); },
});

// 模型类型读数：判定在服务端（internal/modelkind），这里按问到的名字照抄一份结论。
// 单独处理而不是塞进 routes：它是**每个场景都会发**的背景请求（下拉排序用），
// 逐场景预置只会给每个场景定义添一行噪声。
const KIND_TABLE = {
  "model-a": { kinds: ["text"], primary: "text", endpoints: ["chat"] },
  "model-b": { kinds: ["text"], primary: "text", endpoints: ["chat"] },
  "image-a": { kinds: ["image"], primary: "image", endpoints: ["images"] },
  "tts-a": { kinds: ["tts"], primary: "tts", endpoints: ["speech"] },
  "stt-a": { kinds: ["stt"], primary: "stt", endpoints: ["transcriptions"] },
  "video-a": { kinds: ["video"], primary: "video", endpoints: ["videos"] },
  "rerank-a": { kinds: ["rerank"], primary: "rerank", endpoints: ["rerank"] },
};

global.fetch = async (url, options = {}) => {
  const method = options.method || "GET";
  const route = String(url).split("?")[0];
  const payload = options.body ? JSON.parse(options.body) : null;
  requests.push({ route, method, payload });
  if (route === "/ui/model-kinds.json") {
    const models = {};
    for (const name of new URL(String(url), "http://probe.invalid").searchParams.getAll("model")) {
      models[name] = KIND_TABLE[name] || { kinds: [], primary: "unknown", endpoints: [] };
    }
    return respond(200, { version: 1, catalog_available: true, models });
  }
  const entry = routes[`${method} ${route}`];
  // 没预置就抛错：探针要能发现页面发了预期之外的请求，而不是静默拿到空响应。
  if (!entry) throw new Error(`探针未预置响应: ${method} ${route}`);
  return respond(entry.status ?? 200, entry.body);
};

// —— 假配置 ——
const modelEntry = (id) => ({
  id, aliases: [], routing_mode: "round_robin", reasoning_effort: null,
  keys: [{ name: `${id}-key`, enabled: true }],
});
const modelsBody = (ids, revision) => ({ models: ids.map(modelEntry), config_revision: revision });
const unifiedBody = (model, revision) => ({
  unified_model: model ? { default: { primary: { model, key: null } } } : null,
  config_revision: revision,
});
// unifiedPlans 造一份"带分族计划"的响应：各族只在传了才出现，与服务端形状一致。
const unifiedPlans = (plans, revision) => ({ unified_model: plans, config_revision: revision });

const { renderUnified } = await import(pathToFileURL(path.join(WEBUI, "pages", "unified.js")).href);

// —— 查询与驱动工具 ——
const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
const buttonsOf = (root) => findAll(root, (node) => node.tagName === "button");
const buttonWithText = (root, text) =>
  buttonsOf(root).find((node) => node.textContent.trim() === text);
// click 对 null 是安全的：页面坏掉时（例如「保存」被写成禁用的「正在保存」）按钮就是找不到
// 的，这里记一条失败而不是抛栈——否则探针只会打印一段栈，看不出是哪条口径断了。
const click = (node) => {
  if (!node) { checks.click_target_missing = "FAILED: 要点击的按钮在页面上不存在"; return; }
  for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} });
};
// changeTo 驱动一个下拉：把值写进去再触发 change。找不到节点时同样记一条失败而不是抛栈。
const changeTo = (node, value) => {
  if (!node) { checks.change_target_missing = "FAILED: 要改动的下拉在页面上不存在"; return; }
  node.value = value;
  for (const fn of node.listeners.change || []) fn({ target: node });
};
const settle = async () => { for (let i = 0; i < 60; i += 1) await Promise.resolve(); };
const putCalls = () => requests.filter((r) => r.method === "PUT" && r.route.endsWith("/api/unified-model"));
const getCalls = () => requests.filter((r) => r.method === "GET" && r.route.includes("/api/"));
// fieldSelect 按字段标签找一个下拉（标签的 span 文本必须**恰好**等于 label：
// "模型"与"回退模型"/"图像模型"是四个不同的字段，子串匹配会抓错）。
const fieldSelect = (root, label) => {
  const field = findAll(root, (node) => node.tagName === "label"
    && node.children.some((child) => child.textContent === label))[0];
  return field ? findAll(field, (node) => node.tagName === "select")[0] : null;
};
const optionValues = (node) => findAll(node || new FakeNode("select"), (child) => child.tagName === "option")
  .map((option) => option.value);
const optgroupLabels = (node) => findAll(node || new FakeNode("select"), (child) => child.tagName === "optgroup")
  .map((group) => group.attrs.label);
// 按整棵子树的文本比对，而不是只看叶子：文本落在文本节点上，承载它的元素
// （span / dt）本身还有一层子节点，用"无子节点"当叶子判据会漏掉它们。
const findText = (root, needle) =>
  findAll(root, (node) => node.tagName !== "#text" && node.textContent === needle).length > 0;

// enterEditor 让页面停在编辑态并返回「保存」按钮。
//
// state.editing 是**跨页面进入**保留的（未保存的编辑不该因为切页丢失），所以上一场景可能
// 已经停在编辑态：先按「取消」退回摘要，再点「编辑」。
const enterEditor = async (host) => {
  const cancel = buttonWithText(host, "取消");
  if (cancel) { click(cancel); await settle(); }
  const edit = buttonWithText(host, "编辑");
  if (edit) { click(edit); await settle(); }
  return buttonWithText(host, "保存") || buttonWithText(host, "正在保存") || null;
};

const checks = {};
const check = (name, ok, detail = "") => { if (!ok) checks[name] = `FAILED${detail ? `: ${detail}` : ""}`; else checks[name] = true; };

// ═══ 场景 1：一次成功保存之后，表单必须仍然可用 ═══
routes = {
  "GET /api/unified-model": { body: unifiedBody("model-a", "rev-1") },
  "GET /api/models": { body: modelsBody(["model-a"], "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-a", "rev-2") },
};
requests.length = 0;
let host = renderUnified({});
await settle();

let save = await enterEditor(host);
check("first_edit_save_enabled", save !== null && save.disabled === false,
  save ? save.textContent.trim() : "找不到保存按钮");
click(save);
await settle();
check("save_sent_once", putCalls().length === 1, `${putCalls().length} 次 PUT`);

// 保存成功 → 摘要页 → 再点「编辑」：saving 若没复位，这里是一整个禁用的「正在保存」表单。
save = await enterEditor(host);
check("editor_reopens_after_save", save !== null, "保存之后再也进不了编辑态");
check("save_button_not_stuck", save !== null && save.disabled === false,
  save ? `按钮文案 ${save.textContent.trim()}（disabled=${save.disabled}）` : "找不到保存按钮");
check("no_stale_saving_label", buttonWithText(host, "正在保存") === undefined);

// ═══ 场景 2：保存失败的原因必须留在页面上 ═══
routes = {
  "GET /api/unified-model": { body: unifiedBody("model-a", "rev-1") },
  "GET /api/models": { body: modelsBody(["model-a"], "rev-1") },
  "PUT /api/unified-model": {
    status: 422,
    body: { detail: "unified_model.default.primary 引用了未配置的模型: model-a" },
  },
};
host = renderUnified({});
await settle();
await enterEditor(host);
click(buttonWithText(host, "保存"));
await settle();
check("failure_detail_visible", host.textContent.includes("引用了未配置的模型"), host.textContent);
check("save_reenabled_after_failure", buttonWithText(host, "保存")?.disabled === false);

// ═══ 场景 3：每次进入页面都要重新取数 ═══
routes = {
  "GET /api/unified-model": { body: unifiedBody("model-a", "rev-1") },
  "GET /api/models": { body: modelsBody(["model-a"], "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-a", "rev-2") },
};
host = renderUnified({});
await settle();
const getsBefore = getCalls().length;
// 模拟「在供应商页删掉提供 model-a 的那个供应商」：服务端把模型与 unified_model 一起清掉。
routes["GET /api/unified-model"] = { body: unifiedBody(null, "rev-2") };
routes["GET /api/models"] = { body: modelsBody([], "rev-2") };
host = renderUnified({});
await settle();
check("reentry_refetches", getCalls().length > getsBefore,
  `再次进入只发了 ${getCalls().length - getsBefore} 次 GET`);
check("empty_models_state_shown", host.textContent.includes("尚未配置可用模型"), host.textContent);

// ═══ 场景 4：unified 指向已删除的模型时不能再把它发出去 ═══
routes = {
  // 两次取数之间有供应商被删：unified 还指着 model-a，模型清单里只剩 model-b。
  "GET /api/unified-model": { body: unifiedBody("model-a", "rev-1") },
  "GET /api/models": { body: modelsBody(["model-b"], "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-b", "rev-2") },
};
host = renderUnified({});
await settle();
await enterEditor(host);
const primarySelect = findAll(host, (node) => node.tagName === "select")[0];
check("stale_primary_falls_back", primarySelect?.value === "model-b", `实际选中 ${primarySelect?.value}`);
click(buttonWithText(host, "保存"));
await settle();
const lastPut = putCalls().at(-1);
check("save_never_sends_deleted_model", lastPut?.payload?.default?.primary?.model === "model-b",
  `实际发出 ${lastPut?.payload?.default?.primary?.model}`);

// ═══ 场景 5：七个端点族都要有编辑位，提交体带全部计划键 ═══
//
// 为什么值得单独锁：漏掉一族的表现是"这一族的请求永远回落到默认模型"——页面看起来
// 完全正常，只有调用方会发现图像请求打到了聊天模型上。提交体少一个键更隐蔽：服务端
// 把"没传"读成"这次不动它"，用户以为清掉的旧计划还在配置里。
const ALL_MODELS = ["model-a", "model-b", "image-a", "tts-a", "stt-a", "video-a", "rerank-a", "custom-x"];
const FAMILY_LABELS = ["图像模型", "嵌入模型", "语音合成模型", "语音识别模型", "视频模型", "重排模型"];
const PLAN_KEYS = ["default", "embeddings", "image", "rerank", "speech", "transcriptions", "video"];

routes = {
  "GET /api/unified-model": {
    body: unifiedPlans({
      default: { primary: { model: "model-a", key: null } },
      image: { primary: { model: "image-a", key: null } },
      speech: { primary: { model: "tts-a", key: "tts-a-key" } },
    }, "rev-1"),
  },
  "GET /api/models": { body: modelsBody(ALL_MODELS, "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-a", "rev-2") },
};
requests.length = 0;
host = renderUnified({});
await settle();
await enterEditor(host);

check("editor_renders_every_family_section", FAMILY_LABELS.every((label) => findText(host, label)),
  FAMILY_LABELS.filter((label) => !findText(host, label)).join(",") || "全部都在");
// 默认（文本）族那几件控件必须原样保留：模型、推理强度、路由方式、回退模型。
check("editor_keeps_default_section",
  ["模型", "推理强度", "路由方式", "回退模型"].every((label) => findText(host, label)));
check("editor_explains_family_fallback", host.textContent.includes("没有配置的族会回落到它"));

const textSelect = fieldSelect(host, "模型");
const imageSelect = fieldSelect(host, "图像模型");
const videoSelect = fieldSelect(host, "视频模型");
const imageOptions = optionValues(imageSelect);
// 任何模型都不能被筛掉：自定义模型名在服务端没有档案（未分类），挡在外面就等于
// 让用户再也选不回自己的模型。
check("family_select_lists_every_model", ALL_MODELS.every((id) => imageOptions.includes(id)),
  imageOptions.join(","));
check("family_select_leads_with_clear_option", imageOptions[0] === "", imageOptions.join(","));
check("family_select_orders_matching_kind_first",
  imageOptions[1] === "image-a" && optionValues(videoSelect)[1] === "video-a",
  `${imageOptions.slice(0, 4).join(",")} / ${optionValues(videoSelect).slice(0, 4).join(",")}`);
check("family_select_keeps_unclassified_last", imageOptions.at(-1) === "custom-x", imageOptions.join(","));
check("family_select_groups_other_and_unknown",
  optgroupLabels(imageSelect).join(",") === "其它类型,未分类", optgroupLabels(imageSelect).join(","));
check("primary_select_puts_text_first",
  optionValues(textSelect)[0] === "model-a" && optionValues(textSelect).at(-1) === "custom-x",
  optionValues(textSelect).join(","));
check("primary_select_has_no_clear_option", !optionValues(textSelect).includes(""),
  optionValues(textSelect).join(","));
check("family_select_preselects_configured_model", imageSelect?.value === "image-a", imageSelect?.value);
check("family_key_select_preselects_configured_key",
  fieldSelect(host, "语音合成 Key")?.value === "tts-a-key", fieldSelect(host, "语音合成 Key")?.value);
check("family_key_select_disabled_without_model", fieldSelect(host, "视频 Key")?.disabled === true);

click(buttonWithText(host, "保存"));
await settle();
const saved = putCalls().at(-1);
const savedKeys = Object.keys(saved?.payload || {}).filter((key) => key !== "config_revision").sort();
check("put_carries_all_seven_plan_keys", savedKeys.join(",") === PLAN_KEYS.join(","), savedKeys.join(","));
check("put_carries_configured_families",
  saved?.payload?.image?.primary?.model === "image-a"
  && saved?.payload?.speech?.primary?.model === "tts-a"
  && saved?.payload?.speech?.primary?.key === "tts-a-key",
  JSON.stringify({ image: saved?.payload?.image, speech: saved?.payload?.speech }));
check("put_nulls_families_left_empty",
  saved?.payload?.embeddings === null && saved?.payload?.transcriptions === null
  && saved?.payload?.video === null && saved?.payload?.rerank === null,
  JSON.stringify(saved?.payload));
check("put_has_no_legacy_fields",
  !("image_model" in (saved?.payload || {})) && !("image_key" in (saved?.payload || {})),
  JSON.stringify(saved?.payload));
// 只读摘要：新增族**只在配置了才出现**（没配的视频不该占一行"未配置"）。
check("summary_lists_configured_families",
  findText(host, "语音合成模型") && !host.textContent.includes("视频模型"),
  host.textContent.replace(/\s+/g, " ").slice(0, 300));

// ═══ 场景 6：清空一族 = 提交 null ═══
routes = {
  "GET /api/unified-model": {
    body: unifiedPlans({
      default: { primary: { model: "model-a", key: null } },
      speech: { primary: { model: "tts-a", key: "tts-a-key" } },
      rerank: { primary: { model: "rerank-a", key: null } },
    }, "rev-1"),
  },
  "GET /api/models": { body: modelsBody(ALL_MODELS, "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-a", "rev-2") },
};
requests.length = 0;
host = renderUnified({});
await settle();
await enterEditor(host);

check("configured_family_preselects_model", fieldSelect(host, "语音合成模型")?.value === "tts-a",
  fieldSelect(host, "语音合成模型")?.value);
changeTo(fieldSelect(host, "语音合成模型"), "");
await settle();
// 换/清模型必须同时丢掉旧 Key：Key 是模型级的，留着它下一步就会被服务端拒绝。
check("clearing_family_resets_key", fieldSelect(host, "语音合成 Key")?.value === "",
  String(fieldSelect(host, "语音合成 Key")?.value));
click(buttonWithText(host, "保存"));
await settle();
const cleared = putCalls().at(-1);
check("clearing_family_sends_null", cleared?.payload?.speech === null, JSON.stringify(cleared?.payload?.speech));
check("clearing_one_family_keeps_others", cleared?.payload?.rerank?.primary?.model === "rerank-a",
  JSON.stringify(cleared?.payload?.rerank));
check("clearing_family_still_sends_all_plan_keys",
  Object.keys(cleared?.payload || {}).length === 8, Object.keys(cleared?.payload || {}).join(","));

// ═══ 场景 7：保存前的两道校验 ═══
routes = {
  "GET /api/unified-model": {
    body: unifiedPlans({
      default: { primary: { model: "model-a", key: null } },
      speech: { primary: { model: "tts-a", key: "tts-a-key" } },
    }, "rev-1"),
  },
  "GET /api/models": { body: modelsBody(ALL_MODELS, "rev-1") },
  "PUT /api/unified-model": { body: unifiedBody("model-a", "rev-2") },
};
requests.length = 0;
host = renderUnified({});
await settle();
await enterEditor(host);

// (a) 回退模型与主模型相同：这道校验是改动前就有的，改动后必须仍然生效。
changeTo(fieldSelect(host, "回退模型"), "model-a");
click(buttonWithText(host, "保存"));
await settle();
check("validate_rejects_fallback_equal_primary",
  host.textContent.includes("回退模型不能与主模型相同"), host.textContent.replace(/\s+/g, " ").slice(0, 200));
check("validate_failure_sends_nothing", putCalls().length === 0, `${putCalls().length} 次 PUT`);

// (b) 族里只留了 Key 没留模型。界面本身会拦住这个状态（没有模型时 Key 下拉是禁用的），
// 但校验也必须有这一道：服务端只会回一句"引用了未配置的模型"，而用户看到的却是一个
// 已经填好的 Key 框——那句错误指不到问题所在。
changeTo(fieldSelect(host, "回退模型"), "");
changeTo(fieldSelect(host, "语音合成模型"), "");
changeTo(fieldSelect(host, "语音合成 Key"), "tts-a-key");
click(buttonWithText(host, "保存"));
await settle();
check("validate_rejects_family_key_without_model",
  host.textContent.includes("语音合成模型不能只指定 Key 而不指定模型"),
  host.textContent.replace(/\s+/g, " ").slice(0, 240));
check("validate_family_failure_sends_nothing", putCalls().length === 0, `${putCalls().length} 次 PUT`);

// ═══ 场景 8：api.updateUnified 的透传契约 ═══
//
// 它是这一页唯一一处"接口层"契约：老调用方只传 default/image/embeddings（或更老的
// model/key 切换形状），新调用方传七个计划键。透传的意思是**给什么发什么**——给少了
// 不能替它补 null（那会把调用方没打算动的族清空），给多了不能丢（那一族的保存会静默
// 失效）。页面自己负责每次都写全七个键，接口层不替它做决定。
const { api } = await import(pathToFileURL(path.join(WEBUI, "api.js")).href);
requests.length = 0;
await api.updateUnified("rev-1", { default: { primary: { model: "model-a", key: null } }, image: null, speech: null });
let sendBody = putCalls().at(-1)?.payload || {};
check("update_unified_passes_given_plan_keys",
  Object.keys(sendBody).filter((key) => key !== "config_revision").sort().join(",") === "default,image,speech",
  Object.keys(sendBody).join(","));
await api.updateUnified("rev-1", { model: "model-a", key: "primary", image_model: null });
sendBody = putCalls().at(-1)?.payload || {};
check("update_unified_keeps_legacy_fields",
  sendBody.model === "model-a" && sendBody.key === "primary" && sendBody.image_model === null,
  JSON.stringify(sendBody));
check("update_unified_omits_absent_keys",
  !("speech" in sendBody) && !("default" in sendBody) && !("image_key" in sendBody),
  Object.keys(sendBody).join(","));

const failed = Object.entries(checks).filter(([, value]) => value !== true);
console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
if (failed.length) {
  console.error(`\n${failed.length} 项断言失败`);
  process.exit(1);
}
console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
