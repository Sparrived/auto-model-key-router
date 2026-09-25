// 访问密钥页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/accesskeys.js。
// 用法：node webui/probes/webui_accesskeys_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么值得单独锁：这一页管的是**发给外部使用者**的凭据，而两份清单是它唯一的权限收窄
// 手段，其中三态有两个是**相反**的授权：
//
//   不传字段    = 不限制（这把 key 什么都能调）
//   []          = 一个都不许
//   ["x"]       = 限定为 x
//
// 三种翻车方式都不会报错，只会静默把权限放大或缩小：
//
//   1. 提交时把「不限制」与「一个都不许」折叠成同一个值 —— 运维写下的禁令会静默失效；
//   2. 「把勾选全部取消」被解读成「不限制」—— 一个明显的收紧动作反而放开了全部；
//   3. 清单只能手写 —— 拼错一个字符就是某把已经发出去的 key 静默少一项权限（或写盘时
//      422），而模型清单按调用方写的**原始名字**逐字比对，别名与真实 ID 是两项。
//
// 所以这里断两件事：界面上**没有**可以手写清单的文本框；提交体里的三态逐字节正确。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

// —— 最小 DOM 垫片（与 webui_guest_probe.mjs 同一套）——
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

const location = { hash: "#/access-keys", pathname: "/ui/" };
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
define("setTimeout", () => 0);
define("localStorage", { getItem: () => null, setItem() {}, removeItem() {} });

// —— 假配置 ——
// sechz 两个 Key 刻意覆盖三态：一把限定供应商+模型（含别名），一把不限制供应商但把模型
// 收窄到一个**已经不在模型目录里**的名字（模型被改名/删除后的样子）。
const KEYS = [
  {
    id: "trial", name: "试用账号 A", enabled: true, key_fingerprint: "…abcd",
    providers: ["openai"], models: ["gpt-4o", "fast"],
  },
  {
    id: "internal", name: "内部试用", enabled: true, key_fingerprint: "…efgh",
    models: ["ghost-model"],
  },
];
const PROVIDERS = [{ id: "openai" }, { id: "deepseek" }];
// 候选要超过 8 个（模型 ID + 别名）才会出现筛选框，所以这里故意排到 10 个候选。
const MODELS = [
  { id: "gpt-4o", aliases: ["fast"] },
  { id: "claude-sonnet-4-5", aliases: [] },
  { id: "gemini-2.5-pro", aliases: [] },
  { id: "deepseek-chat", aliases: [] },
  { id: "qwen3-max", aliases: [] },
  { id: "kimi-k2", aliases: [] },
  { id: "glm-4.6", aliases: [] },
  { id: "mistral-large", aliases: [] },
  { id: "llama-4-maverick", aliases: [] },
];

const requests = [];
const respond = (status, payload) => ({
  ok: status >= 200 && status < 300,
  status,
  async text() { return payload === undefined ? "" : JSON.stringify(payload); },
});

global.fetch = async (url, options = {}) => {
  const target = String(url);
  const method = options.method || "GET";
  const route = target.split("?")[0];
  const payload = options.body ? JSON.parse(options.body) : null;
  requests.push({ route, method, payload });
  if (route.endsWith("/api/access-keys")) {
    if (method === "POST") {
      return respond(201, { id: "created", name: payload.name, key: "amkr_ak_created", key_fingerprint: "…new" });
    }
    return respond(200, { access_keys: KEYS, config_revision: "rev-1" });
  }
  if (method === "PUT" && route.includes("/api/access-keys/")) return respond(200, { config_revision: "rev-2" });
  if (route.endsWith("/api/providers")) return respond(200, { providers: PROVIDERS, config_revision: "rev-1" });
  if (route.endsWith("/api/models")) return respond(200, { models: MODELS, config_revision: "rev-1" });
  return respond(200, {});
};

const { renderAccessKeys } = await import(pathToFileURL(path.join(WEBUI, "pages", "accesskeys.js")).href);

// —— 查询与驱动工具 ——
const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
const hasClass = (node, name) =>
  [node.className, node.attrs?.class].some((value) => String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (node) => hasClass(node, name));
const inputsOf = (root) => findAll(root, (node) => node.tagName === "input");
const buttonsOf = (root) => findAll(root, (node) => node.tagName === "button");
const buttonWithText = (root, text) =>
  buttonsOf(root).find((node) => node.textContent.trim() === text);
const click = (node) => { for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} }); };
// 垫片不会自己翻复选框：页面挂的是 change 钩子，所以这里手动改值再触发。
const setChecked = (box, checked) => {
  box.checked = checked;
  for (const fn of box.listeners.change || []) fn({ target: box });
};
const settle = async () => { for (let i = 0; i < 40; i += 1) await Promise.resolve(); };
// 选择器：picker 的复选框就是那个「不限制」开关，chip 就是候选项。
const pickersOf = (root) => byClass(root, "picker");
const unrestrictedBoxOf = (picker) => inputsOf(picker).find((node) => node.attrs.type === "checkbox");
const chipNamed = (picker, name) => byClass(picker, "chip").find((node) => node.textContent.trim() === name);
// 摘要那个 span 是 head 里唯一的 .muted（另两个是「全选 / 清空」按钮）。
const summaryOf = (picker) => (byClass(picker, "picker-head")[0]?.children || [])
  .filter((node) => node.tagName !== "#text" && hasClass(node, "muted"))
  .map((node) => node.textContent.trim())
  .join("");

const checks = {};
const check = (name, ok, detail = "") => { if (!ok) checks[name] = `FAILED${detail ? `: ${detail}` : ""}`; else checks[name] = true; };

// ═══ 列表页 ═══
const host = renderAccessKeys({});
await settle();

check("renders_both_keys", findAll(host, (n) => n.tagName === "tr").length === 3,
  `${findAll(host, (n) => n.tagName === "tr").length} 行（含表头）`);
const rows = findAll(host, (n) => n.tagName === "tr").slice(1);
check("unrestricted_scope_says_unrestricted", rows[1].textContent.includes("不限制"), rows[1].textContent);
check("empty_scope_not_shown_as_unrestricted", !rows[0].textContent.includes("不限制"), rows[0].textContent);

// ═══ 新建对话框：直接勾选，不是手写 ═══
click(buttonWithText(host, "新建访问密钥"));
const dialog = byClass(body, "dialog")[0];
check("create_dialog_opened", Boolean(dialog));

// 唯一的两个自由文本框必须是「名称」与「密钥」——清单不再有输入框。
const freeInputs = inputsOf(dialog).filter((node) => node.attrs.type !== "checkbox" && node.attrs.type !== "search");
check("no_free_text_scope_input", freeInputs.length === 2, `${freeInputs.length} 个自由文本框`);
check("no_comma_separated_placeholder",
  freeInputs.every((node) => !String(node.attrs.placeholder || "").includes("逗号分隔")),
  freeInputs.map((node) => node.attrs.placeholder).join(" | "));

const [providerPicker, modelPicker] = pickersOf(dialog);
check("two_pickers", pickersOf(dialog).length === 2);
check("provider_chips_from_config",
  ["openai", "deepseek"].every((id) => Boolean(chipNamed(providerPicker, id))),
  byClass(providerPicker, "chip").map((c) => c.textContent).join(","));
// 别名必须**单独**可选：清单按原始名字逐字比对，勾了 gpt-4o 并不放行 fast。
check("alias_is_selectable_separately", Boolean(chipNamed(modelPicker, "fast")));
check("alias_chip_names_its_model", chipNamed(modelPicker, "fast")?.attrs.title === "别名 · gpt-4o",
  chipNamed(modelPicker, "fast")?.attrs.title);
check("filter_only_for_long_lists",
  inputsOf(modelPicker).filter((n) => n.attrs.type === "search").length === 1
  && inputsOf(providerPicker).filter((n) => n.attrs.type === "search").length === 0);
check("both_pickers_default_to_unrestricted",
  unrestrictedBoxOf(providerPicker).checked && unrestrictedBoxOf(modelPicker).checked);
check("unrestricted_is_explicit", byClass(providerPicker, "check")[0].textContent.includes("不限制"));

// 关掉「不限制」但一个都不勾 = 显式的一个都不许，而不是回到不限制。
setChecked(unrestrictedBoxOf(providerPicker), false);
check("clearing_unrestricted_means_none_allowed", summaryOf(providerPicker) === "一个都不许",
  summaryOf(providerPicker));
check("chips_enabled_once_restricted", chipNamed(providerPicker, "openai").disabled === false);

click(chipNamed(providerPicker, "openai"));
check("chip_marks_pressed", chipNamed(providerPicker, "openai").attrs["aria-pressed"] === "true");
check("selection_counted", summaryOf(providerPicker) === "已选 1 项", summaryOf(providerPicker));

inputsOf(dialog)[0].value = "试用账号 B";
click(buttonWithText(dialog, "创建"));
await settle();

const created = requests.find((r) => r.method === "POST" && r.route.endsWith("/api/access-keys"));
check("create_posted", Boolean(created));
check("create_sends_selected_providers", JSON.stringify(created?.payload?.providers) === '["openai"]',
  JSON.stringify(created?.payload?.providers));
// 另一份清单留在「不限制」：字段整体不传，而不是传 null 或空数组。
check("create_omits_unrestricted_scope", created?.payload && !("models" in created.payload),
  JSON.stringify(created?.payload));
check("create_omits_key_when_blank", created?.payload && !("key" in created.payload));

// ═══ 编辑行：三态必须原样提交 ═══
body.children = [];
click(findAll(host, (n) => n.tagName === "button" && n.textContent.trim() === "编辑")[1]);
const [providerEditor, modelEditor] = pickersOf(host);
check("editor_uses_pickers", pickersOf(host).length === 2);
check("editor_shows_unrestricted_when_absent", unrestrictedBoxOf(providerEditor).checked);
check("editor_shows_restricted_when_present", unrestrictedBoxOf(modelEditor).checked === false);
check("editor_marks_missing_name",
  hasClass(chipNamed(modelEditor, "ghost-model") || new FakeNode("x"), "is-unknown")
  && chipNamed(modelEditor, "ghost-model")?.attrs["aria-pressed"] === "true",
  chipNamed(modelEditor, "ghost-model")?.className);

// 不动清单直接保存：PUT 必须**显式**带 null（漏传会被 specAccessKeyUpdate 拒掉）。
click(buttonWithText(host, "保存"));
await settle();
const firstSave = requests.find((r) => r.method === "PUT");
check("update_posted", Boolean(firstSave));
check("update_sends_null_for_unrestricted",
  firstSave?.payload && "providers" in firstSave.payload && firstSave.payload.providers === null,
  JSON.stringify(firstSave?.payload?.providers));
check("update_sends_existing_scope",
  JSON.stringify(firstSave?.payload?.models) === '["ghost-model"]',
  JSON.stringify(firstSave?.payload?.models));

// 关掉「不限制」再存：一个都不勾必须是空数组，而不是被折叠成 null。
click(findAll(host, (n) => n.tagName === "button" && n.textContent.trim() === "编辑")[1]);
setChecked(unrestrictedBoxOf(pickersOf(host)[0]), false);
click(buttonWithText(host, "保存"));
await settle();
const secondSave = requests.filter((r) => r.method === "PUT").at(-1);
check("update_sends_empty_array_for_none",
  Array.isArray(secondSave?.payload?.providers) && secondSave.payload.providers.length === 0,
  JSON.stringify(secondSave?.payload?.providers));

const failed = Object.entries(checks).filter(([, value]) => value !== true);
console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
if (failed.length) {
  console.error(`\n${failed.length} 项断言失败`);
  process.exit(1);
}
console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
