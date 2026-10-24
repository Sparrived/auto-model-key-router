// 访问密钥两份清单的关系计算：纯函数，页面与测试共用。
//
// 清单按调用方写的原始名字**逐字**比对（别名解析之前），所以真实 ID 与别名是两项；
// 供应商清单则是路由目标实际落到的 provider。失配 = 勾了某个模型，但它的路由目标
// 依赖的供应商一个都没被授权——调用时会在「无可用 Key」这一步失败。

import type { ModelSummaryResponse, Provider, RouteEntry } from "@/lib/types";

export type ScopeMode = "unrestricted" | "list" | "none";

// 三态从响应字段反推：字段缺席 = 不限制；空数组 = 一个都不许；有值 = 指定清单。
export function scopeMode(list: string[] | null | undefined): ScopeMode {
  if (list === undefined || list === null) return "unrestricted";
  return list.length === 0 ? "none" : "list";
}

// 提交值：PUT 里「不限制」= null、「全部禁止」= []、指定 = 排序后的数组。
// 排序只为让写盘结果稳定：这是权限清单，勾选顺序没有含义，而配置 diff 有。
export function scopeValue(mode: ScopeMode, selected: Iterable<string>): string[] | null {
  if (mode === "unrestricted") return null;
  if (mode === "none") return [];
  return [...selected].sort();
}

export interface ModelProviderIndex {
  modelToProviders: Map<string, Set<string>>;
  providerToModels: Map<string, Set<string>>;
}

export function buildModelProviderIndex(
  routes: RouteEntry[] = [],
  models: ModelSummaryResponse[] = [],
  providers: Provider[] = [],
): ModelProviderIndex {
  const modelToProviders = new Map<string, Set<string>>();
  const providerToModels = new Map<string, Set<string>>();

  for (const provider of providers || []) {
    providerToModels.set(provider.id, new Set());
  }

  // 1. 从 routes 的 targets 提取每个模型与别名实际绑定的供应商。
  for (const route of routes || []) {
    const allNames = [route.id, ...(route.aliases || [])];
    const routeProviders = new Set<string>();
    for (const target of route.targets || []) {
      if (target.provider) routeProviders.add(target.provider);
    }
    for (const name of allNames) {
      if (!modelToProviders.has(name)) modelToProviders.set(name, new Set());
      for (const provider of routeProviders) {
        modelToProviders.get(name)!.add(provider);
        if (!providerToModels.has(provider)) providerToModels.set(provider, new Set());
        providerToModels.get(provider)!.add(name);
      }
    }
  }

  // 2. 从 models 补充别名：真实 ID 已有的供应商关系也要覆盖到它的别名。
  for (const model of models || []) {
    const allNames = [model.id, ...(model.aliases || [])];
    const existing = modelToProviders.get(model.id);
    if (!existing || existing.size === 0) continue;
    for (const name of allNames) {
      if (!modelToProviders.has(name)) modelToProviders.set(name, new Set());
      for (const provider of existing) {
        modelToProviders.get(name)!.add(provider);
        if (!providerToModels.has(provider)) providerToModels.set(provider, new Set());
        providerToModels.get(provider)!.add(name);
      }
    }
  }

  return { modelToProviders, providerToModels };
}

export interface ModelOption {
  value: string;
  note: string;
}

// 候选模型名 = 真实 ID + 别名，两者必须能**分别**勾选：清单按调用方写的原始名字逐字
// 比对，选了真实 ID 并不等于放行别名写法。别名注上「别名 · 原模型」，否则一屏名字
// 看不出谁是谁的别名。
export function buildModelOptions(models: ModelSummaryResponse[]): ModelOption[] {
  const catalog = new Map<string, string>();
  for (const model of models) catalog.set(model.id, "");
  for (const model of models) {
    for (const alias of model.aliases || []) {
      if (!catalog.has(alias)) catalog.set(alias, `别名 · ${model.id}`);
    }
  }
  return [...catalog].map(([value, note]) => ({ value, note }));
}

export interface ModelGroup {
  provider: string;
  label: string;
  models: ModelOption[];
}

// 按「服务它的供应商」分组；同一模型被多个供应商服务时只归到供应商顺序里的第一个，
// 避免同一个复选框在多个组里重复出现（勾一处、另一处没跟着变会让人以为权限没生效）。
export function groupModelsByProvider(
  options: ModelOption[],
  providerToModels: Map<string, Set<string>>,
  providerOrder: string[],
): ModelGroup[] {
  const groups = new Map<string, ModelOption[]>(providerOrder.map((id) => [id, []]));
  const unknown: ModelOption[] = [];
  for (const option of options) {
    const owner = providerOrder.find((id) => providerToModels.get(id)?.has(option.value));
    if (owner) groups.get(owner)!.push(option);
    else unknown.push(option);
  }
  const result: ModelGroup[] = [];
  for (const id of providerOrder) {
    const models = groups.get(id) ?? [];
    if (models.length > 0) result.push({ provider: id, label: id, models });
  }
  if (unknown.length > 0) result.push({ provider: "__unknown__", label: "未关联供应商", models: unknown });
  return result;
}

export interface EffectiveScopeInput {
  providerMode: ScopeMode;
  providers: string[];
  modelMode: ScopeMode;
  models: string[];
  allModels: string[];
  modelToProviders: Map<string, Set<string>>;
}

export interface EffectiveRow {
  model: string;
  providers: string[];
}

export interface MismatchRow {
  model: string;
  needed: string[];
}

export interface EffectiveScope {
  blocked: boolean;
  unrestricted: boolean;
  rows: EffectiveRow[];
  mismatched: MismatchRow[];
  missingProviders: string[];
}

export function effectiveScope({
  providerMode, providers, modelMode, models, allModels, modelToProviders,
}: EffectiveScopeInput): EffectiveScope {
  const unrestricted = providerMode === "unrestricted" && modelMode === "unrestricted";
  if (providerMode === "none" || modelMode === "none") {
    return { blocked: true, unrestricted: false, rows: [], mismatched: [], missingProviders: [] };
  }
  const providerSet = providerMode === "list" ? new Set(providers) : null;
  const modelNames = modelMode === "unrestricted" ? allModels : models;
  const rows: EffectiveRow[] = [];
  const mismatched: MismatchRow[] = [];

  for (const model of modelNames) {
    const known = [...(modelToProviders.get(model) ?? [])];
    const reachable = providerSet ? known.filter((provider) => providerSet.has(provider)) : known;
    if (reachable.length > 0 || !providerSet) {
      rows.push({ model, providers: reachable });
    } else if (providerMode === "list" && modelMode === "list") {
      // 勾了模型但没授权它的供应商：调用时会因无可用 Key 失败。
      mismatched.push({ model, needed: known });
    }
  }

  const missingProviders = [...new Set(mismatched.flatMap((item) => item.needed))];
  return { blocked: false, unrestricted, rows, mismatched, missingProviders };
}
