// 供应商页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/providers.js 与 brand-icons.js。
// 用法：node webui_providers_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么必须用 DOM 垫片：这次改动的全部风险都在"渲染出来的结构"上——供应商切换器
// 是否真的竖排在左侧、品牌图标是否真的落到按钮里、预置是否真的填进了输入框。
// brand-icons.js 的纯函数（brandForProvider）测不到这些。
//
// 垫片忠实地保留 null 与文本节点语义（原生 replaceChildren 会把 null 参数变成文本
// "null"），否则会把 dom.js 的回归藏起来——见 webui_tip_probe.mjs 的同款说明。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.parent = null;
    this.value = "";
    this.checked = false;
  }
  // 与 webui/dom.js 的 append() 对齐：它自己过滤 null，再交给这里。
  append(...nodes) { this.#adopt(nodes); }
  // 忠实还原浏览器：null / undefined / false 都会变成文本节点。
  replaceChildren(...nodes) { this.children = []; this.#adopt(nodes); }
  #adopt(nodes) {
    for (const node of nodes.flat()) {
      const child = node instanceof FakeNode ? node : new FakeText(node);
      child.parent = this;
      this.children.push(child);
    }
  }
  appendChild(node) { this.append(node); return node; }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) { return this.attrs[key]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((item) => item !== fn);
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
  }
  // dialog() 用它把焦点放到第一个控件上；只支持 "a, b, c" 这样的标签名列表，
  // 对本次断言够用，也避免引入真正的选择器实现。
  querySelector(selector) {
    const tags = String(selector).split(",").map((part) => part.trim());
    return findAll(this, (node) => tags.includes(node.tagName))[0] || null;
  }
  prepend(...nodes) { this.children.unshift(...nodes.flat()); }
  replaceWith() {}
  focus() {}
  get classList() {
    const self = this;
    const names = () => String(self.className).split(/\s+/).filter(Boolean);
    return {
      add: (...add) => { self.className = [...new Set([...names(), ...add])].join(" "); },
      remove: (...drop) => { self.className = names().filter((n) => !drop.includes(n)).join(" "); },
      contains: (name) => names().includes(name),
    };
  }
  get textContent() {
    return this.children
      .map((c) => (c.textContent === undefined ? String(c) : c.textContent))
      .join(" ");
  }
}

// 文本节点必须也是 Node（dom.js 用 `child instanceof Node` 判断），否则会被
// 当成字符串再包一层，读出来就是 "[object Object]"。
class FakeText extends FakeNode {
  constructor(data) { super("#text"); this.data = String(data); }
  get textContent() { return this.data; }
}

global.Node = FakeNode;
global.document = {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  body: new FakeNode("body"),
  addEventListener() {},
  removeEventListener() {},
};

// —— 假的供应商配置：覆盖"认得出品牌"与"认不出"两种情况 ——
// openai 的 primary 故意让探测结果比绑定结果多一个模型：Key 行下方要显示的是**正在
// 服务**的模型（模型 targets 里的绑定），而不是上游 /v1/models 广告出来的全部。
const PROVIDERS = [
  { id: "openai", base_url: "https://api.openai.com", keys: [{ name: "primary", capabilities: { models: ["gpt-5.5", "unbound-model"], errors: {} } }] },
  { id: "my-deepseek", base_url: "https://api.deepseek.com", keys: [] },
  { id: "local-lab", base_url: "https://gateway.internal.example", keys: [{ name: "a" }, { name: "b" }] },
];

// 绑定关系：primary 只服务 gpt-5.5，所以 unbound-model 不该出现在 Key 行下方。
const ROUTES = [
  { id: "gpt-5.5", targets: [{ provider: "openai", key: "primary", upstream_model: "gpt-5.5" }] },
  { id: "claude-sonnet-4-5", targets: [{ provider: "local-lab", key: "a", upstream_model: "claude-sonnet-4-5" }] },
];

// 请求流水：新增 Key 那一节要断言"真的写到了服务端"，而不只是"页面重画了一下"。
const requests = [];
global.fetch = async (url, options = {}) => {
  const target = String(url);
  const method = (options.method || "GET").toUpperCase();
  const payload = options.body ? JSON.parse(options.body) : null;
  requests.push(`${method} ${target.split("?")[0].replace(/^.*\/ui/, "")}`);
  // 新建 Key：像服务端一样把这把 Key 真的加进配置，后续的重新取数必须能拿到它。
  if (method === "POST" && /\/api\/providers\/[^/]+\/keys$/.test(target)) {
    const provider = PROVIDERS.find((item) => target.includes(`/providers/${item.id}/keys`));
    provider.keys = [...(provider.keys || []), { name: payload.name, enabled: true, capabilities: { models: ["gpt-5.5"], errors: {} } }];
    return { ok: true, status: 201, async text() { return JSON.stringify({ key: { name: payload.name }, config_revision: "rev-000000000001" }); } };
  }
  if (method === "POST" && target.endsWith("/probe")) {
    return { ok: true, status: 200, async text() { return JSON.stringify({ config_revision: "rev-000000000001" }); } };
  }
  // 新建的 Key 还没有任何绑定，所以这份清单是空的。
  if (/\/keys\/[^/]+\/models$/.test(target)) {
    return { ok: true, status: 200, async text() { return JSON.stringify({ models: [] }); } };
  }
  let payloadBody = {};
  if (target.includes("/api/providers")) {
    payloadBody = { providers: PROVIDERS, config_revision: "rev-000000000000" };
  } else if (target.includes("/api/routes")) {
    payloadBody = { routes: ROUTES, config_revision: "rev-000000000000" };
  }
  return { ok: true, status: 200, async text() { return JSON.stringify(payloadBody); } };
};
global.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
global.location = { pathname: "/ui/", hash: "#/providers" };
global.window = { addEventListener() {}, isSecureContext: true, location: global.location };

const { renderProviders } = await import(pathToFileURL(path.join(WEBUI, "pages", "providers.js")).href);
const { PROVIDER_PRESETS, brandForProvider } = await import(pathToFileURL(path.join(WEBUI, "brand-icons.js")).href);

const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};
const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
// SVG 节点的 class 落在属性上（SVGElement.className 只读，dom.js 走 setAttribute）。
const hasClass = (node, name) =>
  [node.className, node.attrs?.class].some((value) =>
    String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (n) => hasClass(n, name));
const buttonWithText = (root, text) =>
  findAll(root, (n) => n.tagName === "button" && n.textContent.includes(text))[0];
const click = (node) => { for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} }); };

// —— 渲染真实页面 ——
const host = renderProviders({});
await new Promise((resolve) => setTimeout(resolve, 0));
await new Promise((resolve) => setTimeout(resolve, 0));

// —— 布局：切换器必须竖排在左侧，横向标签页条必须消失 ——
// 类名用共用的 .rail-* （模型路由页也在用），供应商页特有的 .provider-* 已经不存在了。
const split = byClass(host, "rail-split")[0];
const rail = byClass(host, "rail-nav")[0];
check("split_present", Boolean(split));
check("rail_is_nav", rail?.tagName === "nav", rail?.tagName);
check("rail_is_first_column", split?.children[0] === rail);
check("detail_is_second_column", hasClass(split?.children[1] || new FakeNode("x"), "rail-detail"));
check("old_tabs_strip_removed", byClass(host, "tabs").length === 0);
check("detail_holds_cards", byClass(split?.children[1] || new FakeNode("x"), "card").length >= 2);

// —— 导航项：每个供应商一条，名称与 Key 数都要在 ——
const items = byClass(rail || new FakeNode("x"), "rail-item");
check("rail_item_count", items.length === PROVIDERS.length, `${items.length} != ${PROVIDERS.length}`);
check("rail_marks_active", items[0]?.attrs["aria-current"] === "true", items[0]?.attrs["aria-current"]);
check("rail_others_not_current", items.slice(1).every((n) => n.attrs["aria-current"] === undefined));
check("rail_shows_key_count", items[0]?.textContent.includes("1 Key") === true, items[0]?.textContent);
check("rail_marks_no_key", items[1]?.textContent.includes("无 Key") === true, items[1]?.textContent);
check("rail_semantics_not_fake_tab", items.every((n) => n.attrs.role === undefined));

// —— 品牌图标：认得出的用品牌标志，认不出的回退到通用图标 ——
check("brand_icon_used_for_openai", byClass(items[0] || new FakeNode("x"), "brand-icon").length === 1);
check("brand_icon_used_for_renamed_provider", byClass(items[1] || new FakeNode("x"), "brand-icon").length === 1);
check("generic_icon_for_unknown_host",
  byClass(items[2] || new FakeNode("x"), "brand-icon").length === 0 &&
  byClass(items[2] || new FakeNode("x"), "icon").length === 1);
// 品牌图标是填充路径且继承 currentColor；这两点是它和描边图标共存的约定。
const brandSvg = byClass(host, "brand-icon")[0];
check("brand_icon_fills_currentcolor", brandSvg?.attrs.fill === "currentColor", brandSvg?.attrs.fill);
check("brand_icon_viewbox_24", brandSvg?.attrs.viewBox === "0 0 24 24", brandSvg?.attrs.viewBox);
check("brand_icon_has_path", byTag(brandSvg || new FakeNode("x"), "path").length > 0);

// —— 切换供应商：详情必须跟着换 ——
click(items[1]);
const rail2 = byClass(host, "rail-nav")[0];
check("switch_updates_current", byClass(rail2, "rail-item")[1]?.attrs["aria-current"] === "true");
check("switch_repaints_detail", findText(host, "my-deepseek"));

// —— Key 行下方只列「正在服务」的模型 ——
// 需求：不要把上游探测到的模型全列出来（那可能是几百个），只显示这个 Key 真正在
// 服务的那些（模型 targets 里的绑定）。探测结果里多出来的 unbound-model 必须消失。
click(byClass(host, "rail-nav")[0].children[0]);
const keyTable = byClass(host, "table")[0];
const keyRowText = byTag(keyTable || new FakeNode("x"), "tr")[1]?.textContent || "";
check("key_row_lists_bound_model", keyRowText.includes("gpt-5.5"), keyRowText);
check("key_row_hides_unbound_probe_model", !keyRowText.includes("unbound-model"), keyRowText);

function byTag(root, tag) {
  return findAll(root, (node) => node.tagName === tag);
}
// 按整棵子树的文本比对，而不是只看叶子：文本落在 FakeText 上，承载它的元素
// （h3 / span）本身还有一层子节点，用"无子节点"当叶子判据会漏掉它们。
function findText(root, needle) {
  return findAll(root, (node) => node.tagName !== "#text" && node.textContent === needle).length > 0;
}

// —— 预置：必须在"添加供应商"对话框里，点击即填名称与地址 ——
const addButton = buttonWithText(host, "添加供应商");
check("add_button_present", Boolean(addButton));
click(addButton);

const presets = byClass(document.body, "preset");
check("presets_rendered_in_dialog", presets.length === PROVIDER_PRESETS.length,
  `${presets.length} != ${PROVIDER_PRESETS.length}`);
check("presets_are_buttons", presets.every((n) => n.tagName === "button"));
check("presets_have_icons", presets.every((n) => byClass(n, "brand-icon").length === 1));
check("presets_have_label", presets.every((n) => byClass(n, "brand-icon").length && n.textContent.trim().length > 0));
check("preset_group_labelled",
  byClass(document.body, "preset-grid")[0]?.attrs["aria-label"] === "常见供应商");

const dialogPanel = byClass(document.body, "dialog")[0];
const inputs = byClass(dialogPanel || new FakeNode("x"), "input");
check("dialog_has_name_and_url_inputs", inputs.length === 2, `${inputs.length} 个输入框`);

// 点第二个预置（DeepSeek），断言它把名称与地址都填了进去。
click(presets[1]);
check("preset_fills_name", inputs[0]?.value === PROVIDER_PRESETS[1].id, inputs[0]?.value);
check("preset_fills_base_url", inputs[1]?.value === PROVIDER_PRESETS[1].baseUrl, inputs[1]?.value);
check("preset_marks_pressed", presets[1]?.attrs["aria-pressed"] === "true", presets[1]?.attrs["aria-pressed"]);
check("other_presets_not_pressed", presets[0]?.attrs["aria-pressed"] === "false", presets[0]?.attrs["aria-pressed"]);

// —— 预置地址必须与后端默认路由拼得起来 ——
// 后端把默认路径（v1/chat/completions 一类）拼在 base_url 之后，所以 base_url 里
// 不能再带 /v1 或具体端点，否则会拼出 /v1/v1/... 这种死地址。这条不变量是预置
// 功能真正的风险点：拼错了用户只会看到探测失败，很难想到是预置地址的问题。
const join = (base, p) => `${String(base).replace(/\/+$/, "")}/${String(p).replace(/^\/+/, "")}`;
const badBases = PROVIDER_PRESETS.filter((p) => /\/v\d+$/.test(p.baseUrl) || /\/(chat\/completions|messages|responses)$/.test(p.baseUrl));
check("preset_bases_have_no_version_suffix", badBases.length === 0, badBases.map((p) => p.id).join(","));
check("preset_join_default_path",
  PROVIDER_PRESETS.every((p) => join(p.baseUrl, "v1/chat/completions").endsWith("/v1/chat/completions")));
check("preset_ids_unique", new Set(PROVIDER_PRESETS.map((p) => p.id)).size === PROVIDER_PRESETS.length);
check("preset_bases_unique", new Set(PROVIDER_PRESETS.map((p) => p.baseUrl)).size === PROVIDER_PRESETS.length);
check("preset_hosts_unique", new Set(PROVIDER_PRESETS.map((p) => p.host)).size === PROVIDER_PRESETS.length);
check("preset_count_reasonable", PROVIDER_PRESETS.length >= 8 && PROVIDER_PRESETS.length <= 24, String(PROVIDER_PRESETS.length));

// —— 品牌识别：host 优先于 id，且不得被单字符品牌名误伤 ——
// xAI 的品牌名是单字符 "x"。若拿 brand 当子串去匹配供应商 id，`my-box`、
// `proxy` 这类无关命名都会被判成 xAI——所以匹配必须走 preset.keys。
check("brand_by_host", brandForProvider("任意改名", "https://api.deepseek.com") === "deepseek");
check("brand_by_host_subdomain", brandForProvider("x", "https://api.moonshot.cn") === "moonshot");
check("brand_by_id_substring", brandForProvider("my-deepseek", "https://gateway.internal.example") === "deepseek");
check("brand_by_alias", brandForProvider("claude-main", "https://gateway.internal.example") === "anthropic");
check("brand_host_beats_id", brandForProvider("deepseek", "https://api.openai.com") === "openai");
check("brand_no_single_char_false_positive",
  brandForProvider("my-box", "https://gateway.internal.example") === null,
  String(brandForProvider("my-box", "https://gateway.internal.example")));
check("brand_unknown_is_null", brandForProvider("local-lab", "https://gateway.internal.example") === null);
// 本地三家的 host 靠端口区分，所以端口必须留在 host 里参与比对。
check("brand_local_by_port", brandForProvider("local", "http://127.0.0.1:11434") === "ollama");
check("brand_handles_garbage_url", brandForProvider("vllm", "::not a url::") === "vllm");
check("brand_trailing_slash_ok", brandForProvider("x", "https://api.groq.com/openai/") === "groq");

// —— 新增 Key：页面必须自己更新，不能要求手动刷新 ——
//
// 锁的是一个**静默**故障：新增流程结束时会给「服务模型」编辑器塞一份状态，那份状态若不
// 完整，modelEditor() 会在重画时抛 TypeError，而 draw() 抛错的后果是**整页一个字都不更新**
// ——Key 已经写进服务端了（这里的假 fetch 也真的改了配置），界面却还停在加之前的样子，
// 只有手动刷新才看得到。所以断言落在"新 Key 的行、计数、编辑器都在页面上"，而不是"发过请求"。
// 先把上一个用例留在 body 里的对话框清掉，否则取到的会是「添加供应商」那个框。
document.body.children = [];
click(buttonWithText(host, "添加 Key"));
const keyDialog = byClass(document.body, "dialog")[0];
const keyInputs = byClass(keyDialog || new FakeNode("x"), "input");
check("add_key_dialog_has_name_and_secret", keyInputs.length === 2, `${keyInputs.length} 个输入框`);
keyInputs[0].value = "secondary";
keyInputs[1].value = "sk-secondary";
click(buttonWithText(keyDialog, "添加 Key"));
// 新增流程在探测完成后会停 450ms 再关框重画（探针不垫片化 setTimeout，这里如实等它）。
await new Promise((resolve) => setTimeout(resolve, 600));
await new Promise((resolve) => setTimeout(resolve, 0));

check("add_key_posts_to_server", requests.includes("POST /api/providers/openai/keys"));
check("add_key_count_updated", host.textContent.includes("Key（2）"), host.textContent.replace(/\s+/g, " ").slice(0, 160));
const addedRows = byTag(byClass(host, "table")[0] || new FakeNode("x"), "tr");
check("add_key_row_appears_without_reload",
  addedRows.some((row) => row.textContent.includes("secondary")),
  `${addedRows.length} 行：${addedRows.map((row) => row.textContent.trim()).join(" / ")}`);
check("add_key_drops_empty_placeholder", !findText(host, "尚无 Key。"));
check("add_key_opens_model_editor", findText(host, "Key secondary 的服务模型"));
check("add_key_editor_reads_bindings",
  requests.includes("GET /api/providers/openai/keys/secondary/models") && host.textContent.includes("0 个已选"),
  host.textContent.includes("0 个已选") ? "" : "编辑器仍停在读取中");

const failed = Object.entries(checks).filter(([, value]) => value !== true);
console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
if (failed.length) {
  console.error(`\n${failed.length} 项断言失败`);
  process.exit(1);
}
console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
