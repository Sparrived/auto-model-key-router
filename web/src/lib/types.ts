// 与 AMKR 服务通信的数据形状。
//
// 字段以 docs/API.md 为准；本项目不消费、但服务端会返回的字段一律标成可选，
// 避免把"文档没列全"变成类型谎言。聚合类响应的 stats 与 /metrics 的 total 同形。

export interface WindowInfo {
  from: string | null;
  to: string;
  hours: number | null;
}

export interface MetricStats {
  requests: number;
  successes?: number;
  failures?: number;
  retries?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  total_duration_ms?: number;
  total_first_token_ms?: number;
  status_codes?: Record<string, number>;
  [key: string]: unknown;
}

export interface HealthResponse {
  status?: string;
  version?: string;
  commit?: string;
  build_time?: string;
  local_auth_enabled?: boolean;
  local_api_key_fingerprint?: string;
  webui_available?: boolean;
  webui_enabled?: boolean;
  webui_mounted?: boolean;
  webui_path?: string;
  base_url?: string;
  [key: string]: unknown;
}

export interface MetricsSnapshot {
  count_semantics?: string;
  window: WindowInfo;
  started_at?: string;
  database_path?: string;
  rate_window_seconds?: number;
  current_rpm?: number;
  current_tpm?: number;
  active_requests?: number;
  total: MetricStats;
  caller_types?: Record<string, MetricStats>;
  models?: Record<string, MetricStats>;
  requested_models?: Record<string, MetricStats>;
  model_requested_models?: Record<string, MetricStats>;
  providers?: Record<string, MetricStats>;
  provider_pools?: Record<string, unknown>;
  upstream_models?: Record<string, MetricStats>;
  access_keys?: Record<string, MetricStats>;
  workspaces?: Record<string, MetricStats>;
  tasks?: Record<string, MetricStats>;
  keys?: Record<string, Record<string, MetricStats>>;
  unattributed?: MetricStats;
  [key: string]: unknown;
}

export interface MetricSeriesPoint extends MetricStats {
  started_at: string;
  ended_at: string;
  complete: boolean;
}

export interface SeriesResponse {
  bucket_seconds: number;
  points: MetricSeriesPoint[];
}

export interface RequestItem extends MetricStats {
  id: number;
  created_at: string;
  model_id?: string | null;
  requested_model_id?: string | null;
  provider_id?: string | null;
  key_name?: string | null;
  upstream_model_id?: string | null;
  caller_type?: string | null;
  stream?: boolean;
  api_format?: string | null;
  success?: boolean;
  status_code?: number | null;
  access_key_id?: string | null;
  access_key_name?: string | null;
  [key: string]: unknown;
}

export interface RequestListResponse {
  items: RequestItem[];
  next_before_id: number | null;
  total_items?: number;
}

export interface LogsResponse {
  lines?: string[];
  /** 服务端返回的日志尾部原文；lines 只用于兼容旧调用方。 */
  text?: string;
  truncated?: boolean;
  path?: string;
  error?: string | null;
  [key: string]: unknown;
}

export interface KeyUsageRow {
  provider_id: string;
  key_name: string;
  stats: MetricStats;
}

export interface ModelKeyUsageRow extends KeyUsageRow {
  model_id: string;
}

export interface AccessKeyUsageRow {
  access_key_id: string;
  access_key_name: string | null;
  stats: MetricStats;
}

export interface KeyUsageResponse {
  count_semantics?: string;
  window: WindowInfo;
  upstream_keys: KeyUsageRow[];
  model_keys: ModelKeyUsageRow[];
  access_keys: AccessKeyUsageRow[];
  unattributed: MetricStats;
}

export interface WorkspaceUsageLink {
  source_layer: number;
  target_layer: number;
  source: string;
  target: string;
  requests: number;
  total_tokens: number;
  [key: string]: unknown;
}

export interface WorkspaceUsageWorkspace {
  name: string;
  stats: MetricStats;
  [key: string]: unknown;
}

export interface WorkspaceUsageResponse {
  count_semantics?: string;
  window: WindowInfo;
  workspaces?: WorkspaceUsageWorkspace[];
  unattributed?: MetricStats;
  layers?: Array<string | { name?: string; label?: string }>;
  links: WorkspaceUsageLink[];
  [key: string]: unknown;
}

export interface MetricFilters {
  providerIds?: string[];
  accessKeyIds?: string[];
  modelIds?: string[];
  upstreamKeys?: Array<{ providerId: string; keyName: string }>;
  workspaces?: string[];
  callerTypes?: string[];
  success?: boolean | null;
}

export interface MetricFilterEcho {
  provider_ids: string[];
  access_key_ids: string[];
  model_ids?: string[];
  upstream_keys?: Array<{ provider_id: string; key_name: string }>;
  workspaces?: string[];
  caller_types?: string[];
  success?: boolean | null;
}

export interface FilteredMetricsResponse {
  count_semantics?: string;
  window: WindowInfo;
  filters: MetricFilterEcho;
  snapshot: MetricsSnapshot;
  series: SeriesResponse;
  requests: RequestListResponse | null;
}

export interface StatusMatrixRow {
  provider_id: string | null;
  upstream_model_id: string | null;
  key_name: string | null;
  cells: MetricSeriesPoint[];
}

export interface StatusMonitorResponse {
  count_semantics?: string;
  window: WindowInfo;
  filters: MetricFilterEcho;
  group_by: string;
  bucket_seconds: number;
  snapshot: MetricsSnapshot;
  failure_status_codes: Record<string, number>;
  series: SeriesResponse;
  matrix: {
    group_by: string;
    rows: StatusMatrixRow[];
    rows_truncated: number;
  };
}

export interface PricingModelEntry {
  input: number;
  output: number;
  cache_read?: number | null;
  cache_write?: number | null;
}

export interface PricingResponse {
  version?: number;
  source?: string;
  updated_at?: string;
  error?: string | null;
  models: Record<string, PricingModelEntry>;
}

export interface ModelKindEntryPayload {
  kinds?: string[];
  primary?: string;
  endpoints?: string[];
  grounds?: unknown[];
}

export interface ModelKindsResponse {
  version?: number;
  source?: string;
  catalog_available?: boolean;
  models: Record<string, ModelKindEntryPayload>;
}

export interface DimensionOption {
  requests: number;
}

export interface IdDimensionOption extends DimensionOption {
  id: string;
}

export interface AccessKeyDimensionOption extends IdDimensionOption {
  name: string | null;
}

export interface UpstreamKeyDimensionOption extends DimensionOption {
  provider_id: string;
  key_name: string;
}

export interface NameDimensionOption extends DimensionOption {
  name: string;
}

export interface MetricDimensionsResponse {
  window: WindowInfo;
  providers: IdDimensionOption[];
  models: IdDimensionOption[];
  requested_models: IdDimensionOption[];
  upstream_keys: UpstreamKeyDimensionOption[];
  access_keys: AccessKeyDimensionOption[];
  workspaces: NameDimensionOption[];
  caller_types: IdDimensionOption[];
}

export type BreakdownKey =
  | string
  | {
      model_id?: string | null;
      requested_model_id?: string | null;
      provider_id?: string | null;
      key_name?: string | null;
      upstream_model_id?: string | null;
      access_key_id?: string | null;
      access_key_name?: string | null;
      workspace?: string | null;
      caller_type?: string | null;
      status_code?: number | null;
      api_format?: string | null;
      [key: string]: unknown;
    };

export interface BreakdownSeries {
  requests: number[];
  successes: number[];
  failures: number[];
  retries: number[];
  prompt_tokens: number[];
  completion_tokens: number[];
  total_tokens: number[];
  cached_tokens: number[];
  duration_ms: number[];
  first_token_ms: number[];
}

export interface BreakdownGroup {
  key: BreakdownKey;
  stats: MetricStats;
  series: BreakdownSeries;
}

export interface BreakdownBucket {
  started_at: string;
  complete: boolean;
}

export interface UsageBreakdownResponse {
  count_semantics?: string;
  window?: WindowInfo;
  group_by?: string;
  rank_by?: string;
  bucket_seconds?: number;
  filters?: MetricFilterEcho;
  buckets: BreakdownBucket[];
  groups: BreakdownGroup[];
  other: BreakdownGroup | null;
  total_groups: number;
}

// —— 配置面 ——

export interface ConfigRevisionResponse {
  config_revision: string;
  [key: string]: unknown;
}

export interface ProviderKeyCapabilities {
  models?: string[];
  route_status?: Record<string, string>;
  errors?: Record<string, string>;
  checked_at?: string | null;
  [key: string]: unknown;
}

export interface ProviderKey {
  name: string;
  enabled?: boolean;
  api_key_fingerprint?: string;
  max_concurrency?: number | null;
  capabilities?: ProviderKeyCapabilities | null;
  [key: string]: unknown;
}

export interface Provider {
  id: string;
  base_url: string;
  max_concurrency?: number | null;
  keys: ProviderKey[];
  routes?: Record<string, string>;
  [key: string]: unknown;
}

export type ProviderUpdateKeyPatch = {
  name?: string;
  api_key?: string;
  enabled?: boolean;
  max_concurrency?: number | null;
};

export interface ProvidersResponse {
  providers: Provider[];
  config_revision?: string;
}

export interface KeyModelsResponse {
  models?: string[];
  [key: string]: unknown;
}

export interface ProbeResponse {
  probe_id?: string;
  status?: string;
  results?: unknown[];
  [key: string]: unknown;
}

export interface RouteTarget {
  provider: string;
  key: string;
  upstream_model: string;
}

export interface RouteEntry {
  id: string;
  aliases?: string[];
  routing_mode?: string;
  targets: RouteTarget[];
  endpoints?: Record<string, { primary?: { model?: string; key?: string | null } }> | null;
  [key: string]: unknown;
}

export interface RoutesResponse {
  routes: RouteEntry[];
  config_revision?: string;
}

export interface ModelKeyBinding {
  name: string;
  base_url?: string;
  enabled?: boolean;
  api_key_fingerprint?: string;
  max_concurrency?: number | null;
}

export interface ModelSummaryResponse {
  id: string;
  aliases?: string[];
  routing_mode?: string;
  reasoning_effort?: string | null;
  keys?: ModelKeyBinding[];
  endpoints?: Record<string, { primary?: { model?: string; key?: string | null } }> | null;
  [key: string]: unknown;
}

export interface ModelsResponse {
  models: ModelSummaryResponse[];
  config_revision?: string;
}

export interface UnifiedPlan {
  primary?: { model?: string; key?: string | null } | null;
}

export interface UnifiedModel {
  default?: UnifiedPlan | null;
  image?: UnifiedPlan | null;
  embeddings?: UnifiedPlan | null;
  speech?: UnifiedPlan | null;
  transcriptions?: UnifiedPlan | null;
  video?: UnifiedPlan | null;
  rerank?: UnifiedPlan | null;
  model?: string | null;
  key?: string | null;
  image_model?: string | null;
  image_key?: string | null;
  config_revision?: string;
  [key: string]: unknown;
}

export interface TaskParams {
  temperature?: number;
  top_p?: number;
  frequency_penalty?: number;
  presence_penalty?: number;
  top_k?: number;
  seed?: number;
  max_tokens?: number;
  stop?: string[];
  reasoning_effort?: string;
}

export interface Task {
  name: string;
  display_name?: string;
  workspace?: string;
  model?: string;
  fallback_model?: string | null;
  params?: TaskParams;
  [key: string]: unknown;
}

export interface TasksResponse {
  tasks?: Task[];
  config_revision?: string;
  [key: string]: unknown;
}

export interface TaskPayload {
  name?: string;
  model?: string | null;
  display_name?: string | null;
  fallback_model?: string | null;
  params?: TaskParams;
}

export interface WorkspaceSummary {
  name: string;
  /** GET /api/workspaces 返回的任务数；旧字段 tasks 仅为兼容历史数据。 */
  task_count?: number;
  tasks?: number;
  has_inference_key?: boolean;
  models?: string[] | null;
  [key: string]: unknown;
}

export interface WorkspacesResponse {
  workspaces: WorkspaceSummary[];
  config_revision?: string;
  [key: string]: unknown;
}

export interface AccessKeyEntry {
  id: string;
  name: string;
  enabled?: boolean;
  key_fingerprint?: string;
  providers?: string[];
  models?: string[];
  last_used_at?: string | null;
  [key: string]: unknown;
}

export interface AccessKeysResponse {
  access_keys: AccessKeyEntry[];
  config_revision: string;
}

export interface AccessKeyCreateOptions {
  key?: string;
  enabled?: boolean;
  providers?: string[] | null;
  models?: string[] | null;
}

export interface AccessKeyUpdateOptions {
  name?: string | null;
  enabled?: boolean;
  providers?: string[] | null;
  models?: string[] | null;
}

export interface AccessKeyCreateResponse extends AccessKeyEntry {
  key: string;
  config_revision?: string;
}

export interface WorkspaceCreateResponse extends WorkspaceSummary {
  api_key?: string;
  panel_key?: string;
  inference_key?: string;
  [key: string]: unknown;
}

export interface CPAInstance {
  id?: string;
  base_url?: string;
  management_key?: string;
  [key: string]: unknown;
}

export interface CPAInstancesResponse {
  instances: CPAInstance[];
  config_revision?: string;
}

export interface CPAQuotaWindow {
  key?: string;
  label?: string;
  window?: string;
  group?: string;
  remaining?: number;
  reset_at?: string | null;
  source?: string;
  status?: string;
  description?: string;
  [key: string]: unknown;
}

export interface CPASummaryMetric {
  key?: string;
  label?: string;
  value?: number | string;
  unit?: string;
  currency?: string;
  [key: string]: unknown;
}

export interface CPARecentBucket {
  time?: string;
  success?: number;
  failed?: number;
  [key: string]: unknown;
}

// 额度冻结：某个额度窗口硬性耗尽后，对应上游 Key 被临时摘出选路。
//
// 与配置里的 enabled 是两回事：冻结是自动的临时状态（到期或观测到有余额即解除），
// enabled 是人工长期开关。形状见 docs/design/quota-freeze.md 第 6 节。
export interface QuotaFreeze {
  provider_id: string;
  key_name: string;
  /** 厂商/订阅名，供界面显示（Claude、OpenCode Go…）。 */
  vendor?: string;
  /** subscription | cpa_instance | upstream。 */
  scope?: string;
  /** account_resource | upstream_header。 */
  source?: string;
  /** 触发冻结的账号标识（邮箱/文件名等）。 */
  account?: string;
  /** scope=cpa_instance 时非空。 */
  instance_id?: string;
  /** 窗口稳定标识，如 claude/5h。 */
  window_key?: string;
  /** 人话窗口标签，如「5 小时」。 */
  window_label?: string;
  /** 触发时的剩余比例（0..1）。 */
  remaining?: number;
  /** 触发时上游给的窗口状态词（rejected / exceeded…）。 */
  status?: string;
  /** 冻结触发方式：硬性耗尽或低于配置底限；旧后端缺省按 hard 处理。 */
  trigger?: "hard" | "threshold";
  /** trigger=threshold 时命中的底限（0..1）。 */
  threshold?: number;
  /** 额度窗口刷新时间；只用于说明，不作为自动解冻倒计时。 */
  reset_at?: string;
  frozen_at?: string;
  /** 内置计时器的自动解冻时刻（首次写入后只可提前、不会延后）；倒计时以此为准。 */
  expires_at?: string;
  /** 一句人话，界面直接显示。 */
  reason?: string;
  [key: string]: unknown;
}

export interface QuotaFreezesResponse {
  fetched_at?: string;
  /** false = 配置里 quota_freeze_interval_seconds=0，整套机制关闭。 */
  enabled?: boolean;
  interval_seconds?: number;
  last_evaluated_at?: string;
  /** 最近一次评估失败的原因（空串/缺省表示没有失败）。 */
  last_error?: string;
  freezes?: QuotaFreeze[];
  [key: string]: unknown;
}

export interface QuotaGuardStatus {
  enabled?: boolean;
  interval_seconds?: number;
  /** 5h 窗口的提前冻结底限（0..1）；0 表示不按底限提前冻结。 */
  threshold_5h?: number;
  /** 7d 窗口的提前冻结底限（0..1）；0 表示不按底限提前冻结。 */
  threshold_7d?: number;
  last_evaluated_at?: string;
  last_error?: string;
  [key: string]: unknown;
}

export interface QuotaFreezeReleaseResponse {
  released?: boolean;
  provider_id?: string;
  key_name?: string;
  /** 抑制窗口结束时间：在此之前即使读数仍耗尽也不会自动冻回去。 */
  suppressed_until?: string;
  [key: string]: unknown;
}

export interface CPAAccount {
  /** 该账号是否有硬性耗尽的额度窗口。 */
  quota_exhausted?: boolean;
  /** 该账号是否有低于 5h/7d 配置底限的额度窗口。 */
  quota_below_floor?: boolean;
  auth_index?: string | number;
  name?: string;
  label?: string;
  email?: string;
  provider?: string;
  account_type?: string;
  project_id?: string;
  status?: string;
  status_message?: string;
  disabled?: boolean;
  unavailable?: boolean;
  success?: number;
  failed?: number;
  supports_quota?: boolean;
  quota_error?: string;
  plan?: string;
  tier_id?: string;
  windows?: CPAQuotaWindow[];
  summary?: CPASummaryMetric[];
  model_quotas?: Record<string, { windows?: CPAQuotaWindow[]; [key: string]: unknown }>;
  recent_requests?: CPARecentBucket[];
  server_time_offset_ms?: number;
  signals?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface CPAAccountInstance {
  id: string;
  label?: string;
  base_url?: string;
  ok?: boolean;
  observed_at?: string;
  error?: string;
  accounts?: CPAAccount[];
  /** base_url 指向该实例的供应商 ID 列表。 */
  linked_providers?: string[];
  /** 实例内启用账号是否全部硬性耗尽。 */
  all_accounts_exhausted?: boolean;
  /** 实例内启用账号是否全部低于配置的 5h/7d 底限；可与 all_accounts_exhausted 同为 true。 */
  all_accounts_below_floor?: boolean;
  /** 该实例关联供应商下当前被冻结的 Key。 */
  frozen_keys?: Array<{ provider_id: string; key_name: string }>;
  [key: string]: unknown;
}

export interface CPASubscription {
  id?: string;
  kind?: string;
  vendor?: string;
  provider_id?: string;
  key_name?: string;
  base_url?: string;
  ok?: boolean;
  observed_at?: string;
  plan?: string;
  tier_id?: string;
  account?: string;
  status?: string;
  windows?: CPAQuotaWindow[];
  summary?: CPASummaryMetric[];
  signals?: Record<string, unknown>;
  error?: string;
  /** 该订阅出处的那把 Key 当前是否被冻结。 */
  freeze?: QuotaFreeze;
  [key: string]: unknown;
}

export interface CPAAccountsResponse {
  fetched_at?: string;
  /** 全部当前冻结（与 GET /api/quota-freezes 的 freezes 同形）。 */
  freezes?: QuotaFreeze[];
  /** 后台巡检状态。 */
  guard?: QuotaGuardStatus;
  instances?: CPAAccountInstance[];
  subscriptions?: CPASubscription[];
  accounts?: CPAAccount[];
  [key: string]: unknown;
}

export interface CPAResetCredit {
  id?: string;
  program?: string;
  status?: string;
  remaining_count?: number;
  granted_at?: string;
  expires_at?: string | null;
  usable?: boolean;
  [key: string]: unknown;
}

export interface CPAResetCreditsInfo {
  provider?: string;
  available_count?: number;
  redeemable_count?: number;
  eligible?: boolean;
  credits?: CPAResetCredit[];
  next_credit_id?: string | null;
  cooldown_until?: string | null;
  error?: string;
  reason?: string;
  [key: string]: unknown;
}

export interface CPAResetCreditsAccount extends CPAAccount {
  reset_credits?: CPAResetCreditsInfo;
}

export interface CPAResetCreditsInstance {
  id: string;
  label?: string;
  base_url?: string;
  ok?: boolean;
  error?: string;
  accounts?: CPAResetCreditsAccount[];
  [key: string]: unknown;
}

export interface CPAResetCreditsResponse {
  fetched_at?: string;
  instances?: CPAResetCreditsInstance[];
  [key: string]: unknown;
}

export interface CPAResetCreditUseResponse {
  instance?: string;
  auth_index?: string;
  provider?: string;
  credit_id?: string;
  request_id?: string;
  message?: string;
  [key: string]: unknown;
}

export interface V1Model {
  id: string;
  [key: string]: unknown;
}

export interface V1ModelsResponse {
  data?: V1Model[];
  [key: string]: unknown;
}

export interface SettingsData {
  host?: string;
  port?: number;
  max_retries?: number;
  request_timeout?: number;
  stream_first_byte_timeout?: number;
  stream_idle_timeout?: number;
  local_auth_enabled?: boolean;
  local_api_key_fingerprint?: string;
  [key: string]: unknown;
}

export interface SettingsResponse {
  config_revision?: string;
  settings?: SettingsData;
  local_api_key?: string;
  local_api_key_fingerprint?: string;
  [key: string]: unknown;
}

export interface ToolResponse {
  version?: string;
  latest_version?: string;
  update_available?: boolean;
  release_url?: string | null;
  source?: string;
  error?: string | null;
  webui_available?: boolean;
  webui_enabled?: boolean;
  webui_mounted?: boolean;
  webui_path?: string;
  [key: string]: unknown;
}

export interface ServiceActionResponse {
  [key: string]: unknown;
}

export interface IntegrationEntry {
  agent?: string;
  display_name?: string;
  target_path?: string;
  target_exists?: boolean;
  backup_available?: boolean;
  current_is_applied?: boolean;
  error?: string | null;
  [key: string]: unknown;
}

export interface IntegrationsResponse {
  agents?: Array<Record<string, unknown>>;
  integrations?: IntegrationEntry[];
  [key: string]: unknown;
}

export interface UpdateStatusResponse {
  supported?: boolean;
  available?: boolean;
  current_version?: string;
  latest_version?: string;
  version?: string;
  message?: string;
  release_url?: string | null;
  update_available?: boolean;
  [key: string]: unknown;
}

export interface ConfigExportResponse {
  config?: Record<string, unknown>;
  config_revision?: string;
  imported?: boolean;
  added_models?: number;
  added_keys?: number;
  skipped_keys?: number;
  [key: string]: unknown;
}

// —— 推理面 ——

export interface ChatMessage {
  role: string;
  content?: string | null;
  reasoning_content?: string | null;
  [key: string]: unknown;
}

export interface ChatCompletionRequest {
  model?: string;
  messages?: ChatMessage[];
  stream?: boolean;
  [key: string]: unknown;
}

export interface ChatUsage {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  [key: string]: unknown;
}

export interface ChatChunkDelta {
  content?: string;
  reasoning_content?: string;
  reasoning?: string;
}

export interface ChatStreamChunk {
  content: string;
  reasoning: string;
  deltaContent: string;
  deltaReasoning: string;
  usage: ChatUsage | null;
  raw: unknown;
}

export interface ChatResult {
  message: {
    role: string;
    content: string;
    reasoning_content: string;
  };
  usage: ChatUsage | null;
  duration: number;
  ttft: number;
  raw: unknown;
  aborted?: boolean;
}

export interface ImageGenerationRequest {
  prompt?: string;
  [key: string]: unknown;
}

export interface ImageGenerationResult {
  data: Array<Record<string, unknown>>;
  created?: number;
  duration: number;
  raw: unknown;
}
