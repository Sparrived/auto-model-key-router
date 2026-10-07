// 试验场页面回归探针：用最小 DOM 垫片驱动 webui/pages/playground.js。
// 用法：node webui/probes/webui_playground_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。

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
  replaceChild(newChild, oldChild) {
    const idx = this.children.indexOf(oldChild);
    if (idx >= 0) {
      newChild.parent = this;
      this.children[idx] = newChild;
    }
  }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) { return this.attrs[key]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((item) => item !== fn);
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
  }
  querySelector(selector) {
    return findAll(this, (n) => matchesSelector(n, selector))[0] || null;
  }
  querySelectorAll(selector) {
    return findAll(this, (n) => matchesSelector(n, selector));
  }
  focus() {}
  get textContent() {
    return this.children.map((c) => (c.textContent === undefined ? String(c) : c.textContent)).join(" ");
  }
  get innerHTML() {
    return this.textContent;
  }
  set innerHTML(val) {
    this.children = [new FakeText(val)];
  }
}

class FakeText extends FakeNode {
  constructor(text) { super("#text"); this.data = text; }
  get textContent() { return this.data; }
}

function matchesSelector(node, selector) {
  if (!selector) return false;
  if (selector.startsWith(".")) {
    const cls = selector.slice(1);
    const classes = `${node.className || ""} ${node.attrs?.class || ""}`.split(/\s+/);
    return classes.includes(cls);
  }
  if (selector.startsWith("[")) {
    const match = selector.match(/\[([a-zA-Z0-9_-]+)(?:="([^"]*)")?\]/);
    if (match) {
      const [, attr, val] = match;
      if (val === undefined) return attr in node.attrs;
      return node.attrs[attr] === val;
    }
  }
  return node.tagName?.toLowerCase() === selector.toLowerCase();
}

function findAll(node, predicate, out = []) {
  if (predicate(node)) out.push(node);
  for (const child of node.children) {
    if (child instanceof FakeNode) findAll(child, predicate, out);
  }
  return out;
}

const define = (name, value) =>
  Object.defineProperty(global, name, { value, writable: true, configurable: true });

const location = { hash: "#/playground", pathname: "/ui/" };
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
  appendChild: (child) => body.appendChild(child),
  removeChild: (child) => child.remove(),
  addEventListener() {},
  removeEventListener() {},
});
const realSetTimeout = global.setTimeout;
define("setTimeout", (fn, delay) => realSetTimeout(fn, 0));
define("localStorage", { getItem: () => "test-token", setItem() {}, removeItem() {} });

const requests = [];
let routes = {};

const respond = (status, payload) => ({
  ok: status >= 200 && status < 300,
  status,
  async text() { return payload === undefined ? "" : JSON.stringify(payload); },
  async json() { return payload; },
});

const KIND_TABLE = {
  "model-text-a": { kinds: ["text"], primary: "text", endpoints: ["chat"] },
  "model-text-b": { kinds: ["text"], primary: "text", endpoints: ["chat"] },
  "model-image-a": { kinds: ["image"], primary: "image", endpoints: ["images"] },
  "unified-model": { kinds: ["text", "image"], primary: "text", endpoints: ["chat", "images"] },
};

global.fetch = async (url, options = {}) => {
  const method = options.method || "GET";
  const route = String(url).split("?")[0];
  const payload = options.body ? JSON.parse(options.body) : null;
  requests.push({ route, method, payload, headers: options.headers });

  if (route === "/ui/model-kinds.json") {
    const models = {};
    for (const name of new URL(String(url), "http://probe.invalid").searchParams.getAll("model")) {
      models[name] = KIND_TABLE[name] || { kinds: [], primary: "unknown", endpoints: [] };
    }
    return respond(200, { version: 1, catalog_available: true, models });
  }

  const key = `${method} ${route}`;
  if (routes[key]) return routes[key](payload, options);
  throw new Error(`未预置的请求: ${key}`);
};

// 导入模块
const { renderPlayground } = await import(pathToFileURL(path.join(WEBUI, "pages", "playground.js")).href);
const { api } = await import(pathToFileURL(path.join(WEBUI, "api.js")).href);

const checks = {};
let failed = 0;
const assert = (name, ok) => {
  checks[name] = !!ok;
  if (!ok) {
    failed += 1;
    console.error(`FAIL: ${name}`);
  }
};

// 预置默认路由
routes["GET /v1/models"] = () => respond(200, {
  object: "list",
  data: [
    { id: "model-text-a", object: "model" },
    { id: "model-text-b", object: "model" },
    { id: "model-image-a", object: "model" },
    { id: "unified-model", object: "model" },
  ],
});
routes["GET /api/models"] = () => respond(200, {
  config_revision: "rev1",
  models: [
    { id: "model-text-a", keys: [] },
    { id: "model-text-b", keys: [] },
    { id: "model-image-a", keys: [] },
  ],
});
routes["GET /api/workspaces"] = () => respond(200, {
  workspaces: [{ name: "team-a" }, { name: "team-b" }],
});

let leaveCalled = false;
const ctx = {
  navigate: () => {},
  onLeave: (fn) => { fn(); leaveCalled = true; },
};

// 1. 验证同步渲染契约
const root = renderPlayground(ctx);
assert("render_returns_node_synchronously", root instanceof FakeNode);
assert("render_not_async_promise", root.constructor.name !== "Promise");

// 等待异步数据加载
await new Promise((resolve) => setTimeout(resolve, 10));

// 2. 检查基本结构渲染
assert("has_page_head", !!root.querySelector(".page-head"));
assert("has_tab_switcher", !!root.querySelector(".segmented"));
assert("has_chat_main", !!root.querySelector(".pg-chat-main"));
assert("has_chat_input", !!root.querySelector(".chat-input-textarea"));

// 3. 检查模型加载与默认挑选
assert("models_requested", requests.some((r) => r.route === "/v1/models"));
assert("workspaces_requested", requests.some((r) => r.route === "/api/workspaces"));

// 4. 检查 API 层 chatCompletions (非流式)
routes["POST /v1/chat/completions"] = (payload) => respond(200, {
  id: "chat-123",
  choices: [{
    message: {
      role: "assistant",
      content: "Hello! This is a test response.",
      reasoning_content: "Thinking step 1... step 2...",
    },
  }],
  usage: { prompt_tokens: 10, completion_tokens: 20, total_tokens: 30 },
});

const chatRes = await api.chatCompletions({
  body: {
    model: "model-text-a",
    messages: [{ role: "user", content: "Hi" }],
    stream: false,
  },
});

assert("chat_completions_returns_message", chatRes?.message?.content === "Hello! This is a test response.");
assert("chat_completions_returns_reasoning", chatRes?.message?.reasoning_content.includes("Thinking step"));
assert("chat_completions_returns_usage", chatRes?.usage?.total_tokens === 30);
assert("chat_completions_tracks_duration", typeof chatRes?.duration === "number");

// 5. 检查 API 层 imageGenerations
routes["POST /v1/images/generations"] = (payload) => respond(200, {
  created: 1234567,
  data: [{ url: "https://example.com/test.png", revised_prompt: "enhanced prompt" }],
});

const imgRes = await api.imageGenerations({
  body: {
    model: "model-image-a",
    prompt: "a cat in space",
    size: "1024x1024",
  },
});

assert("image_generations_returns_data", imgRes?.data?.length === 1);
assert("image_generations_url_matches", imgRes?.data?.[0]?.url === "https://example.com/test.png");
assert("image_generations_tracks_duration", typeof imgRes?.duration === "number");

// 6. 检查错误状态处理
routes["POST /v1/chat/completions"] = () => respond(404, {
  error: { message: "模型不存在或未配置" },
});

let errorCaught = false;
try {
  await api.chatCompletions({
    body: { model: "unknown-model", messages: [{ role: "user", content: "test" }] },
  });
} catch (e) {
  errorCaught = true;
  assert("api_error_contains_message", e.message.includes("模型不存在或未配置"));
}
assert("chat_error_thrown", errorCaught);

// 7. 检查离开钩子被触发
assert("on_leave_registered", leaveCalled);

console.log(JSON.stringify({ checks, failed, total: Object.keys(checks).length }, null, 2));
if (failed > 0) process.exit(1);
