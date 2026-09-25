// 用量统计：把"历史上用了多少"讲清楚。
//
// 与概览的分工：概览回答"现在怎么样"（实时速率、当前窗口的状态与构成），
// 这里回答"过去用了多少"——窗口可以拉到一个月/季度/半年/一年/全部历史，
// 重点放在累计量、按天走势、日内节律与维度明细。
//
// 逐条请求流不在这里（它看的是"刚刚发生了什么"，已移到概览）；服务日志也不在
// 这里（另有独立的「服务日志」页）。本页只留聚合数字。

import {
  h, errorText, formatCount, formatCompact, formatDuration, formatPercent,
  formatRate,
} from "../dom.js";
import { api } from "../api.js";
import {
  card, cardHead, stat, statGrid, notice, badge, empty, skeleton, render,
  segmented, table, freshness,
} from "../ui.js";
import { lineChart, barList, stackedBars, donut, legend, chartSummary } from "../charts.js";
import {
  USAGE_RANGES, ALL_HISTORY, MAX_HISTORY_HOURS, historyHours, rangeSpec,
  pickBucketSeconds, bucketLabel, rank, statusGroups,
  formatPercentValue, formatCompactNumber, formatNumber,
  dailyUsage, hourlyProfile, splitCompare,
  METRIC_MAP, series as toSeries,
} from "../chart-math.js";

const state = {
  // 选择值：数字小时数，或 ALL_HISTORY。窗口的解析统一走 rangeSpec()。
  selection: 24,
  // 「全部历史」由最早记录推导出的跨度（小时）；算过一次就复用。
  allHours: null,
  allHoursAt: null,
  allHoursError: null,
  series: null,
  seriesKey: null,
  seriesBucket: null,
  seriesError: null,
  snapshot: null,
  snapshotAt: null,
  snapshotError: null,
  // 按 Key 拆分的读数（哪把上游 Key 出去了、哪把访问密钥发起的）。单独一条接口
  // （/ui/key-usage.json），单独一份错误状态——它读失败不该把整页其余读数一起清掉。
  keyUsage: null,
  keyUsageError: null,
  loading: true,
  sort: { key: "requests", direction: "desc" },
  cumulativeMetric: "requests",
};

let host = null;
let xtxRef = null;
let seriesToken = 0;

// 当前窗口描述：{ hours, label, short, all, truncated }。
// 每次渲染都重新求值（纯函数），不缓存成第二份状态 —— 缓存过的派生值一旦忘记
// 同步就会和 allHours 对不上。
function currentSpec() {
  return rangeSpec(state.selection, { allHours: state.allHours });
}

// 徽标与脚注里的窗口称呼：跨到上限时补一句"只覆盖到上限"，
// 否则「全部历史」会被读成"真的是全部"。
function rangeLabel() {
  const spec = currentSpec();
  if (!spec.truncated) return spec.label;
  return `全部历史（仅最近 ${Math.round(MAX_HISTORY_HOURS / 24)} 天）`;
}

export function renderActivity(context) {
  xtxRef = context;
  const { store } = context;
  host = h("div.stack");

  if (store.health?.local_auth_enabled && !store.authorized) {
    return h("div.stack", {}, notice("需要本地鉴权 Key 才能读取用量统计。", "warn"));
  }

  // 「全部历史」要先知道最早一条记录的时间，才能决定查多少小时。
  // 顺序上先解析跨度再取窗口，避免先按旧跨度画一遍、之后再重画一次。
  prepareRange().then(() => loadWindow(true));
  context.onTick?.(() => loadWindow(true));
  draw(true);
  return host;
}

// —— 时间跨度 ——
//
// 「全部历史」不能直接问 /metrics/series（它没有 all_history，见 docs/API.md），
// 所以先用 all_history 的请求明细拿最早时间，再折算成小时数交给 series/快照。
// 上限 MAX_HISTORY_HOURS：跨度再长也只能查到 series 允许的一年。
async function prepareRange() {
  if (state.selection !== ALL_HISTORY) return;
  // 跨度算过一次就复用：最早记录不会因为刷新而变（除非清库）。
  if (state.allHours !== null && state.allHoursAt) return;
  try {
    const data = await api.requests({ all_history: true, limit: 1 });
    state.allHoursError = null;
    // 库为空时 window.from 是 null：没有历史可查，退回最短窗口让页面显示空态。
    state.allHours = historyHours(data?.window?.from || null) ?? 1;
    state.allHoursAt = new Date().toISOString();
  } catch (error) {
    state.allHoursError = errorText(error);
    state.allHours = 24;
  }
}

function pickRange(selection) {
  state.selection = selection === ALL_HISTORY ? ALL_HISTORY : Number(selection);
  state.loading = true;
  draw();
  prepareRange().then(() => loadWindow(true));
}

// —— 取数 ——
// 快照与序列同一窗口一次取回，保证同屏读数自洽。
async function loadWindow(force = false) {
  const spec = currentSpec();
  const key = `${state.selection}:${spec.hours}`;
  if (!force && state.series && state.seriesKey === key) return;
  const bucket = pickBucketSeconds(spec.hours);
  const token = ++seriesToken;
  // 三条读数用**同一个** spec.hours：本页的「全部历史」是先由最早一条记录推出跨度、
  // 再当小时数查（series 根本没有 all_history），因此这里不能单独传 all_history=true
  // ——那会让按 Key 的表覆盖比上方 KPI/曲线更长的跨度，同屏数字对不上。
  const [snapshot, series, keyUsage] = await Promise.all([
    api.metrics(spec.hours).catch((error) => ({ __error: errorText(error) })),
    api.series(spec.hours, bucket).catch((error) => ({ __error: errorText(error) })),
    api.keyUsage({ hours: spec.hours }).catch((error) => ({ __error: errorText(error) })),
  ]);
  if (token !== seriesToken) return;
  state.snapshotError = snapshot.__error || null;
  state.snapshot = snapshot.__error ? null : snapshot;
  if (!snapshot.__error) state.snapshotAt = new Date().toISOString();
  state.seriesError = series.__error || null;
  state.series = series.__error ? null : (series.points || []);
  state.keyUsageError = keyUsage.__error || null;
  state.keyUsage = keyUsage.__error ? null : keyUsage;
  state.seriesKey = key;
  state.seriesBucket = series.bucket_seconds || bucket;
  state.loading = false;
  draw();
}

// —— KPI ——
// 历史视角的重点不是"此刻多快"，而是"这段时间用了多少"，所以累计量在前；
// 窗口无关的实时速率（当前 RPM/TPM）留在概览，这里放会误导。
function kpiTiles(metrics, points, bucketSeconds) {
  const spec = currentSpec();
  const total = metrics.total || {};
  const successRatio = total.requests ? total.successes / total.requests : null;
  const cacheRatio = total.prompt_tokens ? total.cached_tokens / total.prompt_tokens : null;
  const days = dailyUsage(points || []);
  const activeDays = days.filter((day) => day.requests > 0).length;
  // 峰值日：回答"最忙的一天用了多少"，平均值看不到尖峰。
  const peakDay = days.reduce((best, day) => (best && best.requests >= day.requests ? best : day), null);
  const compare = splitCompare(toSeries(points || [], METRIC_MAP.rpm, bucketSeconds));

  return statGrid(
    stat("窗口请求", formatCount(total.requests), `最近 ${spec.label}`, {
      iconName: "layers", points: points || [], metricId: "rpm", bucketSeconds,
      // 后半段相对前半段的变化：长窗口下比"环比上一窗口"更省数据。
      trendChange: compare?.change ?? null, trendPolarity: "neutral",
    }),
    stat("Token 用量", formatCompact(total.total_tokens),
      `输入 ${formatCompact(total.prompt_tokens)} · 输出 ${formatCompact(total.completion_tokens)}`, {
        iconName: "layers", points: points || [], metricId: "tpm", bucketSeconds,
      }),
    stat("活跃天数", formatCount(activeDays),
      days.length ? `窗口覆盖 ${days.length} 天` : "窗口内无记录", { iconName: "clock" }),
    stat("日均请求", formatCount(days.length ? Math.round(total.requests / days.length) : 0),
      peakDay && peakDay.requests
        ? `峰值 ${formatCount(peakDay.requests)} 次 · ${peakDay.date}`
        : "窗口内无请求", { iconName: "activity" }),
    stat("成功率", successRatio === null ? "-" : formatPercentValue(successRatio, 1),
      `${formatCount(total.successes)} 成功 · ${formatCount(total.failures)} 失败`, {
        iconName: "check",
        tone: successRatio !== null && successRatio < 0.95 ? "bad" : null,
      }),
    stat("缓存命中率", cacheRatio === null ? "-" : formatPercentValue(cacheRatio, 1),
      `缓存 ${formatCompact(total.cached_tokens)} Token`, {
        iconName: "filter", tone: cacheRatio !== null && cacheRatio > 0.3 ? "good" : null,
      }),
    stat("平均耗时", total.requests ? formatDuration(total.avg_duration_ms) : "-",
      total.requests ? `最快 ${formatDuration(total.min_duration_ms)} · 最慢 ${formatDuration(total.max_duration_ms)}` : "窗口内无请求",
      { iconName: "clock" }),
    stat("平均首字", total.requests && total.avg_first_token_ms ? formatDuration(total.avg_first_token_ms) : "-",
      `${formatCount(total.retries)} 次重试`, { iconName: "bolt" }),
  );
}

// —— 累计用量曲线 ——
// 长窗口下逐桶曲线会被日周期噪声填满，"累计涨到多少"反而更能说明规模。
// 累计值交给 lineChart 的 cumulative 模式算（那边才拿得到 metricValue），
// 这里只用同一份 series() 求和做读数，避免两处各算一遍累计口径。
function cumulativeCard(points, bucketSeconds) {
  const target = state.cumulativeMetric;
  const metric = METRIC_MAP[target] || METRIC_MAP.requests;
  const raw = toSeries(points || [], metric, bucketSeconds);
  const sum = raw.reduce((acc, point) => acc + (Number.isFinite(point.value) ? point.value : 0), 0);

  return card(
    cardHead("累计用量",
      badge(rangeLabel(), "muted"),
      h("div.head-tools", {},
        segmented(
          [{ id: "requests", label: "请求数" }, { id: "tokens", label: "Token" }],
          target,
          (id) => { state.cumulativeMetric = id; draw(); },
          { "aria-label": "选择累计指标" },
        ),
      ),
    ),
    h("div.chart-readout", {},
      h("span.readout-main", {},
        target === "tokens" ? formatCompactNumber(sum) : formatNumber(sum, 0),
        h("small", metric.unit)),
      h("span.readout-note", {}, `最近 ${rangeLabel()}累计`),
      h("span.spacer"),
      h("span.readout-note", {}, `${bucketLabel(bucketSeconds)}/点 · 共 ${raw.length} 点`),
    ),
    lineChart({
      points, metricId: target, bucketSeconds, height: 260, cumulative: true,
      ariaLabel: `累计${metric.label}，最近 ${rangeLabel()}`,
    }),
  );
}

// —— 按天用量 ——
// 长窗口的主视图：每天一根柱（输入/输出堆叠）。柱状比折线更贴合"每天用了多少"，
// 也天然处理了"某天没有记录"——没有柱子就是那天没有用量。
function dailyCard(points) {
  const days = dailyUsage(points || []);
  const busiest = days.reduce((best, day) => (best && best.requests >= day.requests ? best : day), null);
  return card(
    cardHead("按天用量", badge(`${days.length} 天`, "muted")),
    days.length
      ? h("div.stack", {},
          legend([
            { label: "输入", tone: "primary" },
            { label: "输出", tone: "secondary" },
          ]),
          // stackedBars 认 started_at / prompt_tokens / completion_tokens，
          // 这里把日汇总映射成同样的字段，复用同一个图表实现。
          h("div", { style: { marginTop: "12px" } },
            stackedBars({
              points: days.map((day) => ({
                started_at: `${day.date}T00:00:00+08:00`,
                prompt_tokens: day.prompt_tokens,
                completion_tokens: day.completion_tokens,
                requests: day.requests,
                complete: !day.partial,
              })),
              height: 240, bucketSeconds: 86400,
            })),
          h("div.card-foot", {},
            busiest
              ? `最忙的一天是 ${busiest.date}：${formatCount(busiest.requests)} 次请求、${formatCompact(busiest.total_tokens)} Token。`
              : "窗口内没有请求记录。"),
        )
      : empty("窗口内没有按天数据。", { icon: "activity", hint: "换一个更长的窗口试试。" }),
  );
}

// —— 日内节律 ——
// 把整段窗口压成 24 个时段，回答"高峰在几点"。长窗口用热力图会糊成一团，
// 时段分布才是能读的粒度。用 barList 而不是自造样式：24 根横向条本来就是这个组件
// 的形状，它也自带"最长条占满、其余按比例"的归一。
function hourlyCard(points) {
  const profile = hourlyProfile(points || []);
  const peak = profile.reduce((best, item) => (best && best.value >= item.value ? best : item), null);
  const rows = profile
    .map((item) => ({
      name: `${String(item.hour).padStart(2, "0")}:00`,
      value: item.value,
    }))
    .filter((row) => row.value > 0)
    .sort((a, b) => b.value - a.value);

  return card(
    cardHead("日内时段分布", badge("北京时间", "muted"), badge(rangeLabel(), "muted")),
    rows.length
      ? h("div.stack", {},
          barList(rows, {
            format: (row) => `${formatCount(row.value)} 次`,
            emptyText: "窗口内没有时段数据。",
          }),
          h("div.card-foot", {},
            peak && peak.value
              ? `最忙的时段是 ${String(peak.hour).padStart(2, "0")}:00 前后，累计 ${formatCount(peak.value)} 次请求。`
              : null),
        )
      : empty("窗口内没有时段数据。", { icon: "clock" }),
  );
}

// —— 构成与分布 ——
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
    h("div.card-foot", {}, "缓存读另计，不重复计入输入输出合计。"),
  );
}

function statusCard(metrics) {
  const groups = statusGroups(metrics.total?.status_codes || {});
  const total = groups.ok + groups.client + groups.server + groups.other;
  if (!total) {
    return card(cardHead("响应状态分布", badge(rangeLabel(), "muted")),
      empty("窗口内没有带状态码的请求。", { icon: "activity" }));
  }
  const rows = [
    { label: "2xx 成功", value: groups.ok, tone: "good" },
    { label: "4xx 客户端错误", value: groups.client, tone: "warn" },
    { label: "5xx 服务端错误", value: groups.server, tone: "bad" },
    { label: "其它状态", value: groups.other, tone: "neutral" },
  ].filter((row) => row.value > 0);

  return card(
    cardHead("响应状态分布", badge(`最近 ${rangeLabel()}`, "muted")),
    h("div.stack", {},
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
    ),
  );
}

// —— 维度明细表 ——
const COLUMNS = [
  { key: "name", label: "", render: (row) => h("span.mono", { title: row.name }, row.name), sortValue: (row) => row.name },
  { key: "requests", label: "请求", numeric: true, render: (row) => formatCount(row.stats.requests), sortValue: (row) => row.stats.requests },
  { key: "successes", label: "成功", numeric: true, render: (row) => formatCount(row.stats.successes), sortValue: (row) => row.stats.successes },
  { key: "failures", label: "失败", numeric: true, render: (row) => row.stats.failures
      ? h("span", { style: { color: "var(--md-error)" } }, formatCount(row.stats.failures))
      : "0", sortValue: (row) => row.stats.failures },
  { key: "rate", label: "成功率", numeric: true, render: (row) => formatPercent(row.stats.successes, row.stats.requests), sortValue: (row) => (row.stats.requests ? row.stats.successes / row.stats.requests : -1) },
  { key: "tokens", label: "Token", numeric: true, render: (row) => formatCompact(row.stats.total_tokens), sortValue: (row) => row.stats.total_tokens },
  { key: "cache", label: "缓存率", numeric: true, render: (row) => formatRate(row.stats.cached_token_rate), sortValue: (row) => row.stats.cached_token_rate },
  { key: "duration", label: "平均耗时", numeric: true, render: (row) => formatDuration(row.stats.avg_duration_ms), sortValue: (row) => row.stats.avg_duration_ms },
];

function comparator() {
  const { key, direction } = state.sort;
  const sign = direction === "asc" ? 1 : -1;
  const column = COLUMNS.find((item) => item.key === key);
  const pick = column?.sortValue || ((row) => row.stats.requests);
  return (a, b) => {
    const left = pick(a);
    const right = pick(b);
    if (typeof left === "string" || typeof right === "string") {
      return sign * String(left).localeCompare(String(right), "zh-CN");
    }
    return sign * ((left ?? 0) - (right ?? 0));
  };
}

// note 是一行可选的脚注：用来解释"这张表的合计为什么不等于页面顶部的总量"
// （例如没有供应商归因的历史行、或只有访问密钥才有的归属），而不是让读者自己猜。
function breakdownCard(title, entries, keyLabel, emptyText, note = null) {
  const rows = Object.entries(entries || {})
    .map(([name, stats]) => ({ name, stats }))
    .sort(comparator());
  if (!rows.length) {
    return card(cardHead(title, badge("0 项", "muted")),
      empty(emptyText, { icon: "logs" }),
      note ? h("div.card-foot", {}, note) : null);
  }
  // 总量行：让"表里各项加起来是多少"有对照，避免只看到分布看不到规模。
  const totalRow = rows.reduce((acc, row) => ({
    requests: acc.requests + (row.stats.requests || 0),
    successes: acc.successes + (row.stats.successes || 0),
    failures: acc.failures + (row.stats.failures || 0),
    total_tokens: acc.total_tokens + (row.stats.total_tokens || 0),
    cached_tokens: acc.cached_tokens + (row.stats.cached_tokens || 0),
    prompt_tokens: acc.prompt_tokens + (row.stats.prompt_tokens || 0),
    total_duration_ms: acc.total_duration_ms + (row.stats.total_duration_ms || 0),
  }), { requests: 0, successes: 0, failures: 0, total_tokens: 0, cached_tokens: 0, prompt_tokens: 0, total_duration_ms: 0 });

  return card(
    cardHead(title, badge(`${rows.length} 项`, "muted"), badge(rangeLabel(), "muted")),
    table(COLUMNS, rows, emptyText, {
      sort: state.sort,
      onSort: (key) => {
        const direction = state.sort.key === key && state.sort.direction === "desc" ? "asc" : "desc";
        state.sort = { key, direction };
        draw();
      },
    }),
    h("div.card-foot", {},
      h("div.row-between", {},
        h("span", `${keyLabel}合计`),
        h("span.inline", {},
          h("strong", formatCount(totalRow.requests)),
          h("span.muted", " 次请求 · "),
          h("strong", formatCompact(totalRow.total_tokens)),
          h("span.muted", " Token · 成功率 "),
          h("strong", formatPercent(totalRow.successes, totalRow.requests)),
        ),
      ),
      note ? h("div.muted", { style: { marginTop: "8px" } }, note) : null,
    ),
  );
}

// —— 按 Key 拆分的三张表 ——
//
// 数据来自 /ui/key-usage.json（本项目自有的读数）：上游 Key 与访问密钥是**两个不同的
// 问题**，因此各成一张表，互不嵌套——一把访问密钥的流量会打到多把上游 Key 上，反之
// 亦然，把两者套成一层会答不出其中任何一个。
//
// 行名都带上能唯一标识那一行的前缀，这不是装饰：
//   - key_name 只在**同一个供应商内**唯一，两家的同名 Key 在 /metrics 的 keys 里会并成
//     一行（「模型 / Key 用量」原先是 `模型 / Key 名`，正是这个问题）；
//   - 访问密钥的显示名可以重名，配置里的 key_id 才是稳定标识符，因此两个都显示。
// 没配到名字（配置里已删掉这把 key）时只显示 id——回填 id 当名字会让"这把 key 还配着"
// 看起来像真的。
function upstreamKeyEntries(rows) {
  const flat = {};
  for (const row of rows || []) flat[`${row.provider_id} / ${row.key_name}`] = row.stats;
  return flat;
}

function accessKeyEntries(rows) {
  const flat = {};
  for (const row of rows || []) {
    const label = row.access_key_name
      ? `${row.access_key_name}（${row.access_key_id}）`
      : row.access_key_id;
    flat[label] = row.stats;
  }
  return flat;
}

function modelKeyEntries(rows) {
  const flat = {};
  for (const row of rows || []) flat[`${row.model_id} / ${row.provider_id} / ${row.key_name}`] = row.stats;
  return flat;
}

// unattributedNote 说明「按上游 Key」这张表少算的那一部分。
//
// provider_id 可空（升级前的历史行、不走 proxy 的写入路径），这些行没有可归属的供应商，
// 因此进不了按 Key 的行。不说清楚的话，表里的合计与页面顶部的总量对不上就成了"看板的
// 数字互相矛盾"，而不是"有一段没归因的历史"。
function unattributedNote(keyUsage) {
  const requests = keyUsage?.unattributed?.requests || 0;
  if (!requests) return null;
  return `另有 ${formatCount(requests)} 次调用没有供应商归因（多为升级前的历史行），`
    + "不在上表内，但计入页面顶部的总量。";
}

// upstreamCard 的三段分别回答「哪个上游模型名」「哪家供应商」「哪把 Key」。
//
// 三段用各自的小标题区分，不靠颜色：因此第三段沿用 .bar-fill 的默认主色，不去新造一个
// 色阶（新增颜色要动 styles.css，而这张卡片的语义不需要第四种颜色）。
function upstreamCard(metrics, keyUsage) {
  const rows = rank(metrics.upstream_models, { limit: 8, value: (stats) => stats.requests });
  const providers = rank(metrics.providers, { limit: 8, value: (stats) => stats.requests });
  const keys = rank(upstreamKeyEntries(keyUsage?.upstream_keys), {
    limit: 8, value: (stats) => stats.requests,
  });
  return card(
    cardHead("上游分布", badge(rangeLabel(), "muted")),
    h("div.stack", {},
      h("div", {},
        h("h4", { class: "muted", style: { marginBottom: "8px" } }, "按上游模型"),
        barList(rows, { emptyText: "没有上游模型归因数据。", format: (row) => `${formatCount(row.value)} 次` }),
      ),
      providers.length
        ? h("div", {},
            h("h4", { class: "muted", style: { marginBottom: "8px" } }, "按供应商"),
            barList(providers, {
              tone: "secondary",
              format: (row) => `${formatCompact(row.stats.total_tokens)} Token`,
            }),
          )
        : h("p.muted", "历史数据的供应商归因可能为空（v4 起不再写入模型池归因）。"),
      h("div", {},
        h("h4", { class: "muted", style: { marginBottom: "8px" } }, "按上游 Key"),
        barList(keys, {
          emptyText: "没有上游 Key 归因数据。",
          format: (row) => `${formatCount(row.value)} 次`,
        }),
      ),
    ),
  );
}

function metadataBadges(metrics) {
  if (!metrics) return null;
  const tone = { green: "good", yellow: "warn", red: "bad" }[metrics.router_status] || "muted";
  const label = { green: "路由正常", yellow: "路由警告", red: "路由异常" }[metrics.router_status] || "路由空闲";
  return h("div.inline", {},
    badge(label, tone),
    badge(`计数口径 ${metrics.count_semantics === "upstream_attempt" ? "上游尝试" : metrics.count_semantics || "未知"}`, "muted"),
  );
}

// —— 性能趋势 ——
// 耗时与首字延迟通常差一个数量级，硬塞进同一张图会把首字曲线压平，
// 所以拆成上下两段各自成图、各自标刻度，每段自己带读数。
// 放在用量统计而不是概览：这是"这段时间的性能如何"，和累计用量同一时间尺度。
function latencyCard(points, bucketSeconds) {
  const spec = currentSpec();
  const panel = (title, metricId) => {
    const summary = chartSummary({ points, metricId, bucketSeconds, windowHours: spec.hours });
    return h("div.latency-panel", {},
      h("div.latency-panel-head", {},
        h("h4", title),
        h("span.latency-panel-value", {},
          summary.latest === null || summary.latest === undefined ? "-" : formatDuration(summary.latest)),
      ),
      lineChart({
        points, metricId, bucketSeconds, height: 140, showArea: false,
        ariaLabel: `${title}趋势，最近 ${rangeLabel()}`,
      }),
    );
  };

  return card(
    cardHead("性能趋势", badge("按请求数加权", "muted"), badge(`${bucketLabel(bucketSeconds)}/点`, "muted")),
    h("div.stack", {},
      panel("平均耗时", "latency"),
      panel("平均首字延迟", "firstToken"),
    ),
    h("div.card-foot", {}, "两项都按桶内请求数加权，而不是对桶平均值再取平均；无请求的桶没有均值可算，跨过它的虚线只表示走势延续。"),
  );
}

// —— 页面组装 ——
function draw(firstPaint = false) {
  if (!host) return;
  if (!firstPaint && !host.isConnected) return;
  const { store } = xtxRef;
  const spec = currentSpec();
  const metrics = state.snapshot;
  const points = state.series || [];
  const bucketSeconds = state.seriesBucket || pickBucketSeconds(spec.hours);

  const children = [
    h("div.page-head", {},
      h("div.page-title", {},
        h("h1", "用量统计"),
        h("p.sub", "历史用量的累计、按天走势与维度明细。"),
      ),
      h("div.page-actions", {},
        state.snapshotAt ? freshness(state.snapshotAt) : null,
        metadataBadges(metrics),
        segmented(
          USAGE_RANGES.map((item) => ({
            id: item.hours, label: item.short, title: item.label,
          })),
          state.selection,
          pickRange,
          { "aria-label": "选择统计窗口" }),
      ),
    ),
  ];

  if (store.connectionError) children.push(notice(`无法连接 AMKR 服务：${store.connectionError}`, "error"));
  if (state.allHoursError) children.push(notice(`无法确定历史跨度：${state.allHoursError}`, "warn"));
  if (spec.truncated) {
    const limitDays = Math.round(MAX_HISTORY_HOURS / 24);
    children.push(notice(`历史跨度超过 ${limitDays} 天，图中只覆盖最近 ${limitDays} 天。`, "warn"));
  }
  if (state.snapshotError && !metrics) children.push(notice(`指标读取失败：${state.snapshotError}`, "error"));
  if (state.seriesError) children.push(notice(`趋势数据读取失败：${state.seriesError}`, "error"));
  // 按 Key 是单独一条读数：它读失败只该让那三张卡显示空态，不该连累上面的总量与曲线。
  if (state.keyUsageError) children.push(notice(`按 Key 用量读取失败：${state.keyUsageError}`, "error"));
  // 切到长窗口时查询要一会儿（1 年 = 扫更多行、点也更多）。这时下面的数字还是上一
  // 个窗口的，必须说明"正在换成你选的那个窗口"，否则会被当成新窗口的读数。
  if (state.loading && metrics) children.push(notice("正在读取所选窗口的数据，下方读数仍属于上一次查询的窗口。", "info"));

  if (!metrics) {
    children.push(skeleton("stats"));
    children.push(card(cardHead("累计用量"), skeleton("chart")));
    render(host, children);
    return;
  }

  children.push(kpiTiles(metrics, points, bucketSeconds));
  // 每排都要正好排满 12 轨，否则右边会剩一条空轨。这条约束在四档断点下同时成立
  // 靠的是两种排法：整宽卡（col-12）自成一行，半宽卡（col-4/col-5/col-6）两两成对。
  // 因此 col-8 不能夹在半宽卡中间：<=1024px 时它会变成整行，把前面的半宽卡剩在
  // 半行里（性能趋势与上游构成原来各占 col-8/col-4，就在这一档留了空轨）。
  //
  // 「按 Key」两张半宽卡与「模型 / 上游 Key」整宽卡排在最后：前者回答"哪把 Key 出去了"
  // （上游 Key 与访问密钥各一张），后者是带供应商前缀的「模型 × Key」明细。
  const keyUsage = state.keyUsage;
  children.push(h("div.grid-12", {},
    h("div.col-12", {}, cumulativeCard(points, bucketSeconds)),
    h("div.col-8", {}, dailyCard(points)),
    h("div.col-4", {}, hourlyCard(points)),
    h("div.col-4", {}, compositionCard(metrics)),
    h("div.col-4", {}, statusCard(metrics)),
    h("div.col-4", {}, upstreamCard(metrics, keyUsage)),
    h("div.col-12", {}, latencyCard(points, bucketSeconds)),
    h("div.col-6", {}, breakdownCard("模型用量", metrics.models, "模型", "窗口内没有模型调用。")),
    h("div.col-6", {}, breakdownCard("调用方用量", metrics.caller_types, "调用方", "窗口内没有调用方数据。")),
    h("div.col-6", {}, breakdownCard("按上游 Key 用量", upstreamKeyEntries(keyUsage?.upstream_keys),
      "Key", "窗口内没有可归因到上游 Key 的调用。", unattributedNote(keyUsage))),
    h("div.col-6", {}, breakdownCard("按访问密钥用量", accessKeyEntries(keyUsage?.access_keys),
      "访问密钥", "窗口内没有访问密钥发起的调用。",
      "只统计访问密钥发起的调用；完整权限与工作空间凭据的流量不在这一维。")),
    h("div.col-12", {}, breakdownCard("模型 / 上游 Key 用量", modelKeyEntries(keyUsage?.model_keys),
      "条目", "窗口内没有 Key 调用数据。")),
  ));

  render(host, children);
}
