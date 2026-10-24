// 模型路由页的共享口径。
import type { ModelSummaryResponse, Provider } from "@/lib/types";

// UNIFIED_ID 是统一模型的固定名称。它不是模型列表里的一条，而是服务端按入站端点族分派
// 的伪模型；在界面上它与普通名称同处一个列表，且永远排第一。
export const UNIFIED_ID = "unified-model";

export const MODES = [
  { value: "", label: "默认策略" },
  { value: "round_robin", label: "轮询" },
  { value: "priority", label: "优先级" },
  { value: "only_first", label: "首 Key" },
];

export const modeLabel = (value: string | null | undefined): string =>
  (MODES.find((mode) => mode.value === (value || "")) || MODES[0]).label;

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

// keyUpstreams 返回某个 Key 探测到的上游模型名（去重排序）。
//
// 这是"上游叫什么"的唯一权威来源：页面用它给上游名输入框做候选，不再把它当成可调用名称。
// 没探测过（capabilities 缺失）的 Key 返回空，用户仍可手填。
export function keyUpstreams(key: { capabilities?: { models?: string[] } | null }): string[] {
  const names = (key.capabilities?.models || []).map((name) => String(name).trim()).filter(Boolean);
  return [...new Set(names)].sort();
}

export interface KeyOption {
  value: string;
  label: string;
  providerId: string;
  keyName: string;
}

// allKeyOptions 列出全部 (供应商, Key)，供"添加目标"与"添加模型"选择。
export function allKeyOptions(providers: Provider[]): KeyOption[] {
  const options: KeyOption[] = [];
  for (const provider of providers) {
    for (const key of provider.keys || []) {
      options.push({
        value: `${provider.id}|${key.name}`,
        label: `${provider.id} / ${key.name}（探测到 ${keyUpstreams(key).length} 个上游模型）`,
        providerId: provider.id,
        keyName: key.name,
      });
    }
  }
  return options;
}

// knownModel 把「已不存在的模型名」折成空串（见 endpoint-plans.ts 的同名说明）。
export function knownModel(models: ModelSummaryResponse[], id: string | null | undefined): string {
  return models.some((model) => model.id === id) ? String(id) : "";
}
