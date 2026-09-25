// amkr WebUI —— 与路由服务通信的唯一入口。
// 除 /health 外，所有管理接口都需要本地鉴权 Key（Authorization: Bearer）。

const KEY_STORAGE = "amkr.apiKey";

export function getKey() {
  return localStorage.getItem(KEY_STORAGE) || "";
}

export function setKey(value) {
  if (value) localStorage.setItem(KEY_STORAGE, value);
  else localStorage.removeItem(KEY_STORAGE);
}

export class ApiError extends Error {
  constructor(message, status, detail) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
  }
  get isConflict() {
    return this.status === 409;
  }
  get isUnauthorized() {
    return this.status === 401;
  }
}

// Key 在会话中途失效（如在设置里重置了本地鉴权 Key）时，任何接口都会 401。
// 这里统一上报，由 app.js 跳回登录页，避免各页面把 401 当成业务错误缓存下来。
// 登录页自身不注册这个钩子：它正在做的事就是验 Key，401 是预期结果而非异常。
let unauthorizedHandler = null;

export function onUnauthorized(handler) {
  unauthorizedHandler = handler;
}

function detailText(payload, status) {
  if (payload && typeof payload === "object") {
    const d = payload.detail;
    if (typeof d === "string") return d;
    if (Array.isArray(d) && d.length) {
      return d.map((item) => item.msg || JSON.stringify(item)).join("；");
    }
    if (d) return JSON.stringify(d);
  }
  return `HTTP ${status}`;
}

// WebUI 可能被挂在子路径下（独立运行是 /ui/，嵌入宿主是 /amkr/ui/），因此 API
// 基址必须从当前页面路径反推。写死绝对路径会让嵌入后的每个请求打到宿主根路径。
//
// 导出给 webui/panel.js：面板是同一套静态资源里的另一个入口，它的请求必须落在
// 同一个基址上，否则挂子路径部署时面板会打到宿主根路径。
export function apiBase() {
  const path = String(location.pathname || "");
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) return path.slice(0, index);
  if (path.endsWith("/ui")) return path.slice(0, -3);
  return "";
}

async function request(path, { method = "GET", body, auth = true, workspace } = {}) {
  const headers = {};
  if (auth) headers.Authorization = `Bearer ${getKey()}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  // 工作空间与代理面共用同一个头：管理面与调用方用同一个概念选空间。
  if (workspace) headers["X-AMKR-Workspace"] = workspace;
  let response;
  try {
    response = await fetch(`${apiBase()}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (error) {
    throw new ApiError(`无法连接 AMKR 服务: ${error.message}`, 0, null);
  }
  if (response.status === 204) return null;
  const text = await response.text();
  let payload = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }
  if (!response.ok) {
    const detail = detailText(payload, response.status);
    if (response.status === 401 && auth && unauthorizedHandler) unauthorizedHandler();
    throw new ApiError(`AMKR 请求失败（HTTP ${response.status}）: ${detail}`, response.status, detail);
  }
  return payload;
}

export const api = {
  health: () => request("/health", { auth: false }),
  metrics: (hours = 1) => request(`/metrics?hours=${hours}`),
  series: (hours = 1, bucketSeconds = 60) =>
    request(`/metrics/series?hours=${hours}&bucket_seconds=${bucketSeconds}`),
  // 逐条请求明细：/metrics/requests 的 hours 上限是 720，limit 上限是 200。
  requests: ({ hours = 1, limit = 50, ...filters } = {}) => {
    const params = new URLSearchParams({ hours: String(hours), limit: String(limit) });
    for (const [key, value] of Object.entries(filters)) {
      if (value !== null && value !== undefined && value !== "") params.set(key, String(value));
    }
    return request(`/metrics/requests?${params}`);
  },
  logs: () => request("/api/logs"),

  // 工作空间用量读数：挂在 /ui/ 下（不占用 /api 那份已发布路由清单），但要完整鉴权
  // ——内容会暴露各空间的用量与模型流向。
  workspaceUsage: ({ hours = 24, allHistory = false } = {}) =>
    request(allHistory
      ? "/ui/workspace-usage.json?all_history=true"
      : `/ui/workspace-usage.json?hours=${hours}`),

  // Key 用量读数：「这些流量是哪把 Key 出去的」（上游 Key + 访问密钥两份拆分）。
  // 同样挂 /ui/ 且要完整鉴权：/metrics 是**对照参照实现**的读数，形状已发布不能加
  // 字段，所以这份按 Key 拆分的读数是单独一条本项目自有的接口。
  keyUsage: ({ hours = 24, allHistory = false } = {}) =>
    request(allHistory
      ? "/ui/key-usage.json?all_history=true"
      : `/ui/key-usage.json?hours=${hours}`),

  // 价格目录（models.dev）：由服务端缓存并定期刷新，挂在 WebUI 前缀下。
  // **不鉴权**——内容是 models.dev 的公开数据，静态资源本身也是公开的。
  // 服务端还没取到目录时回 503，由 webui/pricing.js 吞掉并降级成"无定价"。
  pricing: () => request("/ui/pricing.json", { auth: false }),

  tool: () => request("/api/tool"),
  setWebui: (enabled) => request("/api/tool/webui", { method: "POST", body: { enabled } }),
  runService: (action) => request(`/api/service/${action}`, { method: "POST" }),

  integrations: () => request("/api/integrations"),
  applyIntegration: (agent, mode) =>
    request(`/api/integrations/${agent}`, { method: "POST", body: { mode } }),
  rollbackIntegration: (agent) =>
    request(`/api/integrations/${agent}/rollback`, { method: "POST" }),

  settings: () => request("/api/settings"),
  updateSettings: (revision, settings) =>
    request("/api/settings", { method: "PUT", body: { config_revision: revision, ...settings } }),
  regenerateLocalKey: (revision) =>
    request("/api/settings/local-api-key", { method: "POST", body: { config_revision: revision } }),
  checkUpdate: () => request("/api/update/check", { method: "POST" }),

  // 自更新：与 pricing 一样挂在 /ui/ 前缀下（不占用 47+7 条 /api 路由）。
  // 但**必须鉴权**——替换可执行文件是本服务最特权的操作，服务端要求完整权限，受限的
  // 推理凭据会被拒。status 是公开的（只回答"这个构建有没有自更新能力"），且不鉴权才能
  // 在任何情况下都正确决定按钮是否显示。
  updateStatus: () => request("/ui/update/status", { auth: false }),
  applyUpdate: () => request("/ui/update/apply", { method: "POST" }),

  exportConfig: () => request("/api/config/export", { method: "POST" }),
  importConfig: (revision, config) =>
    request("/api/config/import", { method: "POST", body: { config_revision: revision, config } }),

  providers: () => request("/api/providers"),
  createProvider: (revision, id, baseUrl) =>
    request("/api/providers", { method: "POST", body: { config_revision: revision, id, base_url: baseUrl } }),
  updateProvider: (revision, providerId, id, baseUrl, routes) =>
    request(`/api/providers/${encodeURIComponent(providerId)}`, {
      method: "PUT",
      body: { config_revision: revision, id, base_url: baseUrl, routes },
    }),
  deleteProvider: (revision, providerId) =>
    request(`/api/providers/${encodeURIComponent(providerId)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  createProviderKey: (revision, providerId, name, apiKey) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys`, {
      method: "POST",
      body: { config_revision: revision, name, api_key: apiKey },
    }),
  updateProviderKey: (revision, providerId, keyName, patch) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}`, {
      method: "PUT",
      body: { config_revision: revision, ...patch },
    }),
  deleteProviderKey: (revision, providerId, keyName) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  keyModels: (providerId, keyName) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/models`),
  setKeyModels: (revision, providerId, keyName, models) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/models`, {
      method: "PUT",
      body: { config_revision: revision, models },
    }),

  probeProvider: (revision, providerId) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/probe`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  probeKey: (revision, providerId, keyName) =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/probe`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  startProbe: (providerId, keys, timeoutSeconds) =>
    request("/api/probes/keys", {
      method: "POST",
      body: { provider_id: providerId, keys, timeout_seconds: timeoutSeconds },
    }),
  getProbe: (probeId) => request(`/api/probes/${encodeURIComponent(probeId)}`),
  cancelProbe: (probeId) =>
    request(`/api/probes/${encodeURIComponent(probeId)}/cancel`, { method: "POST" }),

  routes: () => request("/api/routes"),
  createRoute: (revision, id, targets, aliases, routingMode) =>
    request("/api/routes", {
      method: "POST",
      body: { config_revision: revision, id, targets, aliases, routing_mode: routingMode },
    }),
  // newId 非空时同时改名（服务端会一并改写 unified_model 与任务里的引用）。
  updateRoute: (revision, routeId, targets, aliases, routingMode, newId = null) =>
    request(`/api/routes/${encodeURIComponent(routeId)}`, {
      method: "PUT",
      body: { config_revision: revision, id: newId, targets, aliases, routing_mode: routingMode },
    }),
  deleteRoute: (revision, routeId) =>
    request(`/api/routes/${encodeURIComponent(routeId)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  models: () => request("/api/models"),
  updateModelEffort: (revision, modelId, effort) =>
    request(`/api/models/${encodeURIComponent(modelId)}`, {
      method: "PUT",
      body: { config_revision: revision, reasoning_effort: effort },
    }),

  unified: () => request("/api/unified-model"),
  updateUnified: (revision, unified) =>
    request("/api/unified-model", { method: "PUT", body: { config_revision: revision, default: unified.default, image: unified.image ?? null, embeddings: unified.embeddings ?? null } }),
  deleteUnified: (revision) =>
    request("/api/unified-model", { method: "DELETE", body: { config_revision: revision } }),

  tasks: (workspace) => request("/api/tasks", { workspace }),
  createTask: (revision, workspace, payload) =>
    request("/api/tasks", { method: "POST", workspace, body: { config_revision: revision, ...payload } }),
  updateTask: (revision, workspace, taskName, payload) =>
    request(`/api/tasks/${encodeURIComponent(taskName)}`, {
      method: "PUT",
      workspace,
      body: { config_revision: revision, ...payload },
    }),
  deleteTask: (revision, workspace, taskName) =>
    request(`/api/tasks/${encodeURIComponent(taskName)}`, {
      method: "DELETE",
      workspace,
      body: { config_revision: revision },
    }),

  // 工作空间目录：列出有任务的工作空间及各自任务数，供任务页填充切换下拉。
  // GET /api/tasks 是按空间过滤的，因此从任务列表推不出「还有哪些空间」。
  workspaces: () => request("/api/workspaces"),
  // 显式建空间：应用侧集成时先建空间拿一次 api_key，之后才往里填任务。
  // 响应是**唯一**出现明文 key 的地方（目录接口刻意不含它）。
  createWorkspace: (revision, name, apiKey) =>
    request("/api/workspaces", {
      method: "POST",
      body: apiKey
        ? { config_revision: revision, name, api_key: apiKey }
        : { config_revision: revision, name },
    }),
  // 改名会把整组任务搬到新名字下；删除连同组内任务一起删。
  renameWorkspace: (revision, workspace, name) =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}`, {
      method: "PUT",
      body: { config_revision: revision, name },
    }),
  deleteWorkspace: (revision, workspace) =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),
  // 换一把推理 key。旧 key 立刻失效，新明文只在这条响应里出现一次。
  // 与面板 key 分开：面板 key 换掉会让已嵌入的页面立刻失效，两者轮换节奏不同。
  rotateInferenceKey: (revision, workspace) =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}/inference-key`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  // 设定本空间**允许直呼**的模型清单（空数组 = 一个都不许，只走任务名）。
  setWorkspaceModels: (revision, workspace, models) =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}/models`, {
      method: "PUT",
      body: { config_revision: revision, models },
    }),

  // —— 访问密钥：分发给外部使用者的受限推理凭据（取代已删除的访客模式）——
  //
  // 每把 key 带两份清单：providers（能用哪些上游）与 models（能调哪些模型名）。
  // 清单的**三态**是这套接口最容易出错的地方，前端必须原样传递而不能折叠：
  //   字段不传 / null = 本次不动这份清单；`[]` = 一个都不许；`[...]` = 限定为这些。
  // 「清除限制、回到不限制」= 显式传 null（见下面的 scope 参数）。
  accessKeys: () => request("/api/access-keys"),
  // 响应是**唯一**出现明文 key 的两个时机之一（另一个是轮换）。
  createAccessKey: (revision, name, options = {}) =>
    request("/api/access-keys", {
      method: "POST",
      body: {
        config_revision: revision,
        name,
        ...(options.key ? { key: options.key } : {}),
        ...(options.enabled === undefined ? {} : { enabled: options.enabled }),
        ...(options.providers === undefined ? {} : { providers: options.providers }),
        ...(options.models === undefined ? {} : { models: options.models }),
      },
    }),
  // 改名字 / 启停 / 两份清单。**不换 key**：换 key 是下面那个独立动作。
  updateAccessKey: (revision, keyId, options = {}) =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}`, {
      method: "PUT",
      body: {
        config_revision: revision,
        ...options,
      },
    }),
  // 换一把新 key。旧 key 立刻失效，新明文只在这条响应里出现一次。
  rotateAccessKey: (revision, keyId) =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}/rotate`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  deleteAccessKey: (revision, keyId) =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  // CPA 账号资源：实例清单（含 management_key——读写都要求完整管理权限）与账号额度。
  //
  // 额度那一份是**服务端扇出**的结果：浏览器绝不直接访问 CPA（不同源、没有 CORS 头，
  // 而且 management_key 不该长期留在页面里），见 internal/api/handlers_cpa.go。
  cpaInstances: () => request("/api/cpa-instances"),
  saveCPAInstances: (revision, instances) =>
    request("/api/cpa-instances", {
      method: "PUT",
      body: { config_revision: revision, instances },
    }),
  cpaAccounts: () => request("/api/cpa-accounts"),
};
