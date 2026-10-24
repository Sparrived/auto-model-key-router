// 监控页筛选条件在 URL 中的编码。
//
// 这一层只做「字符串 ↔ 状态」的翻译，不碰 DOM：URL 是筛选的唯一真相，页面刷新、
// 前进后退、跨监控页切换都从这里恢复。解析必须对坏链接宽容——手工拼错的参数
// 不能让整个控制台崩掉。

import type { MetricFilters } from "./types";

export type FilterDimension = "provider" | "model" | "uk" | "ak" | "ws" | "caller" | "outcome";

export type OutcomeFilter = "success" | "failure";

export interface UpstreamKeyFilter {
  providerId: string;
  keyName: string;
}

export interface MetricFilterState {
  range: string;
  provider: string[];
  model: string[];
  uk: UpstreamKeyFilter[];
  ak: string[];
  ws: string[];
  caller: string[];
  outcome: OutcomeFilter | null;
}

export const FILTER_DIMENSIONS: FilterDimension[] = [
  "provider",
  "model",
  "uk",
  "ak",
  "ws",
  "caller",
  "outcome",
];

export const DIMENSION_LABELS: Record<FilterDimension, string> = {
  provider: "供应商",
  model: "模型",
  uk: "上游 Key",
  ak: "访问密钥",
  ws: "工作空间",
  caller: "调用方",
  outcome: "请求结果",
};

export interface RangePreset {
  value: string;
  label: string;
}

export const RANGE_PRESETS: RangePreset[] = [
  { value: "1h", label: "最近 1 小时" },
  { value: "6h", label: "最近 6 小时" },
  { value: "24h", label: "最近 24 小时" },
  { value: "3d", label: "最近 3 天" },
  { value: "7d", label: "最近 7 天" },
  { value: "30d", label: "最近 30 天" },
  { value: "90d", label: "最近 90 天" },
  { value: "180d", label: "最近 180 天" },
  { value: "1y", label: "最近 1 年" },
  { value: "all", label: "全部历史" },
];

export const MAX_RANGE_HOURS = 8760;

const PRESET_HOURS: Record<string, number> = {
  "1h": 1,
  "6h": 6,
  "24h": 24,
  "3d": 72,
  "7d": 168,
  "30d": 720,
  "90d": 2160,
  "180d": 4320,
  "1y": 8760,
};

const DIMENSION_PARAMS = FILTER_DIMENSIONS;

interface ParsedRange {
  kind: "all" | "preset" | "custom";
  hours: number;
  amount?: number;
  unit?: "h" | "d";
}

function parseRangeValue(range: string): ParsedRange | null {
  if (range === "all") return { kind: "all", hours: MAX_RANGE_HOURS };

  const presetHours = PRESET_HOURS[range];
  if (presetHours !== undefined) return { kind: "preset", hours: presetHours };

  const match = /^(\d+)([hd])$/.exec(range);
  if (!match) return null;
  const amount = Number(match[1]);
  const unit = match[2] as "h" | "d";
  if (!Number.isSafeInteger(amount) || amount <= 0) return null;
  const hours = unit === "h" ? amount : amount * 24;
  if (hours > MAX_RANGE_HOURS) return null;
  return { kind: "custom", hours, amount, unit };
}

function resolveRange(range: string | null, fallback: string): string {
  if (range && parseRangeValue(range)) return range;
  if (parseRangeValue(fallback)) return fallback;
  return "24h";
}

function readList(params: URLSearchParams, key: string): string[] {
  const values: string[] = [];
  const seen = new Set<string>();
  // 维度内多选在 URL 里既可能重复出现，也可能被上游拼成逗号分隔；两种都收。
  for (const raw of params.getAll(key)) {
    for (const part of raw.split(",")) {
      const value = part.trim();
      if (!value || seen.has(value)) continue;
      seen.add(value);
      values.push(value);
    }
  }
  return values;
}

function upstreamKeyOf(provider: unknown, key: unknown): UpstreamKeyFilter | null {
  if (typeof provider !== "string" || typeof key !== "string") return null;
  const providerId = provider.trim();
  const keyName = key.trim();
  return providerId && keyName ? { providerId, keyName } : null;
}

function upstreamEntryOf(entry: unknown): UpstreamKeyFilter | null {
  if (Array.isArray(entry)) return upstreamKeyOf(entry[0], entry[1]);
  if (entry && typeof entry === "object") {
    const record = entry as Record<string, unknown>;
    return upstreamKeyOf(
      record.providerId ?? record.provider_id,
      record.keyName ?? record.key_name,
    );
  }
  return null;
}

function upstreamEntriesOf(parsed: unknown): UpstreamKeyFilter[] {
  // 兼容单个 ["provider","key"]、成对的 ["p1","k1","p2","k2"] 与列表 [[...]] / [{...}]。
  if (Array.isArray(parsed) && parsed.every((item) => typeof item === "string")) {
    if (parsed.length === 0 || parsed.length % 2 !== 0) return [];
    const pairs: UpstreamKeyFilter[] = [];
    for (let index = 0; index < parsed.length; index += 2) {
      const pair = upstreamKeyOf(parsed[index], parsed[index + 1]);
      if (pair) pairs.push(pair);
    }
    return pairs;
  }
  if (Array.isArray(parsed)) {
    return parsed.map(upstreamEntryOf).filter((item): item is UpstreamKeyFilter => item !== null);
  }
  const single = upstreamEntryOf(parsed);
  return single ? [single] : [];
}

function parseUpstreamKeyValue(raw: string): UpstreamKeyFilter[] {
  const value = raw.trim();
  if (!value) return [];

  let parsed: unknown = undefined;
  let wrapped = false;
  try {
    parsed = JSON.parse(value);
  } catch {
    // 多个 JSON 数组用逗号拼接时，整体不是合法 JSON；包一层再试。
    try {
      parsed = JSON.parse(`[${value}]`);
      wrapped = true;
    } catch {
      parsed = undefined;
    }
  }

  if (parsed !== undefined) {
    let entries = upstreamEntriesOf(parsed);
    // 包一层会把每个 JSON 数组再套成数组，摊平一次才能回到 [[provider,key],...]。
    if (!entries.length && wrapped && Array.isArray(parsed)) {
      entries = upstreamEntriesOf(parsed.flat());
    }
    if (entries.length) return entries;
  }

  // JSON 形状的坏值直接忽略，不能把 "[["a"" 这类碎片当成 provider。
  if (/^[\[{"]/.test(value)) return [];

  // 最后兼容裸写的 provider,key（也可连续写多组 provider,key,provider,key）。
  const parts = value.split(",").map((part) => part.trim()).filter(Boolean);
  if (parts.length === 0 || parts.length % 2 !== 0) return [];
  const pairs: UpstreamKeyFilter[] = [];
  for (let index = 0; index < parts.length; index += 2) {
    const pair = upstreamKeyOf(parts[index], parts[index + 1]);
    if (pair) pairs.push(pair);
  }
  return pairs;
}

function readUpstreamKeys(params: URLSearchParams): UpstreamKeyFilter[] {
  const values: UpstreamKeyFilter[] = [];
  const seen = new Set<string>();
  for (const raw of params.getAll("uk")) {
    for (const pair of parseUpstreamKeyValue(raw)) {
      const key = `${pair.providerId}\u0000${pair.keyName}`;
      if (seen.has(key)) continue;
      seen.add(key);
      values.push(pair);
    }
  }
  return values;
}

/** 从 URL 查询参数恢复筛选状态；非法维度值忽略，非法时间窗回退页面默认值。 */
export function parseFilters(params: URLSearchParams, pageDefaultRange = "24h"): MetricFilterState {
  const outcome = params.get("outcome");
  return {
    range: resolveRange(params.get("range"), pageDefaultRange),
    provider: readList(params, "provider"),
    model: readList(params, "model"),
    uk: readUpstreamKeys(params),
    ak: readList(params, "ak"),
    ws: readList(params, "ws"),
    caller: readList(params, "caller"),
    outcome: outcome === "success" || outcome === "failure" ? outcome : null,
  };
}

/** 时间窗转接口参数；非法值按 24 小时处理，保证调用方永远拿到可用窗口。 */
export function rangeToQuery(range: string): { hours?: number; allHistory?: boolean } {
  const parsed = parseRangeValue(range) ?? parseRangeValue("24h");
  if (!parsed) return { hours: 24 };
  if (parsed.kind === "all") return { allHistory: true };
  return { hours: parsed.hours };
}

/** 时间窗的中文标签；自定义窗口按小时/天还原成「最近 N 小时/天」。 */
export function rangeLabel(range: string): string {
  const parsed = parseRangeValue(range);
  if (!parsed) {
    return RANGE_PRESETS.find((preset) => preset.value === "24h")?.label ?? "最近 24 小时";
  }
  if (parsed.kind === "all") return "全部历史";
  if (parsed.kind === "preset") {
    return RANGE_PRESETS.find((preset) => preset.value === range)?.label ?? "最近 24 小时";
  }
  return parsed.unit === "d" ? `最近 ${parsed.amount} 天` : `最近 ${parsed.amount} 小时`;
}

/** 把筛选状态写回 URL；返回新对象，保留非筛选参数，不修改调用方传入的 params。 */
export function writeFilters(params: URLSearchParams, state: MetricFilterState): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const key of ["range", ...DIMENSION_PARAMS]) next.delete(key);

  if (state.range) next.set("range", state.range);

  const lists: Array<[string, string[]]> = [
    ["provider", state.provider],
    ["model", state.model],
    ["ak", state.ak],
    ["ws", state.ws],
    ["caller", state.caller],
  ];
  for (const [key, values] of lists) {
    for (const value of values) {
      if (value) next.append(key, value);
    }
  }

  if (state.uk.length) {
    next.set("uk", JSON.stringify(state.uk.map((pair) => [pair.providerId, pair.keyName])));
  }
  if (state.outcome) next.set("outcome", state.outcome);

  return next;
}

/** 筛选状态转接口形状；上游 Key 的命名与 URL 层不同，只在这里转换一次。 */
export function toApiFilters(state: MetricFilterState): MetricFilters {
  return {
    providerIds: [...state.provider],
    modelIds: [...state.model],
    upstreamKeys: state.uk.map((pair) => ({ providerId: pair.providerId, keyName: pair.keyName })),
    accessKeyIds: [...state.ak],
    workspaces: [...state.ws],
    callerTypes: [...state.caller],
    success: state.outcome === null ? null : state.outcome === "success",
  };
}
