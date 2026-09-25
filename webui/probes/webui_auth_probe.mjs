// WebUI 鉴权流程回归探针：用最小 DOM 垫片驱动真实的 webui ES 模块。
// 用法：node webui_auth_probe.mjs <scenario>，结果以 JSON 打到 stdout。
// 每个场景单独起进程，保证模块级缓存（各页面的 state）互不干扰。

import { existsSync } from "node:fs";
import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");
const scenario = process.argv[2];

// loginTargetExists 断言跳转目标在磁盘上真的存在。
//
// 拼 URL 的代码与资产文件是两处，改了一处忘了另一处时，跳过去只会得到一个 404——
// 而那时用户已经被带离了主界面，看到的是一张"页面不存在"，不是登录框。
const loginTargetExists = (url) => {
  const file = String(url || "").split("?")[0].replace(/^[^/]*/, "");
  return existsSync(path.join(WEBUI, path.basename(file)));
};

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
    this.parent = null;
  }
  append(...nodes) {
    for (const node of nodes.flat()) {
      if (node === null || node === undefined) continue;
      node.parent = this;
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
  // 忠实还原 isConnected：沿 parent 走到文档根才算已挂载。页面（概览/活动）用它
  // 跳过"离开后仍在途的重绘"，垫片若恒为 true/false 都会把这个判断测成另一回事。
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

// Node 24 已内置只读的 navigator/location 全局，只能用 defineProperty 覆盖。
const define = (name, value) =>
  Object.defineProperty(global, name, { value, writable: true, configurable: true });

let reloads = 0;
// 登录已拆成独立页：未授权时主界面不再就地画表单，而是 location.replace 到
// /ui/login.html。replaces 记下跳转目标，好断言"跳了、跳到哪、带没带 next/reason"。
const replaces = [];
define("location", {
  hash: "#/settings",
  // 独立运行时 WebUI 在 /ui/ 下；嵌入场景会覆盖成 /<prefix>/ui/。
  pathname: "/ui/",
  search: "",
  reload() { reloads += 1; },
  replace(url) { replaces.push(String(url)); },
});
define("window", {
  addEventListener() {},
  isSecureContext: true,
  location: global.location,
  // 新建工作空间走 prompt 收名字。垫片必须给一个（默认返回 null = 取消），否则
  // 选中「＋ 新建工作空间…」那一项会直接抛 TypeError。
  prompt: () => null,
});
define("navigator", { clipboard: null });

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

// app.js 的 scheduleTimers 会拉起真实定时器，会把探针进程挂住；这里一律 stub。
// 回调**记下来**而不是丢掉：登录页的"轮询重绘不重建输入框"必须驱动真实的重绘路径
// （早先的写法是手动调 renderShell，那是模拟健康轮询，不是它本身）。
const intervalFns = [];
define("setInterval", (fn) => { intervalFns.push(fn); return 0; });
define("clearInterval", () => {});
define("setTimeout", () => 0);

const storage = new Map();
define("localStorage", {
  getItem: (k) => (storage.has(k) ? storage.get(k) : null),
  setItem: (k, v) => storage.set(k, String(v)),
  removeItem: (k) => storage.delete(k),
});

// —— 假服务端 ——
const server = {
  authEnabled: true,
  accepted: new Set(["good-key"]),
  requests: [],
  // 嵌入宿主时的挂载前缀；独立运行是空串。
  prefix: "",
  // 任务路由探针用的假数据与写入记录。
  // tasks 是 tasksWorkspace 这个空间里的任务；其余空间一律为空。
  tasks: [],
  tasksWorkspace: "default",
  writes: [],
  // 工作空间目录（GET /api/workspaces）。默认只有默认空间，与真实后端一致：
  // 默认空间永远在清单里，哪怕它一个任务都没有。
  workspaces: [{ name: "default", task_count: 0 }],
  // 工作空间的改名/删除请求（PUT/DELETE /api/workspaces/{name}）。
  workspaceWrites: [],
  // 任务路由按空间过滤：记录每次请求带的空间头，好断言切换真的传到了后端。
  taskWorkspaces: [],
  // 成本页探针用：价格目录（null = 服务端尚未就绪，应回 503）与该窗口的上游用量。
  pricing: null,
  pricingStatus: 200,
  upstreamModels: {},
  providers: {},
  // 逐条明细（成本页的"最近请求成本"与"供应商成本"用）。
  requestItems: [],
  // 请求明细里 window.from 的值：用量统计页的「全部历史」靠它推导跨度。
  // null 表示库是空的（此时页面应退回最短窗口而不是报错）。
  historyFrom: null,
  // 服务日志页的文本。
  logs: "",
  logsError: null,
  // 工作空间用量快照（/ui/workspace-usage.json）。null = 服务端尚未给出，
  // 此时页面应走加载态骨架而不是画空看板。
  workspaceUsage: null,
  // Key 用量读数（/ui/key-usage.json）：用量统计页的「按上游 Key」「按访问密钥」
  // 与「模型 / 上游 Key」三张表都出自这一份。
  keyUsage: null,
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
  server.requests.push({ url, bearer });

  // 前缀必须体现在真实请求上：这里剥掉前缀再匹配，未加前缀的请求会落到 401。
  const path = server.prefix && url.startsWith(server.prefix)
    ? url.slice(server.prefix.length)
    : url;

  if (server.offline) throw new TypeError("fetch failed");
  if (path === "/health") {
    return respond(200, {
      status: "ok",
      version: "4.0.3",
      models: [],
      local_auth_enabled: server.authEnabled,
    });
  }
  // 价格目录（models.dev）：由服务端缓存后挂在 WebUI 前缀下，且**不鉴权**——它是
  // models.dev 的公开数据。因此必须排在**鉴权分支之前**：放到后面就永远拿不到，
  // 而未鉴权时拿不到目录正是成本列该显示 "—" 的场景之一。
  if (path === "/ui/pricing.json") {
    if (!server.pricing) return respond(server.pricingStatus, { detail: "价格目录尚不可用" });
    return respond(200, server.pricing);
  }
  if (server.authEnabled && !server.accepted.has(bearer)) {
    return respond(401, { detail: "本地 API key 验证失败" });
  }
  if (path.startsWith("/metrics")) {
    // 概览页的两条读取：窗口快照与时间序列。给最小但结构完整的载荷，
    // 让首页能真正画出 KPI 瓦片与两张图（否则测的就不是"重绘"而是"空态"）。
    if (path.startsWith("/metrics/series")) {
      return respond(200, {
        bucket_seconds: 15,
        points: [{ started_at: "2026-01-01T10:00:00+08:00", ended_at: "2026-01-01T10:00:15+08:00", complete: true, requests: 3, successes: 3, failures: 0, retries: 0, prompt_tokens: 30, completion_tokens: 10, total_tokens: 40, cached_tokens: 0, total_duration_ms: 300, total_first_token_ms: 100 }],
      });
    }
    // 逐条明细：成本页的"最近请求成本"与"供应商成本"两张卡都靠它，
    // 且它们是**唯一**能同时看到 provider_id 与 upstream_model_id 的地方。
    if (path.startsWith("/metrics/requests")) {
      return respond(200, {
        count_semantics: "attempts",
        rate_window_seconds: 60,
        current_rpm: 3,
        current_tpm: 40,
        window: { from: server.historyFrom, to: "2026-01-01T10:00:00+08:00", hours: 1 },
        summary: { requests: server.requestItems.length },
        total_items: server.requestItems.length,
        items: server.requestItems,
      });
    }
    return respond(200, {
      count_semantics: "attempts",
      window: { from: null, to: "2026-01-01T10:00:00+08:00", hours: 1 },
      rate_window_seconds: 60,
      current_rpm: 3,
      current_tpm: 40,
      router_status: "green",
      active_requests: 0,
      total: { requests: 3, successes: 3, failures: 0, retries: 0, prompt_tokens: 30, completion_tokens: 10, total_tokens: 40, cached_tokens: 0 },
      caller_types: {},
      models: {},
      providers: server.providers,
      upstream_models: server.upstreamModels,
      unattributed: {},
    });
  }

  // 读取失败也是 200，错误文本放在 error 字段里（见 internal/api/handlers_ops.go）——
  // 前端因此必须看 error 字段而不是 HTTP 状态码。
  if (path.startsWith("/api/logs")) {
    return respond(200, {
      text: server.logsError ? "" : server.logs,
      truncated: false,
      path: "/tmp/amkr.log",
      error: server.logsError,
    });
  }

  if (path.startsWith("/api/settings")) {
    return respond(200, {
      config_revision: "rev-1",
      settings: { host: "127.0.0.1", port: 28881, max_retries: 2, local_auth_enabled: true },
    });
  }
  // 工作空间用量（工作空间页的 KPI、流向图与两张表都出自这一份）。
  // 挂在 /ui/ 下但仍需鉴权，所以放在鉴权分支之后。
  if (path.startsWith("/ui/workspace-usage.json")) {
    if (!server.workspaceUsage) return respond(503, { detail: "工作空间用量尚不可用" });
    return respond(200, server.workspaceUsage);
  }
  // Key 用量读数（/ui/key-usage.json）。与 /metrics 同级的完整权限读数，因此放在
  // 鉴权分支之后；未配置时给一份结构完整的空载荷，页面应画空态而不是崩掉。
  if (path.startsWith("/ui/key-usage.json")) {
    return respond(200, server.keyUsage || {
      count_semantics: "upstream_attempt",
      window: { from: null, to: "2026-01-01T10:00:00+08:00", hours: 1 },
      upstream_keys: [],
      model_keys: [],
      access_keys: [],
      unattributed: { requests: 0, successes: 0, failures: 0, total_tokens: 0 },
    });
  }
  if (path.startsWith("/api/models")) {
    return respond(200, {
      config_revision: "rev-1",
      models: [
        { id: "model-a", aliases: [], keys: [], routing_mode: "round_robin" },
        { id: "model-b", aliases: [], keys: [], routing_mode: "round_robin" },
      ],
    });
  }
  if (path.startsWith("/api/workspaces")) {
    // 目录与创建/改名/删除都在这个前缀下（工作空间是管理面的正式资源）。
    // 放在鉴权分支**之后**：内容暴露配置结构，与 tasks 同级。
    if (options.method === "POST") {
      const payload = JSON.parse(options.body);
      // 服务端生成 key，并**只在这一条响应里**回明文——目录接口刻意不含它，
      // 垫片必须照做：否则"目录不含 key"这条断言测的就是假的。
      server.workspaceWrites.push({ method: "POST", path, payload });
      server.workspaces.push({ name: payload.name, task_count: 0, has_inference_key: true });
      // 真实接口同时返回两把 key（面板 key + 推理 key），垫片必须照做：否则
      // 「两把都显示」这条断言测的就是假的。
      return respond(201, {
        config_revision: "rev-2",
        name: payload.name,
        task_count: 0,
        api_key: `amkr_ws_${payload.name}_generated`,
        inference_key: `amkr_ik_${payload.name}_generated`,
      });
    }
    if (options.method === "PUT" || options.method === "DELETE") {
      const payload = JSON.parse(options.body);
      server.workspaceWrites.push({ method: options.method, path, payload });
      // 目录要跟着动：否则改名后页面拿到的仍是旧清单，重命名过的空间会显示成
      // 「未写入配置」，它的改名/删除按钮也就一直是禁用的——那是垫片不真实，
      // 不是页面缺陷。
      const from = decodeURIComponent(path.slice("/api/workspaces/".length));
      const entry = server.workspaces.find((w) => w.name === from);
      if (entry && options.method === "PUT") entry.name = payload.name;
      if (entry && options.method === "DELETE") {
        server.workspaces = server.workspaces.filter((w) => w !== entry);
      }
      // 需要观察"保存进行中"的状态时，把响应挂住，由用例自己放行。
      if (server.holdSave) return new Promise((resolve) => { server.releaseSave = () => resolve(respond(200, { config_revision: "rev-2" })); });
      return respond(200, { config_revision: "rev-2", name: payload.name, task_count: entry?.task_count ?? 0 });
    }
    return respond(200, { workspaces: server.workspaces });
  }
  if (path.startsWith("/api/tasks")) {
    // 任务路由是按空间过滤的：把请求头里的空间记下来，好断言切换真的传到了后端。
    const workspace = headers["X-AMKR-Workspace"] || "default";
    server.taskWorkspaces.push(workspace);
    if (options.method === "POST" || options.method === "PUT") {
      server.writes.push(JSON.parse(options.body));
      // 需要观察"保存进行中"的状态时，把响应挂住，由用例自己放行。
      if (server.holdSave) return new Promise((resolve) => { server.releaseSave = () => resolve(respond(200, { config_revision: "rev-2" })); });
      return respond(options.method === "POST" ? 201 : 200, { config_revision: "rev-2" });
    }
    return respond(200, {
      config_revision: "rev-1",
      // 真实后端只回本空间的任务，垫片必须照做：否则「切空间后列表变了」这件事
      // 测不出来（切换前后都拿到同一份数据，等于没验证过滤）。
      tasks: workspace === server.tasksWorkspace ? server.tasks : [],
    });
  }
  return respond(200, {});
};

// —— 驱动 ——
const { boot, store, renderShell, navigate } = await import(pathToFileURL(path.join(WEBUI, "app.js")).href);
const { api } = await import(pathToFileURL(path.join(WEBUI, "api.js")).href);
// 登录是独立一页（pages/login.js），它自己的入口是 bootLogin。管理器与登录页是两个
// 页面，本探针两种都要驱动：管理器断言"未授权时跳走了"，登录页断言"跳来之后能收凭据"。
const { bootLogin } = await import(pathToFileURL(path.join(WEBUI, "pages", "login.js")).href);

function findAll(node, predicate, out = []) {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
}

const text = () => root.textContent;
const inputs = () => findAll(root, (n) => n.tagName === "input");
const buttons = () => findAll(root, (n) => n.tagName === "button");
// 按类名找节点。必须**同时**看 className 与 attrs.class：SVG 节点的 className 是
// 只读的 SVGAnimatedString，dom.js 因此对 SVG 一律走 setAttribute，类名落在 attrs 里。
// 早先只读 className，于是所有 SVG 节点（桑基图的 sankey-node 等）对 byClass 隐形——
// 断言它们存在时会永远为 0，断言它们不存在时则永远为真，两种都是假的。
const byClass = (name) => findAll(root, (n) => `${n.className || ""} ${n.attrs?.class || ""}`
  .split(/\s+/).includes(name));
const clickButton = async (label) => {
  const target = buttons().find((b) => b.textContent.trim() === label);
  if (!target) throw new Error(`找不到按钮: ${label}`);
  for (const handler of target.listeners.click || []) await handler({});
};
const submitKey = async (value) => {
  const field = inputs()[0];
  if (!field) throw new Error("验证页没有 Key 输入框");
  field.value = value;
  await clickButton("连接");
};

// 预期：每个场景前置的 localStorage 与假服务端状态
const setup = {
  stale_key_prompts_login: () => { storage.set("amkr.apiKey", "stale-key"); },
  no_key_prompts_login: () => {},
  valid_key_renders_page: () => { storage.set("amkr.apiKey", "good-key"); },
  mid_session_401_returns_to_login: () => { storage.set("amkr.apiKey", "good-key"); },
  auth_disabled_no_login: () => {
    server.authEnabled = false;
    storage.set("amkr.apiKey", "anything");
  },
  // 深链进入：地址栏直接指向某个内页，未鉴权时不能绕过登录页。
  deeplink_without_key_stays_on_login: () => {
    global.location.hash = "#/providers";
  },
  unreachable_service_stays_on_login: () => {
    server.offline = true;
    storage.set("amkr.apiKey", "good-key");
  },
  // 嵌入宿主：页面位于 /amkr/ui/，API 必须打到 /amkr 下而不是根路径，
  // 跳转目标也必须是 /amkr/ui/login.html。
  mounted_prefix_uses_prefixed_api: () => {
    server.prefix = "/amkr";
    global.location.pathname = "/amkr/ui/";
    storage.set("amkr.apiKey", "good-key");
  },

  // —— 登录页本身（webui/login.html → pages/login.js）——
  // 这一组由 bootLogin 驱动，而不是 boot：登录页与主界面是两个页面。
  login_page_collects_key_and_returns_to_next: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "?next=%2Fui%2F%23%2Fproviders";
  },
  login_page_wrong_key_shows_error: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "";
  },
  // 开放重定向：?next= 是攻击者可控的输入，站外地址必须被丢掉。
  // 第二个用例是**归一化绕过**：浏览器解析 URL 前会剥掉制表符/换行，所以
  // "/\t/evil.example" 在它眼里就是 "//evil.example"。只校验原始串会放过它。
  login_page_rejects_offsite_next: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "?next=%2F%2Fevil.example%2Fsteal";
  },
  login_page_rejects_obfuscated_next: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "?next=%2F%09%2Fevil.example%2Fsteal";
  },
  login_page_rejects_backslash_next: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "?next=%2F%5Cevil.example%2Fsteal";
  },
  login_page_reports_expired_session: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "?reason=expired";
  },
  login_page_unreachable_offers_retry: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "";
    server.offline = true;
  },
  // 健康轮询会周期性重绘提示，但**不得**重建输入框：重建会丢掉用户粘了一半的 Key。
  login_page_input_survives_poll: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "";
  },
  // 服务端关掉本地鉴权时登录页没有要问的东西，必须直接放行，
  // 否则会卡在一个永远填不对的表单上。
  login_page_skips_when_auth_disabled: () => {
    global.location.pathname = "/ui/login.html";
    global.location.search = "";
    server.authEnabled = false;
  },

  // 任务路由页：直接驱动真实页面模块，锁住表单与请求体形状。
  tasks_page_lists_and_saves: () => {
    global.location.hash = "#/tasks";
    storage.set("amkr.apiKey", "good-key");
    server.tasks = [
      {
        name: "TASK_000001",
        model: "model-a",
        fallback_model: null,
        params: { temperature: 0.2, reasoning_effort: "high" },
      },
    ];
  },
  // 任务路由页「新建」：空列表下点新建必须画出编辑器。taskEditor(null) 走的是
  // 与编辑不同的分支，任何一处对 task 直接取属性都会抛错，表现为点了没反应。
  tasks_new_task_opens_editor: () => {
    global.location.hash = "#/tasks";
    storage.set("amkr.apiKey", "good-key");
    server.tasks = [];
  },
  // 任务路由页的工作空间切换：目录里的空间要画进下拉，切换要真的换掉请求头。
  //
  // 注意 setup 在模块 import **之后**才跑（app.js/tasks.js 的模块级 state 在那时
  // 已经初始化），因此这里的 storage 设置影响不到首次渲染的默认空间。断言 accordingly
  // 从「默认空间」出发，切到 teamA。
  tasks_workspace_switch_scopes_requests: () => {
    global.location.hash = "#/tasks";
    storage.set("amkr.apiKey", "good-key");
    server.workspaces = [
      { name: "default", task_count: 2 },
      { name: "teamA", task_count: 1 },
    ];
    // teamA 里有任务，默认空间是空的：这样「切换真的换了数据」才有可观测的差别。
    server.tasksWorkspace = "teamA";
    server.tasks = [
      { name: "TEAM_TASK", model: "model-a", fallback_model: null, params: {} },
    ];
  },
  // 成本页（有价格目录）：KPI、排行与逐条成本都必须画出来。
  // 目录与用量都给真实形状，这样断言的是"算出来的钱对不对"，而不是"页面没崩"。
  cost_page_renders_with_pricing: () => {
    global.location.hash = "#/cost";
    storage.set("amkr.apiKey", "good-key");
    server.pricing = {
      version: 1,
      source: "https://models.dev/api.json",
      updated_at: "2026-01-02T03:04:05Z",
      error: null,
      models: { "gpt-4o": { input: 2.5, output: 10, cache_read: 1.25 } },
    };
    server.upstreamModels = {
      "gpt-4o": { requests: 2, successes: 2, failures: 0, retries: 0, prompt_tokens: 1000000, completion_tokens: 0, total_tokens: 1000000, cached_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 },
    };
    server.requestItems = [
      { id: 2, created_at: "2026-01-01T09:59:00+08:00", caller_type: "local", model_id: "route-a", upstream_model_id: "gpt-4o", provider_id: "openai", key_name: "k1", status_code: 200, success: true, retried: false, prompt_tokens: 500000, completion_tokens: 0, total_tokens: 500000, cached_tokens: 0 },
      { id: 1, created_at: "2026-01-01T09:58:00+08:00", caller_type: "local", model_id: "route-a", upstream_model_id: "gpt-4o", provider_id: "openai", key_name: "k1", status_code: 200, success: true, retried: false, prompt_tokens: 500000, completion_tokens: 0, total_tokens: 500000, cached_tokens: 0 },
    ];
  },
  // 成本页（目录可用，但该上游模型**不在**目录里）：这是"无定价"最真实的样子——
  // 目录是好的，只是没这个模型的价。此处必须显示 "—"，且绝不能退化成 $0。
  cost_page_unmatched_model_shows_dash: () => {
    global.location.hash = "#/cost";
    storage.set("amkr.apiKey", "good-key");
    server.pricing = {
      version: 1,
      source: "https://models.dev/api.json",
      updated_at: "2026-01-02T03:04:05Z",
      error: null,
      models: { "gpt-4o": { input: 2.5, output: 10, cache_read: 1.25 } },
    };
    // 名字与目录里任何一条都对不上（且没有日期后缀可剥）。
    server.upstreamModels = {
      "totally-unknown-model-xyz": { requests: 2, successes: 2, failures: 0, retries: 0, prompt_tokens: 1000000, completion_tokens: 500000, total_tokens: 1500000, cached_tokens: 0 },
    };
  },
  // 成本页（目录尚未就绪 → 服务端 503）：必须显示"无定价"，**绝不能**显示 $0。
  // 这是整条链路最要命的失败模式：把"不知道"渲染成"免费"。
  cost_page_without_pricing_shows_dash: () => {
    global.location.hash = "#/cost";
    storage.set("amkr.apiKey", "good-key");
    server.pricing = null;
    server.pricingStatus = 503;
    server.upstreamModels = {
      "gpt-4o": { requests: 2, successes: 2, failures: 0, retries: 0, prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100, cached_tokens: 0 },
    };
  },
  // 概览页二次进入：模块级 state 已有缓存时，进入必须**同步**画出内容。
  // 曾经的缺陷：renderOverview 先建好尚未挂载的 host 再调 draw()，而 draw() 用
  // isConnected 守卫挡住了这次同步首绘；同时 loadWindow/loadHeatmap 命中缓存后
  // 直接返回、不会再回调 draw —— 于是切回概览整页空白，一直等到下一次轮询
  // （健康轮询 5 秒）才补画，用户看到的就是"进入会卡顿一段时间"。
  overview_reentry_paints_immediately: () => {
    global.location.hash = "#/overview";
    storage.set("amkr.apiKey", "good-key");
  },
  // 概览页必须真的带出请求流：它从「实时活动」移了过来，若只改了导航没搬卡片，
  // 这一页会安静地少掉"刚刚发生了什么"这块内容。同时确认成本列**不会**谎报 $0。
  overview_shows_request_stream: () => {
    global.location.hash = "#/overview";
    storage.set("amkr.apiKey", "good-key");
    server.pricing = {
      version: 1, source: "https://models.dev/api.json",
      updated_at: "2026-01-02T03:04:05Z", error: null,
      models: { "gpt-4o": { input: 2.5, output: 10, cache_read: 1.25 } },
    };
    server.requestItems = [
      { id: 3, created_at: "2026-01-01T09:59:00+08:00", caller_type: "local", model_id: "route-a", upstream_model_id: "gpt-4o", provider_id: "openai", key_name: "k1", status_code: 200, success: true, retried: false, prompt_tokens: 100, completion_tokens: 20, total_tokens: 120, cached_tokens: 0, duration_ms: 800 },
      { id: 2, created_at: "2026-01-01T09:58:30+08:00", caller_type: "access_key", model_id: "route-b", upstream_model_id: "no-price-model", provider_id: "openai", key_name: "k2", status_code: 502, success: false, retried: true, prompt_tokens: 50, completion_tokens: 0, total_tokens: 50, cached_tokens: 0, duration_ms: 1200 },
    ];
  },
  // 用量统计页（原「实时活动」）：长窗口必须能画出来，且窗口切换项包含历史档。
  usage_page_renders_history_ranges: () => {
    global.location.hash = "#/activity";
    storage.set("amkr.apiKey", "good-key");
    // 按 Key 拆分（/ui/key-usage.json）：两家供应商**同名** Key（都是 main）+ 一把访问
    // 密钥 + 一行没有供应商归因的历史行。这个组合专门覆盖这次要修的三种情形：
    // 同名 Key 不能并成一行、访问密钥要单独一张表、未归属要在表里说明出处。
    server.keyUsage = {
      count_semantics: "upstream_attempt",
      window: { from: null, to: "2026-01-01T10:00:00+08:00", hours: 1 },
      upstream_keys: [
        { provider_id: "openai", key_name: "main", stats: { requests: 7, successes: 7, failures: 0, total_tokens: 700 } },
        { provider_id: "azure", key_name: "main", stats: { requests: 3, successes: 3, failures: 0, total_tokens: 300 } },
      ],
      model_keys: [
        { model_id: "route-a", provider_id: "openai", key_name: "main", stats: { requests: 7, successes: 7, failures: 0, total_tokens: 700 } },
        { model_id: "route-b", provider_id: "azure", key_name: "main", stats: { requests: 3, successes: 3, failures: 0, total_tokens: 300 } },
      ],
      access_keys: [
        { access_key_id: "ak1", access_key_name: "试用账号 A", stats: { requests: 5, successes: 5, failures: 0, total_tokens: 500 } },
      ],
      unattributed: { requests: 2, successes: 1, failures: 1, total_tokens: 20 },
    };
  },
  // 「全部历史」的跨度由 /metrics/requests 的 window.from 推导：服务端给出的一年多
  // 以前的记录，应当被夹到后端上限（8760 小时）并如实说明"只覆盖到上限"。
  usage_all_history_clamps_span: () => {
    global.location.hash = "#/activity";
    storage.set("amkr.apiKey", "good-key");
    server.historyFrom = "2020-01-01T00:00:00+08:00";
  },
  // 工作空间页：KPI 是 10 张瓦片，必须排满每一档（5 列两整行 / 2 列五行）。
  // 5 张时 2 列档会甩出一张孤儿瓦片——这是概览页修过一轮的同一个毛病。
  workspaces_kpi_grid_has_no_orphan: () => {
    global.location.hash = "#/workspaces";
    storage.set("amkr.apiKey", "good-key");
    server.workspaceUsage = {
      count_semantics: "attempts",
      window: { from: "2026-01-01T09:00:00+08:00", to: "2026-01-01T10:00:00+08:00", hours: 1 },
      workspaces: [
        // 两个空间：只给一个空间有量，另一个为空，顺带覆盖"空空间仍要列出来"。
        { name: "default", stats: { requests: 120, successes: 118, failures: 2, retries: 1, prompt_tokens: 8000, completion_tokens: 2000, total_tokens: 10000, cached_tokens: 4000, total_duration_ms: 9600, total_first_token_ms: 2400 } },
        { name: "team-a", stats: { requests: 0, successes: 0, failures: 0, retries: 0, prompt_tokens: 0, completion_tokens: 0, total_tokens: 0, cached_tokens: 0, total_duration_ms: 0, total_first_token_ms: 0 } },
      ],
      // 未归属非零：升级前的历史行，页面要把它当"说明"而不是"次要细节"。
      unattributed: { requests: 30, total_tokens: 3000, successes: 29, failures: 1 },
      // 六层：供应商与上游模型之间还有「上游 Key」这一层。
      layers: ["workspace", "requested_model_id", "model_id", "provider_id", "key_name", "upstream_model_id"],
      links: [
        { source_layer: 0, target_layer: 1, source: "default", target: "unified-model", requests: 120, total_tokens: 10000 },
        { source_layer: 1, target_layer: 2, source: "unified-model", target: "deepseek-v4.1-flash", requests: 120, total_tokens: 10000 },
        { source_layer: 2, target_layer: 3, source: "deepseek-v4.1-flash", target: "wb2api", requests: 120, total_tokens: 10000 },
        { source_layer: 3, target_layer: 4, source: "wb2api", target: "primary", requests: 120, total_tokens: 10000 },
        { source_layer: 4, target_layer: 5, source: "primary", target: "deepseek-v4.1-flash", requests: 120, total_tokens: 10000 },
      ],
    };
  },
  // 服务日志页：级别过滤、关键字搜索与自动跟随都必须真的作用在文本上。
  logs_page_filters_by_level: () => {
    global.location.hash = "#/logs";
    storage.set("amkr.apiKey", "good-key");
    server.logs = [
      "2026-01-01 10:00:00 INFO  service started",
      "2026-01-01 10:00:01 DEBUG cache warm",
      "2026-01-01 10:00:02 WARN  upstream slow",
      "2026-01-01 10:00:03 ERROR upstream 502",
    ].join("\n");
  },
  // 日志读取失败时，服务端仍是 200、错误在 error 字段里：页面必须显示该错误，
  // 而不是把空文本渲染成"日志为空。"（那就把故障说成了正常）。
  logs_page_surfaces_read_error: () => {
    global.location.hash = "#/logs";
    storage.set("amkr.apiKey", "good-key");
    server.logsError = "permission denied";
  },
};
// 没给场景名时，把自己按场景逐个重跑一遍。两个理由：
//   1) 各页面模块的 state 是模块级缓存，同进程连跑多个场景会互相污染，必须一场景一进程；
//   2) 不传场景名时 setup 不会命中，一个断言都不跑、failed 为空，进程照样退出 0。
//      CI 与 README 都是裸调这条探针的，于是它成了永远绿灯的空转——"点新建没反应"
//      这类只在某个分支上出现的 bug，正好从这种空子里漏过去。默认跑全部才不漏。
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
// 场景名打错同样会静默空转，必须响亮地失败而不是"什么都没检查却通过"。
if (!scenarioNames.includes(scenario)) {
  console.error(`未知场景：${scenario}\n可用场景：\n  ${scenarioNames.join("\n  ")}`);
  process.exit(2);
}
setup[scenario]();

// 登录页的场景驱动 bootLogin（页面是 login.html），其余驱动主界面 boot（index.html）。
// 两者的区别是真实的：登录页不再由 app.js 渲染，主界面未授权时也不再画表单。
const loginScenarios = new Set([...scenarioNames].filter((name) => name.startsWith("login_page_")));
if (loginScenarios.has(scenario)) await bootLogin();
else await boot();
// 页面首屏的读取是异步的，且可能会渲染不止一次；这里把在途的微任务排空，让断言
// 看到的是稳定后的页面（否则断言的就是"恰好还没画完"的中间态）。
const settle = async () => { for (let i = 0; i < 20; i += 1) await Promise.resolve(); };
await settle();

const checks = {};
// 登录页（webui/login.html）必须自己就是登录界面，不再依赖 app.js 画表单。
const onLoginPage = () => text().includes("连接到 AMKR");

if (scenario === "stale_key_prompts_login") {
  // 报告的问题：带着失效 Key 进入时，不应把 401 当成设置读取失败缓存下来。
  // 现在也不再就地画表单，而是整页跳去登录页。
  checks.redirectedToLogin = replaces.length === 1 && String(replaces[0]).includes("/ui/login.html");
  checks.loginPageExists = loginTargetExists(replaces[0]);
  checks.notCachedSettingsError = !text().includes("读取设置失败");
  checks.unauthorized = store.authorized === false;
  checks.keyCleared = !storage.has("amkr.apiKey");
  // 失效不是"从没登录过"，但也不是会话中途失效（那由 reason=expired 表达）：
  // 首次进入就发现 Key 不能用时不该谎称"已失效"，只给获取提示即可。
  checks.noReasonForFirstVisit = !String(replaces[0] || "").includes("reason=");
} else if (scenario === "no_key_prompts_login") {
  // 未授权时主界面**一个节点都不画**，只跳转：跳转是唯一的未授权表现，
  // 因此"带着无效 Key 进入主界面"没有入口。
  checks.redirectedToLogin = replaces.length === 1 && String(replaces[0]).includes("/ui/login.html");
  checks.loginPageExists = loginTargetExists(replaces[0]);
  checks.unauthorized = store.authorized === false;
  // 「什么都没画」= 既没有主界面外壳，也没有就地登录表单。
  checks.renderedNoShell = byClass("shell").length === 0;
  checks.noLoginCardHere = !onLoginPage();
  checks.noAppBar = byClass("app-bar").length === 0;
  checks.noNav = byClass("nav").length === 0;
  // 只跳一次：健康轮询会反复 renderShell，重复 replace 会在历史里堆记录。
  renderShell();
  renderShell();
  checks.redirectedOnce = replaces.length === 1;
} else if (scenario === "deeplink_without_key_stays_on_login") {
  // 地址栏直达内页也必须先过登录页，而且原地址要作为 next 带过去（深链不丢）。
  checks.pageWasProviders = store.page === "providers";
  checks.stillUnauthorized = store.authorized === false;
  checks.redirectedToLogin = replaces.length === 1 && String(replaces[0]).includes("/ui/login.html");
  checks.loginPageExists = loginTargetExists(replaces[0]);
  checks.nextKeepsDeeplink = decodeURIComponent(String(replaces[0] || "")).includes("#/providers");
  checks.noProvidersPage = !text().includes("供应商");
  checks.renderedNoShell = byClass("shell").length === 0;
} else if (scenario === "unreachable_service_stays_on_login") {
  // 连不上时无从判断是否需要鉴权：同样停在登录页，且**保留**本机 Key
  // （可能只是服务还没起来，重试即可，不该让人重贴）。
  checks.redirectedToLogin = replaces.length === 1 && String(replaces[0]).includes("/ui/login.html");
  checks.stillUnauthorized = store.authorized === false;
  checks.keyKept = storage.get("amkr.apiKey") === "good-key";
} else if (scenario === "mounted_prefix_uses_prefixed_api") {
  // 嵌入宿主时页面在 /amkr/ui/ 下，若 API 基址写死绝对路径，每个请求都会打到宿主
  // 根路径（404/401）；跳转目标同理必须是 /amkr/ui/login.html。
  checks.authorized = store.authorized === true;
  checks.pageRendered = text().includes("设置");
  checks.requestsArePrefixed = server.requests.length > 0
    && server.requests.every((r) => String(r.url).startsWith("/amkr"));
  checks.healthPrefixed = server.requests.some((r) => r.url === "/amkr/health");
  checks.settingsPrefixed = server.requests.some((r) => r.url === "/amkr/api/settings");
} else if (scenario === "valid_key_renders_page") {
  checks.didNotRedirect = replaces.length === 0;
  checks.authorized = store.authorized === true;
  checks.pageRendered = text().includes("设置");
  checks.noAuthError = !text().includes("401");
  checks.hasAppBar = byClass("app-bar").length === 1;
  checks.hasNav = byClass("nav").length === 1;
} else if (scenario === "mid_session_401_returns_to_login") {
  checks.startedAuthorized = store.authorized === true;
  // 模拟"重置本地鉴权 Key"：服务端换了 Key，浏览器里的旧 Key 立刻失效。
  server.accepted = new Set(["new-key"]);
  await api.settings().catch(() => {});
  checks.backToLogin = store.authorized === false;
  checks.redirectedToLogin = replaces.length === 1 && String(replaces[0]).includes("/ui/login.html");
  // 会话中途失效要说明"已失效"，登录页据 reason=expired 显示这句。
  checks.reasonIsExpired = String(replaces[0] || "").includes("reason=expired");
} else if (scenario === "auth_disabled_no_login") {
  checks.didNotRedirect = replaces.length === 0;
  checks.authorized = store.authorized === true;
  checks.pageRendered = text().includes("设置");
  checks.hasAppBar = byClass("app-bar").length === 1;
} else if (scenario === "login_page_collects_key_and_returns_to_next") {
  // 登录页收下凭据，然后回到 next 指定的内页。
  checks.showsLoginForm = onLoginPage();
  checks.hasKeyInput = inputs().length >= 1;
  await submitKey("good-key");
  checks.keyStored = storage.get("amkr.apiKey") === "good-key";
  checks.returnedToNext = replaces.length === 1 && String(replaces[0]) === "/ui/#/providers";
  checks.notReloading = reloads === 0;
} else if (scenario === "login_page_wrong_key_shows_error") {
  await submitKey("bad-key");
  checks.errorShown = text().includes("无效");
  checks.stillLoginForm = onLoginPage();
  // 未经服务端验证的 Key 不能留在本机：留下的话下次打开会白打一轮 401。
  checks.keyNotStored = !storage.has("amkr.apiKey");
  checks.didNotEnter = replaces.length === 0;
} else if (scenario === "login_page_rejects_offsite_next") {
  // 开放重定向：next 指向站外时必须被丢掉，回到本站首页。
  await submitKey("good-key");
  checks.enteredApp = replaces.length === 1;
  checks.stayedOnOrigin = String(replaces[0] || "").startsWith("/ui/");
  checks.notOffsite = !String(replaces[0] || "").includes("evil.example");
  checks.notProtocolRelative = !String(replaces[0] || "").startsWith("//");
} else if (scenario === "login_page_rejects_obfuscated_next"
  || scenario === "login_page_rejects_backslash_next") {
  // 同一类开放重定向的变体：制表符/换行（浏览器归一化后才成 `//`）与反斜杠。
  await submitKey("good-key");
  checks.enteredApp = replaces.length === 1;
  checks.stayedOnOrigin = String(replaces[0] || "").startsWith("/ui/");
  checks.notOffsite = !String(replaces[0] || "").includes("evil.example");
} else if (scenario === "login_page_reports_expired_session") {
  // 主界面因会话失效跳过来时带 reason=expired：登录页要说明"已失效"，
  // 而不是显示"没登录过"的获取 Key 提示。
  checks.showsLoginForm = onLoginPage();
  checks.expiredExplained = text().includes("已失效");
} else if (scenario === "login_page_unreachable_offers_retry") {
  checks.showsLoginForm = onLoginPage();
  checks.reasonShown = text().includes("无法连接");
  checks.retryOffered = buttons().some((b) => b.textContent.includes("重试连接"));
  // 还没填 Key 时点重试只该重探服务，不该拿空凭据去验一遍再报"Key 无效"。
  await clickButton("重试连接");
  checks.retryDidNotClaimKeyInvalid = !text().includes("无效");
} else if (scenario === "login_page_input_survives_poll") {
  const field = inputs()[0];
  checks.loginFieldPresent = Boolean(field);
  if (field) {
    field.value = "half-typed";
    // 驱动**真实**的轮询回调（bootLogin 注册的 setInterval），而不是手动调重绘。
    for (const fn of intervalFns) await fn();
    await settle();
    checks.sameNode = inputs()[0] === field;
    checks.valueKept = inputs()[0]?.value === "half-typed";
  }
} else if (scenario === "login_page_skips_when_auth_disabled") {
  // 服务端关掉本地鉴权时登录页没有要问的东西，必须直接放行。
  checks.notShowingForm = !onLoginPage();
  checks.enteredApp = replaces.length === 1 && String(replaces[0]).startsWith("/ui/");
} else if (scenario === "tasks_page_lists_and_saves") {
  checks.authorized = store.authorized === true;
  checks.onTasksPage = store.page === "tasks";
  // 已有任务要列出来，并显示它的固定参数。
  checks.listsTask = text().includes("TASK_000001");
  checks.showsParams = text().includes("temperature") && text().includes("reasoning_effort");

  // 打开编辑器，改一个固定参数并保存。
  await clickButton("编辑");
  checks.explainsRejection = text().includes("会被直接拒绝");
  checks.hasAllParamFields = inputs().filter((node) => node.attrs.placeholder === "留空表示不固定").length === 7;
  const taskInput = inputs().find((node) => node.value === "TASK_000001");
  // 编辑时任务名不可改（它是调用方用的 model 名，改名等于换了个任务）。
  checks.taskNameReadOnly = Boolean(taskInput) && taskInput.disabled === true;
  const tempInput = inputs().find((node) => node.attrs.placeholder === "留空表示不固定");
  checks.tempPrefilled = tempInput?.value === "0.2";
  if (tempInput) tempInput.value = "0.7";

  // 切首选模型不能重建表单：重建会把手填的参数一起清掉。这里先填一个只在内存里的
  // 值，再切模型，确认那个输入框还是同一个节点、值还在。
  // 页面顶部的工作空间切换也是一个 <select>，必须排掉它，否则 selections[0] 会
  // 指到空间而不是首选模型。
  const selects = findAll(root, (n) => n.tagName === "select"
    && !String(n.className || "").split(/\s+/).includes("workspace-select"));
  const primarySelect = selects[0];
  // 3 = 两个模型 + 「（尚未指定）」：首选可以为空（任务先占位），那个空选项必须存在，
  // 否则用户没法把已有任务改回未指定状态。
  // option 的 value 是**属性**（dom.js 对 value 走 el.value 赋值），因此读 .value。
  checks.primarySelectListsAllModels = primarySelect?.children.length === 3
    && primarySelect.children.some((option) => option.value === "");
  if (primarySelect) {
    primarySelect.value = "model-b";
    for (const handler of primarySelect.listeners.change || []) await handler({ target: primarySelect });
  }
  checks.survivesModelChange = inputs().find((node) => node.attrs.placeholder === "留空表示不固定") === tempInput;
  checks.valueKeptOnModelChange = tempInput?.value === "0.7";
  // 备选下拉必须排掉当前首选，否则会存出「首选 == 备选」的任务。
  checks.fallbackExcludesPrimary = selects[1]?.children.every((option) => option.attrs.value !== "model-b");

  // 保存期间表单要锁住：请求已经在路上，此时让用户继续改只会造成"改了却没生效"。
  // 这里把响应挂住，趁机检查锁定状态，再放行。
  server.holdSave = true;
  const saving = clickButton("保存任务");
  await settle();
  checks.nameStillReadOnly = inputs().find((node) => node.value === "TASK_000001")?.disabled === true;
  // 输入框、两个模型下拉、以及编辑器自己的两个按钮都要锁住（导航按钮不参与）。
  checks.formLockedWhileSaving = inputs().every((node) => node.disabled === true)
    && selects.every((node) => node.disabled === true)
    && ["保存中…", "取消"].every((label) =>
      buttons().find((node) => node.textContent.trim() === label)?.disabled === true);
  checks.saveButtonShowsProgress = buttons().some((node) => node.textContent.includes("保存中"));
  server.holdSave = false;
  server.releaseSave();
  await saving;
  await settle();

  const write = server.writes.at(-1);
  checks.wroteTask = Boolean(write);
  // 保存走的是 PUT /api/tasks/<name>（新建才用 POST + 任务名放 body）。
  checks.wroteUpdateUrl = server.requests.some((r) => r.url === "/api/tasks/TASK_000001");
  checks.writeHasModel = write?.model === "model-b";
  checks.writeHasNewTemperature = write?.params?.temperature === 0.7;
  checks.writeKeptEffort = write?.params?.reasoning_effort === "high";
  // 留空的参数不该被写成 null 塞进配置。
  checks.writeOmitsBlankParams = write !== undefined && !("top_p" in (write.params || {}));
  // 保存成功后编辑器关闭，表单不再锁着。
  checks.editorClosedAfterSave = !buttons().some((node) => node.textContent.trim() === "保存任务");
} else if (scenario === "tasks_new_task_opens_editor") {
  checks.authorized = store.authorized === true;
  checks.onTasksPage = store.page === "tasks";
  // 空列表时要给出空态，并且空态自带一个新建入口。
  checks.showsEmptyState = text().includes("尚未配置任务路由");

  // 点「新建任务」必须真的画出编辑器：这里若抛错，draw() 中断，界面停在原样，
  // 用户看到的就是"点了没反应"。
  await clickButton("新建任务");
  checks.editorOpened = text().includes("固定采样参数");
  checks.hasSaveButton = buttons().some((node) => node.textContent.trim() === "保存任务");

  // 新建时任务名可填（编辑既有任务时才只读）。
  const nameInput = inputs().find((node) => node.attrs.placeholder === "TASK_000001");
  checks.hasNameField = Boolean(nameInput) && nameInput.disabled !== true;
  // 7 个数值参数 + stop，全部为空且可编辑。
  checks.hasAllParamFields = inputs().filter((node) => node.attrs.placeholder === "留空表示不固定").length === 7;
  checks.hasStopField = inputs().some((node) => node.attrs.placeholder === "逗号分隔，留空表示不固定");

  // 填名字后保存：新建走 POST，任务名放在 body 里。
  if (nameInput) nameInput.value = "TASK_000002";
  await clickButton("保存任务");
  await settle();

  const write = server.writes.at(-1);
  checks.wroteCreate = Boolean(write);
  checks.wroteCreateUrl = server.requests.some((r) => r.url === "/api/tasks");
  checks.writeHasName = write?.name === "TASK_000002";
  checks.writeHasModel = write?.model === "model-a";
  // 没填的参数不该被写进配置。
  checks.writeOmitsBlankParams = write !== undefined && !("temperature" in (write.params || {}));
} else if (scenario === "tasks_workspace_switch_scopes_requests") {
  checks.authorized = store.authorized === true;
  checks.onTasksPage = store.page === "tasks";
  // 目录里的两个空间都要出现在下拉里，且带上各自的任务数。
  const workspaceSelect = findAll(root, (n) => n.tagName === "select"
    && String(n.className || "").split(/\s+/).includes("workspace-select"))[0];
  checks.hasWorkspaceSwitcher = Boolean(workspaceSelect);
  // option 的 value 是**属性**（dom.js 对 value 走 el.value 赋值），因此读 .value。
  checks.workspaceOptionsIncludeDefault = Boolean(workspaceSelect?.children.some((o) => o.value === "default"));
  checks.workspaceOptionsIncludeTeamA = Boolean(workspaceSelect?.children.some((o) => o.value === "teamA"));
  // 下拉里带任务数：空空间与有任务的空间长得一样会让人选错。
  checks.workspaceOptionShowsCount = Boolean(workspaceSelect?.children
    .some((o) => String(o.textContent).includes("2")));
  // 首次进入不带空间头（默认空间），后端按缺省处理；该空间里没有任务。
  checks.firstRequestUsesDefaultWorkspace = server.taskWorkspaces.at(-1) === "default";
  checks.defaultWorkspaceIsEmpty = text().includes("尚未配置任务路由");

  // 切到 teamA：后续请求必须带上新的空间头，并把选择记进 localStorage。
  if (workspaceSelect) {
    workspaceSelect.value = "teamA";
    for (const handler of workspaceSelect.listeners.change || []) await handler({ target: workspaceSelect });
  }
  await settle();
  checks.switchIssuedRequest = server.taskWorkspaces.at(-1) === "teamA";
  checks.switchRemembersChoice = storage.get("amkr.workspace") === "teamA";
  // 切换后要重画：该空间的任务（TEAM_TASK）必须出现在页面上，而默认空间的空态文案
  // 必须消失——否则画的是上一个空间的数据。
  checks.switchRepaints = text().includes("TEAM_TASK");
  checks.switchDropsPreviousWorkspace = !text().includes("尚未配置任务路由");
  // 切空间要把编辑态清掉：编辑器里的任务属于**原来**的空间，留着再点保存会打到
  // 新空间去（甚至因重名覆盖新空间里的同名任务）。
  checks.switchClearsEditor = !buttons().some((node) => node.textContent.trim() === "保存任务");

  // 下拉里要有「新建工作空间…」入口：显式创建是拿到面板 key 的唯一途径。
  checks.hasNewWorkspaceOption = Boolean(workspaceSelect?.children
    .some((o) => String(o.textContent).includes("新建工作空间")));

  // 选中它 → 弹创建对话框。填名字并确认：必须发 POST /api/workspaces（而不是
  // 仅仅在本地切一个名字），并把服务端回的面板 key 显示出来。
  //
  // 对话框由 ui.js 挂到 document.body（不是 root），因此断言要从那里找节点。
  if (workspaceSelect) {
    workspaceSelect.value = "";
    for (const handler of workspaceSelect.listeners.change || []) await handler({ target: workspaceSelect });
  }
  await settle();
  const bodyInputs = () => findAll(global.document.body, (n) => n.tagName === "input");
  const bodyButtons = () => findAll(global.document.body, (n) => n.tagName === "button");
  const clickBodyButton = async (label) => {
    const target = bodyButtons().filter((b) => b.textContent.trim() === label).at(-1);
    if (!target) throw new Error(`对话框里找不到按钮: ${label}`);
    for (const handler of target.listeners.click || []) await handler({});
  };
  checks.openedCreateDialog = text().includes("新建工作空间") || bodyInputs().length > 0;
  const createName = bodyInputs().find((n) => n.attrs["aria-label"] === "工作空间名");
  checks.createDialogHasNameField = Boolean(createName);
  // 没填名字就确认：必须报错且**不发请求**（不能建出一个空名字的空间）。
  await clickBodyButton("创建");
  await settle();
  checks.rejectsBlankName = !server.workspaceWrites.some((w) => w.method === "POST");

  if (createName) createName.value = "teamB";
  await clickBodyButton("创建");
  await settle();
  const created = server.workspaceWrites.find((w) => w.method === "POST");
  checks.createUsesWorkspaceEndpoint = created?.path === "/api/workspaces";
  checks.createSendsName = created?.payload?.name === "teamB";
  checks.createSendsRevision = typeof created?.payload?.config_revision === "string";
  // 空 key 表示"由服务端生成"——界面不该自己编一个 key 出来。
  // 面板 key 由应用侧提供是允许的（configops.CreateWorkspace 接受非空 api_key），
  // 但 WebUI 没有这个入口，因此这里必须是 undefined。
  checks.createOmitsGeneratedKey = created !== undefined && !("api_key" in created.payload);
  // 关键：明文 key 只在这一条响应里回。界面必须当场显示它——关掉就再也拿不到了。
  const bodyText = () => global.document.body.textContent;
  checks.showsReturnedKey = bodyText().includes("amkr_ws_teamB_generated");
  // 推理 key 是**另一把**凭据（调 /v1 用），同样只显示这一次，必须一起给出。
  // 建空间是这个实例唯一能拿到明文 key 的时刻，漏了它应用侧就还得再找一条路要。
  checks.showsReturnedInferenceKey = bodyText().includes("amkr_ik_teamB_generated");
  checks.warnsKeyShownOnce = bodyText().includes("只显示这一次");
  // 两把 key 的复制入口要分别存在：混成一个「复制 key」最容易把面板 key 配进项目。
  checks.offersCopyPanelKey = bodyButtons().some((b) => b.textContent.trim() === "复制面板 key");
  checks.offersCopyInferenceKey = bodyButtons().some((b) => b.textContent.trim() === "复制推理 key");
  // 嵌入片段：带 key 的 fragment + iframe 标签，复制即可用。
  checks.showsEmbedSnippet = bodyText().includes("panel.html#k=")
    && bodyText().includes("<iframe");
  // 新空间要真的落进下拉（目录里有了），并且页面切到它。
  checks.newWorkspaceShownInSwitcher = Boolean(findAll(root, (n) => n.tagName === "select"
    && String(n.className || "").split(/\s+/).includes("workspace-select"))[0]?.children
    .some((o) => o.value === "teamB"));
  checks.newWorkspaceRequestsIt = server.taskWorkspaces.at(-1) === "teamB";
  // 创建后空间里还没有任务：空态要说清"调用方要指定任务名或模型别名"，
  // 而不是留下一个看起来像加载失败的空壳。
  checks.newWorkspaceEmptyState = text().includes("teamB");

  // 改名与删除是**空间自身**的操作，走 PUT/DELETE /api/workspaces/{name}，而不是
  // /api/tasks。改到 teamA 上（它是目录里的真实空间）。
  if (workspaceSelect) {
    workspaceSelect.value = "teamA";
    for (const handler of workspaceSelect.listeners.change || []) await handler({ target: workspaceSelect });
  }
  await settle();
  // 默认空间不可改名/删除：那两个按钮必须禁用（默认空间是缺省调用方命中的空间）。
  if (workspaceSelect) {
    workspaceSelect.value = "default";
    for (const handler of workspaceSelect.listeners.change || []) await handler({ target: workspaceSelect });
  }
  await settle();
  const defaultRename = byClass("workspace-rename")[0];
  const defaultDelete = byClass("workspace-delete")[0];
  const defaultModels = byClass("workspace-models")[0];
  checks.hasWorkspaceActions = Boolean(defaultRename && defaultDelete && defaultModels);
  // 模型授权在默认空间上也不可用：它没有作用域凭据，清单配了也没有对象生效。
  checks.defaultWorkspaceActionsDisabled = Boolean(defaultRename?.disabled && defaultDelete?.disabled
    && defaultModels?.disabled);

  // 切到 teamA：三个动作可用，改名会 PUT 到 /api/workspaces/teamA 并带上新名字。
  if (workspaceSelect) {
    workspaceSelect.value = "teamA";
    for (const handler of workspaceSelect.listeners.change || []) await handler({ target: workspaceSelect });
  }
  await settle();
  checks.namedWorkspaceActionsEnabled = Boolean(byClass("workspace-rename")[0]
    && !byClass("workspace-rename")[0].disabled);
  checks.namedWorkspaceModelsEnabled = Boolean(byClass("workspace-models")[0]
    && !byClass("workspace-models")[0].disabled);
  global.window.prompt = () => "teamC";
  const renameButton = byClass("workspace-rename")[0];
  if (renameButton) for (const handler of renameButton.listeners.click || []) await handler({});
  await settle();
  const rename = server.workspaceWrites.find((w) => w.method === "PUT");
  checks.renameUsesWorkspaceEndpoint = Boolean(rename) && rename.path === "/api/workspaces/teamA";
  checks.renameSendsNewName = rename?.payload?.name === "teamC";
  checks.renameSendsRevision = typeof rename?.payload?.config_revision === "string";
  // 改名后页面必须跟着新名字走，否则下一次读取会拿旧名字查任务、显示成空列表。
  checks.renameFollowsNewName = storage.get("amkr.workspace") === "teamC"
    && server.taskWorkspaces.at(-1) === "teamC";

  // 删除：确认后 DELETE 到 /api/workspaces/teamC，并切回默认空间。
  const deleteButton = byClass("workspace-delete")[0];
  if (deleteButton) for (const handler of deleteButton.listeners.click || []) await handler({});
  await settle();
  // 确认对话框由 ui.js 挂到 document.body（不是 root），因此从那里找确认按钮。
  const confirm = findAll(global.document.body, (n) => n.tagName === "button"
    && n.textContent.trim() === "删除").at(-1);
  checks.deleteAsksForConfirmation = Boolean(confirm);
  if (confirm) for (const handler of confirm.listeners.click || []) await handler({});
  await settle();
  const removed = server.workspaceWrites.find((w) => w.method === "DELETE");
  checks.deleteUsesWorkspaceEndpoint = removed?.path === "/api/workspaces/teamC";
  checks.deleteSendsRevision = typeof removed?.payload?.config_revision === "string";
  // 删掉的是当前空间：必须切回一个真实存在的空间，不能停在已消失的名字上。
  checks.deleteReturnsToDefault = storage.get("amkr.workspace") === "default";
} else if (scenario === "cost_page_renders_with_pricing") {
  await settle();
  // 页面骨架与四张卡都在。
  checks.hasStatGrid = byClass("stat-grid").length === 1;
  checks.hasCostList = byClass("cost-row").length === 0 || byClass("cost-list").length === 1;
  const body = text();
  // 1M 输入 token × $2.5/1M = $2.50：金额必须真的算出来，而不是只画了壳。
  checks.showsComputedAmount = body.includes("$2.50");
  checks.showsCoverage = body.includes("计价覆盖率");
  checks.showsPriceDetail = body.includes("$2.5");
  // 中文标题确认渲染的是成本页而不是别的页。
  checks.isCostPage = body.includes("成本") && body.includes("models.dev");
  // 供应商成本必须真的画出来：它只能靠逐条明细关联 provider_id 与 upstream_model_id
  // （快照的 providers 里没有上游模型信息，拿 provider_id 查价格实测全部匹配不到）。
  checks.showsProviderCost = body.includes("openai");
  // 逐条成本卡必须显示"2/2 条有定价"。
  checks.showsRequestPricing = body.includes("2/2 条有定价");
  // 目录已就绪时不应出现"无定价"降级提示。
  checks.noUnavailableNotice = !body.includes("价格目录尚不可用");
  checks.requestedPricing = server.requests.some((r) => r.url === "/ui/pricing.json");
} else if (scenario === "cost_page_unmatched_model_shows_dash") {
  await settle();
  const body = text();
  // 目录是好的，所以**不该**出现"目录尚不可用"；该出现的是"没匹配到单价"。
  checks.noUnavailableNotice = !body.includes("价格目录尚不可用");
  checks.showsUnmatched = body.includes("没有匹配到单价") || body.includes("无定价") || body.includes("未匹配");
  // 关键：1.5M token 的用量在没有任何单价时**绝不能**变成 $0。
  checks.neverShowsZeroDollars = !body.includes("$0");
  // 覆盖率必须显示为 0%，把"一分钱都没算进来"讲明白。
  checks.showsZeroCoverage = body.includes("0%");
  checks.requestedPricing = server.requests.some((r) => r.url === "/ui/pricing.json");
} else if (scenario === "cost_page_without_pricing_shows_dash") {
  await settle();
  const body = text();
  // 目录不可用 → 明确说明，且**任何位置都不出现 $0**。
  checks.showsUnavailable = body.includes("无定价") || body.includes("价格目录尚不可用");
  checks.neverShowsZeroDollars = !body.includes("$0");
  // 用量仍然可见（页面降级但不空转）。
  checks.stillShowsTokens = body.includes("Token 用量");
  // 即便目录拿不到，也必须真的去请求过（否则"没显示 $0"只是因为压根没刷新）。
  checks.requestedPricing = server.requests.some((r) => r.url === "/ui/pricing.json");
} else if (scenario === "overview_reentry_paints_immediately") {
  // 首次进入：等异步数据落地，确认首页确实画出了 KPI 瓦片。
  await settle();
  checks.firstEntryHasContent = byClass("stat-grid").length === 1;
  // 概览是 10 张瓦片排 5 列（两整行）。列数写死在 CSS 的 .cols-5 里，这里同时
  // 锁住"类名还在"和"瓦片数正好排满"，否则以后加第 11 张又会甩出孤儿行。
  checks.overviewUsesFiveColumns = byClass("cols-5").length === 1;
  checks.overviewHasTenTiles = byClass("stat").length === 10;
  // 重试率是补进第 10 格的那张，得真的画出来（不是被 cols-5 挤掉）。
  checks.showsRetryKpi = text().includes("重试率");

  // 切走再切回：这一次 state 里的快照/序列/热力图都还在缓存里，两条 load* 都会
  // 提前 return（不产生任何回调），所以内容只能靠 renderOverview 里的同步首绘。
  navigate("settings");
  await settle();
  checks.leftOverview = store.page === "settings" && byClass("stat-grid").length === 0;

  navigate("overview");
  // 刻意**不**排空微任务：切回后必须当场就有内容，而不是等下一次轮询。
  checks.reentryPaintsSynchronously = byClass("stat-grid").length === 1;
  checks.reentryHasHeatmap = byClass("heat-cell").length > 0;
  // 用 .chart-host（折线 + 两处堆叠柱）而不是数 <svg>：导航图标也是 svg，
  // 数 svg 在"页面空白只剩余壳"时照样为真，那种断言等于没测。
  // 三处分别是：流量趋势折线、结果构成堆叠柱、Token 构成随时间堆叠柱。
  checks.reentryHasCharts = byClass("chart-host").length === 3;
} else if (scenario === "overview_shows_request_stream") {
  await settle();
  const body = text();
  // 请求流真的画出来了：两条记录都要在（含失败那条）。
  checks.hasStreamRows = byClass("stream-row").length === 2;
  // 失败优先于重试着色：同一条记录既失败又重试时，红色比黄色更重要。
  checks.showsFailureRow = byClass("is-failure").length === 1;
  checks.noRetryToneOnFailure = byClass("is-retry").length === 0;
  // 概览同时保留它自己的实时图，不能为了塞进请求流把原有内容挤掉。
  checks.stillHasHeatmap = byClass("heat-cell").length > 0;
  checks.hasPulseGrid = byClass("pulse-cell").length === 4;
  // 成本列：逐格断言，而不是全页搜 "$0"——小额金额本来就渲染成 $0.000250，
  // 全页搜会把"正确的小额"误判成"把不知道渲染成免费"。
  const costCells = byClass("stream-cost").map((n) => n.textContent.trim());
  checks.costCellsFilled = costCells.length === 2 && costCells.every((t) => t.length > 0);
  // 未匹配到单价的条目显示 "—"，**不能**是 "$0"。
  checks.unpricedShowsDash = costCells.includes("—");
  checks.noCostCellIsZero = costCells.every((t) => t !== "$0");
} else if (scenario === "workspaces_kpi_grid_has_no_orphan") {
  await settle();
  const body = text();
  checks.isWorkspacesPage = body.includes("工作空间") && body.includes("归属请求");
  // 10 张瓦片排 5 列 = 两整行，没有孤儿瓦片。列数写死在 CSS 的 .cols-5 里，
  // 这里同时锁住"类名还在"和"瓦片数正好排满"（2 列档还有一层：10 也要能被 2 整除）。
  checks.usesFiveColumns = byClass("cols-5").length === 1;
  checks.hasTenTiles = byClass("stat").length === 10;
  // 十张瓦片各自的读数都在（少一张也照样可能凑够 10 个 .stat，所以要按名字点）。
  checks.showsAllKpiLabels = [
    "工作空间", "归属请求", "归属 Token", "成功率", "未归属请求",
    "失败请求", "重试次数", "缓存命中率", "平均耗时", "平均首字",
  ].every((label) => body.includes(label));
  // 未归属 30 / 全部 150 = 20.0%：这个占比是升级后第一眼要看的数字。
  checks.showsOrphanShare = body.includes("20.0%");
  // 缓存 4000 / 输入 8000 = 50.0%：补进来的那批瓦片必须真的在算，不是摆样子。
  checks.showsCacheRatio = body.includes("50.0%");
  // 未归属非零时流向图前面必须给说明，否则用户会以为图少画了一块。
  checks.explainsUnattributed = body.includes("没有工作空间归属");
  // 流向图真的画出来了（五列都要有节点）。
  checks.hasFlowNodes = byClass("sankey-node").length >= 5;
} else if (scenario === "usage_page_renders_history_ranges") {
  await settle();
  const body = text();
  checks.isUsagePage = body.includes("用量统计");
  // 历史档必须都在切换项里：1 个月/3 个月/6 个月/1 年/全部。
  // 分段控件显示短标签（1m/3m/6m/1y/全部），完整中文名在 title 里 —— 两处都要对，
  // 否则用户看到的是看不懂的缩写、或者悬停提示与按钮对不上。
  const titles = buttons().map((b) => String(b.attrs.title || ""));
  checks.hasAllUsageRangeTitles = ["1 个月", "3 个月", "6 个月", "1 年", "全部历史"]
    .every((label) => titles.includes(label));
  const shortLabels = buttons().map((b) => b.textContent.trim());
  checks.hasUsageRangeShortLabels = ["1m", "3m", "6m", "1y", "全部"]
    .every((label) => shortLabels.includes(label));
  // 累计用量与按天用量是用量统计的主视图，缺了就等于还是旧的实时页。
  checks.hasCumulativeCard = body.includes("累计用量");
  checks.hasDailyCard = body.includes("按天用量");
  checks.hasHourlyCard = body.includes("日内时段分布");
  // 性能趋势不能在重做这一页时弄丢（它原本就在活动页上）。
  checks.hasLatencyCard = body.includes("性能趋势") && byClass("latency-panel").length === 2;
  // 日志不该再留在这一页（已独立成页）。
  checks.noLogPanel = byClass("log-panel").length === 0;
  // 请求流也搬走了（它属于概览）。
  checks.noStreamRows = byClass("stream-row").length === 0;

  // —— 按 Key 拆分：这次改动的核心，答「这些流量是哪把 Key 出去的」 ——
  checks.hasUpstreamKeyCard = body.includes("按上游 Key 用量");
  checks.hasAccessKeyCard = body.includes("按访问密钥用量");
  // 同名 Key 必须带上供应商前缀，否则两家的 main 看起来是一把。
  checks.showsProviderPrefixedKeys = body.includes("openai / main") && body.includes("azure / main");
  // 「模型 / Key」也要能区分同名 Key（模型 / 供应商 / Key 三段）。
  checks.showsModelProviderKeyRows = body.includes("route-a / openai / main")
    && body.includes("route-b / azure / main");
  // 上游分布卡里多出来的那一段。
  checks.hasUpstreamDistributionByKey = body.includes("上游分布") && body.includes("按上游 Key");
  // 访问密钥那一张：显示名 + 配置里的 key_id 都要在（显示名可重名，id 才是标识符）。
  checks.showsAccessKeyNameAndID = body.includes("试用账号 A") && body.includes("ak1");
  // 没有供应商归因的历史行必须在表里说明出处，否则表内合计与顶部总量对不上就成了
  // 「看板数字互相矛盾」。
  checks.explainsUnattributedKeyRows = body.includes("没有供应商归因")
    && body.includes("另有 2 次调用");
} else if (scenario === "usage_all_history_clamps_span") {
  // 切到「全部」触发跨度推导：这里直接驱动页面上的分段控件。
  await settle();
  const allButton = buttons().find((b) => b.textContent.trim() === "全部");
  if (allButton) for (const handler of allButton.listeners.click || []) await handler({});
  await settle();
  const body = text();
  // 跨度顶到上限时必须如实说明，否则「全部」会被读成"真的是全部"。
  checks.warnsTruncated = body.includes("只覆盖最近");
  // 推导出的小时数必须是上限（8760），而不是从 2020 年算出的五万多小时——
  // 那样请求会被后端 422 挡掉，页面只剩报错。
  const seriesCall = server.requests.filter((r) => r.url.startsWith("/metrics/series")).pop();
  checks.seriesHoursClamped = !!seriesCall && seriesCall.url.includes("hours=8760"),
    seriesCall && seriesCall.url;
  // 推导跨度用的必须是 all_history（/metrics/series 没有这个参数）。
  checks.probedHistorySpan = server.requests.some((r) => r.url.includes("all_history=true"));
} else if (scenario === "logs_page_filters_by_level") {
  await settle();
  const body = text();
  checks.isLogsPage = body.includes("服务日志");
  checks.hasLogPanel = byClass("log-panel").length === 1;
  // 默认"全部"：四行都要在。
  checks.showsAllLines = ["service started", "cache warm", "upstream slow", "upstream 502"]
    .every((line) => body.includes(line));
  checks.countsLines = body.includes("共 4 行");
  // 切到"仅错误"：只剩 ERROR 那一行。级别判定与着色共用同一份口径，
  // 否则会出现"标红了却没被筛中"。
  const errorButton = buttons().find((b) => b.textContent.trim() === "仅错误");
  if (errorButton) for (const handler of errorButton.listeners.click || []) await handler({});
  await settle();
  const filtered = text();
  checks.errorFilterKeepsError = filtered.includes("upstream 502");
  checks.errorFilterDropsInfo = !filtered.includes("service started");
  checks.errorFilterDropsDebug = !filtered.includes("cache warm");
  checks.errorFilterDropsWarn = !filtered.includes("upstream slow");
  // 过滤生效后行数提示要跟着变，否则"显示 4 行"和屏幕上的 1 行自相矛盾。
  checks.footNotesFiltering = filtered.includes("显示 1 / 4 行");

  // 关键字搜索：与级别筛选是两条独立路径，改动时容易只保住其中一条。
  // 先切回"全部"，再搜 "cache"，应只剩 DEBUG 那一行。
  const allButton = buttons().find((b) => b.textContent.trim() === "全部");
  if (allButton) for (const handler of allButton.listeners.click || []) await handler({});
  await settle();
  const search = inputs()[0];
  checks.hasSearchInput = !!search;
  if (search) {
    search.value = "cache";
    for (const handler of search.listeners.input || []) await handler({ target: search });
    await settle();
  }
  const searched = text();
  checks.searchKeepsMatch = searched.includes("cache warm");
  checks.searchDropsOthers = !searched.includes("service started") && !searched.includes("upstream 502");
  checks.searchNotResettingLevel = searched.includes("显示 1 / 4 行");
  // 搜索时不能整块重绘：那会重建输入框、把焦点和光标位置丢掉。
  // 判据是输入框节点仍是同一个（重绘会换新节点）。
  checks.searchKeepsInputNode = inputs()[0] === search;
} else if (scenario === "logs_page_surfaces_read_error") {
  await settle();
  const body = text();
  // 服务端用 200 + error 字段报告失败，页面必须照实显示，
  // 而不是把空文本渲染成"日志为空。"（那是把故障说成正常）。
  checks.showsReadError = body.includes("permission denied");
  checks.notClaimedEmpty = !body.includes("日志为空。");
}

const failed = Object.entries(checks).filter(([, ok]) => !ok).map(([name]) => name);
console.log(JSON.stringify({ scenario, checks, failed }));
process.exit(failed.length ? 1 : 0);
