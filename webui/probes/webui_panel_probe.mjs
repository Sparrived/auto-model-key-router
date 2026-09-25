// 工作空间面板回归探针：用最小 DOM 垫片驱动 webui/panel.js 与 panel-api.js。
// 用法：node webui/probes/webui_panel_probe.mjs <scenario>，结果以 JSON 打到 stdout。
//
// 为什么值得单独锁：面板是**唯一**把工作空间 key 交给浏览器的地方，它的失败模式都是
// 安全性的，而不是"页面不好看"：
//
//   1. 面板绝不能读写 localStorage —— 它与管理面同源，用同一份存储会把管理员的
//      本地鉴权 Key 覆盖成面板 key（或反过来让面板拿到管理员 Key）；
//   2. 面板绝不能自己指定空间 —— 空间必须完全由 key 决定，前端多发一个空间头等于
//      把"钉死"这件事交给一个可以被改的输入；
//   3. 凭据只能从 fragment 来 —— 走查询串会进 Referer 与服务端访问日志。
//
// 每个场景单独起进程：panel.js 的 state 是模块级缓存，同进程连跑会互相污染。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");
const scenario = process.argv[2];

// —— 最小 DOM 垫片 ——
class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.value = "";
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

const root = new FakeNode("div");
root.attrs.id = "root";
root.documentRoot = true;

const define = (name, value) =>
  Object.defineProperty(global, name, { value, writable: true, configurable: true });

const location = {
  hash: "",
  pathname: "/ui/panel.html",
  href: "http://127.0.0.1:28881/ui/panel.html",
};
define("location", location);
const replaceStates = [];
define("window", {
  addEventListener() {},
  isSecureContext: true,
  location,
  confirm: () => true,
});
define("navigator", { clipboard: null });
define("history", {
  replaceState(_state, _title, url) {
    replaceStates.push(url);
    // 忠实还原：浏览器确实会改地址栏，因此后续读 location.hash 必须拿到新值。
    const index = String(url).indexOf("#");
    location.hash = index >= 0 ? String(url).slice(index) : "";
  },
});
global.Node = FakeNode;
define("document", {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  getElementById: (id) => (id === "root" ? root : null),
  body: new FakeNode("body"),
  addEventListener() {},
  removeEventListener() {},
  execCommand: () => true,
});

define("setInterval", () => 0);
define("clearInterval", () => {});
define("setTimeout", () => 0);

// localStorage 是**毒药**：面板碰到它就必须响亮地失败，而不是静默共享管理面的凭据。
// 因此这里不提供可用实现，而是记录每一次访问。
const storageTouches = [];
const storage = {
  getItem: (k) => { storageTouches.push(`get:${k}`); return null; },
  setItem: (k, v) => { storageTouches.push(`set:${k}=${v}`); },
  removeItem: (k) => { storageTouches.push(`remove:${k}`); },
  clear: () => { storageTouches.push("clear"); },
};
define("localStorage", storage);

// —— 假服务端 ——
const server = {
  panelKey: "amkr_ws_teamA",
  workspace: "teamA",
  requests: [],
  tasks: [],
  writes: [],
  usage: null,
};

function respond(status, payload) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async text() { return payload === undefined ? "" : JSON.stringify(payload); },
  };
}

global.fetch = async (url, options = {}) => {
  const headers = options.headers || {};
  const bearer = (headers.Authorization || "").replace(/^Bearer /, "");
  server.requests.push({
    url,
    method: options.method || "GET",
    bearer,
    workspaceHeader: headers["X-AMKR-Workspace"] ?? null,
    body: options.body ? JSON.parse(options.body) : null,
  });

  if (bearer !== server.panelKey) return respond(401, { detail: "本地 API key 验证失败" });

  const path = url.split("?")[0];
  if (path === "/ui/workspace-panel.json") {
    return respond(200, {
      count_semantics: "attempts",
      window: { from: null, to: "2026-01-01T10:00:00+08:00", hours: 24 },
      workspace: server.workspace,
      models: ["model-a", "alias-a"],
      workspaces: [{
        name: server.workspace,
        stats: { requests: 4, successes: 3, failures: 1, total_tokens: 400, cached_tokens: 0, avg_duration_ms: 120 },
      }],
      unattributed: {},
      layers: ["workspace", "requested_model_id", "model_id", "provider_id", "upstream_model_id"],
      links: [
        { source_layer: 0, target_layer: 1, source: server.workspace, target: "TASK_1", requests: 4, total_tokens: 400 },
        { source_layer: 1, target_layer: 2, source: "TASK_1", target: "model-a", requests: 4, total_tokens: 400 },
        { source_layer: 2, target_layer: 3, source: "model-a", target: "prov-a", requests: 4, total_tokens: 400 },
        { source_layer: 3, target_layer: 4, source: "prov-a", target: "up-model", requests: 4, total_tokens: 400 },
      ],
    });
  }
  if (path === "/api/tasks") {
    if (options.method === "POST") {
      server.writes.push({ method: "POST", body: JSON.parse(options.body) });
      if (server.holdSave) {
        return new Promise((resolve) => {
          server.releaseSave = () => resolve(respond(201, { config_revision: "rev-2" }));
        });
      }
      return respond(201, { config_revision: "rev-2" });
    }
    return respond(200, { config_revision: "rev-1", tasks: server.tasks });
  }
  if (path.startsWith("/api/tasks/")) {
    server.writes.push({
      method: options.method,
      name: decodeURIComponent(path.slice("/api/tasks/".length)),
      body: JSON.parse(options.body),
    });
    return respond(200, { config_revision: "rev-2" });
  }
  return respond(404, { detail: "Not Found" });
};

// —— 驱动 ——
const { bootPanel } = await import(pathToFileURL(path.join(WEBUI, "panel.js")).href);

const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) if (typeof child === "object") findAll(child, predicate, out);
  return out;
};
const text = () => root.textContent;
const inputs = () => findAll(root, (n) => n.tagName === "input");
const selects = () => findAll(root, (n) => n.tagName === "select");
const buttons = () => findAll(root, (n) => n.tagName === "button");
const clickButton = async (label) => {
  const target = buttons().find((b) => b.textContent.trim() === label);
  if (!target) throw new Error(`找不到按钮: ${label}（页面文字：${text().slice(0, 200)}）`);
  for (const handler of target.listeners.click || []) await handler({});
};
const settle = async () => { for (let i = 0; i < 12; i += 1) await Promise.resolve(); };

const setup = {
  // 没有 key：必须请人填，而不是直接打接口（否则用户看到 401，以为 key 填错了）。
  no_key_asks_for_one: () => {},
  // fragment 里带 key：应立刻用它取数，并把空间名画在页头。
  fragment_key_loads_panel: () => {
    location.hash = `#k=${encodeURIComponent(server.panelKey)}`;
  },
  // 错的 key：必须说"key 无效"，而不是"服务故障"。
  wrong_key_says_invalid: () => {
    location.hash = `#k=amkr_ws_wrong`;
  },
  // 任务管理：新建与编辑都要能落地，且请求体里**不带** params（那属于管理面）。
  tasks_editor_preserves_params: () => {
    location.hash = `#k=${encodeURIComponent(server.panelKey)}`;
    server.tasks = [
      { name: "TASK_1", model: "model-a", fallback_model: null, params: { temperature: 0.2 } },
    ];
  },
  // 嵌入方指定 API 基址（面板页与接口不同源时）。
  api_base_from_fragment: () => {
    location.hash = `#k=${encodeURIComponent(server.panelKey)}&api=${encodeURIComponent("http://amkr.internal:28881")}`;
  },
};
const scenarioNames = Object.keys(setup);
if (scenario === undefined) {
  const { spawnSync } = await import("node:child_process");
  let failedRuns = 0;
  for (const name of scenarioNames) {
    process.stdout.write(`--- ${name}\n`);
    const result = spawnSync(process.execPath, [process.argv[1], name], { stdio: "inherit" });
    if (result.status !== 0) failedRuns += 1;
  }
  process.exit(failedRuns ? 1 : 0);
}
if (!scenarioNames.includes(scenario)) {
  console.error(`未知场景：${scenario}\n可用场景：\n  ${scenarioNames.join("\n  ")}`);
  process.exit(2);
}
setup[scenario]();

bootPanel();
await settle();

const checks = {};

if (scenario === "no_key_asks_for_one") {
  checks.promptsForKey = text().includes("工作空间 key");
  checks.hasKeyField = inputs().some((n) => n.attrs.type === "password");
  // 关键：**没有**发过任何请求。直接打接口会让用户看到 401，误以为 key 错了。
  checks.madeNoRequests = server.requests.length === 0;
  checks.touchedNoStorage = storageTouches.length === 0;
} else if (scenario === "fragment_key_loads_panel") {
  checks.requestedUsage = server.requests.some((r) => r.url.startsWith("/ui/workspace-panel.json"));
  checks.requestedTasks = server.requests.some((r) => r.url === "/api/tasks");
  // 凭据走 Authorization: Bearer。
  checks.sentKeyAsBearer = server.requests.every((r) => r.bearer === server.panelKey);
  // 关键：**绝不**自己指定空间。服务端会忽略这个头，但前端多发一个就等于把"钉死"
  // 交给一个可被改的输入，读代码的人也会以为换个头能换空间。
  checks.neverSendsWorkspaceHeader = server.requests.every((r) => r.workspaceHeader === null);
  // 关键：全程不碰 localStorage（与管理面同源，共享存储会互相覆盖凭据）。
  checks.touchedNoStorage = storageTouches.length === 0;
  // 空间名来自服务端响应，而不是从 key 猜。
  checks.showsWorkspaceName = text().includes("teamA");
  checks.showsRequests = text().includes("4");
  checks.showsFlowLayers = text().includes("任务/别名") && text().includes("上游模型");
  checks.showsTasks = text().includes("TASK_1") || text().includes("任务（1）");
  // 任务表下面那两张半宽卡：任务用量按连边起点（任务名）汇总，上游模型用量按第 4 段
  // 终点汇总。栅格本身的 12 + 6 + 6 形状由 webui_layout_probe.mjs 锁。
  checks.hasUsageCards = text().includes("任务用量") && text().includes("上游模型用量");
} else if (scenario === "wrong_key_says_invalid") {
  // 被拒时要说"key 无效"，并且**重新给出填写入口**——只说失败会让人无处可去。
  checks.saysKeyInvalid = text().includes("无效");
  checks.offersKeyField = inputs().some((n) => n.attrs.type === "password");
  checks.triedTheKey = server.requests.length > 0 && server.requests.every((r) => r.bearer === "amkr_ws_wrong");
  checks.touchedNoStorage = storageTouches.length === 0;
} else if (scenario === "tasks_editor_preserves_params") {
  checks.listsTask = text().includes("TASK_1");
  // 编辑：改显示名后保存，请求走 PUT /api/tasks/TASK_1。
  await clickButton("编辑");
  checks.openedEditor = buttons().some((b) => b.textContent.trim() === "保存修改");
  const display = inputs().find((n) => n.attrs["aria-label"] === "显示名");
  checks.hasDisplayField = Boolean(display);
  if (display) display.value = "长文摘要";
  await clickButton("保存修改");
  await settle();

  const write = server.writes.at(-1);
  checks.wroteUpdate = write?.method === "PUT" && write?.name === "TASK_1";
  checks.sentDisplayName = write?.body?.display_name === "长文摘要";
  checks.sentRevision = typeof write?.body?.config_revision === "string";
  // 关键：不发 params。服务端 UpdateParams=false 会原样保留既有参数；如果前端把
  // params 一起发上来（哪怕是从上面的读数里抄回来的），面板就成了参数的第二个
  // 编辑入口，两处会打架。
  checks.omitsParams = write !== undefined && !("params" in write.body);
  // 新建：任务名放 body，走 POST /api/tasks。
  await clickButton("新建任务");
  const nameInput = inputs().find((n) => n.attrs["aria-label"] === "任务名");
  checks.newTaskNameEditable = Boolean(nameInput) && nameInput.disabled !== true;
  // 模型下拉来自面板读数里的 models（模型 ID + 可见别名），且带一个「未指定」空选项
  // ——任务可以先占位，那个空选项必须存在。要在编辑器还开着的时候断言。
  const modelSelect = selects().find((n) => n.attrs["aria-label"] === "模型");
  checks.modelOptionsPresent = Boolean(modelSelect)
    && modelSelect.children.length === 3
    && modelSelect.children.some((option) => option.value === "");
  if (nameInput) nameInput.value = "TASK_2";
  await clickButton("创建任务");
  await settle();
  const created = server.writes.at(-1);
  checks.wroteCreate = created?.method === "POST" && created?.body?.name === "TASK_2";
  checks.touchedNoStorage = storageTouches.length === 0;
} else if (scenario === "api_base_from_fragment") {
  // 宿主指定的基址必须真的作用在每个请求上。
  checks.usesCustomBase = server.requests.length > 0
    && server.requests.every((r) => r.url.startsWith("http://amkr.internal:28881/"));
  checks.touchedNoStorage = storageTouches.length === 0;
}

const failed = Object.entries(checks).filter(([, ok]) => !ok).map(([name]) => name);
console.log(JSON.stringify({ scenario, checks, failed }));
process.exit(failed.length ? 1 : 0);
