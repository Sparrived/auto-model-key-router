// WebUI 外壳：连接状态、鉴权、导航与全局轮询。
//
// 轮询分层：健康 5s；指标在监控页（概览/活动）较快，其余页面放慢。
// 页面不会因为轮询被整块重建 —— 重建会丢掉展开的详情、输入框焦点与滚动位置，
// 对工作台页面（供应商/设置）尤其致命。改由 onTick 通知当前页面自行重绘。

import { h, mount, errorText, formatCount, formatCompact } from "./dom.js";
import { api, apiBase, setKey, ApiError, onUnauthorized } from "./api.js";
import { installToastHost, notice, buttonNode, empty } from "./ui.js";
import { icon } from "./icons.js";

import { renderOverview } from "./pages/overview.js";
import { renderActivity } from "./pages/activity.js";
import { renderWorkspaces } from "./pages/workspaces.js";
import { renderLogs } from "./pages/logs.js";
import { renderCost } from "./pages/cost.js";
import { renderAccounts } from "./pages/accounts.js";
import { renderProviders } from "./pages/providers.js";
import { renderRouting } from "./pages/routing.js";
import { renderUnified } from "./pages/unified.js";
import { renderTasks } from "./pages/tasks.js";
import { renderAccessKeys } from "./pages/accesskeys.js";
import { renderIntegrations } from "./pages/integrations.js";
import { renderSettings } from "./pages/settings.js";

export const PAGES = [
  { group: "监控", items: [
    { id: "overview", label: "概览", icon: "overview", render: renderOverview },
    { id: "activity", label: "用量统计", icon: "activity", render: renderActivity },
    { id: "accounts", label: "账号资源", icon: "gauge", render: renderAccounts },
    { id: "workspaces", label: "工作空间", icon: "layers", render: renderWorkspaces },
    { id: "logs", label: "服务日志", icon: "logs", render: renderLogs },
    { id: "cost", label: "成本", icon: "cost", render: renderCost },
  ]},
  { group: "配置", items: [
    { id: "providers", label: "供应商", icon: "providers", render: renderProviders },
    { id: "routing", label: "模型路由", icon: "routing", render: renderRouting },
    { id: "unified", label: "统一模型", icon: "unified", render: renderUnified },
    { id: "tasks", label: "任务路由", icon: "task", render: renderTasks },
    { id: "access-keys", label: "访问密钥", icon: "key", render: renderAccessKeys },
    { id: "integrations", label: "集成", icon: "integrations", render: renderIntegrations },
  ]},
  { group: "系统", items: [{ id: "settings", label: "设置", icon: "settings", render: renderSettings }] },
];

// 指标轮询节奏（毫秒）：概览/用量统计要看趋势，日志页自己管轮询，其余放慢。
const METRICS_INTERVAL = { overview: 10000, activity: 15000, workspaces: 20000, default: 30000 };
// 订阅了 onTick、需要被主动通知重新取数的页面。不在这里的页面只在强制刷新时整块重绘。
const TICK_PAGES = new Set(["overview", "activity", "workspaces"]);
const HEALTH_INTERVAL = 5000;

export const store = {
  page: location.hash.replace(/^#\/?/, "") || "overview",
  health: null,
  metrics: null,
  metricsError: null,
  metricsAt: null,
  connectionError: null,
  authorized: false,
  revision: null,
  healthTicking: false,
  metricsTicking: false,
};

let root = null;      // #root
let appHost = null;   // 应用栏 + 导航 + 内容（每次重绘整块替换）
let barHost = null;   // 单独的应用栏槽位：健康轮询只重绘这里
let contentHost = null;
let healthTimer = null;
let metricsTimer = null;
let renderedPage = null;

// 页面订阅：页面重绘时重新注册，切页时清空，避免旧页面继续被通知。
let tickListeners = new Set();
// 页面自持资源的清理函数（定时器、事件订阅）。切页时必须调用，否则
// 离开页面后定时器仍在跑：活动页的 2 秒日志轮询就是这么泄漏的。
let leaveHandlers = new Set();

export function onTick(listener) {
  tickListeners.add(listener);
  return () => tickListeners.delete(listener);
}

export function onLeave(handler) {
  leaveHandlers.add(handler);
  return () => leaveHandlers.delete(handler);
}

function runLeaveHandlers() {
  const handlers = [...leaveHandlers];
  leaveHandlers.clear();
  for (const handler of handlers) {
    try {
      handler();
    } catch (error) {
      console.warn("page cleanup failed", error);
    }
  }
}

function notifyTicks() {
  for (const listener of tickListeners) {
    try {
      listener(store);
    } catch (error) {
      // 单个页面重绘失败不应影响其它订阅者与后续轮询。
      console.warn("page tick failed", error);
    }
  }
}

export function currentPage() {
  for (const group of PAGES) {
    const found = group.items.find((item) => item.id === store.page);
    if (found) return found;
  }
  return PAGES[0].items[0];
}

// 深链（#/providers）与浏览器前进后退都走这里，未知页回落到首页。
function pageFromHash() {
  return location.hash.replace(/^#\/?/, "") || "overview";
}

export function navigate(page) {
  if (store.page === page) return;
  store.page = page;
  location.hash = `/${page}`;
  renderShell();
}

// 页面通过 ctx 触发局部重绘，避免整壳重建导致输入框失焦。
export function ctx() {
  return {
    store,
    navigate,
    rerender: () => renderContent(true),
    refreshHealth: loadHealth,
    refreshMetrics: () => loadMetrics(true),
    askLogin,
    onTick,
    onLeave,
  };
}

async function fetchHealth() {
  try {
    store.health = await api.health();
    store.connectionError = null;
  } catch (error) {
    store.health = null;
    store.connectionError = errorText(error);
  }
}

async function loadHealth() {
  if (store.healthTicking) return;
  store.healthTicking = true;
  const before = store.connectionError;
  await fetchHealth();
  store.healthTicking = false;
  // 连接状态变了就重绘一次。鉴权通过后这只会刷新应用栏上的服务状态；未授权时
  // renderShell 会去跳登录页，而 redirectToLogin 自带去重，重复调用是无害的空转。
  if (requiresKey() && before !== store.connectionError) renderShell();  // 健康轮询只更新应用栏，不动页面内容。
  renderBar();
  if (TICK_PAGES.has(store.page)) notifyTicks();
}

// localStorage 里有 Key 不代表 Key 可用（可能已被重置或来自旧版本），必须实际请求一次。
// 返回值区分"未授权"与"连不上"，前者才清掉本地 Key，避免网络抖动误删有效 Key。
async function verifyAccess() {
  // 连不上时无从判断是否需要鉴权，一律当作未通过：宁可停在验证页，也不拿没验证过
  // 的状态进主界面。
  if (!store.health) return "unreachable";
  if (!store.health.local_auth_enabled) return "ok";
  try {
    await api.settings();
    return "ok";
  } catch (error) {
    if (error instanceof ApiError && error.isUnauthorized) return "unauthorized";
    // 连不上时记下原因，让验证页能说明"是服务没起来"而不是"Key 错了"。
    store.connectionError = errorText(error);
    return "unreachable";
  }
}

// 未授权（含"连不上、无从判断"）时整页只渲染验证页：不渲染应用栏与导航，页面
// 模块也不会带着错误 Key 加载并把 401 缓存成业务错误。authorized 只会在确认可访问
// 后置真，所以服务掉线时也停在验证页，而不是拿没验证过的状态进主界面。
function requiresKey() {
  return !store.authorized;
}

async function loadMetrics(force = false) {
  if (!store.authorized && store.health?.local_auth_enabled) return;
  if (store.metricsTicking && !force) return;
  store.metricsTicking = true;
  try {
    const data = await api.metrics(1);
    if (data && data.total) { store.metrics = data; store.metricsError = null; }
    else if (!store.metrics) { store.metrics = data; store.metricsError = null; }
    store.metricsAt = new Date().toISOString();
  } catch (error) {
    if (error instanceof ApiError && error.isUnauthorized) { store.authorized = false; }
    store.metricsError = errorText(error);
  }
  store.metricsTicking = false;
  // 页面自己决定怎么用新数据；只有还没渲染出内容的页面才需要整块重绘。
  if (TICK_PAGES.has(store.page)) notifyTicks();
  else if (force) renderContent(true);
}

function metricsInterval() {
  return METRICS_INTERVAL[store.page] ?? METRICS_INTERVAL.default;
}

function scheduleTimers() {
  clearInterval(healthTimer);
  clearInterval(metricsTimer);
  healthTimer = setInterval(loadHealth, HEALTH_INTERVAL);
  metricsTimer = setInterval(() => loadMetrics(), metricsInterval());
}

function serviceTone() {
  if (store.connectionError) return "bad";
  if (!store.health) return "warn";
  if (store.health.status !== "ok") return "warn";
  if (store.health.local_auth_enabled && !store.authorized) return "warn";
  return "good";
}

function serviceLabel() {
  if (store.connectionError) return "服务未连接";
  if (!store.health) return "正在连接";
  if (store.health.status !== "ok") return "服务未运行";
  if (store.health.local_auth_enabled && !store.authorized) return "需要本地鉴权 Key";
  return "服务运行中";
}

// 应用栏里的实时读数：只有授权且拿到指标时才显示，否则会谎报 0 RPM。
function liveMetrics() {
  if (requiresKey() || !store.metrics) return null;
  const rpm = store.metrics.current_rpm;
  const tpm = store.metrics.current_tpm;
  const active = store.metrics.active_requests ?? 0;
  return [
    { label: "RPM", value: formatCount(rpm ?? 0) },
    { label: "TPM", value: formatCompact(tpm ?? 0) },
    { label: "进行中", value: formatCount(active) },
  ];
}

function buildBar() {
  const metrics = liveMetrics();
  const tone = serviceTone();
  return h("header.app-bar", {},
    h("div.brand", {},
      h("span.brand-mark", {}, icon("bolt", { size: 18 })),
      h("div", {}, "AMKR", h("small", "Auto Model Key Router")),
    ),
    h("div.spacer"),
    h("div.live-pill", {},
      h("span.status-dot", { class: `tone-${tone}`, title: serviceLabel() }),
      h("span", serviceLabel()),
      metrics
        ? h("div.live-metrics", {}, metrics.map((item) =>
            h("span", {}, `${item.label} `, h("strong", item.value))))
        : null,
    ),
    h("div.bar-actions", {},
      buttonNode("", {
        class: "icon-btn",
        "aria-label": "立即刷新",
        title: "立即刷新状态与指标",
        onClick: async () => {
          await fetchHealth();
          await loadMetrics(true);
          renderBar();
          renderContent(true);
        },
      }, icon("refresh", { size: 18 })),
    ),
  );
}

function buildNav() {
  return h("nav.nav", { "aria-label": "主导航" },
    PAGES.map((group) => h("div", {},
      h("div.group-label", group.group),
      group.items.map((item) => h("button.nav-item", {
        type: "button",
        "aria-current": store.page === item.id ? "page" : null,
        onClick: () => navigate(item.id),
      }, icon(item.icon, { size: 18 }), h("span", item.label))),
    )),
    navFoot(),
  );
}

function navFoot() {
  const health = store.health;
  return h("div.nav-foot", {},
    h("div", {}, "版本 ", h("code", health?.version ? `v${health.version}` : "读取中")),
    health?.base_url ? h("div", {}, "入口 ", h("code", { title: health.base_url }, health.base_url)) : null,
    health?.webui_path ? h("div", {}, "界面 ", h("code", health.webui_path)) : null,
  );
}

// 只重绘应用栏：健康轮询走这条路，避免动到页面内容。
// 用独立槽位而不是 querySelector(".app-bar")，既不依赖节点查找，也不需要 prepend。
function renderBar() {
  if (!barHost) return;
  mount(barHost, buildBar());
}

export function renderShell() {
  tickListeners.clear();
  renderedPage = null;
  // 未通过鉴权时不在本页画表单，而是整页跳到独立的登录页（webui/login.html），并把
  // 当前地址作为 next 交给它。深链/切页/会话中途失效都走这里，"带着无效 Key 进入
  // 主界面"因此没有任何入口——本页在未授权时一个节点都不渲染。
  if (requiresKey()) {
    barHost = null;
    contentHost = null;
    redirectToLogin();
    return;
  }
  const nav = buildNav();
  contentHost = h("main.content", { id: "content" });
  barHost = h("div.bar-slot");
  const shell = h("div.shell", nav, contentHost);
  mount(appHost, barHost, shell);
  renderBar();
  renderContent(true);
}

// 版本门槛：管理 API 是 4.0.0 起稳定对外，低于此版本只开放只读诊断页。
const MIN_VERSION = "4.0.0";

export function versionCompatible(version) {
  if (!version) return true;
  const parse = (value) => value.split(".").slice(0, 3).map((part) => parseInt(part, 10));
  const current = parse(version);
  const minimum = parse(MIN_VERSION);
  if (current.length !== 3 || current.some(Number.isNaN)) return false;
  for (let i = 0; i < 3; i += 1) {
    if (current[i] !== minimum[i]) return current[i] > minimum[i];
  }
  return true;
}

export function requiresCompatible(page, children) {
  if (versionCompatible(store.health?.version)) return children;
  return h("div.stack", {},
    notice(`当前 AMKR ${store.health?.version || "未知版本"}，WebUI 至少需要 ${MIN_VERSION}。升级前仅开放设置与只读诊断。`, "warn"),
    empty("后端版本不兼容。"),
  );
}

// force=false 时同页重入不重建 DOM，保留展开状态与滚动位置。
function renderContent(force = false) {
  if (!contentHost) return;
  const page = currentPage();
  if (renderedPage === page.id && !force) return;
  // 换页前先让上一页释放自己持有的定时器/订阅；重绘同一页（force）同理，
  // 因为页面会在 render 里重新注册。
  runLeaveHandlers();
  tickListeners.clear();
  try {
    const node = page.render(ctx());
    mount(contentHost, node);
    renderedPage = page.id;
  } catch (error) {
    mount(contentHost, notice(`页面渲染失败: ${errorText(error)}`, "error"));
    renderedPage = page.id;
  }
}

// ══ 未授权时的去向 ══
// 登录是**独立一页**（webui/login.html、pages/login.js）：本页在未授权时什么都不画，
// 只负责把用户送过去，并把当前地址带上（登录成功后回到原本要去的内页，深链不丢）。
//
// 为什么用 replace 而不是 href：登录页不该留在历史记录里。用 href 的话，登录成功后
// 按"后退"会退回到登录表单，而那时浏览器里已经有可用凭据了——一个已经登录的人被
// 送回登录框，是很容易被当成"又掉线了"的假故障。
//
// 会话中途 401（如在设置里重置了本地鉴权 Key）带上 reason=expired：登录页据此区分
// "从没登录过"与"原来那把失效了"。

// loginURL 拼登录页地址。与 api.js 的 apiBase 同源：独立运行是 /ui/login.html，
// 嵌入宿主是 /amkr/ui/login.html，写死 /ui 会让墙子路径部署跳到打不开的地址。
function loginURL(reason) {
  const params = new URLSearchParams();
  const next = `${location.pathname}${location.search || ""}${location.hash || ""}`;
  if (next && next !== "/") params.set("next", next);
  if (reason) params.set("reason", reason);
  const query = params.toString();
  return `${apiBase()}/ui/login.html${query ? `?${query}` : ""}`;
}

// redirecting 保证只跳一次：健康轮询会反复 renderShell，重复调用 replace 会让浏览器
// 在历史里留下多条记录（并刷屏报错）。
let redirecting = false;

function redirectToLogin(reason) {
  if (redirecting) return;
  redirecting = true;
  location.replace(loginURL(reason));
}

// askLogin 由 ctx 暴露给页面模块：任何接口 401 都能把用户送回登录页。
function askLogin(message) {
  store.authorized = false;
  redirectToLogin(message ? "expired" : null);
}

async function boot() {
  root = document.getElementById("root");
  // 应用内容与 Toast 分层：整壳重绘 mount 到 appHost，不能顺手把 Toast 容器清掉。
  // Toast 放在 appHost 之后，DOM 顺序上就在应用内容之上（另有高 z-index 兜底）。
  appHost = h("div.app");
  root.append(appHost);
  installToastHost(root);
  store.page = pageFromHash();

  // 先挂 hash 监听：未授权时 renderShell 一律落回验证页，因此浏览器前进/后退或
  // 手改地址栏都无法绕过鉴权。
  window.addEventListener("hashchange", () => {
    const next = pageFromHash();
    if (next !== store.page) { store.page = next; renderShell(); scheduleTimers(); }
  });

  // 会话中途失效（如设置里重置了本地鉴权 Key）走这里跳回登录页；本页首次进入时
  // 还没授权，401 由下面的 verifyAccess 处理，不必重复跳转。
  onUnauthorized(() => { if (store.authorized) askLogin("本地鉴权 Key 已失效，请重新输入。"); });

  await fetchHealth();
  // 一律先验证再进主界面：需要鉴权时要校验 Key，连不上时也无从判断是否放行。
  const status = await verifyAccess();
  if (status !== "ok") {
    // 无效凭据要清掉，否则下次打开还会拿它白打一轮 401；连不上时保留——可能只是
    // 服务还没起来，重试即可，不该让用户在登录页重贴一次。
    if (status === "unauthorized") setKey("");
    store.authorized = false;
    // 登出即离开本页：登录页接管轮询，这里不必再拉定时器（页面马上被替换）。
    renderShell();
    return;
  }
  store.authorized = true;
  renderShell();
  await loadMetrics(true);
  renderContent(true);
  scheduleTimers();
}

export { boot };
