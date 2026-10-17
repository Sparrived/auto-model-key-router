// 状态监控：渠道健康矩阵看板。
//
// 这一页回答的是"现在谁不太行"：把窗口切成一列列时间桶，每个渠道一行，按健康分
// 给格子着色。同屏的四块材料（KPI、矩阵、趋势、错误原因、模型明细）都来自**同一次**
// /ui/status-monitor.json 请求，因此时间窗、筛选条件与桶宽天然一致——把它们拆成
// 多条接口取数，迟早会出现"矩阵是 24 小时、趋势图是 1 小时"这类对不上号的读数。
//
// 矩阵的着色口径在 webui/status-math.js：页面只负责画，阈值与分档由纯函数给出，
// node 探针能直接断言同一套判据。

import {
  h, errorText, formatCount, formatCompact, formatDuration, formatPercent,
  formatRate, formatAxisTime,
} from "../dom.js";
import { api } from "../api.js";
import {
  metricFilterBar, selectedProviders, selectedAccessKeys,
} from "../metric-filter.js";
import {
  card, cardHead, stat, statGrid, statSkeleton, notice, badge, empty,
  skeleton, render, buttonNode, segmented, freshness, table, progressBar,
} from "../ui.js";
import { lineChart } from "../charts.js";
import { TIME_RANGES, bucketLabel, formatNumber } from "../chart-math.js";
import {
  GROUP_BY_MODES, HEALTH_MODES, HEALTH_MODE_MAP, HEALTH_THRESHOLDS, HEALTH_BAND_HINTS,
  MIN_HEALTH_SAMPLES, cellMetrics, healthScore, healthBand, healthBandLabel, aggregateCells,
  rowLabel, formatHealthTooltip, errorReasons,
} from "../status-math.js";

// 趋势卡的三条曲线。metricId 对应 chart-math 的 METRIC_MAP（success / firstToken / cache），
// 图表层的加权口径与矩阵同源，两处读数不会打架。
const TREND_MODES = [
  { id: "success", label: "成功率", hint: "每个桶的成功请求 / 全部请求" },
  { id: "firstToken", label: "首字", hint: "每个桶按请求数加权的平均首字延迟" },
  { id: "cache", label: "缓存率", hint: "每个桶缓存 Token / 输入 Token" },
];

const TREND_MODE_MAP = Object.fromEntries(TREND_MODES.map((item) => [item.id, item]));

// 模型明细表的列。sortValue 与 render 同源：排序键就是显示值，点两下列头不会
// 出现"按一个数排、看到的是另一个数"的错觉。
const MODEL_COLUMNS = [
  {
    key: "name", label: "模型",
    render: (row) => h("span.mono", { title: row.name }, row.name),
    sortValue: (row) => row.name,
  },
  {
    key: "success", label: "成功率", numeric: true,
    render: (row) => formatPercent(row.stats.successes, row.stats.requests),
    sortValue: (row) => (number(row.stats.requests) > 0 ? number(row.stats.successes) / number(row.stats.requests) : -1),
  },
  {
    key: "ttft", label: "平均首字", numeric: true,
    render: (row) => {
      const value = statsAvgTtft(row.stats);
      return value === null ? "—" : formatDuration(value);
    },
    sortValue: (row) => statsAvgTtft(row.stats) ?? -1,
  },
  {
    key: "cache", label: "缓存率", numeric: true,
    render: (row) => formatRate(statsCacheRate(row.stats)),
    sortValue: (row) => statsCacheRate(row.stats) ?? -1,
  },
  {
    key: "requests", label: "请求数", numeric: true,
    render: (row) => formatCount(row.stats.requests),
    sortValue: (row) => number(row.stats.requests),
  },
  {
    // 窗口平均 RPM：与 KPI 同一条算式（请求数 ÷ 窗口分钟数），但这里是逐模型的。
    // 不用快照里的 current_rpm：那是全局 60 秒滚动值，拆不到模型上。
    key: "rpm", label: "窗口平均 RPM", numeric: true,
    render: (row) => {
      const rpm = windowRpm(row.stats.requests);
      return rpm === null ? "—" : formatNumber(rpm, 2);
    },
    sortValue: (row) => windowRpm(row.stats.requests) ?? -1,
  },
];

const state = {
  hours: 24,
  // 矩阵维度切换要重新取数（后端按 group_by 聚合），健康显示只在本页重新着色。
  groupBy: "provider",
  healthMode: "overall",
  trendMetric: "success",
  data: null,
  dataKey: null,
  error: null,
  at: null,
  sort: { key: "requests", direction: "desc" },
};

let host = null;
let xtxRef = null;
let requestToken = 0;
// inflight 只用于**轮询去重**：上一次请求还没回来时，10 秒一次的 onTick 不该再叠一条。
// 用户主动切窗口/维度/筛选时不看它——那些操作必须立即发新请求，旧响应靠 token 丢弃。
let inflight = false;

function number(value) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function rangeLabel() {
  return TIME_RANGES.find((range) => range.hours === state.hours)?.label || `${state.hours} 小时`;
}

// 窗口小时数以**服务端回声**为准：请求的是 24，但后端可能因窗口对齐返回 24.0 或 null。
// 拿不到时退回用户选择，保证 RPM 的分母与页面标注一致。
function windowHours(data) {
  const hours = Number(data?.window?.hours);
  return Number.isFinite(hours) && hours > 0 ? hours : state.hours;
}

function bucketSecondsOf(data) {
  const seconds = number(data?.series?.bucket_seconds) || number(data?.bucket_seconds);
  return seconds > 0 ? seconds : 0;
}

function bucketLabelOf(seconds) {
  return seconds > 0 ? bucketLabel(seconds) : "未知桶宽";
}

// scopeKey 是"这份数据属于哪一组查询条件"：时间窗 + 维度 + 全局筛选。
// 进入页面、切条件、以及带着别页改过的筛选回来时，都靠它判断要不要丢弃旧读数。
function filterKeyOf(store) {
  return `${selectedProviders(store).join(",")}|${selectedAccessKeys(store).join(",")}`;
}

function scopeKey() {
  return `${state.hours}:${state.groupBy}:${filterKeyOf(xtxRef?.store)}`;
}

function windowRpm(requests) {
  const hours = windowHours(state.data);
  if (!(hours > 0)) return null;
  return number(requests) / (hours * 60);
}

function statsAvgTtft(stats) {
  const requests = number(stats?.requests);
  const avg = number(stats?.avg_first_token_ms);
  return requests > 0 && avg > 0 ? avg : null;
}

// statsCacheRate 优先用后端算好的 cached_token_rate；它缺失时才用分子分母重算。
// prompt_tokens 为 0 时返回 null（而不是后端的 0.0）——没有输入 Token 就没有
// "缓存命中率"这个读数，显示 0% 会谎报"缓存一条都没命中"。
function statsCacheRate(stats) {
  if (!(number(stats?.prompt_tokens) > 0)) return null;
  const rate = stats?.cached_token_rate;
  if (rate !== null && rate !== undefined && Number.isFinite(Number(rate))) return Number(rate);
  return number(stats?.cached_tokens) / number(stats.prompt_tokens);
}

function snapshotAvgTtft(total) {
  const requests = number(total?.requests);
  if (!(requests > 0)) return null;
  const avg = number(total?.avg_first_token_ms);
  if (avg > 0) return avg;
  // 快照的 avg_first_token_ms 是四舍五入后的整数；它缺省而 total 有读数时再除一次，
  // 避免 0.4ms 被舍成 0 后整格显示"—"。
  const totalMs = number(total?.total_first_token_ms);
  return totalMs > 0 ? totalMs / requests : null;
}

function snapshotCacheRate(total) {
  const requests = number(total?.requests);
  const promptTokens = number(total?.prompt_tokens);
  if (!(requests > 0) || !(promptTokens > 0)) return null;
  const rate = total?.cached_token_rate;
  if (rate !== null && rate !== undefined && Number.isFinite(Number(rate))) return Number(rate);
  return number(total?.cached_tokens) / promptTokens;
}

// —— 取数 ——
//
// silent 只被 onTick 使用：轮询在途时直接返回，不叠请求、不显示"正在读取"——
// 10 秒一次的自动刷新不该让界面闪。用户操作走默认值，永远立即发新请求。
async function load({ silent = false } = {}) {
  if (silent && inflight) return;
  const token = ++requestToken;
  inflight = true;
  const hours = state.hours;
  const groupBy = state.groupBy;
  const accessKeyIds = selectedAccessKeys(xtxRef?.store);
  const providerIds = selectedProviders(xtxRef?.store);
  // 归属键必须在**发请求前**取好并与 scopeKey() 同格式：请求回来时 store 可能又变了
  // （用户切到筛选），用那一刻的值会给数据贴上错误的归属。
  const filterKey = filterKeyOf(xtxRef?.store);
  try {
    const data = await api.statusMonitor({ hours, groupBy, accessKeyIds, providerIds });
    // 过期响应：期间用户又换了窗口/维度/筛选，丢弃。没有这道闸，慢请求回来会把
    // 新条件的页面覆盖成旧数据——而且看起来完全正常，最难排查。
    if (token !== requestToken) return;
    state.data = data;
    state.dataKey = `${hours}:${groupBy}:${filterKey}`;
    state.error = null;
    state.at = new Date().toISOString();
  } catch (error) {
    if (token !== requestToken) return;
    state.error = errorText(error);
  } finally {
    if (token === requestToken) inflight = false;
  }
  draw();
}

// resetData 丢掉与旧条件绑定的读数。切条件后先清空再取数：异步延迟的几百毫秒里，
// 屏幕上不能还留着上一组条件的矩阵——那比骨架更容易被当成新读数。
function resetData() {
  state.data = null;
  state.dataKey = null;
  state.error = null;
  state.at = null;
}

function reloadAndDraw() {
  resetData();
  draw();
  load();
}

export function renderStatus(context) {
  xtxRef = context;
  const { store } = context;
  host = h("div.stack");
  if (store.health?.local_auth_enabled && !store.authorized) {
    return h("div.stack", {}, notice("需要本地鉴权 Key 才能读取状态监控。", "warn"));
  }
  // 全局筛选可能是在别的页面改的：归属不符就先清掉，避免用旧条件下的矩阵冒充新筛选。
  if (state.data && state.dataKey !== scopeKey()) resetData();
  // 同步首绘必须绕过"已挂载"守卫（app.js 先拿节点、后 mount）；有缓存时先把缓存
  // 画出来，再在后台静默换成新数据，首绘不闪骨架。
  draw(true);
  load({ silent: Boolean(state.data) });
  context.onTick?.(() => load({ silent: true }));
  return host;
}

// —— 页头 ——
function pageHead() {
  const { store } = xtxRef;
  return h("div.page-head", {},
    h("div.page-title", {},
      h("h1", "状态监控"),
      h("p.sub", "本机各供应商 / 上游模型 / Key 的可用性、延迟与缓存健康度。"),
    ),
    h("div.page-actions", {},
      state.at ? freshness(state.at) : null,
      buttonNode("刷新", {
        variant: "text", small: true, iconName: "refresh",
        onClick: () => load(),
      }),
      // 全局筛选与时间窗并排：两者回答的都是"这批读数覆盖什么"。
      // 筛选变化会同时改变矩阵行与 KPI，必须立即重取，且旧读数先清掉。
      metricFilterBar(store, reloadAndDraw),
      segmented(
        TIME_RANGES.map((range) => ({ id: range.hours, label: range.short, title: range.label })),
        state.hours,
        (id) => { state.hours = Number(id); reloadAndDraw(); },
        { "aria-label": "选择统计窗口" },
      ),
    ),
  );
}

// —— KPI ——
function kpiTiles(data) {
  const total = data.snapshot?.total || {};
  const hours = windowHours(data);
  const requests = number(total.requests);
  const successes = number(total.successes);
  const failures = number(total.failures);
  const successRate = requests > 0 ? successes / requests : null;
  const cacheRate = snapshotCacheRate(total);
  const avgTtft = snapshotAvgTtft(total);
  const rpm = hours > 0 ? requests / (hours * 60) : null;

  return statGrid(
    stat("成功率", successRate === null ? "—" : formatPercent(successes, requests),
      requests > 0
        ? `${formatCount(successes)} 成功 · ${formatCount(failures)} 失败`
        : "窗口内无请求", {
        iconName: "check",
        // 95% 是告警线：低于它说明失败已经影响正常使用；99% 以上给绿色，
        // 两者之间的"小抖动"保持中性色，避免整屏常年红灯。
        tone: successRate === null ? null : successRate < 0.95 ? "bad" : successRate >= 0.99 ? "good" : null,
      }),
    stat("平均首字", avgTtft === null ? "—" : formatDuration(avgTtft),
      avgTtft === null ? "无首字读数（非流式或未记录）" : `按 ${formatCount(requests)} 次请求加权`, {
        iconName: "bolt",
      }),
    stat("缓存命中率", cacheRate === null ? "—" : formatRate(cacheRate),
      cacheRate === null
        ? "窗口内没有输入 Token 读数"
        : `缓存 ${formatCompact(total.cached_tokens)} / 输入 ${formatCompact(total.prompt_tokens)} Token`, {
        iconName: "filter",
        tone: cacheRate === null ? null : cacheRate >= 0.3 ? "good" : null,
      }),
    stat("窗口平均 RPM", rpm === null ? "—" : formatNumber(rpm, 2),
      `${formatCount(requests)} 次请求 / ${formatNumber(hours, 1)} 小时`, {
        iconName: "activity",
        unit: "次/分",
      }),
  );
}

// —— 健康矩阵 ——

// matrixContext 汇总矩阵渲染需要的一次性派生值：列数、时间轴、桶宽。
function matrixContext(data) {
  const rows = Array.isArray(data.matrix?.rows) ? data.matrix.rows : [];
  const columns = rows.reduce((max, row) => Math.max(max, row.cells?.length || 0), 0);
  const points = data.series?.points || [];
  const firstCells = rows[0]?.cells || [];
  // 时间轴优先用 series.points 的 started_at：后端保证它与矩阵同一桶网格；
  // 序列缺失时退回第一行的 cells（每格都带 started_at）。两者都缺就是空列。
  const timestamps = Array.from({ length: columns }, (_, index) =>
    points[index]?.started_at || firstCells[index]?.started_at || null);
  return {
    rows,
    columns,
    groupBy: data.matrix?.group_by || data.group_by || state.groupBy,
    bucketSeconds: bucketSecondsOf(data),
    timestamps,
    truncated: number(data.matrix?.rows_truncated),
    totalRequests: number(data.snapshot?.total?.requests),
  };
}

function matrixCard(data) {
  const ctx = matrixContext(data);
  const controls = h("div.toolbar.status-matrix-toolbar", {},
    h("span.toolbar-label", "维度"),
    segmented(
      GROUP_BY_MODES.map((item) => ({ id: item.id, label: item.label, title: item.hint })),
      state.groupBy,
      // 维度是后端聚合口径，切换必须重取；这里不能只重着色。
      (id) => { state.groupBy = id; reloadAndDraw(); },
      { "aria-label": "选择矩阵维度" },
    ),
    h("span.spacer"),
    h("span.toolbar-label", "健康显示"),
    segmented(
      HEALTH_MODES.map((item) => ({ id: item.id, label: item.label, title: item.hint })),
      state.healthMode,
      // 健康显示只改变上色口径，四份数据完全一样，本地重画即可，不发请求。
      (id) => { state.healthMode = id; draw(); },
      { "aria-label": "选择健康着色口径" },
    ),
  );

  const head = cardHead("渠道健康矩阵",
    badge(rangeLabel(), "muted"),
    badge(`${bucketLabelOf(ctx.bucketSeconds)}/格`, "muted"),
  );

  let body;
  if (!ctx.totalRequests) {
    body = empty("窗口内没有请求，健康矩阵暂时没有可着色的格子。", {
      icon: "activity",
      hint: "把时间窗拉长，或等下一次轮询。",
    });
  } else if (!ctx.rows.length) {
    // 有请求却没有任何行：多为历史行缺少供应商归因。空态要说明原因，
    // 否则会被当成"页面没取到数"。
    body = empty("窗口内有请求，但没有任何可归因的渠道数据。", {
      icon: "providers",
      hint: "历史行可能缺少供应商 / 模型归因，无法分组。",
    });
  } else {
    body = h("div.stack", {},
      h("div.status-matrix-scroll", {},
        h("div.status-matrix", {
          role: "img",
          "aria-label": matrixAriaLabel(ctx),
        },
          matrixAxis(ctx),
          ctx.rows.map((row) => matrixRow(row, ctx)),
        ),
      ),
      matrixLegend(),
    );
  }

  return card(head, controls,
    h("p.muted.status-matrix-hint", {},
      `按「${HEALTH_MODE_MAP[state.healthMode]?.label || "综合"}」着色：${HEALTH_MODE_MAP[state.healthMode]?.hint || ""}`),
    body,
    matrixFoot(ctx),
  );
}

function matrixAriaLabel(ctx) {
  const { good, warn } = HEALTH_THRESHOLDS;
  return `渠道健康矩阵，${formatCount(ctx.rows.length)} 行 × ${formatCount(ctx.columns)} 列，`
    + `按${HEALTH_MODE_MAP[state.healthMode]?.label || "综合"}健康分着色，`
    + `最近 ${rangeLabel()}；绿色表示健康（≥${good} 分），黄色表示注意（${warn}–${good - 1}），`
    + `红色表示异常（<${warn}），灰色表示无流量或样本不足。`;
}

// 时间轴标签：每格只有十几像素，逐格写时刻会糊成一条，所以定一个步长稀疏标注。
// 步长同时受"最多 8 个标签"和"至少 60px 间距"约束，短窗口下不会挤在一起。
function matrixAxis(ctx) {
  const { columns, bucketSeconds, timestamps } = ctx;
  const stride = Math.max(4, Math.ceil(columns / 8));
  const spanSeconds = columns * bucketSeconds;
  const cells = [];
  for (let index = 0; index < columns; index += 1) {
    const showLabel = index % stride === 0;
    const at = timestamps[index];
    cells.push(h("span.status-axis-cell", {}, showLabel && at
      ? formatAxisTime(at, spanSeconds)
      : null));
  }
  return h("div.status-matrix-axis", { "aria-hidden": "true" },
    h("span.status-row-head", {}, "渠道"),
    h("div.status-cells", {}, cells),
  );
}

function matrixRow(row, ctx) {
  const label = rowLabel(row, ctx.groupBy);
  const aggregate = aggregateCells(row.cells);
  const metrics = cellMetrics(aggregate);
  const score = healthScore(aggregate, state.healthMode);
  const band = healthBand(aggregate, state.healthMode);
  // 行摘要里"没有分数"有两种原因：样本不够，或是这个口径本来就没有读数
  // （首字未记录 / 没有输入 Token）。分开写，免得把"没数据"说成"量太少"。
  const scoreText = score !== null
    ? `健康分 ${score}`
    : metrics.requests < MIN_HEALTH_SAMPLES
      ? "样本不足"
      : `${HEALTH_MODE_MAP[state.healthMode]?.label || "综合"}无读数`;
  const summary = metrics.requests > 0
    ? `${formatCount(metrics.requests)} 次 · 成功率 ${formatPercent(metrics.successes, metrics.requests)} · ${scoreText}`
    : "窗口内无请求";

  const labelNode = h("div.status-row-label", { title: `${label.label}\n${summary}` },
    h(`span.status-band-dot.band-${band}`),
    h("div.status-row-text", {},
      h("span.status-row-name", {}, label.label),
      h("span.status-row-meta", {}, summary),
    ),
  );

  const cellNodes = (row.cells || []).map((cell) => {
    const cellBand = healthBand(cell, state.healthMode);
    return h(`span.status-cell.band-${cellBand}${cell.complete === false ? ".is-partial" : ""}`, {
      // 颜色不能是唯一信息：title 里给全时间、请求数、成功率、首字、缓存率与健康分。
      title: formatHealthTooltip(cell, state.healthMode),
    });
  });

  return h("div.status-matrix-row", {},
    labelNode,
    h("div.status-cells", {}, cellNodes),
  );
}

// 图例写清阈值与样本门槛：只有色块的话，色弱用户与第一次看这页的人都读不出含义。
function matrixLegend() {
  const order = ["no-traffic", "unknown", "good", "warn", "bad"];
  return h("div.status-legend", { role: "list", "aria-label": "健康分档说明" },
    order.map((band) => h("span.status-legend-item", { role: "listitem", title: HEALTH_BAND_HINTS[band] },
      h(`i.status-legend-swatch.band-${band}`),
      h("span", {}, band === "unknown"
        ? `样本不足（<${MIN_HEALTH_SAMPLES} 次）`
        : `${healthBandLabel(band)}（${bandHintText(band)}）`),
    )),
    h("span.status-legend-item", { role: "listitem" },
      h("i.status-legend-swatch.band-good.is-partial"),
      h("span", {}, "累加中的尾桶（读数偏低）"),
    ),
  );
}

function bandHintText(band) {
  const { good, warn } = HEALTH_THRESHOLDS;
  if (band === "good") return `≥${good}`;
  if (band === "warn") return `${warn}–${good - 1}`;
  if (band === "bad") return `<${warn}`;
  return "无请求";
}

function matrixFoot(ctx) {
  const notes = [
    `${bucketLabelOf(ctx.bucketSeconds)}/格，窗口内共 ${formatCount(ctx.columns)} 个时间桶；`,
    `每格请求数 ≥ ${MIN_HEALTH_SAMPLES} 次才参与着色，样本不足显示为浅灰。`,
  ];
  if (ctx.truncated > 0) {
    // rows_truncated 是后端主动截断的行数：不提示的话，用户会以为"矩阵里就是全部渠道"，
    // 而正确读法是"还有 N 行没画出来"。
    notes.push(`矩阵一次最多显示固定行数，另有 ${formatCount(ctx.truncated)} 行未显示——可按供应商筛选或缩短时间窗收窄范围。`);
  }
  return h("div.card-foot", {}, notes.join(" "));
}

// —— 健康趋势 ——
function trendCard(data) {
  const points = data.series?.points || [];
  const bucketSeconds = bucketSecondsOf(data);
  const metric = TREND_MODE_MAP[state.trendMetric] || TREND_MODES[0];
  const requests = number(data.snapshot?.total?.requests);

  return card(
    cardHead("健康趋势",
      badge(rangeLabel(), "muted"),
      badge(`${bucketLabelOf(bucketSeconds)}/点`, "muted"),
      h("div.head-tools", {},
        segmented(
          TREND_MODES.map((item) => ({ id: item.id, label: item.label, title: item.hint })),
          state.trendMetric,
          (id) => { state.trendMetric = id; draw(); },
          { "aria-label": "选择趋势指标" },
        ),
      ),
    ),
    h("p.muted", { style: { marginBottom: "12px" } }, metric.hint),
    points.length && requests
      ? lineChart({
          points,
          metricId: state.trendMetric,
          bucketSeconds,
          height: 240,
          ariaLabel: `${metric.label}趋势，最近 ${rangeLabel()}`,
        })
      : empty("窗口内没有请求，趋势暂时没有可画的读数。", { icon: "activity" }),
    h("div.card-foot", {},
      "比率型指标（成功率 / 缓存率）在无请求的桶上没有读数；首字按桶内请求数加权，不是对桶均值再平均。"),
  );
}

// —— 错误原因 ——
function errorCard(data) {
  const total = data.snapshot?.total || {};
  const reasons = errorReasons(total);
  const head = cardHead("错误原因",
    badge(rangeLabel(), "muted"),
    reasons.failures > 0 ? badge(`${formatCount(reasons.failures)} 次失败`, "bad") : null,
  );

  if (!reasons.failures) {
    return card(head, empty("窗口内没有失败请求。", {
      icon: "check",
      hint: "2xx 只计成功；这里统计非 2xx 状态码与拿不到状态码的失败。",
    }));
  }

  const rows = reasons.rows.filter((row) => row.count > 0);
  const body = h("div.stack.tight", {}, rows.map((row) => h("div.status-error-row", {},
    h("div.row-between", { title: row.hint },
      h("span", row.label),
      h("span.inline", {},
        h("strong", formatCount(row.count)),
        h("span.muted", ` · ${formatPercent(row.count, reasons.failures)}`),
      ),
    ),
    progressBar((row.count / reasons.failures) * 100, row.tone === "bad" ? "bad" : row.tone === "warn" ? "warn" : "primary"),
  )));

  const foot = [
    `失败合计 ${formatCount(reasons.failures)} 次；其中 ${formatCount(reasons.coded)} 次带非 2xx 状态码，`,
    `${formatCount(reasons.noStatus)} 次没有状态码（连接失败 / 超时等）。2xx 只计成功，不在这里。`,
  ];
  if (reasons.unclassified > 0) {
    foot.push(`另有 ${formatCount(reasons.unclassified)} 次非 2xx 状态码（如 3xx）未归入上面几类。`);
  }

  return card(head, body, h("div.card-foot", {}, foot.join("")));
}

// —— 模型明细 ——
function modelCard(data) {
  const entries = Object.entries(data.snapshot?.models || {})
    .map(([name, stats]) => ({ name, stats }))
    .filter((row) => number(row.stats.requests) > 0);
  const column = MODEL_COLUMNS.find((item) => item.key === state.sort.key);
  const sign = state.sort.direction === "asc" ? 1 : -1;
  const pick = column?.sortValue || ((row) => number(row.stats.requests));
  entries.sort((a, b) => {
    const left = pick(a);
    const right = pick(b);
    if (typeof left === "string" || typeof right === "string") {
      return sign * String(left).localeCompare(String(right), "zh-CN");
    }
    return sign * ((left ?? 0) - (right ?? 0));
  });

  return card(
    cardHead("模型明细",
      badge(`${formatCount(entries.length)} 个模型`, "muted"),
      badge(rangeLabel(), "muted"),
    ),
    table(MODEL_COLUMNS, entries, "窗口内没有模型调用记录。", {
      sort: state.sort,
      onSort: (key) => {
        const direction = state.sort.key === key && state.sort.direction === "desc" ? "asc" : "desc";
        state.sort = { key, direction };
        draw();
      },
    }),
    h("div.card-foot", {},
      "模型为本地路由名（model_id），按请求数降序；窗口平均 RPM = 该模型请求数 ÷ 窗口分钟数。"),
  );
}

// —— 页面组装 ——
function draw(firstPaint = false) {
  if (!host) return;
  if (!firstPaint && !host.isConnected) return;
  const { store } = xtxRef;

  const children = [pageHead()];

  if (store.connectionError) {
    children.push(notice(`无法连接 AMKR 服务：${store.connectionError}`, "error"));
  }
  if (state.error) {
    children.push(notice(
      state.data
        ? `状态监控刷新失败，下方显示的是上次成功读数：${state.error}`
        : `状态监控数据读取失败：${state.error}`,
      state.data ? "warn" : "error",
    ));
  }

  const data = state.data;
  if (!data) {
    if (state.error) {
      // 取数失败时不再画骨架：骨架表示"正在加载"，而这里已经知道失败了。
      // 只留错误通知 + 一个可重试的空态。
      children.push(empty("暂时读不到状态监控数据。", {
        icon: "alert",
        hint: "检查 AMKR 服务是否在线后重试。",
        action: buttonNode("重试", {
          variant: "secondary", small: true, iconName: "refresh",
          onClick: () => reloadAndDraw(),
        }),
      }));
    } else {
      children.push(statSkeleton(4));
      children.push(card(cardHead("渠道健康矩阵"), skeleton("chart")));
    }
    render(host, children);
    return;
  }

  children.push(kpiTiles(data));
  children.push(h("div.grid-12", {},
    // 矩阵是主卡，独占整行：桶一多就需要横向滚动，半宽放不下。
    h("div.col-12", {}, matrixCard(data)),
    // 趋势与错误原因各半宽：同一个窗口的两个侧面，并排才好对照"成功率掉了"是否
    // 对应某类错误上涨。<=860px 时两张卡都会塌成整行。
    h("div.col-6", {}, trendCard(data)),
    h("div.col-6", {}, errorCard(data)),
    h("div.col-12", {}, modelCard(data)),
  ));
  render(host, children);
}
