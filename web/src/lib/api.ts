// amkr WebUI —— 与路由服务通信的唯一入口。
// 除 /health 外，所有管理接口都需要本地鉴权 Key（Authorization: Bearer）。

import { chunkModelIDs } from "./model-kinds";
import { readStorage, removeStorage, writeStorage } from "./storage";
import type {
  AccessKeyCreateOptions,
  AccessKeyCreateResponse,
  AccessKeyEntry,
  AccessKeyUpdateOptions,
  AccessKeysResponse,
  AccessKeyUsageRow,
  BreakdownGroup,
  ChatChunkDelta,
  ChatCompletionRequest,
  ChatResult,
  ChatStreamChunk,
  ChatUsage,
  ConfigExportResponse,
  ConfigRevisionResponse,
  CPAAccountsResponse,
  CPAInstancesResponse,
  CPAResetCreditUseResponse,
  CPAResetCreditsResponse,
  FilteredMetricsResponse,
  HealthResponse,
  ImageGenerationRequest,
  ImageGenerationResult,
  IntegrationsResponse,
  KeyModelsResponse,
  KeyUsageResponse,
  LogsResponse,
  MetricDimensionsResponse,
  MetricFilters,
  MetricSeriesPoint,
  MetricsSnapshot,
  ModelKindsResponse,
  ModelSummaryResponse,
  ModelsResponse,
  PricingResponse,
  ProbeResponse,
  Provider,
  ProviderKey,
  ProviderUpdateKeyPatch,
  ProvidersResponse,
  QuotaFreezeReleaseResponse,
  QuotaFreezesResponse,
  RequestListResponse,
  RouteEntry,
  RouteTarget,
  RoutesResponse,
  SeriesResponse,
  ServiceActionResponse,
  SettingsResponse,
  StatusMonitorResponse,
  Task,
  TaskPayload,
  TasksResponse,
  ToolResponse,
  UnifiedModel,
  UpdateStatusResponse,
  UsageBreakdownResponse,
  V1ModelsResponse,
  WorkspaceCreateResponse,
  WorkspaceSummary,
  WorkspaceUsageResponse,
  WorkspacesResponse,
} from "./types";

const KEY_STORAGE = "amkr.apiKey";

export function getKey(): string {
  return readStorage(KEY_STORAGE) || "";
}

export function setKey(value: string | null | undefined): void {
  if (value) writeStorage(KEY_STORAGE, value);
  else removeStorage(KEY_STORAGE);
}

export class ApiError extends Error {
  status: number;
  detail: string | null;

  constructor(message: string, status: number, detail: string | null) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
  }

  get isConflict(): boolean {
    return this.status === 409;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

// Key 在会话中途失效（如在设置里重置了本地鉴权 Key）时，任何接口都会 401。
// 这里统一上报，由 app.js 跳回登录页，避免各页面把 401 当成业务错误缓存下来。
// 登录页自身不注册这个钩子：它正在做的事就是验 Key，401 是预期结果而非异常。
let unauthorizedHandler: (() => void) | null = null;

export function onUnauthorized(handler: (() => void) | null): void {
  unauthorizedHandler = handler;
}

function detailText(payload: unknown, status: number): string {
  if (payload && typeof payload === "object") {
    const d = (payload as { detail?: unknown }).detail;
    if (typeof d === "string") return d;
    if (Array.isArray(d) && d.length) {
      return d.map((item) => (item as { msg?: string }).msg || JSON.stringify(item)).join("；");
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
export function apiBase(): string {
  const path = String(location.pathname || "");
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) return path.slice(0, index);
  if (path.endsWith("/ui")) return path.slice(0, -3);
  return "";
}

interface RequestOptions {
  method?: string;
  body?: unknown;
  auth?: boolean;
  workspace?: string;
}

async function request<T>(path: string, { method = "GET", body, auth = true, workspace }: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {};
  if (auth) headers.Authorization = `Bearer ${getKey()}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  // 工作空间与代理面共用同一个头：管理面与调用方用同一个概念选空间。
  if (workspace) headers["X-AMKR-Workspace"] = workspace;
  let response: Response;
  try {
    response = await fetch(`${apiBase()}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (error) {
    throw new ApiError(`无法连接 AMKR 服务: ${(error as Error).message}`, 0, null);
  }
  if (response.status === 204) return null as T;
  const text = await response.text();
  let payload: unknown = null;
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
  return payload as T;
}

// 干跑开关：写接口带 `dry_run=1` 时跑一遍**完全相同**的改动、回报它连带改掉哪些引用，
// 但一个字节都不落盘。WebUI 用它做二次确认（见 webui/model-impact.js），用户点了头再
// 发一次不带该参数的请求。
//
// 走查询参数而不是请求体：这几个接口的请求体只有 `config_revision` 与本次要写的字段，
// 预演与落盘共用同一份请求体，差别只剩这一个开关——塞进请求体就得为每个接口各加一个
// 字段，而请求体是对外契约（docs/API.md 逐个列字段）。
export interface DryRunOptions {
  dryRun?: boolean;
}

const dryRunQuery = (options?: DryRunOptions): string => (options?.dryRun ? "?dry_run=1" : "");

// UNIFIED_PLAN_KEYS 是 GET/PUT /api/unified-model 的七个计划键，顺序与服务端
// config.UnifiedPlanNames 一致（default 必填，其余可选，未配置的族不出现/传 null）。
const UNIFIED_PLAN_KEYS = ["default", "image", "embeddings", "speech", "transcriptions", "video", "rerank"];

// UNIFIED_LEGACY_KEYS 是 PUT 的第二条分支（只切换目标、不动其它族）用的旧字段，
// 仍然原样透传：它们是已发布的请求形状，删掉会打断既有调用方。
const UNIFIED_LEGACY_KEYS = ["model", "key", "image_model", "image_key"];

// appendFilterParams 把筛选维度写进查询串。
//
// 维度内多选靠**同名参数重复出现**表达（后端收成 IN 列表），维度间是 AND——所以这里
// 只能用 append 不能用 set：set 会把同一维度的第二项覆盖掉，多选会静默退化成单选。
// 顺序固定（供应商、访问密钥、模型、上游 Key、工作空间、调用方、成败）：URL 因此可读，
// 探针也能逐项读参数而不是靠子串猜。
export function appendFilterParams(params: URLSearchParams, filters: MetricFilters = {}): URLSearchParams {
  const {
    providerIds = [], accessKeyIds = [], modelIds = [], upstreamKeys = [],
    workspaces = [], callerTypes = [], success = null,
  } = filters;
  for (const id of providerIds) params.append("provider_id", id);
  for (const id of accessKeyIds) params.append("access_key_id", id);
  for (const id of modelIds) params.append("model_id", id);
  for (const key of upstreamKeys) {
    // 成对值用 JSON 数组：供应商 ID 与 Key 名都可能包含任意分隔符，只有 JSON 能
    // 无歧义地表达两个字符串；服务端按 [provider, key] 解析。
    params.append("upstream_key", JSON.stringify([key.providerId, key.keyName]));
  }
  for (const workspace of workspaces) params.append("workspace", workspace);
  for (const callerType of callerTypes) params.append("caller_type", callerType);
  if (success !== null && success !== undefined) params.append("success", String(success));
  return params;
}

export type BreakdownGroupBy =
  | "model"
  | "requested_model"
  | "provider"
  | "upstream_model"
  | "upstream_key"
  | "access_key"
  | "workspace"
  | "caller_type"
  | "status"
  | "api_format";

export interface MetricDimensionsOptions {
  hours?: number;
  allHistory?: boolean;
}

export interface UsageBreakdownOptions extends MetricFilters {
  groupBy: BreakdownGroupBy;
  rankBy?: "requests" | "tokens";
  limit?: number;
  hours?: number;
  allHistory?: boolean;
  bucketSeconds?: number;
}

export interface KeyUsageOptions extends MetricFilters {
  hours?: number;
  allHistory?: boolean;
}

export interface FilteredMetricsOptions extends MetricFilters {
  hours?: number;
  allHistory?: boolean;
  bucketSeconds?: number;
  limit?: number;
}

export interface StatusMonitorOptions extends MetricFilters {
  hours?: number;
  groupBy?: "provider" | "provider_model" | "provider_key";
  bucketSeconds?: number;
}

export const api = {
  health: (): Promise<HealthResponse> => request("/health", { auth: false }),
  metrics: (hours = 1): Promise<MetricsSnapshot> => request(`/metrics?hours=${hours}`),
  series: (hours = 1, bucketSeconds = 60): Promise<SeriesResponse> =>
    request(`/metrics/series?hours=${hours}&bucket_seconds=${bucketSeconds}`),
  // 逐条请求明细：/metrics/requests 的 hours 上限是 720，limit 上限是 200。
  requests: ({ hours = 1, limit = 50, ...filters }: { hours?: number; limit?: number } & Record<string, unknown> = {}): Promise<RequestListResponse> => {
    const params = new URLSearchParams({ hours: String(hours), limit: String(limit) });
    for (const [key, value] of Object.entries(filters)) {
      if (value !== null && value !== undefined && value !== "") params.set(key, String(value));
    }
    return request(`/metrics/requests?${params}`);
  },
  logs: (): Promise<LogsResponse> => request("/api/logs"),

  // 工作空间用量读数：挂在 /ui/ 下（不占用 /api 那份已发布路由清单），但要完整鉴权
  // ——内容会暴露各空间的用量与模型流向。
  workspaceUsage: ({ hours = 24, allHistory = false }: { hours?: number; allHistory?: boolean } = {}): Promise<WorkspaceUsageResponse> =>
    request(allHistory
      ? "/ui/workspace-usage.json?all_history=true"
      : `/ui/workspace-usage.json?hours=${hours}`),

  // Key 用量读数：「这些流量是哪把 Key 出去的」（上游 Key + 访问密钥两份拆分）。
  // 同样挂 /ui/ 且要完整鉴权：/metrics 是**对照参照实现**的读数，形状已发布不能加
  // 字段，所以这份按 Key 拆分的读数是单独一条本项目自有的接口。
  keyUsage: ({ hours = 24, allHistory = false, ...filters }: KeyUsageOptions = {}): Promise<KeyUsageResponse> => {
    const params = new URLSearchParams(allHistory ? { all_history: "true" } : { hours: String(hours) });
    // 筛选生效时三份拆分一起收窄（上游分布卡里的「按上游 Key」也要跟着走）。同一个参数
    // 重复出现 = 维度内多选（后端按 IN 处理），两个维度同时出现则是 AND。
    appendFilterParams(params, filters);
    return request(`/ui/key-usage.json?${params}`);
  },

  // 按筛选条件收窄的整页读数：快照 + 序列（可选逐条明细）一次取回，三者共用同一个
  // 时间窗口，同屏数字因此自洽。为什么不用 /metrics 系列：那是**对照参照实现**的读数，
  // 形状与查询参数都已发布，不能给它加过滤条件（见 docs/API.md「新增能力挂在
  // /api/ 还是 /ui/」）。limit=0 表示不要明细（用量统计页只画图与表）。
  filteredMetrics: ({
    hours = 24, allHistory = false, bucketSeconds = 60, limit = 0, ...filters
  }: FilteredMetricsOptions = {}): Promise<FilteredMetricsResponse> => {
    const params = new URLSearchParams();
    appendFilterParams(params, filters);
    params.set("bucket_seconds", String(bucketSeconds));
    if (allHistory) params.set("all_history", "true");
    else params.set("hours", String(hours));
    if (limit > 0) params.set("limit", String(limit));
    return request(`/ui/filtered-metrics.json?${params}`);
  },

  // 状态监控读数：KPI 快照 + 趋势序列 + 渠道健康矩阵 + 错误归因一次取回，共用同一个
  // 时间窗与筛选条件，同屏数字因此自洽。挂在 /ui/ 下且要完整鉴权：它是本项目自有的
  // 聚合读数（/metrics 系列是与参照实现对齐的已发布形状，不能加 group_by 这类参数）。
  //
  // group_by 决定矩阵行的粒度（provider / provider_model / provider_key），切换它要
  // 重新请求而不是本地重排——行是由服务端聚合出来的。
  statusMonitor: ({
    hours = 24, groupBy = "provider", bucketSeconds = 0, ...filters
  }: StatusMonitorOptions = {}): Promise<StatusMonitorResponse> => {
    const params = new URLSearchParams({ hours: String(hours), group_by: groupBy });
    // bucket_seconds 可选：状态页矩阵固定 15 分钟桶（12 小时 = 49 列），显式传入后
    // 后端不再按窗口自动挑桶；KPI/趋势那次请求仍不带它，保持自动桶。
    if (bucketSeconds > 0) params.set("bucket_seconds", String(bucketSeconds));
    appendFilterParams(params, filters);
    return request(`/ui/status-monitor.json?${params}`);
  },

  // 筛选器选项：窗口内实际出现过的各维度取值与请求数。**不接受筛选条件**——
  // 选项若跟着当前条件收窄，用户选了模型 A 之后模型 B 就会从列表里消失，筛选器
  // 会把自己锁死。
  metricDimensions: ({ hours = 24, allHistory = false }: MetricDimensionsOptions = {}): Promise<MetricDimensionsResponse> => {
    const params = new URLSearchParams(allHistory ? { all_history: "true" } : { hours: String(hours) });
    return request(`/ui/metric-dimensions.json?${params}`);
  },

  // 按一个维度拆分的时间序列：拆分趋势、成本趋势、行内 Sparkline 共用。
  usageBreakdown: ({
    groupBy, rankBy = "requests", limit = 8, hours = 24, allHistory = false, bucketSeconds = 0, ...filters
  }: UsageBreakdownOptions): Promise<UsageBreakdownResponse> => {
    const params = new URLSearchParams({
      group_by: groupBy,
      rank_by: rankBy,
      limit: String(limit),
    });
    if (bucketSeconds > 0) params.set("bucket_seconds", String(bucketSeconds));
    if (allHistory) params.set("all_history", "true");
    else params.set("hours", String(hours));
    appendFilterParams(params, filters);
    return request(`/ui/usage-breakdown.json?${params}`);
  },

  // 价格目录（models.dev）：由服务端缓存并定期刷新，挂在 WebUI 前缀下。
  // **不鉴权**——内容是 models.dev 的公开数据，静态资源本身也是公开的。
  // 服务端还没取到目录时回 503，由 webui/pricing.js 吞掉并降级成"无定价"。
  pricing: (): Promise<PricingResponse> => request("/ui/pricing.json", { auth: false }),

  // 模型类型读数（文本 / 图像 / 视频 / 语音 / 嵌入 / 重排）：与价格目录同源（同一份
  // models.dev 文档 + 名字规则），同样不鉴权。
  //
  // **判定在服务端做**（internal/modelkind），这里只负责把名字分块问回来再合并：
  // 一次问几百个名字会把请求行撑到几 KB，而服务端单次上限是 200。目录还没取回来时
  // 服务端仍然回 200（只有名字规则的结论），因此这里不把 catalog_available 为假
  // 当成失败——界面用 false 提示"分类依据仅名字规则"。
  modelKinds: async (names: string[]): Promise<ModelKindsResponse> => {
    const wanted = [...new Set((names || []).map((name) => String(name).trim()).filter(Boolean))];
    if (!wanted.length) return { catalog_available: false, models: {} };
    const pages = await Promise.all(chunkModelIDs(wanted).map((chunk) =>
      request<ModelKindsResponse>(`/ui/model-kinds.json?${chunk.map((name) => `model=${encodeURIComponent(name)}`).join("&")}`,
        { auth: false })));
    return {
      catalog_available: pages.every((page) => page?.catalog_available === true),
      models: Object.assign({}, ...pages.map((page) => page?.models || {})),
    };
  },

  tool: (): Promise<ToolResponse> => request("/api/tool"),
  setWebui: (enabled: boolean): Promise<ConfigRevisionResponse> => request("/api/tool/webui", { method: "POST", body: { enabled } }),
  runService: (action: string): Promise<ServiceActionResponse> => request(`/api/service/${action}`, { method: "POST" }),

  integrations: (): Promise<IntegrationsResponse> => request("/api/integrations"),
  // 集成没有「路由模式」参数：服务端一律写入 AMKR 当前可用的全部模型。
  applyIntegration: (agent: string): Promise<ServiceActionResponse> => request(`/api/integrations/${agent}`, { method: "POST" }),
  rollbackIntegration: (agent: string): Promise<ServiceActionResponse> =>
    request(`/api/integrations/${agent}/rollback`, { method: "POST" }),

  settings: (): Promise<SettingsResponse> => request("/api/settings"),
  updateSettings: (revision: string, settings: Record<string, unknown>): Promise<SettingsResponse> =>
    request("/api/settings", { method: "PUT", body: { config_revision: revision, ...settings } }),
  regenerateLocalKey: (revision: string): Promise<SettingsResponse> =>
    request("/api/settings/local-api-key", { method: "POST", body: { config_revision: revision } }),
  checkUpdate: (): Promise<UpdateStatusResponse> => request("/api/update/check", { method: "POST" }),

  // 自更新：与 pricing 一样挂在 /ui/ 前缀下（不占用 47+7 条 /api 路由）。
  // 但**必须鉴权**——替换可执行文件是本服务最特权的操作，服务端要求完整权限，受限的
  // 推理凭据会被拒。status 是公开的（只回答"这个构建有没有自更新能力"），且不鉴权才能
  // 在任何情况下都正确决定按钮是否显示。
  updateStatus: (): Promise<UpdateStatusResponse> => request("/ui/update/status", { auth: false }),
  applyUpdate: (): Promise<UpdateStatusResponse> => request("/ui/update/apply", { method: "POST" }),

  exportConfig: (): Promise<ConfigExportResponse> => request("/api/config/export", { method: "POST" }),
  importConfig: (revision: string, config: unknown): Promise<ConfigRevisionResponse> =>
    request("/api/config/import", { method: "POST", body: { config_revision: revision, config } }),

  providers: (): Promise<ProvidersResponse> => request("/api/providers"),
  createProvider: (revision: string, id: string, baseUrl: string): Promise<Provider> =>
    request("/api/providers", { method: "POST", body: { config_revision: revision, id, base_url: baseUrl } }),
  // maxConcurrency 是供应商级别的并发上限：null 或 0 都表示不限制，正整数表示上限。
  //
  // 这里**总是**把 max_concurrency 发出去（省略时补成 null），与其它按需字段不同：
  // 服务端对 PUT 区分「省略 = 不改」与「显式 null = 清除」，表单一律带这个键就让
  // 请求体的形状与「用户这次改没改上限」无关——清上限的唯一动作是显式 null，
  // 少发一次字段就等于静默保留旧值。
  updateProvider: (
    revision: string, providerId: string, id: string, baseUrl: string,
    routes: Record<string, string>, maxConcurrency: number | null = null,
  ): Promise<Provider> =>
    request(`/api/providers/${encodeURIComponent(providerId)}`, {
      method: "PUT",
      body: {
        config_revision: revision,
        id,
        base_url: baseUrl,
        routes,
        max_concurrency: maxConcurrency ?? null,
      },
    }),
  deleteProvider: (revision: string, providerId: string, options?: DryRunOptions): Promise<ConfigRevisionResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}${dryRunQuery(options)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  // maxConcurrency **只在非 null 时**进请求体：新建 Key 没有"清除上限"这回事（新 Key
  // 本来就没有上限），发一个显式 null 只是多余字段。沿用 updateUnified 的"给什么发什么"。
  createProviderKey: (revision: string, providerId: string, name: string, apiKey: string, maxConcurrency: number | null = null): Promise<ProviderKey> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys`, {
      method: "POST",
      body: {
        config_revision: revision,
        name,
        api_key: apiKey,
        ...(maxConcurrency === null || maxConcurrency === undefined ? {} : { max_concurrency: maxConcurrency }),
      },
    }),
  // patch 的字段**原样透传**（展开运算符不做任何过滤，JSON.stringify 也保留 null）：
  // `max_concurrency: null` 是"取消这把 Key 的上限"的唯一写法，一旦有人以"少发个空字段
  // 更干净"为由把它筛掉，上限就设上再也取消不掉——而请求照样成功，看起来毫无异样。
  updateProviderKey: (revision: string, providerId: string, keyName: string, patch: ProviderUpdateKeyPatch): Promise<ProviderKey> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}`, {
      method: "PUT",
      body: { config_revision: revision, ...patch },
    }),
  deleteProviderKey: (revision: string, providerId: string, keyName: string, options?: DryRunOptions): Promise<ConfigRevisionResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}${dryRunQuery(options)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  keyModels: (providerId: string, keyName: string): Promise<KeyModelsResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/models`),
  setKeyModels: (revision: string, providerId: string, keyName: string, models: string[], options?: DryRunOptions): Promise<ConfigRevisionResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/models${dryRunQuery(options)}`, {
      method: "PUT",
      body: { config_revision: revision, models },
    }),

  probeProvider: (revision: string, providerId: string): Promise<ProbeResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/probe`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  probeKey: (revision: string, providerId: string, keyName: string): Promise<ProbeResponse> =>
    request(`/api/providers/${encodeURIComponent(providerId)}/keys/${encodeURIComponent(keyName)}/probe`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  startProbe: (providerId: string, keys: string[], timeoutSeconds?: number): Promise<ProbeResponse> =>
    request("/api/probes/keys", {
      method: "POST",
      body: { provider_id: providerId, keys, timeout_seconds: timeoutSeconds },
    }),
  getProbe: (probeId: string): Promise<ProbeResponse> => request(`/api/probes/${encodeURIComponent(probeId)}`),
  cancelProbe: (probeId: string): Promise<ProbeResponse> =>
    request(`/api/probes/${encodeURIComponent(probeId)}/cancel`, { method: "POST" }),

  routes: (): Promise<RoutesResponse> => request("/api/routes"),
  // endpoints 是「端点族 → 计划」的覆盖表（见 pages/endpoint-plans.js）。它**只在显式
  // 传入时**才出现在请求体里：不传 = 服务端不动这张表；传 null 或 {} = 清空它。搬目标
  // 这类只动 targets 的调用因此不会顺手抹掉别的模型配好的端点计划。
  createRoute: (
    revision: string, id: string, targets: RouteTarget[], aliases: string[], routingMode: string,
    endpoints?: unknown,
  ): Promise<RouteEntry> => {
    const body: Record<string, unknown> = { config_revision: revision, id, targets, aliases, routing_mode: routingMode };
    if (endpoints !== undefined) body.endpoints = endpoints;
    return request("/api/routes", { method: "POST", body });
  },
  // newId 非空时同时改名（服务端会把引用它的名字一起改：unified_model、模型的端点计划、
  // 任务、工作空间与访问密钥清单）。
  updateRoute: (
    revision: string, routeId: string, targets: RouteTarget[], aliases: string[], routingMode: string,
    newId: string | null = null, endpoints?: unknown,
  ): Promise<RouteEntry> => {
    const body: Record<string, unknown> = { config_revision: revision, targets, aliases, routing_mode: routingMode };
    // 历史原因：v6.9.1 的 RouteUpdate 把 `id: null` 当成改名目标，直接 500。后端现已
    // 修复（null 等同不改名），这里仍保留「未改名时省略 id」：新旧服务端都安全，也避免
    // 让 `id` 在无改名时成为一个语义含糊的字段。
    if (newId !== null) body.id = newId;
    if (endpoints !== undefined) body.endpoints = endpoints;
    return request(`/api/routes/${encodeURIComponent(routeId)}`, { method: "PUT", body });
  },
  deleteRoute: (revision: string, routeId: string, options?: DryRunOptions): Promise<ConfigRevisionResponse> =>
    request(`/api/routes/${encodeURIComponent(routeId)}${dryRunQuery(options)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  models: (): Promise<ModelsResponse> => request("/api/models"),
  updateModelEffort: (revision: string, modelId: string, effort: string | null): Promise<ModelSummaryResponse> =>
    request(`/api/models/${encodeURIComponent(modelId)}`, {
      method: "PUT",
      body: { config_revision: revision, reasoning_effort: effort },
    }),

  unified: (): Promise<UnifiedModel> => request("/api/unified-model"),
  // 七个计划键 + 四个遗留字段（model/key/image_model/image_key 是「只切换目标」那条
  // 分支用的旧形状，仍要能用）。
  //
  // 这里**只透传调用方真正给了的键**：给什么发什么，没给的不发。这样老的调用方
  // （只传 default/image/embeddings）发出去的请求体与改动前逐字节相同，而统一模型页
  // 会把七个键写全（未配置的族是 null，表示清空该族）。
  updateUnified: (revision: string, unified: Record<string, unknown> = {}): Promise<UnifiedModel> => {
    const body: Record<string, unknown> = { config_revision: revision };
    for (const key of UNIFIED_PLAN_KEYS) {
      if (key in unified) body[key] = unified[key] ?? null;
    }
    for (const key of UNIFIED_LEGACY_KEYS) {
      if (key in unified) body[key] = unified[key];
    }
    return request("/api/unified-model", { method: "PUT", body });
  },
  deleteUnified: (revision: string): Promise<ConfigRevisionResponse> =>
    request("/api/unified-model", { method: "DELETE", body: { config_revision: revision } }),

  tasks: (workspace?: string): Promise<TasksResponse> => request("/api/tasks", { workspace }),
  createTask: (revision: string, workspace: string | undefined, payload: TaskPayload): Promise<Task> =>
    request("/api/tasks", { method: "POST", workspace, body: { config_revision: revision, ...payload } }),
  updateTask: (revision: string, workspace: string | undefined, taskName: string, payload: TaskPayload): Promise<Task> =>
    request(`/api/tasks/${encodeURIComponent(taskName)}`, {
      method: "PUT",
      workspace,
      body: { config_revision: revision, ...payload },
    }),
  deleteTask: (revision: string, workspace: string | undefined, taskName: string): Promise<ConfigRevisionResponse> =>
    request(`/api/tasks/${encodeURIComponent(taskName)}`, {
      method: "DELETE",
      workspace,
      body: { config_revision: revision },
    }),

  // 工作空间目录：列出有任务的工作空间及各自任务数，供任务页填充切换下拉。
  // GET /api/tasks 是按空间过滤的，因此从任务列表推不出「还有哪些空间」。
  workspaces: (): Promise<WorkspacesResponse> => request("/api/workspaces"),
  // 显式建空间：应用侧集成时先建空间拿一次 api_key，之后才往里填任务。
  // 响应是**唯一**出现明文 key 的地方（目录接口刻意不含它）。
  createWorkspace: (revision: string, name: string, apiKey?: string): Promise<WorkspaceCreateResponse> =>
    request("/api/workspaces", {
      method: "POST",
      body: apiKey
        ? { config_revision: revision, name, api_key: apiKey }
        : { config_revision: revision, name },
    }),

  // 改名会把整组任务搬到新名字下；删除连同组内任务一起删。
  renameWorkspace: (revision: string, workspace: string, name: string): Promise<WorkspaceSummary> =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}`, {
      method: "PUT",
      body: { config_revision: revision, name },
    }),
  deleteWorkspace: (revision: string, workspace: string): Promise<ConfigRevisionResponse> =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),
  // 换一把推理 key。旧 key 立刻失效，新明文只在这条响应里出现一次。
  // 与面板 key 分开：面板 key 换掉会让已嵌入的页面立刻失效，两者轮换节奏不同。
  rotateInferenceKey: (revision: string, workspace: string): Promise<WorkspaceCreateResponse> =>
    request(`/api/workspaces/${encodeURIComponent(workspace)}/inference-key`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  // 设定本空间**允许直呼**的模型清单：null = 不限制，[] = 一个都不许（只走任务名）。
  setWorkspaceModels: (revision: string, workspace: string, models: string[] | null): Promise<WorkspaceSummary> =>
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
  accessKeys: (): Promise<AccessKeysResponse> => request("/api/access-keys"),
  // 响应是**唯一**出现明文 key 的两个时机之一（另一个是轮换）。
  createAccessKey: (revision: string, name: string, options: AccessKeyCreateOptions = {}): Promise<AccessKeyCreateResponse> =>
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
  updateAccessKey: (revision: string, keyId: string, options: AccessKeyUpdateOptions = {}): Promise<AccessKeyEntry> =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}`, {
      method: "PUT",
      body: {
        config_revision: revision,
        ...options,
      },
    }),
  // 换一把新 key。旧 key 立刻失效，新明文只在这条响应里出现一次。
  rotateAccessKey: (revision: string, keyId: string): Promise<AccessKeyCreateResponse> =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}/rotate`, {
      method: "POST",
      body: { config_revision: revision },
    }),
  deleteAccessKey: (revision: string, keyId: string): Promise<ConfigRevisionResponse> =>
    request(`/api/access-keys/${encodeURIComponent(keyId)}`, {
      method: "DELETE",
      body: { config_revision: revision },
    }),

  // CPA 账号资源：实例清单（含 management_key——读写都要求完整管理权限）与账号额度。
  //
  // 额度那一份是**服务端扇出**的结果：浏览器绝不直接访问 CPA（不同源、没有 CORS 头，
  // 而且 management_key 不该长期留在页面里），见 internal/api/handlers_cpa.go。
  cpaInstances: (): Promise<CPAInstancesResponse> => request("/api/cpa-instances"),
  saveCPAInstances: (revision: string, instances: Record<string, unknown> | unknown[]): Promise<CPAInstancesResponse> =>
    request("/api/cpa-instances", {
      method: "PUT",
      body: { config_revision: revision, instances },
    }),
  cpaAccounts: (): Promise<CPAAccountsResponse> => request("/api/cpa-accounts"),

  // 额度冻结：窗口硬性耗尽后自动摘出选路的 Key 列表，以及手工解冻。
  //
  // GET 回的是**本地状态快照**（冻结表 + 巡检状态），不会扇出到 CPA/上游，可以放心随页面读；
  // DELETE 只解除本次冻结并给该 Key 一个抑制窗口（否则下一轮巡检会立刻冻回去），不写配置、
  // 不写上游，所以没有 config_revision。
  quotaFreezes: (): Promise<QuotaFreezesResponse> => request("/api/quota-freezes"),
  releaseQuotaFreeze: (providerId: string, keyName: string): Promise<QuotaFreezeReleaseResponse> =>
    request(`/api/quota-freezes/${encodeURIComponent(providerId)}/${encodeURIComponent(keyName)}`, {
      method: "DELETE",
    }),

  // CPA 重置卡：GET 会真实访问上游且有限流，只在用户点「查看重置卡」时调用；POST 会消费
  // 一张卡并改变账号状态，必须由页面先做危险确认。404 表示当前构建没有该能力。
  cpaResetCredits: (): Promise<CPAResetCreditsResponse> => request("/api/cpa-reset-credits"),
  // 兼容页面/测试里的短名称；两者完全等价。
  resetCredits: (): Promise<CPAResetCreditsResponse> => request("/api/cpa-reset-credits"),
  // 既接受对象（页面用 camelCase），也接受位置参数（旧的调用习惯）；body 始终按 API 的
  // snake_case 发出。保留 auth_index / credit_id 的原样写法，避免调用方为了适配再包一层。
  useCpaResetCredit: (
    optionsOrInstance: { instance: string; authIndex?: string; auth_index?: string; creditId?: string; credit_id?: string; requestId?: string; request_id?: string } | string,
    authIndex?: string,
    creditId?: string,
  ): Promise<CPAResetCreditUseResponse> => {
    const options = typeof optionsOrInstance === "string"
      ? { instance: optionsOrInstance, authIndex, creditId }
      : optionsOrInstance;
    return request("/api/cpa-reset-credits/use", {
      method: "POST",
      body: {
        instance: options.instance,
        auth_index: options.authIndex ?? options.auth_index ?? "",
        ...(options.creditId ?? options.credit_id ? { credit_id: options.creditId ?? options.credit_id } : {}),
        ...(options.requestId ?? options.request_id ? { request_id: options.requestId ?? options.request_id } : {}),
      },
    });
  },
  useResetCredit: (
    optionsOrInstance: { instance: string; authIndex?: string; auth_index?: string; creditId?: string; credit_id?: string; requestId?: string; request_id?: string } | string,
    authIndex?: string,
    creditId?: string,
  ): Promise<CPAResetCreditUseResponse> => {
    const options = typeof optionsOrInstance === "string"
      ? { instance: optionsOrInstance, authIndex, creditId }
      : optionsOrInstance;
    return request("/api/cpa-reset-credits/use", {
      method: "POST",
      body: {
        instance: options.instance,
        auth_index: options.authIndex ?? options.auth_index ?? "",
        ...(options.creditId ?? options.credit_id ? { credit_id: options.creditId ?? options.credit_id } : {}),
        ...(options.requestId ?? options.request_id ? { request_id: options.requestId ?? options.request_id } : {}),
      },
    });
  },

  // —— 试验场（Playground）推理接口 ——
  v1Models: (workspace?: string): Promise<V1ModelsResponse> => request("/v1/models", { workspace }),

  // 对话补全（支持流式 SSE 与非流式）
  chatCompletions: async ({
    body, workspace, signal, onChunk,
  }: {
    body: ChatCompletionRequest;
    workspace?: string;
    signal?: AbortSignal;
    onChunk?: (chunk: ChatStreamChunk) => void;
  }): Promise<ChatResult> => {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${getKey()}`,
    };
    if (workspace) headers["X-AMKR-Workspace"] = workspace;
    const now = () => (typeof performance !== "undefined" && performance.now ? performance.now() : Date.now());
    const startTime = now();
    let ttft: number | null = null;
    let response: Response;
    try {
      response = await fetch(`${apiBase()}/v1/chat/completions`, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal,
      });
    } catch (error) {
      if (signal?.aborted) throw error;
      throw new ApiError(`请求失败: ${(error as Error).message}`, 0, null);
    }

    if (!response.ok) {
      const text = await response.text();
      let payload: unknown = null;
      try { payload = JSON.parse(text); } catch { payload = text; }
      const detail = detailText(payload, response.status);
      if (response.status === 401 && unauthorizedHandler) unauthorizedHandler();
      throw new ApiError(
        (payload as { error?: { message?: string } } | null)?.error?.message || `HTTP ${response.status}: ${detail}`,
        response.status,
        payload as string | null,
      );
    }

    const isStream = !!body?.stream;
    if (!isStream) {
      const result = (await response.json()) as {
        choices?: Array<{ message?: { role?: string; content?: string; reasoning_content?: string; reasoning?: string } }>;
        usage?: ChatUsage;
      };
      const duration = Math.round(now() - startTime);
      const choice = result.choices?.[0];
      const message = choice?.message || {};
      return {
        message: {
          role: message.role || "assistant",
          content: message.content || "",
          reasoning_content: message.reasoning_content || message.reasoning || "",
        },
        usage: result.usage ?? null,
        duration,
        ttft: duration,
        raw: result,
      };
    }

    // SSE 流式解析
    if (!response.body) throw new ApiError("上游没有返回流式响应体", response.status, null);
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let accumulatedContent = "";
    let accumulatedReasoning = "";
    let finalUsage: ChatUsage | null = null;
    let lastChunk: unknown = null;

    const parsedChunk = (value: unknown) => value as {
      usage?: ChatUsage;
      choices?: Array<{ delta?: ChatChunkDelta }>;
    };

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        const chunkText = decoder.decode(value, { stream: true });
        buffer += chunkText;
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        for (const line of lines) {
          const trimmed = line.trim();
          if (!trimmed || !trimmed.startsWith("data:")) continue;
          const dataStr = trimmed.slice(5).trim();
          if (dataStr === "[DONE]") continue;

          let parsed: ReturnType<typeof parsedChunk> | null = null;
          try {
            parsed = parsedChunk(JSON.parse(dataStr));
          } catch {
            continue;
          }
          lastChunk = parsed;
          if (parsed.usage) finalUsage = parsed.usage;
          const delta = parsed.choices?.[0]?.delta;
          if (!delta) continue;
          // 只带 role 的首包没有增量内容，不应制造一条空回调；下游 UI 会把它
          // 当成一次真实输出，既多一次重渲染，也让“首个分片”的计数失真。
          const deltaContent = delta.content || "";
          const deltaReasoning = delta.reasoning_content || delta.reasoning || "";
          if (!deltaContent && !deltaReasoning && !parsed.usage) continue;

          if (ttft === null) {
            ttft = Math.round(now() - startTime);
          }

          accumulatedReasoning += deltaReasoning;
          accumulatedContent += deltaContent;

          if (onChunk) {
            onChunk({
              content: accumulatedContent,
              reasoning: accumulatedReasoning,
              deltaContent,
              deltaReasoning,
              usage: finalUsage,
              raw: parsed,
            });
          }
        }
      }
    } catch (err) {
      if (signal?.aborted) {
        const duration = Math.round(now() - startTime);
        return {
          message: {
            role: "assistant",
            content: accumulatedContent,
            reasoning_content: accumulatedReasoning,
          },
          usage: finalUsage,
          duration,
          ttft: ttft ?? duration,
          raw: lastChunk,
          aborted: true,
        };
      }
      throw err;
    }

    const duration = Math.round(now() - startTime);
    return {
      message: {
        role: "assistant",
        content: accumulatedContent,
        reasoning_content: accumulatedReasoning,
      },
      usage: finalUsage,
      duration,
      ttft: ttft ?? duration,
      raw: lastChunk,
      aborted: false,
    };
  },

  // 图像生成
  imageGenerations: async ({
    body, workspace, signal,
  }: {
    body: ImageGenerationRequest;
    workspace?: string;
    signal?: AbortSignal;
  }): Promise<ImageGenerationResult> => {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Authorization: `Bearer ${getKey()}`,
    };
    if (workspace) headers["X-AMKR-Workspace"] = workspace;
    const now = () => (typeof performance !== "undefined" && performance.now ? performance.now() : Date.now());
    const startTime = now();
    let response: Response;
    try {
      response = await fetch(`${apiBase()}/v1/images/generations`, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal,
      });
    } catch (error) {
      if (signal?.aborted) throw error;
      throw new ApiError(`请求失败: ${(error as Error).message}`, 0, null);
    }

    const text = await response.text();
    let payload: unknown = null;
    try { payload = JSON.parse(text); } catch { payload = text; }
    if (!response.ok) {
      const detail = detailText(payload, response.status);
      if (response.status === 401 && unauthorizedHandler) unauthorizedHandler();
      throw new ApiError(
        (payload as { error?: { message?: string } } | null)?.error?.message || `HTTP ${response.status}: ${detail}`,
        response.status,
        payload as string | null,
      );
    }
    const duration = Math.round(now() - startTime);
    const result = payload as { data?: Array<Record<string, unknown>>; created?: number } | null;
    return {
      data: result?.data || [],
      created: result?.created,
      duration,
      raw: payload,
    };
  },
};

// 供页面按需引用：响应里各拆分行的类型。
export type { AccessKeyUsageRow, BreakdownGroup, MetricFilters, MetricSeriesPoint };
