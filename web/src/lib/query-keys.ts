// React Query 的查询键集中在这里。
//
// 为什么集中：失效（invalidateQueries）必须命中所有观察同一份数据的页面。键散落在各页
// 时，漏掉一处就会出现"保存成功但概览还是旧数字"这类只在切页后才发现的问题。
// 约定：第一段是资源名，第二段起是影响结果的参数；筛选对象整体入键（React Query 会做
// 结构化比较，不必手工序列化）。

import type { FilteredMetricsOptions, KeyUsageOptions, MetricDimensionsOptions, StatusMonitorOptions, UsageBreakdownOptions } from "./api";

export const queryKeys = {
  health: () => ["health"] as const,

  metrics: (hours: number) => ["metrics", hours] as const,
  series: (hours: number, bucketSeconds: number) => ["metrics", "series", hours, bucketSeconds] as const,
  requests: (params: Record<string, unknown>) => ["metrics", "requests", params] as const,
  logs: () => ["logs"] as const,

  workspaceUsage: (options: { hours?: number; allHistory?: boolean }) => ["workspace-usage", options] as const,
  keyUsage: (options: KeyUsageOptions) => ["key-usage", options] as const,
  filteredMetrics: (options: FilteredMetricsOptions) => ["filtered-metrics", options] as const,
  statusMonitor: (options: StatusMonitorOptions) => ["status-monitor", options] as const,
  metricDimensions: (options: MetricDimensionsOptions) => ["metric-dimensions", options] as const,
  usageBreakdown: (options: UsageBreakdownOptions) => ["usage-breakdown", options] as const,

  pricing: () => ["pricing"] as const,
  modelKinds: (names: string[]) => ["model-kinds", [...names].sort()] as const,

  tool: () => ["tool"] as const,
  integrations: () => ["integrations"] as const,
  settings: () => ["settings"] as const,
  updateStatus: () => ["update-status"] as const,

  providers: () => ["config", "providers"] as const,
  provider: (providerId: string) => ["config", "providers", providerId] as const,
  routes: () => ["config", "routes"] as const,
  route: (routeId: string) => ["config", "routes", routeId] as const,
  models: () => ["config", "models"] as const,
  model: (modelId: string) => ["config", "models", modelId] as const,
  unified: () => ["config", "unified"] as const,
  tasks: (workspace?: string) => ["config", "tasks", workspace ?? ""] as const,
  workspaces: () => ["config", "workspaces"] as const,
  accessKeys: () => ["config", "access-keys"] as const,
  cpaInstances: () => ["config", "cpa-instances"] as const,
  cpaAccounts: () => ["config", "cpa-accounts"] as const,
  v1Models: (workspace?: string) => ["v1-models", workspace ?? ""] as const,
};
