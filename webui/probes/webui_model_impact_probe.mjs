// 模型改动「连带影响」的回归探针：用最小 DOM 垫片驱动真实的 webui/pages/providers.js
// 与 webui/model-impact.js。用法：node webui_model_impact_probe.mjs，结果以 JSON 打到
// stdout，失败时退出码 1。
//
// 锁的是这条流程的**顺序与措辞**：删掉一个模型从来不只是删那个模型——引用它的访问密钥、
// 工作空间与任务都要跟着变。而这些变动原先会让整次保存被校验驳回（用户想删的恰恰是那个
// 模型，却因为一份清单里还写着它而删不动）。现在的口径是「自动改，但先说清楚」，于是：
//
//   1. 先发一次 `dry_run=1` 的预演，参数与真写**完全相同**，只多一个开关；
//   2. 预演报出连带变动时必须先弹确认框，把要变动的资源逐个列出来；
//   3. 用户点确认之前一个字节都不能落盘（取消 = 什么都没发生，编辑态保留）；
//   4. 没有连带变动时不弹框、直接写。
//
// 顺序是这里最容易写错的地方：先写后问等于把「确认」变成事后通知，而先问后写又必须保证
// 两次请求送的是同一份清单。
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
  // dialog() 用它把焦点放到第一个控件上；只支持 "a, b, c" 这样的标签名列表。
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

// —— 假配置：Key primary 同时服务 model-a 与 model-b ——
// 取消勾选 model-a 之后它失去全部绑定，于是四类引用一起失效，正好把确认框的每一节都用上。
const PROVIDERS = [
  {
    id: "openai",
    base_url: "https://api.openai.com",
    keys: [{ name: "primary", enabled: true, capabilities: { models: ["model-a", "model-b"], errors: {} } }],
  },
];
const ROUTES = [
  { id: "model-a", targets: [{ provider: "openai", key: "primary", upstream_model: "model-a" }] },
  { id: "model-b", targets: [{ provider: "openai", key: "primary", upstream_model: "model-b" }] },
];

// 服务端的预演结果。字段与服务端响应逐一对齐（见 internal/api/impact.go）。
const IMPACT = {
  dry_run: true,
  removed_models: ["model-a"],
  access_keys: [{
    id: "public", name: "公开",
    models: ["model-a"], providers: [],
    models_cleared: false, providers_cleared: false,
  }],
  workspaces: [{
    id: "teamA", name: "teamA",
    models: ["model-a"], providers: [],
    models_cleared: false, providers_cleared: false,
  }],
  removed_tasks: ["shared"],
  unified_model: true,
  config_revision: "rev-000000000002",
};
const NO_IMPACT = {
  dry_run: true,
  removed_models: [],
  access_keys: [],
  workspaces: [],
  removed_tasks: [],
  unified_model: false,
  config_revision: "rev-000000000002",
};

// impactMode 由用例切换：full = 预演报出连带变动，none = 没有连带变动。
let impactMode = "full";
let bound = ["model-a", "model-b"];
const calls = [];

const jsonResponse = (payload, status = 200) => ({
  ok: status < 400,
  status,
  async text() { return JSON.stringify(payload); },
});

global.fetch = async (url, options = {}) => {
  const target = String(url);
  const method = (options.method || "GET").toUpperCase();
  const path = target.split("?")[0].replace(/^.*\/ui/, "");
  const dryRun = target.includes("dry_run=1");
  const body = options.body ? JSON.parse(options.body) : null;
  if (method !== "GET") calls.push({ path, method, dryRun, body });

  if (path.endsWith("/models") && path.includes("/keys/")) {
    if (method === "PUT") {
      if (dryRun) return jsonResponse(impactMode === "full" ? IMPACT : NO_IMPACT);
      bound = body.models;
      return jsonResponse({ provider_id: "openai", key: "primary", models: bound, config_revision: "rev-000000000003" });
    }
    return jsonResponse({ provider_id: "openai", key: "primary", models: bound, config_revision: "rev-000000000001" });
  }
  if (path.includes("/api/routes")) {
    return jsonResponse({ routes: ROUTES, config_revision: "rev-000000000001" });
  }
  if (path.includes("/api/providers")) {
    return jsonResponse({ providers: PROVIDERS, config_revision: "rev-000000000001" });
  }
  return jsonResponse({});
};
global.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
global.location = { pathname: "/ui/", hash: "#/providers" };
global.window = { addEventListener() {}, isSecureContext: true, location: global.location };

const { renderProviders } = await import(pathToFileURL(path.join(WEBUI, "pages", "providers.js")).href);

const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};
const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
const hasClass = (node, name) =>
  [node.className, node.attrs?.class].some((value) =>
    String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (n) => hasClass(n, name));
const buttonWithText = (root, text) =>
  findAll(root, (n) => n.tagName === "button" && n.textContent.includes(text))[0];
const click = (node) => { for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} }); };
const settle = async () => {
  for (let i = 0; i < 4; i += 1) await new Promise((resolve) => setTimeout(resolve, 0));
};
// 确认框挂在 document.body 上（不在页面 host 里），因此找按钮要从 body 找。
const dialogPanel = () => byClass(document.body, "dialog")[0] || null;
const dialogText = () => (dialogPanel() || new FakeNode("x")).textContent;
const modelsWrites = () => calls.filter((call) => call.path.endsWith("/models") && call.path.includes("/keys/"));

// —— 打开 Key 的「服务模型」编辑器并取消勾选 model-a ——
const host = renderProviders({});
await settle();
check("editor_closed_initially", !findText(host, "Key primary 的服务模型"));

click(buttonWithText(host, "管理模型"));
await settle();
check("editor_open", findText(host, "Key primary 的服务模型"), host.textContent);
check("editor_reads_bindings", findText(host, "2 个已选"), host.textContent);

click(buttonWithText(host, "model-a"));
check("chip_deselected", findText(host, "1 个已选"), host.textContent);

// —— 第一次保存：必须先预演、再弹确认框，且此时还没落盘 ——
click(buttonWithText(host, "保存模型"));
await settle();

const beforeConfirm = modelsWrites();
check("dry_run_sent_first", beforeConfirm.length === 1 && beforeConfirm[0].dryRun === true,
  JSON.stringify(beforeConfirm.map((call) => `${call.method}${call.dryRun ? "?dry_run=1" : ""}`)));
check("dry_run_carries_selection",
  JSON.stringify(beforeConfirm[0]?.body?.models) === JSON.stringify(["model-b"]),
  JSON.stringify(beforeConfirm[0]?.body));
check("dry_run_keeps_revision", beforeConfirm[0]?.body?.config_revision === "rev-000000000001",
  String(beforeConfirm[0]?.body?.config_revision));
check("no_write_before_confirm", beforeConfirm.every((call) => call.dryRun), "确认之前不该有真写");

const text = dialogText();
check("confirm_dialog_shown", Boolean(dialogPanel()));
check("impact_lists_model", text.includes("model-a 失去全部 Key 绑定，将被删除"), text);
check("impact_lists_access_key", text.includes("公开（public）：模型清单移除 model-a"), text);
check("impact_lists_workspace", text.includes("teamA：模型清单移除 model-a"), text);
check("impact_lists_task", text.includes("shared 引用了被删的模型，将被一并删除"), text);
check("impact_lists_unified", text.includes("unified_model 指向被删的模型"), text);
check("confirm_button_present", Boolean(buttonWithText(dialogPanel(), "确认改动")));

// —— 取消：一个字节都不能落盘，编辑态也要留着 ——
click(buttonWithText(dialogPanel(), "取消"));
await settle();
check("cancel_writes_nothing", modelsWrites().every((call) => call.dryRun),
  JSON.stringify(modelsWrites().map((call) => `${call.method}${call.dryRun ? "?dry_run=1" : ""}`)));
check("cancel_keeps_editor", findText(host, "Key primary 的服务模型"), host.textContent);
check("dialog_closed_after_cancel", !dialogPanel());

// —— 第二次保存：确认之后才真写，且送的是同一份清单 ——
click(buttonWithText(host, "保存模型"));
await settle();
click(buttonWithText(dialogPanel(), "确认改动"));
await settle();

const writes = modelsWrites();
const realWrites = writes.filter((call) => !call.dryRun);
check("real_write_after_confirm", realWrites.length === 1, JSON.stringify(writes.length));
check("real_write_same_selection",
  JSON.stringify(realWrites[0]?.body?.models) === JSON.stringify(["model-b"]),
  JSON.stringify(realWrites[0]?.body));
check("editor_closed_after_confirm", !findText(host, "Key primary 的服务模型"), host.textContent);

// —— 没有连带变动时不弹框：一次预演 + 一次真写，中间不问 ——
impactMode = "none";
click(buttonWithText(host, "管理模型"));
await settle();
const beforeQuiet = modelsWrites().length;
click(buttonWithText(host, "保存模型"));
await settle();

check("no_dialog_without_impact", !dialogPanel(), dialogText());
const quiet = modelsWrites().slice(beforeQuiet);
check("quiet_flow_is_dry_run_then_write",
  quiet.length === 2 && quiet[0].dryRun === true && quiet[1].dryRun === false,
  JSON.stringify(quiet.map((call) => (call.dryRun ? "dry" : "write"))));

function findText(root, needle) {
  return findAll(root, (node) => node.tagName !== "#text" && node.textContent === needle).length > 0;
}

const failed = Object.entries(checks).filter(([, value]) => value !== true);
console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
if (failed.length) {
  console.error(`\n${failed.length} 项断言失败`);
  process.exit(1);
}
console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
