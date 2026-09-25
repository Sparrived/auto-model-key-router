// 概览：监控看板的首页。
//
// 数据来源分两类，必须说清楚，否则读数会被误读：
//   1. /metrics（小时=1）—— 窗口汇总与实时速率，KPI 用这里；
//   2. /metrics/series —— 时间序列，趋势图用这里。桶宽由 chart-math 选最小的
//      可行值（后端上限 500 点），所以 1 小时窗口是 15 秒一个桶。
// 两张图各自标注自己的时间窗与桶宽，避免"图上是 15 秒桶、数字是 1 小时窗口"混淆。

import {
  h, errorText, copyText, formatCount, formatCompact, formatDuration,
  formatPercent, formatRate, formatClockSeconds,
} from "../dom.js";
import { api } from "../api.js";
import {
  card, cardHead, stat, statGrid5, statSkeleton, notice, badge, empty, skeleton, render,
  buttonNode, segmented, freshness, progressBar, toast,
} from "../ui.js";
import { icon } from "../icons.js";
import { lineChart, stackedBars, donut, barList, legend, chartSummary, heatmap, sparkline } from "../charts.js";
import {
  TIME_RANGES, pickBucketSeconds, bucketLabel, METRIC_MAP, HEATMAP_METRICS,
  rank, statusGroups, formatPercentValue, formatCompactNumber, formatNumber,
  trend as computeTrend,
} from "../chart-math.js";
// 成本是派生读数：目录拿不到时 KPI 与排行卡显示"—"，其余看板不受影响。
import {
  loadPricing, currentIndex, lookupPrice, aggregateCost, sumCost, formatCost, formatPrice,
  requestCost,
} from "../pricing.js";

// 热力图固定看 7 天：它的价值就在于"周内节律"（工作日 vs 周末、白天 vs 夜间），
// 跟着页头的 1h/6h 窗口走就没有可比性了。桶宽固定半小时，正好一格一个桶
// （7 × 48 = 336 格，仍在后端 500 点上限内：168*3600/1800 + 1 = 337）。
export const HEATMAP_WINDOW_HOURS = 168;
export const HEATMAP_BUCKET_SECONDS = 1800;
// 尾桶是半小时聚合值，半分钟内不会变，没必要跟着 10 秒轮询重算 337 个桶。
const HEATMAP_TTL_MS = 60000;

// CALLER_TYPE_LABELS 把 caller_type 取值翻成中文。
//
// 三档：local（本机主 key）、workspace（工作空间推理凭据）、access_key（访问密钥）。
// 后两者是 Go 侧新增的（见 server/query.go 的 callerTypes）；原先的 visitor 档已随
// 访客模式删除，由 access_key 取代。
//
// **必须用查表而不是三元表达式**：原先写的是 `=== "visitor" ? "访客" : "本机"`，多一档
// 之后工作空间流量会被显示成「本机」——那正好是权限最高的一档，看板上混淆这两者会让
// 人误判谁在用这个实例。
//
// 兜底回显原始值：将来再加档时宁可看到 "unknown" 这种生词，也不要被悄悄归进某一档。
const CALLER_TYPE_LABELS = {
  local: "本机",
  workspace: "工作空间",
  access_key: "访问密钥",
};

// 页面级状态：时间范围与主图指标是用户选择，需在轮询重绘间保持。
const state = {
  hours: 1,
  metric: "rpm",
  series: null,
  seriesHours: null,
  seriesBucket: null,
  seriesError: null,
  // 本页自己按所选窗口取快照，而不是复用全局的 1 小时快照：
  // 否则把窗口切到 7 天时，折线是 7 天、排行与状态分布却仍是 1 小时，
  // 同一屏出现两个口径的"总量"。
  snapshot: null,
  snapshotHours: null,
  snapshotError: null,
  snapshotAt: null,
  loading: true,
  trendMode: "line",
  // 热力图独立于上面的窗口：自己的 7 天数据、自己的指标、自己的刷新节奏。
  heatMetric: "requests",
  heatPoints: null,
  heatError: null,
  heatAt: null,
  heatToken: 0,
  // 请求流：从「实时活动」移到这里，因为它回答的正是"此刻正在发生什么"。
  // 跟随概览的 1h/6h/24h 窗口，但只取最近若干条明细。
  requests: null,
  requestsError: null,
  requestsAt: null,
  streamFilter: "all",
};

let host = null;
let xtxRef = null;
let windowToken = 0;
let streamToken = 0;

function rangeLabel() {
  return TIME_RANGES.find((range) => range.hours === state.hours)?.label || `${state.hours} 小时`;
}

// 请求明细：与窗口同一 hours，保证"图上是 1 小时、列表里却是 24 小时"不会发生。
async function loadRequests() {
  const token = ++streamToken;
  try {
    const data = await api.requests({ hours: state.hours, limit: 40 });
    if (token !== streamToken) return;
    state.requests = data;
    state.requestsError = null;
    state.requestsAt = new Date().toISOString();
  } catch (error) {
    if (token !== streamToken) return;
    state.requestsError = errorText(error);
  }
  draw();
}

// 快照与序列必须同一个窗口、同一次请求周期内取，保证同屏数字自洽。
async function loadWindow(force = false) {
  if (!force && state.series && state.seriesHours === state.hours) return;
  const bucket = pickBucketSeconds(state.hours);
  const token = ++windowToken;
  const hours = state.hours;
  const [snapshot, series] = await Promise.all([
    api.metrics(hours).catch((error) => ({ __error: errorText(error) })),
    api.series(hours, bucket).catch((error) => ({ __error: errorText(error) })),
  ]);
  if (token !== windowToken) return; // 期间用户又换了范围，丢弃过期响应
  state.snapshotError = snapshot.__error || null;
  state.snapshot = snapshot.__error ? null : snapshot;
  state.seriesError = series.__error || null;
  state.series = series.__error ? null : (series.points || []);
  state.seriesHours = hours;
  state.snapshotHours = hours;
  state.seriesBucket = series.bucket_seconds || bucket;
  state.snapshotAt = new Date().toISOString();
  state.loading = false;
  draw();
}

// 热力图数据与主图分开取：它固定 7 天，且不需要每次都重算。
// TTL 对失败同样生效（heatAt 记录的是"上次尝试"），否则接口报错时每次轮询都会重试。
async function loadHeatmap(force = false) {
  if (!force && state.heatAt && Date.now() - Date.parse(state.heatAt) < HEATMAP_TTL_MS) return;
  const token = ++state.heatToken;
  const series = await api
    .series(HEATMAP_WINDOW_HOURS, HEATMAP_BUCKET_SECONDS)
    .catch((error) => ({ __error: errorText(error) }));
  if (token !== state.heatToken) return; // 已有更新的请求在途，丢弃过期响应
  state.heatError = series.__error || null;
  state.heatPoints = series.__error ? null : (series.points || []);
  state.heatAt = new Date().toISOString();
  draw();
}

// —— KPI 区 ——
// 实时速率（当前 RPM/TPM）来自 /metrics 的 60 秒滚动窗口，与所选统计窗口无关；
// 其余累计量都取自同一份窗口快照，保证同屏数字出自同一口径。
function kpiTiles(metrics, points, bucketSeconds) {
  const total = metrics.total || {};
  const successRatio = total.requests ? total.successes / total.requests : null;
  const cacheRatio = total.prompt_tokens ? total.cached_tokens / total.prompt_tokens : null;
  // retries 是 SUM(retried)，每行一条尝试记录，所以分母同样用 requests（= COUNT(*)）。
  const retryRatio = total.requests ? total.retries / total.requests : null;
  const currentRpm = metrics.current_rpm ?? 0;
  const currentTpm = metrics.current_tpm ?? 0;
  // 环比只看"已完结"的桶，否则末尾残桶会把结论拖偏。
  const rpmTrend = computeTrend((points || []).map((point) => ({
    value: point.complete === false ? null : (point.requests || 0) * (60 / (bucketSeconds || 60)),
  })));

  const statusTone = metrics.router_status === "red" ? "bad"
    : metrics.router_status === "yellow" ? "warn" : null;

  return statGrid5(
    stat("当前 RPM", formatCount(currentRpm), `近 ${metrics.rate_window_seconds || 60} 秒窗口`, {
      unit: "次/分", iconName: "bolt", tone: statusTone,
    }),
    stat("当前 TPM", formatCompact(currentTpm), "近 60 秒 Token 速率", {
      unit: "Token/分", iconName: "activity",
    }),
    stat("窗口请求", formatCount(total.requests), `最近 ${rangeLabel()}`, {
      iconName: "layers", points: points || [], metricId: "rpm", bucketSeconds,
      // 流量涨跌本身不分好坏，用中性色；否则一次正常高峰会被标成红色告警。
      trendChange: rpmTrend?.change ?? null, trendPolarity: "neutral",
    }),
    stat("成功率", successRatio === null ? "-" : formatPercentValue(successRatio, 1),
      `${formatCount(total.successes)} 成功 · ${formatCount(total.failures)} 失败`, {
        iconName: "check",
        tone: successRatio !== null && successRatio < 0.95 ? "bad" : null,
      }),
    // 重试率补的是成功率盖住的那一半：成功率只说"最终成没成"，
    // 上游抖动到靠重试兜住时它照样接近 100%，这里才看得出来。
    // 分母用 requests 与成功率同源，两个数字可以直接对读。
    stat("重试率", retryRatio === null ? "-" : formatPercentValue(retryRatio, 1),
      retryRatio === null
        ? "窗口内无请求"
        : `${formatCount(total.retries)} 次重试 / ${formatCount(total.requests)} 次请求`, {
        iconName: "refresh",
        // 重试率升高是坏事，用默认的 inverse 极性（涨=红）；>10% 直接把瓦片标红。
        tone: retryRatio !== null && retryRatio > 0.1 ? "bad"
          : retryRatio !== null && retryRatio > 0.02 ? "warn" : null,
      }),
    stat("Token 用量", formatCompact(total.total_tokens),
      `输入 ${formatCompact(total.prompt_tokens)} · 输出 ${formatCompact(total.completion_tokens)}`, {
        iconName: "layers", points: points || [], metricId: "tpm", bucketSeconds,
      }),
    stat("缓存命中率", cacheRatio === null ? "-" : formatPercentValue(cacheRatio, 1),
      `缓存 ${formatCompact(total.cached_tokens)} Token`, {
        iconName: "filter",
        tone: cacheRatio !== null && cacheRatio > 0.3 ? "good" : null,
      }),
    stat("平均耗时", total.requests ? formatDuration(total.avg_duration_ms) : "-",
      total.requests ? `最快 ${formatDuration(total.min_duration_ms)} · 最慢 ${formatDuration(total.max_duration_ms)}` : "窗口内无请求", {
        iconName: "clock",
      }),
    stat("平均首字", total.requests && total.avg_first_token_ms ? formatDuration(total.avg_first_token_ms) : "-",
      `${formatCount(metrics.active_requests ?? 0)} 个请求进行中`, {
        iconName: "activity",
      }),
    costTile(metrics),
  );
}

// costTile 是"估算成本"KPI。
//
// 放在最后而不是嵌进上面几项之间：它是本项目的扩展读数（参照实现只记 token），
// 且口径与其余 KPI 不同——金额由 models.dev 的外部单价折算，会随目录更新而变。
//
// 三个"不撒谎"的规则：
//   - 目录没就绪 → 显示 "—"，不显示 $0；
//   - 一个上游模型都没匹配到 → 显示 "—"，并把无定价的原因写进 hint；
//   - 部分匹配 → 显示金额，但 hint 里明确"x/y 项有定价"，让人知道这不是全量。
function costTile(metrics) {
  const index = currentIndex();
  const upstream = metrics.upstream_models || {};
  const names = Object.keys(upstream);
  if (!index) {
    return stat("估算成本", "—", "价格目录尚未就绪（models.dev）", {
      iconName: "cost",
    });
  }
  const priced = names.filter((name) => lookupPrice(index, name)).length;
  const { total: amount } = sumCost(index, upstream);
  if (!priced) {
    return stat("估算成本", "—",
      names.length ? `${names.length} 个上游模型都没有匹配到单价` : "窗口内无请求", {
        iconName: "cost",
      });
  }
  const hint = priced === names.length
    ? `${rangeLabel()} · 全部 ${priced} 个上游模型已计价`
    : `${rangeLabel()} · 仅 ${priced}/${names.length} 个上游模型有定价`;
  return stat("估算成本", formatCost(amount), hint, {
    iconName: "cost",
    tone: priced === names.length ? "good" : "warn",
  });
}

// —— 主趋势图 ——
function trendCard() {
  const points = state.series || [];
  const bucketSeconds = state.seriesBucket || pickBucketSeconds(state.hours);
  const metric = METRIC_MAP[state.metric] || METRIC_MAP.rpm;
  const { total, latest } = chartSummary({ points, metricId: state.metric, bucketSeconds, windowHours: state.hours });

  const body = state.seriesError
    ? notice(`趋势数据读取失败: ${state.seriesError}`, "error")
    : state.loading && !points.length
      ? skeleton("chart")
      : h("div.stack", {},
          h("div.chart-readout", {},
            h("span.readout-main", {},
              state.metric === "tokens" ? formatCompactNumber(total ?? 0) : formatNumber(total ?? 0, 1),
              h("small", state.metric === "tokens" ? "Token" : metric.unit),
            ),
            h("span.readout-note", {},
              state.metric === "tokens"
                ? `最近 ${rangeLabel()}合计`
                : `最近 ${rangeLabel()}平均 · 最新 ${metric.format(latest ?? 0)}`),
            h("span.spacer"),
            h("span.readout-note", {}, `${bucketLabel(bucketSeconds)}/点 · 共 ${points.length} 点`),
          ),
          lineChart({
            points, metricId: state.metric, bucketSeconds, height: 280,
            ariaLabel: `${metric.label}趋势，最近 ${rangeLabel()}`,
          }),
        );

  return card(
    cardHead("流量趋势",
      badge(`${rangeLabel()}窗口`, "muted"),
      h("div.head-tools", {},
        segmented(
          [{ id: "rpm", label: "请求速率" }, { id: "tpm", label: "Token 速率" },
           { id: "tokens", label: "Token 用量" }, { id: "latency", label: "耗时" },
           { id: "success", label: "成功率" }],
          state.metric,
          (id) => { state.metric = id; draw(); },
          { "aria-label": "选择趋势指标" },
        ),
      ),
    ),
    h("p.muted", { style: { marginBottom: "12px" } }, metric.hint),
    body,
  );
}

// —— 构成与分布 ——
// 用窗口快照而不是序列求和：这两个数字会和 KPI 瓦片并排出现，
// 不同来源的小数位差异会让人怀疑数据不准。
function compositionCard(metrics) {
  const total = metrics.total || {};
  const prompt = total.prompt_tokens || 0;
  const completion = total.completion_tokens || 0;
  const grandTotal = prompt + completion;
  return card(
    cardHead("Token 构成", badge(rangeLabel(), "muted")),
    h("div.stack", {},
      donut({
        ratio: grandTotal ? completion / grandTotal : null,
        label: "输出占比",
        tone: "secondary",
        size: 152,
        caption: `合计 ${formatCompact(grandTotal)} Token`,
      }),
      legend([
        { label: "输入", value: formatCompact(prompt), tone: "primary" },
        { label: "输出", value: formatCompact(completion), tone: "secondary" },
        { label: "缓存读", value: formatCompact(total.cached_tokens || 0), tone: "neutral" },
      ]),
    ),
  );
}

function statusCard(metrics) {
  const groups = statusGroups(metrics.total?.status_codes || {});
  const total = groups.ok + groups.client + groups.server + groups.other;
  const rows = [
    { label: "2xx 成功", value: groups.ok, tone: "good" },
    { label: "4xx 客户端错误", value: groups.client, tone: "warn" },
    { label: "5xx 服务端错误", value: groups.server, tone: "bad" },
    { label: "其它状态", value: groups.other, tone: "neutral" },
  ].filter((row) => row.value > 0);

  return card(
    cardHead("响应状态分布", badge(`最近 ${rangeLabel()}`, "muted")),
    total
      ? h("div.stack", {},
          h("div.status-bar", { role: "img", "aria-label": "响应状态占比" },
            rows.map((row) => h("span", {
              class: `status-seg tone-${row.tone}`,
              style: { flex: String(row.value) },
              title: `${row.label} ${formatCount(row.value)}`,
            }))),
          h("div.stack.tight", {}, rows.map((row) =>
            h("div.row-between", {},
              h("span.inline", {},
                h("i", { class: `swatch tone-${row.tone === "good" ? "secondary" : row.tone === "neutral" ? "neutral" : row.tone}` }),
                row.label,
              ),
              h("span.inline", {},
                h("strong", formatCount(row.value)),
                h("span.muted", ` · ${formatPercent(row.value, total)}`),
              ),
            ))),
        )
      : empty("窗口内没有带状态码的请求。", { icon: "activity" }),
  );
}

// 请求流转：本次窗口内的"尝试"如何收敛成成功。
// 刻意不用箭头列：那会让人误读成顺序漏斗（请求→重试→失败→成功），
// 而成功率/重试率/失败率是同一批请求的三个侧面，用横向占比条最诚实。
function waterfallCard(metrics) {
  const total = metrics.total || {};
  const requests = total.requests || 0;
  const retries = total.retries || 0;
  const failures = total.failures || 0;
  const successes = total.successes || 0;

  const rows = [
    { label: "成功", value: successes, tone: "secondary", hint: "上游返回 2xx" },
    { label: "重试", value: retries, tone: "warn", hint: "同一次调用换了 Key/上游再试" },
    { label: "失败", value: failures, tone: "bad", hint: "所有尝试均未成功" },
  ];

  return card(
    cardHead("请求结果", badge("按上游尝试计数", "muted"), badge(`最近 ${rangeLabel()}`, "muted")),
    h("div.flow-total", {},
      h("span.flow-total-value", formatCount(requests)),
      h("span.flow-total-label", "次上游尝试"),
    ),
    h("div.flow-rows", {}, rows.map((row) => {
      const ratio = requests ? row.value / requests : 0;
      return h("div.flow-row", {},
        h("div.flow-row-head", {},
          h("span.inline", {}, h("i", { class: `swatch tone-${row.tone === "secondary" ? "secondary" : row.tone}` }), row.label),
          h("span.inline", {},
            h("strong", formatCount(row.value)),
            h("span.muted", ` · ${formatPercent(row.value, requests)}`),
          ),
        ),
        progressBar(ratio * 100, row.tone === "secondary" ? "good" : row.tone),
        h("span.flow-row-hint", row.hint),
      );
    })),
    h("div.card-foot", {},
      `平均耗时 ${formatDuration(total.avg_duration_ms)} · 缓存率 ${formatRate(total.cached_token_rate)}`),
  );
}

function rankingCard(title, entries, keyLabel, options = {}) {
  const rows = rank(entries, { limit: options.limit || 6, value: options.value });
  return card(
    cardHead(title, badge(`${Object.keys(entries || {}).length} 项`, "muted")),
    barList(rows, {
      tone: options.tone || "primary",
      emptyText: `窗口内没有${keyLabel}数据。`,
      format: options.format || ((row) => `${formatCount(row.value)} 次`),
    }),
  );
}

// costRankingCard 是"模型成本排行"。
//
// 按 upstream_models 计价（model_id 是本地路由名，价格表里没有它），用金额排序而不是
// 请求数或 token 数——便宜模型跑一万次也可能不如贵模型跑十次花钱多，这正是这张卡要
// 暴露的信息。无定价的条目不计入也不显示为 $0。
function costRankingCard(metrics) {
  const index = currentIndex();
  const upstream = metrics.upstream_models || {};
  if (!index) {
    return card(
      cardHead("模型成本排行", badge("无定价", "muted")),
      empty("价格目录尚未就绪。", { icon: "cost", hint: "服务端会定期从 models.dev 刷新目录。" }),
    );
  }
  const rows = [];
  for (const [name, stats] of Object.entries(upstream)) {
    const cost = aggregateCost(index, name, stats);
    if (cost === null || cost <= 0) continue;
    rows.push({ name, stats, value: cost });
  }
  rows.sort((a, b) => b.value - a.value);
  const unpriced = Object.keys(upstream).length - rows.length;
  return card(
    cardHead("模型成本排行",
      badge(`${rows.length} 项`, "muted"),
      unpriced > 0 ? badge(`${unpriced} 项无定价`, "warn") : null),
    barList(rows.slice(0, 6), {
      tone: "primary",
      emptyText: "窗口内没有匹配到单价的用量。",
      format: (row) => formatCost(row.value),
    }),
    unpriced > 0
      ? h("div.card-foot", {}, `有 ${unpriced} 个上游模型未匹配到 models.dev 单价，未计入。`)
      : null,
  );
}

// —— 统一模型入口卡 ——
function unifiedCard() {
  const { store, navigate } = xtxRef;
  const unified = store.health?.unified_model;
  const target = unified?.default?.primary;
  const description = unified
    ? (target?.key ? `固定 Key · ${target.key}` : "自动路由（按 Key 池顺序）")
    : "未启用";
  return h("div.card.is-lift", {
    role: "link", tabindex: "0", "aria-label": "查看统一模型配置",
    onClick: () => navigate("unified"),
    onKeydown: (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); navigate("unified"); } },
  },
    cardHead("统一模型入口",
      unified ? badge("已启用", "good") : badge("未启用", "muted"),
      h("div.head-tools", {}, icon("chevron", { size: 18, class: "muted" })),
    ),
    h("div.stack.tight", {},
      h("div.unified-target", {},
        h("span.muted", "主模型"),
        h("strong", target?.model || "—"),
      ),
      h("div", {}, h("span.muted", "路由方式："), description),
      unified?.default?.fallback?.model
        ? h("div", {}, h("span.muted", "回退模型："), h("span.mono", unified.default.fallback.model))
        : h("div.muted", "未配置回退模型"),
      unified?.image?.primary?.model
        ? h("div", {}, h("span.muted", "图像模型："), h("span.mono", unified.image.primary.model))
        : null,
      unified?.embeddings?.primary?.model
        ? h("div", {}, h("span.muted", "嵌入模型："), h("span.mono", unified.embeddings.primary.model))
        : null,
    ),
    h("div.card-foot", {},
      h("span", `可用模型 ${(store.health?.models || []).length} 个`),
    ),
  );
}

function runtimeCard() {
  const { store, refreshHealth } = xtxRef;
  const health = store.health || {};
  const native = Object.values(health.native_endpoint_states || {});
  const ok = native.filter((state) => state?.supported).length;
  const rows = [
    ["监听地址", health.base_url || "—"],
    ["版本", health.version ? `v${health.version}` : "—"],
    ["配置路径", health.config_path || "—"],
    ["本地鉴权", health.local_auth_enabled ? "已启用" : "未启用"],
  ];
  return card(
    cardHead("运行状态",
      health.local_auth_enabled ? badge("鉴权已启用", "good") : badge("鉴权未启用", "warn"),
      h("div.head-tools", {},
        buttonNode("刷新", { variant: "text", small: true, iconName: "refresh", onClick: () => refreshHealth() }),
      ),
    ),
    h("dl.kv", {}, rows.flatMap(([key, value]) => [h("dt", key), h("dd", {}, value)])),
    native.length
      ? h("div.card-foot", {},
          h("div.row-between", {},
            h("span", `原生端点可用 ${ok} / ${native.length}`),
            h("span", { class: "muted" }, "响应/Anthropic 原生转发能力"),
          ),
          h("div", { style: { marginTop: "8px" } }, progressBar(native.length ? (ok / native.length) * 100 : 0, ok === native.length ? "good" : "primary")),
        )
      : null,
  );
}

// —— 热力图：一周的用量节律 ——
// 与页头窗口无关（见 HEATMAP_WINDOW_HOURS 的说明），所以徽标自己写死"7 天"，
// 不跟 rangeLabel() 走 —— 否则切到 1h 时图上写着"最近 1 小时"、矩阵却是整周。
function heatmapCard() {
  const metric = HEATMAP_METRICS.find((item) => item.id === state.heatMetric) || HEATMAP_METRICS[0];
  const points = state.heatPoints || [];
  const body = state.heatError
    ? notice(`热力图数据读取失败: ${state.heatError}`, "error")
    : !state.heatPoints
      ? skeleton("chart")
      : heatmap({
          points,
          metric,
          ariaLabel: `最近 7 天的${metric.label}热力图，按周内与半小时分布`,
        });

  return card(
    cardHead("用量热力图",
      badge("最近 7 天", "muted"),
      badge("30 分钟/格", "muted"),
      h("div.head-tools", {},
        segmented(HEATMAP_METRICS.map((item) => ({ id: item.id, label: item.label })),
          state.heatMetric,
          (id) => { state.heatMetric = id; draw(); },
          { "aria-label": "选择热力图指标" }),
      ),
    ),
    body,
  );
}

// —— 请求流：刚刚发生了什么 ——
// 从「用量统计」（原「实时活动」）移到概览：它回答的是"此刻正在发生什么"，
// 与概览的实时定位一致；用量统计则专注于历史聚合。
//
// 每一行要回答四件事，缺一件就得去翻日志：
//   1. 谁、从哪儿来 —— 调用方档位、工作空间、来源地址（含 User-Agent 悬停）；
//   2. 走了哪条路 —— 请求模型 → 实际模型 → 供应商 / 上游 Key / 上游模型；
//   3. 花了多少 Token —— 输入、输出、缓存读、合计（缓存写与未缓存输入在悬停里）；
//   4. 结果如何 —— 成功/失败、状态码、是否重试、耗时与首字。
// 四组信息一列一组，因此这张卡在概览里独占一整行（col-12）：半宽放不下，
// 硬塞会把模型名与来源挤成省略号。
function filteredRequests() {
  const items = state.requests?.items || [];
  if (state.streamFilter === "failure") return items.filter((item) => !item.success);
  if (state.streamFilter === "retry") return items.filter((item) => item.retried);
  if (state.streamFilter === "success") return items.filter((item) => item.success);
  return items;
}

function streamCard() {
  if (state.requestsError) {
    return card(cardHead("请求流"), notice(`请求明细读取失败: ${state.requestsError}`, "error"));
  }
  const all = state.requests?.items || [];
  const items = filteredRequests();
  const counts = {
    all: all.length,
    success: all.filter((item) => item.success).length,
    failure: all.filter((item) => !item.success).length,
    retry: all.filter((item) => item.retried).length,
  };

  return card(
    cardHead("请求流",
      badge(`最近 ${all.length} 条`, "muted"),
      state.requestsAt ? freshness(state.requestsAt, { prefix: "拉取于" }) : null,
    ),
    // 过滤器放在卡头下方而不是塞进卡头：卡片不宽时四个筛选项会把标题挤成竖排。
    h("div.toolbar", { style: { marginBottom: "8px" } },
      segmented([
        { id: "all", label: `全部 ${counts.all}` },
        { id: "success", label: `成功 ${counts.success}` },
        { id: "failure", label: `失败 ${counts.failure}` },
        { id: "retry", label: `重试 ${counts.retry}` },
      ], state.streamFilter, (id) => { state.streamFilter = id; draw(); },
        { "aria-label": "按结果过滤请求流" }),
    ),
    items.length
      ? h("div.stream", {}, items.map(streamRow))
      : empty("该条件下暂无请求记录。", { icon: "activity", hint: "窗口内没有请求时，这里会是空的。" }),
  );
}

// 每列一条：子元素个数必须与 styles.css 里 .stream-row 的轨道数一致
// （webui/probes/webui_layout_probe.mjs 会逐档断言这件事）。
function streamRow(item) {
  const tone = !item.success ? "is-failure" : item.retried ? "is-retry" : "";
  return h(`div.stream-row${tone ? `.${tone}` : ""}`, {},
    h("span.stream-bar", { title: resultText(item) }),
    h("span.stream-time", { title: item.created_at }, formatClockSeconds(item.created_at)),
    h("div.stream-main", {},
      h("span.stream-model", { title: routeTitle(item) }, item.model_id),
      h("span.stream-meta", { title: routeTitle(item) }, routeText(item)),
    ),
    h("div.stream-source", { title: sourceTitle(item) },
      h("span.stream-source-line", {}, sourceText(item)),
      h("span.stream-source-addr", {}, clientAddress(item.client_addr)),
    ),
    h("div.stream-tokens", { title: tokenTitle(item) },
      item.total_tokens
        ? [
            h("span.stream-tokens-line", {},
              `输入 ${formatCompact(item.prompt_tokens)} · 输出 ${formatCompact(item.completion_tokens)}`),
            h("span.stream-tokens-line.muted", {},
              `缓存 ${formatCompact(item.cached_tokens)} · 合计 ${formatCompact(item.total_tokens)}`),
          ]
        : h("span.stream-tokens-line.muted", {}, "无 Token 读数"),
    ),
    h("div.stream-result", {},
      h("span.stream-result-line", { class: resultTone(item) }, resultText(item)),
      h("span.stream-result-sub", {}, `${formatDuration(item.duration_ms)} · 首字 ${formatDuration(item.first_token_ms)}`),
    ),
    h("span.stream-cost", { title: costTitle(item) }, costText(item)),
  );
}

// routeText 描述这次请求实际走的路径：请求模型 → 实际模型 → 供应商 / Key / 上游模型。
//
// 请求模型与实际模型不同才写前者：两者相同时（直接请求真实模型 ID）重复一遍只是噪声。
function routeText(item) {
  return [
    item.requested_model_id && item.requested_model_id !== item.model_id
      ? `${item.requested_model_id} → ${item.model_id}`
      : null,
    item.provider_id || null,
    item.key_name || null,
    item.upstream_model_id && item.upstream_model_id !== item.model_id ? item.upstream_model_id : null,
  ].filter(Boolean).join(" · ");
}

function routeTitle(item) {
  return [
    `请求模型 ${item.requested_model_id || "—"}`,
    `实际模型 ${item.model_id || "—"}`,
    `上游模型 ${item.upstream_model_id || "—"}`,
    `供应商 ${item.provider_id || "—"}`,
    `上游 Key ${item.key_name || "—"}`,
  ].join("\n");
}

// sourceText 是调用方身份：档位 + 工作空间。空值一律不写，避免出现" · · "。
function sourceText(item) {
  return [
    CALLER_TYPE_LABELS[item.caller_type] || item.caller_type,
    item.workspace,
  ].filter(Boolean).join(" · ") || "—";
}

// clientAddress 把 host:port 折成可读的地址：端口对"谁在用这个实例"没有信息量，
// 一列里还占掉近一半宽度。IPv6 的方括号一并去掉，完整原值留在悬停提示里。
function clientAddress(addr) {
  if (!addr) return "—";
  const text = String(addr);
  const v6 = /^\[(.+)\]:\d+$/.exec(text);
  if (v6) return v6[1];
  const v4 = /^(.+):\d+$/.exec(text);
  if (v4) return v4[1];
  return text;
}

// sourceTitle 在悬停里给出完整的来源：地址带端口、User-Agent、请求 ID。
// User-Agent 往往很长（一整串客户端版本信息），放进列里会把地址挤没，只适合悬停。
function sourceTitle(item) {
  return [
    `来源地址 ${item.client_addr || "未记录"}`,
    `工作空间 ${item.workspace || "未记录"}`,
    `调用方 ${CALLER_TYPE_LABELS[item.caller_type] || item.caller_type || "未记录"}`,
    `User-Agent ${item.user_agent || "未记录"}`,
    `请求 #${item.id}`,
  ].join("\n");
}

// tokenTitle 给出完整 Token 读数：列里放最常看的四项，缓存写与未缓存输入在悬停里。
function tokenTitle(item) {
  return [
    `输入 ${formatCount(item.prompt_tokens)}（未缓存 ${formatCount(item.uncached_prompt_tokens)}）`,
    `输出 ${formatCount(item.completion_tokens)}`,
    `缓存读 ${formatCount(item.cache_read_input_tokens)}`,
    `缓存写 ${formatCount(item.cache_creation_input_tokens)}`,
    `缓存合计 ${formatCount(item.cached_tokens)}`,
    `总计 ${formatCount(item.total_tokens)}`,
  ].join("\n");
}

function resultText(item) {
  return `${item.success ? "成功" : "失败"} · HTTP ${item.status_code ?? "无响应"}${item.retried ? " · 已重试" : ""}`;
}

// resultTone 复用设计令牌里的语义色：失败用错误色，重试用警示色，成功不再上色
// （成功是绝大多数，给每一行都染一遍绿反而让失败那一行不显眼）。
function resultTone(item) {
  if (!item.success) return "tone-bad";
  if (item.retried) return "tone-warn";
  return "";
}

// 成本列：拿不到价格时显示 "—" 而不是 "$0" —— 0 会被读成"这次请求免费"，
// 而事实是"不知道"。目录尚未加载（首屏）时同样显示 "—"，避免先闪一堆 $0。
function costText(item) {
  const index = currentIndex();
  if (!index) return "—";
  return formatCost(requestCost(index, item));
}

function costTitle(item) {
  const index = currentIndex();
  const name = item.upstream_model_id;
  if (!index) return "价格目录尚未就绪";
  if (!name) return "该请求没有 upstream_model 归因，无法匹配价格";
  const entry = lookupPrice(index, name);
  if (!entry) return `${name} 未在 models.dev 目录中匹配到价格`;
  return `${name} · 输入 ${formatPrice(entry.input)} / 输出 ${formatPrice(entry.output)} USD per 1M token`;
}

// —— 结果构成随时间 ——
// 成功 / 失败 / 重试随时间堆叠。折线只能看"总量"，这张图看的是"结构变化"：
// 总量平稳但失败占比抬升，是上游开始抖动的早期信号。
function outcomeCard(points, bucketSeconds) {
  const specs = [
    { id: "successes", label: "成功", tone: "secondary", pick: (point) => point.successes },
    { id: "failures", label: "失败", tone: "error", pick: (point) => point.failures },
    { id: "retries", label: "重试", tone: "warn", pick: (point) => point.retries },
  ];
  return card(
    cardHead("结果构成随时间",
      badge(`${bucketLabel(bucketSeconds)}/柱`, "muted"),
      h("div.head-tools", {},
        legend(specs.map((spec) => ({
          label: spec.label,
          tone: spec.id === "successes" ? "secondary" : spec.id === "failures" ? "bad" : "warn",
        }))),
      ),
    ),
    h("div", { style: { marginTop: "12px" } },
      stackedBars({
        points, series: specs, height: 220, bucketSeconds,
        ariaLabel: `成功、失败与重试随时间构成，最近 ${rangeLabel()}`,
        emptyText: "该时间窗内没有请求",
      })),
    h("div.card-foot", {}, "重试是成功与失败之外的第三次尝试计数，三者可能同时出现在同一个桶里。"),
  );
}

// —— 状态码明细 ——
// 比状态分组更细一层：分组告诉你"有 5xx"，这张表告诉你"是 502 还是 504"，
// 排查时这是两种完全不同的原因。
function statusCodeCard(metrics) {
  const codes = metrics.total?.status_codes || {};
  const entries = Object.entries(codes)
    .map(([code, count]) => ({ code: Number(code), count: Number(count) || 0 }))
    .filter((row) => row.count > 0)
    .sort((a, b) => b.count - a.count);
  if (!entries.length) {
    return card(cardHead("状态码明细", badge(rangeLabel(), "muted")),
      empty("窗口内没有带状态码的请求。", { icon: "activity" }));
  }
  const total = entries.reduce((sum, row) => sum + row.count, 0);
  const group = (code) => (code >= 200 && code < 300 ? "good" : code >= 500 ? "bad" : code >= 400 ? "warn" : "neutral");
  return card(
    cardHead("状态码明细", badge(`${entries.length} 种`, "muted"), badge(rangeLabel(), "muted")),
    h("div.stack.tight", {}, entries.slice(0, 8).map((row) =>
      h("div.row-between", {},
        h("span.inline", {},
          h("i", { class: `swatch tone-${group(row.code)}` }),
          h("span.mono", String(row.code)),
        ),
        h("span.inline", {},
          h("strong", formatCount(row.count)),
          h("span.muted", ` · ${formatPercent(row.count, total)}`),
        ),
      ))),
  );
}

// —— 实时脉搏：四个指标的小倍数图 ——
// 把 RPM / TPM / 耗时 / 成功率并成一组同宽的迷你曲线。分开看四张折线要上下滚动，
// 摆在一起才能回答"这次耗时抬升是不是伴随了成功率下滑"。
// 每格只画形状、不标刻度：读数是上面 KPI 瓦片的事，这里看的是"有没有同步异动"。
function pulseCard(points, bucketSeconds) {
  const cells = [
    { metricId: "rpm", tone: "primary" },
    { metricId: "tpm", tone: "primary" },
    { metricId: "latency", tone: "warn" },
    { metricId: "success", tone: "secondary" },
  ];
  return card(
    cardHead("实时脉搏",
      badge(`${bucketLabel(bucketSeconds)}/点`, "muted"),
      badge(rangeLabel(), "muted"),
    ),
    h("div.pulse-grid", {}, cells.map((cell) => {
      const metric = METRIC_MAP[cell.metricId] || METRIC_MAP.rpm;
      const summary = chartSummary({ points, metricId: cell.metricId, bucketSeconds, windowHours: state.hours });
      const latest = summary.latest;
      return h("div.pulse-cell", {},
        h("div.pulse-head", {},
          h("span.pulse-label", metric.label),
          h("span.pulse-value", { class: `tone-${cell.tone}` },
            latest === null || latest === undefined ? "—" : metric.format(latest)),
        ),
        h("div.pulse-spark", {},
          sparkline({ points, metricId: cell.metricId, bucketSeconds, width: 200, height: 40, tone: cell.tone })),
        h("span.pulse-foot", {},
          `均 ${summary.total === null || summary.total === undefined ? "—" : metric.format(summary.total)}`),
      );
    })),
    h("div.card-foot", {}, "四格共用同一时间轴；只画走势不标刻度，具体读数见上方 KPI。"),
  );
}

// —— 页面组装 ——
export function renderOverview(context) {
  xtxRef = context;
  host = h("div.stack");
  if (context.store.health?.local_auth_enabled && !context.store.authorized) {
    return h("div.stack", {}, notice("需要本地鉴权 Key 才能读取用量。", "warn"));
  }
  if (!state.snapshot && !state.loading) state.loading = true;
  if (state.seriesHours !== state.hours) loadWindow();
  loadHeatmap();
  // 请求流与窗口同一 hours：切窗口时旧列表会被下一次 onTick 覆盖，
  // 这里先取一次，避免切完窗口后列表还停在上一个窗口的记录上。
  loadRequests();
  // 价格目录只拉一次（见 webui/pricing.js），失败不阻断看板：成本 KPI 与排行卡
  // 会退化成"—"/空态，其余卡片照常。
  loadPricing(() => api.pricing()).then(draw).catch(() => {});
  // 指标轮询时刷新本页窗口数据（快照 + 序列 + 请求流，保持同屏口径一致）。
  // 热力图有自己的 60 秒 TTL，force 会绕过它 —— 但那时桶内容其实没变，
  // 所以只在窗口数据真正变化时顺带刷新，避免每 10 秒重画 336 个格子。
  context.onTick?.(() => { loadWindow(true); loadHeatmap(); loadRequests(); });
  // 同步首绘必须绕过"已挂载"守卫：app.js 是**先**拿本函数返回的节点、**后**mount 进
  // 文档的（renderContent 里 page.render(ctx()) 在前，mount 在后），此刻 host 还不在
  // 文档里，isConnected 恒为 false。而下面的 loadWindow/loadHeatmap 在缓存命中时会直接
  // 返回、不会回调 draw —— 于是重新进入本页时整页空白，一直等到下一次轮询（健康轮询
  // 5 秒）触发 onTick 里的 loadWindow(true) 才补画。
  draw(true);
  return host;
}

// firstPaint 为真时表示"本轮是 renderOverview 里的同步首绘"，节点尚未挂载；
// 其余调用（轮询回调）仍要求节点在文档里，避免离开页面后继续白画。
function draw(firstPaint = false) {
  if (!host) return;
  if (!firstPaint && !host.isConnected) return;
  const { store } = xtxRef;
  const metrics = state.snapshot;
  const points = state.series || [];
  const bucketSeconds = state.seriesBucket || pickBucketSeconds(state.hours);

  const head = h("div.page-head", {},
    h("div.page-title", {},
      h("h1", "概览"),
      h("p.sub", "本机 AMKR 的统一入口、实时速率与用量趋势。"),
    ),
    h("div.page-actions", {},
      state.snapshotAt ? freshness(state.snapshotAt) : null,
      store.health?.base_url
        ? buttonNode("复制入口地址", {
            variant: "secondary", small: true, iconName: "copy",
            onClick: () => copyText(store.health.base_url).then(() => toast("入口地址已复制")),
          })
        : null,
      segmented(TIME_RANGES.map((range) => ({ id: range.hours, label: range.short, title: range.label })),
        state.hours,
        // 请求流也跟着换窗口：否则折线变成 7 天、列表还停在上一窗口的记录上。
        (id) => { state.hours = Number(id); state.loading = true; draw(); loadWindow(true); loadRequests(); },
        { "aria-label": "选择统计窗口" }),
    ),
  );

  const children = [head];

  if (store.connectionError) {
    children.push(notice(`无法连接 AMKR 服务：${store.connectionError}`, "error"));
  } else if (state.snapshotError && !metrics) {
    children.push(notice(`指标读取失败：${state.snapshotError}`, "error"));
  }
  if (store.health?.local_auth_enabled === false) {
    children.push(notice("本地鉴权未启用：管理接口对本机开放，建议在设置中启用。", "warn"));
  }

  if (!metrics) {
    // 10 张 / 5 列，与 kpiTiles 的真实网格对齐。
    children.push(statSkeleton(10, 5));
    children.push(card(cardHead("流量趋势"), skeleton("chart")));
    render(host, children);
    return;
  }

  children.push(kpiTiles(metrics, points, bucketSeconds));
  // 所有看板卡放进同一个栅格，而不是每排一个 .grid-12：
  // 分开写时排内间距是 16px、排间却是 .content 的 24px，横向纵向对不上，
  // 整体节奏显得松散。合成一个栅格后，所有间隙统一为 gap。
  // 每排仍按最高卡等高（见 styles.css 的说明），因此排内底边平齐。
  //
  // 排序按"从实时到历史"：脉搏 → 热力图 → 趋势 → 请求流 → 构成与排行。
  children.push(h("div.grid-12", {},
    // 实时脉搏紧跟在 KPI 下方并占满整行：它和 KPI 是同一个时间尺度上的两种读法。
    h("div.col-12", {}, pulseCard(points, bucketSeconds)),
    // 趋势与统一视图固定 6+6：<=1024px 时 col-7/col-8 会变成整行，若这里是
    // 8+4，窄档会把 unifiedCard 剩成半行；6+6 在半宽档仍然是 6+6，四档都排满。
    h("div.col-6", {}, trendCard()),
    h("div.col-6", {}, unifiedCard()),
    // 热力图是"一眼看节律"的图，占满整行；24 个小时列塞进 col-4 每格只剩十几像素。
    h("div.col-12", {}, heatmapCard()),
    // 请求流独占一整行：它要一列一组地给出「谁从哪儿来 / 走了哪条路 / 花了多少
    // Token / 结果如何」四组读数，半宽（col-6）会把模型名、来源与 Token 挤成省略号。
    // 之前它与右边那列的「结果构成 + Token 构成」两张图同排，图表看不出受损，
    // 受损的正是请求流这一侧。
    h("div.col-12", {}, streamCard()),
    // 两张堆叠柱各占半宽并排：它们画的是同一份 points、同一个桶宽，列宽相等，
    // 横轴因此仍然完全对齐，并排看"结果结构"与"Token 结构"才读得出同步异动。
    h("div.col-6", {}, outcomeCard(points, bucketSeconds)),
    h("div.col-6", {}, tokenBreakdownCard(points, bucketSeconds)),
    h("div.col-4", {}, compositionCard(metrics)),
    h("div.col-4", {}, statusCard(metrics)),
    h("div.col-4", {}, statusCodeCard(metrics)),
    h("div.col-4", {}, waterfallCard(metrics)),
    h("div.col-4", {}, rankingCard("模型调用排行", metrics.models, "模型")),
    h("div.col-4", {}, rankingCard("调用方排行", metrics.caller_types, "调用方")),
    h("div.col-6", {}, rankingCard("上游 Token 排行", metrics.upstream_models, "上游模型的 Token", {
      value: (stats) => stats.total_tokens,
      tone: "secondary",
      format: (row) => `${formatCompact(row.value)} Token`,
    })),
    h("div.col-6", {}, costRankingCard(metrics)),
    h("div.col-12", {}, runtimeCard()),
  ));

  render(host, children);
}

// Token 构成随时间：堆叠柱，和折线看"总量趋势"互补，看"输入/输出结构变化"。
// 图例只作颜色索引、不带数字：窗口总量已经在 KPI 与 Token 构成卡上给过，
// 这里再算一遍（按图内桶求和）只会多出一个对不上的数字。
function tokenBreakdownCard(points, bucketSeconds) {
  return card(
    cardHead("Token 构成随时间",
      badge(rangeLabel(), "muted"),
      badge(`${bucketLabel(bucketSeconds)}/柱`, "muted"),
    ),
    legend([
      { label: "输入", tone: "primary" },
      { label: "输出", tone: "secondary" },
    ]),
    h("div", { style: { marginTop: "12px" } },
      stackedBars({ points, height: 220, bucketSeconds }),
    ),
  );
}
