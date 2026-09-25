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
//      否则下拉显示空值、保存却仍把那个名字发给服务端，被「引用了未配置的模型」顶回来。

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

global.fetch = async (url, options = {}) => {
  const method = options.method || "GET";
  const route = String(url).split("?")[0];
  const payload = options.body ? JSON.parse(options.body) : null;
  requests.push({ route, method, payload });
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
const settle = async () => { for (let i = 0; i < 60; i += 1) await Promise.resolve(); };
const putCalls = () => requests.filter((r) => r.method === "PUT" && r.route.endsWith("/api/unified-model"));
const getCalls = () => requests.filter((r) => r.method === "GET" && r.route.includes("/api/"));

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

const failed = Object.entries(checks).filter(([, value]) => value !== true);
console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
if (failed.length) {
  console.error(`\n${failed.length} 项断言失败`);
  process.exit(1);
}
console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
