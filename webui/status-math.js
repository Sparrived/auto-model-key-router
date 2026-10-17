// 状态监控的口径层：健康分、分档与错误归因，全部是纯函数、不碰 DOM。
//
// 为什么单独成模块：页面只负责"上色"，而"多少分算健康"必须能被 node 直接断言。
// 颜色是最容易被悄悄改坏的东西——阈值挪了 5 分，界面上看不出任何异常，只有
// 探针逐格比对才能发现。把这些判据集中在纯函数里，页面与探针共用同一份真相。
//
// 输入形状来自 GET /ui/status-monitor.json 的 matrix.rows[].cells[]，也兼容把多格
// 相加后的汇总（字段同名，见 aggregateCells）。

// 参与着色的最小样本数。请求数只有一两次的格子，成功率非 0 即 100、首字也充满
// 抖动；把它们涂成红/绿等于把噪声当结论。少于它一律 unknown（浅灰）。
export const MIN_HEALTH_SAMPLES = 3;

// 分档阈值：>= good 绿、>= warn 黄、其余红。
export const HEALTH_THRESHOLDS = { good: 80, warn: 50 };

// 首字延迟的满分/零分锚点（毫秒），两端之间线性插值。
export const TTFT_GOOD_MS = 1500;
export const TTFT_BAD_MS = 8000;

// 缓存率的满分锚点：命中 30% 即视为满分。再往上对"健康"没有额外信息量，
// 却会让满分门槛随流量结构漂移。
export const CACHE_FULL_RATE = 0.3;

// 综合模式下三项的权重。成功率最高：它直接回答"能不能用"；首字与缓存是体验项。
export const OVERALL_WEIGHTS = { success: 0.6, ttft: 0.2, cache: 0.2 };

export const HEALTH_BAND_LABELS = {
  good: "健康",
  warn: "注意",
  bad: "异常",
  unknown: "样本不足",
  "no-traffic": "无流量",
};

export const HEALTH_BAND_HINTS = {
  good: `健康分 ≥ ${HEALTH_THRESHOLDS.good}`,
  warn: `健康分 ${HEALTH_THRESHOLDS.warn}–${HEALTH_THRESHOLDS.good - 1}`,
  bad: `健康分 < ${HEALTH_THRESHOLDS.warn}`,
  unknown: `请求数少于 ${MIN_HEALTH_SAMPLES} 次，或该口径缺少读数`,
  "no-traffic": "该时间段没有请求",
};

// 矩阵维度的顺序与后端 group_by 取值一一对应；页面与探针共用这份清单，避免
// 两处各写一套 id 后出现"按钮按下的和请求发出去的不是同一个维度"。
export const GROUP_BY_MODES = [
  { id: "provider", label: "供应商", hint: "按供应商聚合" },
  { id: "provider_model", label: "供应商·模型", hint: "按供应商与上游模型聚合" },
  { id: "provider_key", label: "供应商·Key", hint: "按供应商与上游 Key 聚合" },
];

// 健康显示口径。success 的按钮文案是「错误率」：用户想看的通常是"错多少"，
// 而着色仍然按成功率（越高越绿）——文案与颜色方向必须在这个注释里说清楚，
// 否则会出现"错误率高反而变绿"的误读。
export const HEALTH_MODES = [
  { id: "overall", label: "综合", hint: "成功率 60% + 首字 20% + 缓存率 20%" },
  { id: "success", label: "错误率", hint: "按成功率着色：成功率越高越绿" },
  { id: "ttft", label: "首Token", hint: "按平均首字延迟着色：≤1.5s 满分，≥8s 零分" },
  { id: "cache", label: "缓存率", hint: "按缓存命中率着色：命中 30% 即满分" },
];

export const HEALTH_MODE_MAP = Object.fromEntries(HEALTH_MODES.map((item) => [item.id, item]));

function number(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

// cellMetrics 把一格（或一格汇总）换算成页面要用的读数。
//
// 两个刻意的"null 而不是 0"：
//   - successRate 在无请求时为 null：0/0 没有意义，显示 0% 会谎报"全失败"；
//   - cacheRate 在没有 prompt_tokens 时为 null：缓存率的分母不存在，不能当 0。
// 首字则是另一回事：total_first_token_ms 为 0 通常表示"这批请求没有首字读数"
// （非流式、或历史行没记），0ms 的平均首字不存在，所以单列 hasFirstToken。
export function cellMetrics(cell) {
  const requests = Math.max(0, number(cell?.requests));
  const successes = Math.max(0, number(cell?.successes));
  const failures = cell?.failures === undefined || cell?.failures === null
    ? Math.max(0, requests - successes)
    : Math.max(0, number(cell.failures));
  const promptTokens = Math.max(0, number(cell?.prompt_tokens));
  const cachedTokens = Math.max(0, number(cell?.cached_tokens));
  const totalFirstTokenMS = Math.max(0, number(cell?.total_first_token_ms));
  const successRate = requests > 0 ? successes / requests : null;
  const hasFirstToken = requests > 0 && totalFirstTokenMS > 0;
  return {
    requests,
    successes,
    failures,
    promptTokens,
    cachedTokens,
    totalFirstTokenMS,
    successRate,
    // 平均首字按请求数加权；没有首字读数时给 null（调用方显示"—"）。
    avgTtft: hasFirstToken ? totalFirstTokenMS / requests : null,
    hasFirstToken,
    // 缓存率 = 缓存 Token / 输入 Token；没有输入 Token 就没有这个比率。
    cacheRate: promptTokens > 0 ? cachedTokens / promptTokens : null,
  };
}

// aggregateCells 把一行的所有格子加成一个格：行摘要（请求数 / 成功率 / 健康分）
// 必须与格子同口径——对每格比率再平均会让样本少的格子权重过大。
export function aggregateCells(cells) {
  const total = {
    requests: 0,
    successes: 0,
    failures: 0,
    prompt_tokens: 0,
    cached_tokens: 0,
    total_first_token_ms: 0,
    total_duration_ms: 0,
    complete: true,
  };
  for (const cell of cells || []) {
    total.requests += number(cell?.requests);
    total.successes += number(cell?.successes);
    total.failures += number(cell?.failures);
    total.prompt_tokens += number(cell?.prompt_tokens);
    total.cached_tokens += number(cell?.cached_tokens);
    total.total_first_token_ms += number(cell?.total_first_token_ms);
    total.total_duration_ms += number(cell?.total_duration_ms);
    // 只要有一个尾桶还在累加，整行汇总就是"部分区间"——但这里只用于行摘要，
    // 不参与着色，所以保留标记供调用方自行决定要不要提示。
    if (cell?.complete === false) total.complete = false;
  }
  return total;
}

// 首字延迟得分：<=1.5s 满分，>=8s 零分，之间线性。
function ttftScore(metrics) {
  if (!metrics.hasFirstToken) return null;
  const ms = metrics.avgTtft;
  if (ms <= TTFT_GOOD_MS) return 100;
  if (ms >= TTFT_BAD_MS) return 0;
  return (100 * (TTFT_BAD_MS - ms)) / (TTFT_BAD_MS - TTFT_GOOD_MS);
}

function cacheScore(metrics) {
  if (metrics.cacheRate === null) return null;
  return Math.min(metrics.cacheRate / CACHE_FULL_RATE, 1) * 100;
}

// healthScore 返回 0–100 的健康分；null 表示这个口径下没有可用读数（显示为 unknown）。
//
// overall 的取舍：首字 / 缓存率缺失时该项按 0 分计，而不是按满分或跳过。
//   - 按满分：一个从没测过延迟的渠道会显示成健康，等于用"没数据"冒充"很好"；
//   - 跳过：权重会悄悄从 20% 摊到其它项，同一份数据在不同渠道上分量不同。
// 代价是只有成功率读数的渠道最高只能到 60 分（warn）——这恰好提示"还有口径没数据"。
export function healthScore(cell, mode = "overall") {
  const metrics = cellMetrics(cell);
  if (metrics.requests <= 0) return null;
  if (metrics.requests < MIN_HEALTH_SAMPLES) return null;
  // rounds 到整数：界面只显示整数，分档也按整数判，避免 79.6 与 80.2 在显示上
  // 都是 "80" 却落在不同档位。
  const success = metrics.successRate === null ? null : metrics.successRate * 100;
  const ttft = ttftScore(metrics);
  const cache = cacheScore(metrics);
  let score;
  switch (mode) {
    case "success":
      score = success;
      break;
    case "ttft":
      score = ttft;
      break;
    case "cache":
      score = cache;
      break;
    default:
      score = (OVERALL_WEIGHTS.success * success)
        + (OVERALL_WEIGHTS.ttft * (ttft ?? 0))
        + (OVERALL_WEIGHTS.cache * (cache ?? 0));
  }
  return score === null || score === undefined ? null : Math.round(score);
}

// healthBand 返回 good / warn / bad / unknown / no-traffic。
//   - 无请求 → no-traffic（灰）；
//   - 样本不足 → unknown（浅灰）；
//   - 该口径缺读数（ttft 没有首字、cache 没有输入 Token）→ unknown。
export function healthBand(cell, mode = "overall") {
  const metrics = cellMetrics(cell);
  if (metrics.requests <= 0) return "no-traffic";
  if (metrics.requests < MIN_HEALTH_SAMPLES) return "unknown";
  if (mode === "ttft" && !metrics.hasFirstToken) return "unknown";
  if (mode === "cache" && metrics.cacheRate === null) return "unknown";
  const score = healthScore(cell, mode);
  if (score === null) return "unknown";
  if (score >= HEALTH_THRESHOLDS.good) return "good";
  if (score >= HEALTH_THRESHOLDS.warn) return "warn";
  return "bad";
}

export function healthBandLabel(band) {
  return HEALTH_BAND_LABELS[band] || HEALTH_BAND_LABELS.unknown;
}

// —— 行标签 ——

// rowLabel 按维度生成一行的显示名。
//
// provider_id 可能为空：升级前的历史行、不走 proxy 的写入路径都没有归因。显示
// 「未归属」而不是留白或回填一个假名字——那正是"这段流量没有可归属的供应商"。
export function rowLabel(row, groupBy = "provider") {
  const provider = row?.provider_id || "未归属";
  if (groupBy === "provider_model") {
    const detail = row?.upstream_model_id || "未归属";
    return { provider, detail, label: `${provider} / ${detail}` };
  }
  if (groupBy === "provider_key") {
    const detail = row?.key_name || "未归属";
    return { provider, detail, label: `${provider} / ${detail}` };
  }
  return { provider, detail: "", label: provider };
}

// —— 格子的可读提示 ——

const CELL_TIME = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit",
  hour: "2-digit", minute: "2-digit", hour12: false,
});
const CELL_CLOCK = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit", hour12: false,
});
const CELL_DAY = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit",
});
const CELL_FULL = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit",
});

function toDate(value) {
  if (!value) return null;
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function formatCellTime(value) {
  const date = toDate(value);
  return date ? CELL_TIME.format(date) : "—";
}

// formatCellRange 写"起 – 止"：同一天只写时刻，跨天补月日，跨年补年份。
// 桶宽可能是 15 秒到 1 天，两种极端都读得懂。
function formatCellRange(startedAt, endedAt) {
  const start = toDate(startedAt);
  const end = toDate(endedAt);
  if (!start) return "—";
  if (!end) return CELL_TIME.format(start);
  const sameDay = CELL_FULL.format(start) === CELL_FULL.format(end);
  const endText = sameDay ? CELL_CLOCK.format(end) : CELL_DAY.format(end);
  return `${CELL_TIME.format(start)} – ${endText}`;
}

function formatCountText(value) {
  return Number(value || 0).toLocaleString("zh-CN");
}

function formatPercentText(ratio) {
  if (ratio === null || ratio === undefined || !Number.isFinite(Number(ratio))) return "—";
  const percent = Number(ratio) * 100;
  if (percent > 0 && percent < 0.1) return "<0.1%";
  if (percent > 99.9 && percent < 100) return ">99.9%";
  return `${percent.toFixed(1)}%`;
}

function formatDurationText(ms) {
  const value = Number(ms);
  if (!Number.isFinite(value)) return "—";
  if (value >= 10000) return `${(value / 1000).toFixed(1)}s`;
  if (value >= 1000) return `${(value / 1000).toFixed(2)}s`;
  return `${Math.round(value)}ms`;
}

// formatHealthTooltip 生成一格的多行 title：颜色不是唯一信息，数值必须都写出来。
// 行说明与页面卡脚同源（阈值、样本数），悬停里再重复一遍是刻意的——用户看单格
// 时不该被要求先记住图例。
export function formatHealthTooltip(cell, mode = "overall") {
  const metrics = cellMetrics(cell);
  const band = healthBand(cell, mode);
  const score = healthScore(cell, mode);
  const modeLabel = (HEALTH_MODE_MAP[mode] || HEALTH_MODE_MAP.overall).label;
  const lines = [
    `时段 ${formatCellRange(cell?.started_at, cell?.ended_at)}${cell?.complete === false ? "（累加中，读数偏低）" : ""}`,
    `着色口径 ${modeLabel}：${healthBandLabel(band)}${score === null ? "" : `（健康分 ${score}）`}`,
    `请求 ${formatCountText(metrics.requests)} 次 · 成功 ${formatCountText(metrics.successes)} · 失败 ${formatCountText(metrics.failures)}`,
    `成功率 ${formatPercentText(metrics.successRate)}`,
    `平均首字 ${metrics.hasFirstToken ? formatDurationText(metrics.avgTtft) : "无读数"}`,
    `缓存率 ${formatPercentText(metrics.cacheRate)}`,
  ];
  return lines.join("\n");
}

// —— 错误原因归因 ——

// 分档顺序固定：认证 → 限流 → 请求错误 → 上游故障 → 其它 4xx → 连接失败。
// 不按次数排序，这样每次刷新各档位置不变，才能对比"这一档是不是涨了"。
export const ERROR_REASON_ROWS = [
  {
    id: "auth", label: "认证失败", tone: "bad",
    hint: "HTTP 401 / 403：上游 Key 失效或没有该模型的权限",
  },
  {
    id: "rate", label: "限流", tone: "warn",
    hint: "HTTP 429：上游限流或配额用尽",
  },
  {
    id: "request", label: "请求错误", tone: "warn",
    hint: "HTTP 400 / 404 / 413 / 422：请求本身不被上游接受",
  },
  {
    id: "upstream", label: "上游故障", tone: "bad",
    hint: "HTTP 5xx：上游服务端错误",
  },
  {
    id: "other4xx", label: "其它 4xx", tone: "primary",
    hint: "未归入上面几类的 4xx 状态码",
  },
  {
    id: "no-status", label: "连接失败 / 无状态码", tone: "bad",
    hint: "没拿到上游状态码就失败的请求（连接失败、超时等）",
  },
];

function reasonOf(code) {
  if (code === 401 || code === 403) return "auth";
  if (code === 429) return "rate";
  if (code === 400 || code === 404 || code === 413 || code === 422) return "request";
  if (code >= 500 && code < 600) return "upstream";
  if (code >= 400 && code < 500) return "other4xx";
  return null;
}

// errorReasons 从快照的 status_codes + failures 归出错误原因。
//
// 口径要点：
//   - 2xx 只算成功，不进错误分类（status_codes 里本来就有 2xx，直接跳过）；
//   - 无状态码的失败 = failures - 非2xx状态码合计。不能为负：两条读数在聚合边界
//     上可能短暂不一致，负条会让人以为看板坏了；
//   - 3xx / 1xx 在业务上不是失败，但如果 status_codes 里出现且非 2xx，它们确实
//     占了"有状态码"的部分，单独记在 unclassified 里，不硬塞进「其它 4xx」。
export function errorReasons(total) {
  const failures = Math.max(0, number(total?.failures));
  const counts = { auth: 0, rate: 0, request: 0, upstream: 0, other4xx: 0, "no-status": 0 };
  let coded = 0;
  let unclassified = 0;
  for (const [key, raw] of Object.entries(total?.status_codes || {})) {
    const code = Number(key);
    const count = number(raw);
    if (!Number.isFinite(code) || count <= 0) continue;
    // 2xx 是成功，不参与错误分档；其余都算"有状态码的失败"。
    if (code >= 200 && code < 300) continue;
    coded += count;
    const reason = reasonOf(code);
    if (reason) counts[reason] += count;
    else unclassified += count;
  }
  const noStatus = Math.max(0, failures - coded);
  counts["no-status"] = noStatus;
  return {
    failures,
    coded,
    // 有状态码但排在失败里（coded 可能大于 failures 的口径差）与无状态码分开暴露，
    // 卡脚据此解释"各档之和为什么可能小于失败总数"。
    unclassified,
    noStatus,
    rows: ERROR_REASON_ROWS.map((spec) => ({
      ...spec,
      count: counts[spec.id] || 0,
      ratio: failures > 0 ? (counts[spec.id] || 0) / failures : null,
    })),
  };
}
