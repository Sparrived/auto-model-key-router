// 成本估算：把「本地 token 用量」和「models.dev 单价」相乘。
//
// 三条必须说清楚的语义，否则读数会被误读：
//
//   1. **这是估算，不是账单。** 单价来自 models.dev 的公开目录，与上游实际计费可能
//      有出入（区域价、合约价、阶梯价、促销）。金额只能用来比较量级与相对开销。
//   2. **金额不落库。** 指标库只存 token 用量（与参照实现一致），成本是现算的派生
//      读数。目录更新后，历史请求的成本会跟着变——单价是外部事实，不是本项目的记账
//      结果，这一点是刻意的。
//   3. **拿不到单价时返回 null，不是 0。** 界面必须显示 "—"，因为 0 会被读成
//      "这次请求免费"，那是最坏的误导。
//
// 匹配口径（用户确认）：按 upstream_model 名称**大小写不敏感**地匹配 models.dev 的
// 模型 id；目录里同一 id 有多家供应商时，服务端已经按"排除 0 元挂名条目后取最便宜"
// 收敛成一条，前端不再做二次比价。

import { readJSON, writeJSON } from "./storage";

// 单价单位是 **USD / 100 万 token**（与 models.dev 一致）。
const PER_TOKENS = 1e6;

// DATE_SUFFIX 匹配模型名的日期后缀（gpt-4o-mini-2024-07-18 / claude-3-5-haiku-20241022）。
//
// 用户常用带日期的上游名，而目录里往往只有不带日期的那条；反过来也有。实测本地常见
// 名字里 exact 命中 24/28，去掉日期后缀再补 2 个——本仓库自己的
// router-config.example.json 用的就是 gpt-4o-mini-2024-07-18，所以这条回退有真实依据。
const DATE_SUFFIX = /-(\d{4}-\d{2}-\d{2}|\d{8})$/;

export interface PriceEntry {
  input: number;
  output: number;
  cacheRead: number | null;
  cacheWrite: number | null;
}

export interface PriceIndex {
  models: Map<string, PriceEntry>;
  updatedAt: string | null;
  error: string | null;
  source: string | null;
}

export interface PriceDocument {
  version?: number;
  source?: string;
  updated_at?: string;
  error?: string | null;
  models?: Record<string, unknown>;
}

export interface UsageLike {
  prompt_tokens?: number | string | null;
  completion_tokens?: number | string | null;
  cached_tokens?: number | string | null;
  cache_read_input_tokens?: number | string | null;
  cache_creation_input_tokens?: number | string | null;
}

export interface CostParts {
  input: number;
  cacheRead: number;
  cacheWrite: number;
  output: number;
}

// normalizeCatalog 把服务端载荷转成便于检索的索引；形状不对时返回 null。
//
// 返回 null 会让所有成本读数变成 "—"，这正是"宁可没有也不要错"的取向。
export function normalizeCatalog(document: unknown): PriceIndex | null {
  if (!document || typeof document !== "object") return null;
  const doc = document as PriceDocument;
  const models = doc.models;
  if (!models || typeof models !== "object") return null;

  const index = new Map<string, PriceEntry>();
  for (const [id, raw] of Object.entries(models)) {
    if (!raw || typeof raw !== "object") continue;
    const entry = raw as Record<string, unknown>;
    const input = finite(entry.input);
    const output = finite(entry.output);
    if (input === null || output === null) continue;
    index.set(String(id).toLowerCase(), {
      input,
      output,
      // 缓存价缺失时保持 null：计费要回退到输入价，而不是当成 0（免费）。
      cacheRead: finite(entry.cache_read),
      cacheWrite: finite(entry.cache_write),
    });
  }
  if (!index.size) return null;

  return {
    models: index,
    updatedAt: typeof doc.updated_at === "string" ? doc.updated_at : null,
    // 非 null 表示服务端最近一次刷新失败——价格是旧的，界面应当一起显示。
    error: typeof doc.error === "string" ? doc.error : null,
    source: typeof doc.source === "string" ? doc.source : null,
  };
}

// finite 把数字字段收敛成 number 或 null（NaN / Infinity / 字符串都算缺失）。
function finite(value: unknown): number | null {
  if (value === null || value === undefined) return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

// lookupPrice 按模型名找单价。
//
// 依次尝试：原名、去供应商前缀（vendor/model -> model）、去日期后缀、两者都去。
// 先命中者胜——顺序固定，因此同一份目录上的结果可复现。
export function lookupPrice(index: PriceIndex | null | undefined, modelId: string | null | undefined): PriceEntry | null {
  if (!index || !index.models || !modelId) return null;
  const key = String(modelId).trim().toLowerCase();
  if (!key) return null;

  for (const candidate of candidates(key)) {
    const entry = index.models.get(candidate);
    if (entry) return entry;
  }
  return null;
}

// candidates 给出按优先级排序的候选键。
function candidates(key: string): string[] {
  const list = [key];
  const slash = key.lastIndexOf("/");
  const tail = slash >= 0 ? key.slice(slash + 1) : "";
  if (tail) list.push(tail);
  // 先收集再整体去日期，避免边遍历边追加导致顺序不稳。
  for (const base of [...list]) {
    const stripped = base.replace(DATE_SUFFIX, "");
    if (stripped && stripped !== base) list.push(stripped);
  }
  return list;
}

// estimateCost 计算一次用量的金额（USD）；没有单价时返回 null。
//
// 计费口径：按 token 的**类别**分别计价，而不是把 prompt_tokens 整个按输入价算。
//
//   fresh   = prompt_tokens - 缓存读 - 缓存写   （剩余的"首次输入"）
//   缓存读  × cache_read  （缺失时回退输入价）
//   缓存写  × cache_write （缺失时回退输入价）
//   补全    × output
//
// **不能直接用 uncached_prompt_tokens。** 对 Anthropic，参照实现的
// prompt_tokens = input_tokens + cache_read + cache_creation，而 cached_tokens 取
// cache_read，于是 uncached = input + cache_creation——拿它乘输入价会让缓存写被计费
// 两次（一次按输入价、一次按 cache_write）。按类别相减就没有这个问题。
//
// 缓存读的取法：优先 cache_read_input_tokens，为 0 时退回 cached_tokens
// （OpenAI 走 prompt_tokens_details.cached_tokens，此时前者是 0）。
export function estimateCost(entry: PriceEntry | null | undefined, usage: UsageLike | null | undefined): number | null {
  const parts = costParts(entry, usage);
  if (!parts) return null;
  return parts.input + parts.cacheRead + parts.cacheWrite + parts.output;
}

// costParts 把成本拆成四项（USD）。estimateCost 是它的和。
//
// **四项与总额必须出自同一处算术。** 界面同屏既有"估算成本"合计、又有"成本构成"的四项
// 分解；两处各写一份公式的话，日后改了一处就会让同屏两个数字互相矛盾，而且很难被发现。
// 因此这里只算一次，合计与分解都基于它。
//
// 没有单价或没有用量时返回 null（调用方据此显示 "—"，而不是 $0）。
export function costParts(entry: PriceEntry | null | undefined, usage: UsageLike | null | undefined): CostParts | null {
  if (!entry || !usage) return null;

  const prompt = count(usage.prompt_tokens);
  const completion = count(usage.completion_tokens);
  const cacheReadField = count(usage.cache_read_input_tokens);
  const cacheRead = cacheReadField > 0 ? cacheReadField : count(usage.cached_tokens);
  const cacheWrite = count(usage.cache_creation_input_tokens);

  // 缓存写与缓存读不重叠（前者是写入、后者是命中），但都要从 prompt 里扣掉，
  // 剩下的才按输入价计。负数兜到 0：上游字段偶尔自相矛盾，宁可少算也不要出现负成本。
  let fresh = prompt - cacheRead - cacheWrite;
  if (fresh < 0) fresh = 0;

  const inputPrice = entry.input;
  // 缺缓存价时回退到输入价（此时"缓存"与"普通输入"同价，不会凭空变便宜）。
  const readPrice = entry.cacheRead === null ? inputPrice : entry.cacheRead;
  const writePrice = entry.cacheWrite === null ? inputPrice : entry.cacheWrite;

  return {
    input: (fresh * inputPrice) / PER_TOKENS,
    cacheRead: (cacheRead * readPrice) / PER_TOKENS,
    cacheWrite: (cacheWrite * writePrice) / PER_TOKENS,
    output: (completion * entry.output) / PER_TOKENS,
  };
}

// count 把 token 字段收敛成非负整数。
function count(value: number | string | null | undefined): number {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return 0;
  return n;
}

// requestCost 计算一条 /metrics/requests 明细的成本。
//
// 用 upstream_model_id 匹配：model_id 是**本地路由名**（用户自取，如 "gpt-4o"），
// upstream_model_id 才是真正发给上游的模型名，也是唯一能和 models.dev 对上的字段。
export function requestCost(index: PriceIndex | null | undefined, item: { upstream_model_id?: string | null; model_id?: string | null } & UsageLike | null | undefined): number | null {
  if (!item) return null;
  const entry = lookupPrice(index, item.upstream_model_id);
  if (!entry) return null;
  return estimateCost(entry, item);
}

// aggregateCost 计算一组聚合 stats 的成本（/metrics 的 models / upstream_models 等）。
//
// 聚合计费有个无法回避的偏差：同一模型下混用了多个上游模型时，只有一份合计 token，
// 因此按**第一条能匹配到的**单价计价。这里明确按传入的键（upstream_model_id）取值，
// 调用方应当传上游维度的统计。
export function aggregateCost(index: PriceIndex | null | undefined, modelId: string | null | undefined, stats: UsageLike | null | undefined): number | null {
  if (!stats) return null;
  const entry = lookupPrice(index, modelId);
  if (!entry) return null;
  return estimateCost(entry, stats);
}

// sumCost 把 {键: stats} 里的成本加总。
//
// 返回 {total, priced, total_count}：
//   - total 是已计价部分的金额；
//   - priced 是匹配到单价的条目数；
//   - total_count 是全部条目数。
// 界面上必须能区分"总共 3 项、其中 1 项有价"，否则合计会被误读成全量。
export function sumCost(
  index: PriceIndex | null | undefined,
  entries: Record<string, UsageLike> | null | undefined,
  keyOf: (key: string) => string = (key) => key,
): { total: number; priced: number; total_count: number } {
  let total = 0;
  let priced = 0;
  let totalCount = 0;
  for (const [key, stats] of Object.entries(entries || {})) {
    totalCount += 1;
    const cost = aggregateCost(index, keyOf(key), stats);
    if (cost === null) continue;
    priced += 1;
    total += cost;
  }
  return { total, priced, total_count: totalCount };
}

// formatCost 把美元金额渲染成适合界面的短串。
//
// 金额跨度极大：一次请求可能花 $0.0001，一个窗口可能花 $50。分档是为了让每个量级都
// 可读，同时**不用科学计数法**（"1.2e-4" 对运维没有意义）。
export function formatCost(value: number | null | undefined): string {
  if (value === null || value === undefined) return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  if (n === 0) return "$0";
  const abs = Math.abs(n);
  if (abs < 0.0001) return "<$0.0001";
  if (abs < 0.01) return `$${n.toFixed(5)}`;
  if (abs < 1) return `$${n.toFixed(4)}`;
  if (abs < 1000) return `$${n.toFixed(2)}`;
  return `$${compact(n)}`;
}

// formatCostExact 给出完整精度（详情/提示用）。
export function formatCostExact(value: number | null | undefined): string {
  if (value === null || value === undefined) return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  if (n === 0) return "$0";
  // 小额保留 6 位有效数字，避免 0.000123 被截成 0.0001。
  if (Math.abs(n) < 0.01) return `$${n.toPrecision(3)}`;
  return `$${n.toFixed(4)}`;
}

// formatPrice 渲染单价（USD / 1M token）。
export function formatPrice(value: number | null | undefined): string {
  if (value === null || value === undefined) return "—";
  const n = Number(value);
  if (!Number.isFinite(n)) return "—";
  return `$${trimNumber(n)}`;
}

// compact 给大额金额加千分位。
function compact(n: number): string {
  return n.toLocaleString("en-US", { maximumFractionDigits: 2 });
}

// trimNumber 去掉多余小数位。
function trimNumber(n: number): string {
  if (Number.isInteger(n)) return String(n);
  return String(Number(n.toFixed(4)));
}

// —— 目录的加载与缓存 ——

// CACHE_KEY 是 localStorage 里的兜底缓存键。
//
// 服务端已经有缓存与 ETag，这里再存一份是为了**首个渲染**：网络慢或服务刚重启时，
// 界面能先显示上次的价格而不是一片 "—"。目录约 230 KB，localStorage 装得下。
const CACHE_KEY = "amkr.pricing";

// state 是模块级状态：目录在多个页面间共享，只加载一次。
const state: {
  index: PriceIndex | null;
  loadedAt: number | null;
  error: string | null;
  loading: boolean;
  waiters: Array<(index: PriceIndex | null) => void>;
} = {
  index: null,
  loadedAt: null,
  error: null,
  loading: false,
  waiters: [],
};

// currentIndex 返回当前索引（可能为 null）。
export function currentIndex(): PriceIndex | null {
  return state.index;
}

// pricingStatus 报告目录的加载状态，供界面显示新鲜度与告警。
export function pricingStatus(): {
  index: PriceIndex | null;
  loadedAt: number | null;
  error: string | null;
  loading: boolean;
  updatedAt: string | null;
  stale: boolean;
  staleReason: string | null;
} {
  return {
    index: state.index,
    loadedAt: state.loadedAt,
    error: state.error,
    loading: state.loading,
    updatedAt: state.index?.updatedAt || null,
    stale: Boolean(state.index?.error),
    staleReason: state.index?.error || null,
  };
}

// loadPricing 拉取目录（并发调用共享同一次请求）。
//
// 失败**不抛异常**：成本是附加读数，拿不到目录时界面显示 "—" 即可，不该让整个页面
// 报错。失败会保留上一次成功的索引（若内存或 localStorage 里有）。
export async function loadPricing(fetcher: () => Promise<unknown>): Promise<PriceIndex | null> {
  if (state.index) return state.index;
  if (state.loading) {
    return new Promise<PriceIndex | null>((resolve) => state.waiters.push(resolve));
  }
  state.loading = true;
  try {
    const payload = await fetcher();
    const index = normalizeCatalog(payload);
    if (index) {
      state.index = index;
      state.loadedAt = Date.now();
      state.error = null;
      persist(index);
    } else {
      state.error = "价格目录为空或格式不符";
    }
  } catch (error) {
    // 503（目录还在取）也走这里：不是错误状态，只是暂时没有价格。
    state.error = (error as { message?: string } | null)?.message || String(error);
    if (!state.index) state.index = restore();
  } finally {
    state.loading = false;
    const waiters = state.waiters;
    state.waiters = [];
    for (const resolve of waiters) resolve(state.index);
  }
  return state.index;
}

// restore 从 localStorage 恢复上次的索引（失败一律当作没有）。
function restore(): PriceIndex | null {
  const parsed = readJSON<PriceDocument>(CACHE_KEY);
  if (!parsed) return null;
  // 本地缓存只做首屏兜底，仍然要求服务端的形状校验；时间戳标记它是旧数据。
  const index = normalizeCatalog(parsed);
  if (index) index.error = "显示的是本地缓存的旧价格";
  return index;
}

// persist 把索引写进 localStorage（配额不足等失败一律忽略）。
function persist(index: PriceIndex): void {
  const models: Record<string, unknown> = {};
  for (const [key, entry] of index.models) {
    models[key] = {
      input: entry.input,
      output: entry.output,
      ...(entry.cacheRead === null ? {} : { cache_read: entry.cacheRead }),
      ...(entry.cacheWrite === null ? {} : { cache_write: entry.cacheWrite }),
    };
  }
  writeJSON(CACHE_KEY, {
    version: 1, updated_at: index.updatedAt, error: index.error, models,
  });
}

// __setIndexForTest 让探针注入固定目录，不碰网络也不碰 localStorage。
export function __setIndexForTest(index: PriceIndex | null): void {
  state.index = index;
  state.loadedAt = Date.now();
  state.error = null;
}
