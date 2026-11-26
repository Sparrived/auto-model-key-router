// 模型路由页的共享口径。
import { freezeLookupKey } from "@/lib/quota-freeze";
import type {
  ModelSummaryResponse,
  Provider,
  ProviderKey,
  QuotaFreeze,
  RouteEntry,
  RouteTarget,
  UnifiedModel,
} from "@/lib/types";

// UNIFIED_ID 是统一模型的固定名称。它不是模型列表里的一条，而是服务端按入站端点族分派
// 的伪模型；在界面上它与普通名称同处一个列表，且永远排第一。
export const UNIFIED_ID = "unified-model";

export const MODES = [
  { value: "round_robin", label: "轮询" },
  { value: "priority", label: "优先级" },
  { value: "only_first", label: "首 Key" },
];

export function normalizeMode(value: string | null | undefined): string {
  return MODES.some((mode) => mode.value === value) ? String(value) : "round_robin";
}

export const modeLabel = (value: string | null | undefined): string =>
  (MODES.find((mode) => mode.value === normalizeMode(value)) || MODES[0]).label;

export const EFFORTS = [
  { value: "", label: "默认" },
  { value: "none", label: "none" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

// routeNames 返回一条路由的对外名称集合：路由 ID + 别名。两者等价，都会出现在 /v1/models。
export const routeNames = (route: { id: string; aliases?: string[] }): string[] => [route.id, ...(route.aliases || [])];

export const targetText = (target: { provider: string; key: string; upstream_model: string }): string =>
  `${target.provider} / ${target.key} / ${target.upstream_model}`;

export const targetIdentity = (
  target: { provider: string; key: string; upstream_model?: string | null },
  fallbackModel = "",
): string => `${target.provider}\u0000${target.key}\u0000${String(target.upstream_model || "").trim() || fallbackModel}`;

// keyUpstreams 返回某个 Key 探测到的上游模型名（去重排序）。
//
// 这是"上游叫什么"的唯一权威来源：页面用它给上游名输入框做候选，不再把它当成可调用名称。
// 没探测过（capabilities 缺失）的 Key 返回空，用户仍可手填。
export function keyUpstreams(key: { capabilities?: { models?: string[] } | null }): string[] {
  const names = (key.capabilities?.models || []).map((name) => String(name).trim()).filter(Boolean);
  return [...new Set(names)].sort();
}

export function findProviderKey(
  providers: Provider[],
  providerId: string,
  keyName: string,
): { provider: Provider; key: ProviderKey } | null {
  const provider = providers.find((item) => item.id === providerId);
  if (!provider) return null;
  const key = (provider.keys || []).find((item) => item.name === keyName);
  if (!key) return null;
  return { provider, key };
}

export interface KeyOption {
  value: string;
  label: string;
  providerId: string;
  keyName: string;
  enabled: boolean;
  freeze?: QuotaFreeze;
  upstreamCount: number;
  maxConcurrency: number | null;
}

// allKeyOptions 列出全部 (供应商, Key)，供"添加目标"与"添加模型"选择。
export function allKeyOptions(
  providers: Provider[],
  freezes?: Map<string, QuotaFreeze>,
): KeyOption[] {
  const options: KeyOption[] = [];
  for (const provider of providers) {
    for (const key of provider.keys || []) {
      const upstreamCount = keyUpstreams(key).length;
      const enabled = key.enabled !== false;
      const freeze = freezes?.get(freezeLookupKey(provider.id, key.name));
      options.push({
        value: `${provider.id}|${key.name}`,
        label: `${provider.id} / ${key.name}`,
        providerId: provider.id,
        keyName: key.name,
        enabled,
        freeze,
        upstreamCount,
        maxConcurrency: key.max_concurrency ?? null,
      });
    }
  }
  return options;
}

// knownModel 把「已不存在的模型名」折成空串（见 endpoint-plans.ts 的同名说明）。
export function knownModel(models: ModelSummaryResponse[], id: string | null | undefined): string {
  return models.some((model) => model.id === id) ? String(id) : "";
}

/** 端点族模型的无障碍名称：避免对已含「模型」的标签重复拼接成「图像模型模型」。 */
export function familyModelAriaLabel(family: { label: string }): string {
  return family.label.endsWith("模型") ? family.label : `${family.label}模型`;
}

/** 路由搜索：匹配对外名称、别名、供应商、Key 与上游模型名。 */
export function matchesRouteQuery(route: RouteEntry, rawQuery: string): boolean {
  const query = rawQuery.trim().toLowerCase();
  if (!query) return true;
  if (route.id.toLowerCase().includes(query)) return true;
  if ((route.aliases || []).some((alias) => alias.toLowerCase().includes(query))) return true;
  return (route.targets || []).some((target) =>
    target.provider.toLowerCase().includes(query)
    || target.key.toLowerCase().includes(query)
    || (target.upstream_model || "").toLowerCase().includes(query),
  );
}

/** 统一模型是否命中搜索词（含名称、中文别名及已配置的下游模型名）。 */
export function matchesUnifiedQuery(unified: UnifiedModel | null, rawQuery: string): boolean {
  const query = rawQuery.trim().toLowerCase();
  if (!query) return true;
  if (UNIFIED_ID.includes(query) || "统一模型".includes(query) || "统一入口".includes(query)) return true;
  if (!unified) return false;
  const plans = Object.values(unified) as Array<{ primary?: { model?: string; key?: string | null } | null; fallback?: { model?: string; key?: string | null } | null } | null>;
  return plans.some((plan) => {
    if (!plan || typeof plan !== "object") return false;
    return Boolean(
      plan.primary?.model?.toLowerCase().includes(query)
      || plan.primary?.key?.toLowerCase().includes(query)
      || plan.fallback?.model?.toLowerCase().includes(query)
      || plan.fallback?.key?.toLowerCase().includes(query),
    );
  });
}

export function routeHasEndpointPlans(route: RouteEntry): boolean {
  return Object.values(route.endpoints || {}).some((plan) => Boolean(plan?.primary?.model));
}

export interface RouteIssueSummary {
  disabledCount: number;
  frozenCount: number;
  missingCount: number;
  total: number;
}

export function routeTargetIssues(
  route: RouteEntry,
  providers: Provider[],
  freezes?: Map<string, QuotaFreeze>,
): RouteIssueSummary {
  let disabledCount = 0;
  let frozenCount = 0;
  let missingCount = 0;
  for (const target of route.targets || []) {
    const found = findProviderKey(providers, target.provider, target.key);
    if (!found) {
      if (providers.length > 0) missingCount += 1;
      continue;
    }
    if (found.key.enabled === false) disabledCount += 1;
    if (freezes?.has(freezeLookupKey(target.provider, target.key))) frozenCount += 1;
  }
  return {
    disabledCount,
    frozenCount,
    missingCount,
    total: disabledCount + frozenCount + missingCount,
  };
}

export interface CandidateTarget extends RouteTarget {
  exact: boolean;
  enabled: boolean;
  frozen: boolean;
}

function normalizeModelToken(name: string): string {
  const trimmed = name.trim().toLowerCase();
  const slash = trimmed.lastIndexOf("/");
  return slash >= 0 ? trimmed.slice(slash + 1) : trimmed;
}

/**
 * 找出可能匹配本路由名称/别名的未绑定上游目标。
 * 精确同名排在前，其次是去掉厂商前缀后相等、或互相包含的相似上游名（例如带日期后缀）。
 */
export function findUpstreamCandidates(
  providers: Provider[],
  targets: RouteTarget[],
  names: string[],
  freezes?: Map<string, QuotaFreeze>,
  limit = 24,
): CandidateTarget[] {
  const exactSet = new Set(names.map((item) => item.trim()).filter(Boolean));
  const normalizedNames = [...new Set(names.map(normalizeModelToken).filter((item) => item.length >= 3))];
  const bound = new Set(targets.map((target) => targetIdentity(target)));

  const exactMatches: CandidateTarget[] = [];
  const fuzzyMatches: CandidateTarget[] = [];

  for (const provider of providers) {
    for (const key of provider.keys || []) {
      const enabled = key.enabled !== false;
      const frozen = Boolean(freezes?.has(freezeLookupKey(provider.id, key.name)));
      for (const upstream of keyUpstreams(key)) {
        if (bound.has(targetIdentity({ provider: provider.id, key: key.name, upstream_model: upstream }))) {
          continue;
        }
        if (exactSet.has(upstream)) {
          exactMatches.push({ provider: provider.id, key: key.name, upstream_model: upstream, exact: true, enabled, frozen });
          continue;
        }
        const normUpstream = normalizeModelToken(upstream);
        const similar = normalizedNames.some((token) =>
          normUpstream === token
          || normUpstream.startsWith(`${token}-`)
          || normUpstream.startsWith(`${token}@`)
          || normUpstream.startsWith(`${token}:`)
          || token.startsWith(`${normUpstream}-`),
        );
        if (similar) {
          fuzzyMatches.push({ provider: provider.id, key: key.name, upstream_model: upstream, exact: false, enabled, frozen });
        }
      }
    }
  }

  return [...exactMatches, ...fuzzyMatches].slice(0, limit);
}
