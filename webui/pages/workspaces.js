// 工作空间：各空间的用量读数与请求流向。
//
// 与「用量统计」的分工：那边把整个实例当一个整体看（总量、按天、维度明细）；
// 这里回答"谁在用、用在哪"——每个工作空间各自用了多少，以及请求从工作空间出发
// 经过任务/模型/供应商一直流到上游模型的那条链路。
//
// 数据源是 /ui/workspace-usage.json（**本项目自有的接口**，不对照参照实现）：
// 它同时给出各空间的用量统计与六层流向连边，共用同一个时间窗口。

import {
  h, formatCount, formatCompact, formatDuration, errorText,
} from "../dom.js";
import { api } from "../api.js";
import {
  card, cardHead, stat, statGrid5, statSkeleton, notice, badge, empty, skeleton, render,
  segmented, table, freshness,
} from "../ui.js";
import { sankey, barList } from "../charts.js";
import {
  USAGE_RANGES, ALL_HISTORY, MAX_HISTORY_HOURS, historyHours, rangeSpec,
  formatPercentValue, formatCompactNumber,
} from "../chart-math.js";

const state = {
  selection: 24,
  allHours: null,
  allHoursAt: null,
  data: null,
  dataKey: null,
  error: null,
  loading: true,
  // 流向的宽度口径：请求数还是 Token。两者回答不同问题——请求数看"调用次数",
  // Token 看"实际消耗"，长上下文的工作空间在 Token 视图下会明显更粗。
  metric: "requests",
  sort: { key: "requests", direction: "desc" },
  at: null,
};

let host = null;
let xtxRef = null;
let token = 0;

function currentSpec() {
  return rangeSpec(state.selection, { allHours: state.allHours });
}

function rangeLabel() {
  const spec = currentSpec();
  if (!spec.truncated) return spec.label;
  return `全部历史（仅最近 ${Math.round(MAX_HISTORY_HOURS / 24)} 天）`;
}

export function renderWorkspaces(context) {
  xtxRef = context;
  const { store } = context;
  host = h("div.stack");

  if (store.health?.local_auth_enabled && !store.authorized) {
    return h("div.stack", {}, notice("需要本地鉴权 Key 才能读取工作空间用量。", "warn"));
  }

  prepareRange().then(() => load(true));
  context.onTick?.(() => load(false));
  draw(true);
  return host;
}

// 「全部历史」先用明细接口拿最早一条记录的时间，再折算成小时数。
// 与用量统计页同一套做法（/ui/workspace-usage.json 也不接受"全部历史"作为小时数）。
async function prepareRange() {
  if (state.selection !== ALL_HISTORY) return;
  if (state.allHours !== null && state.allHoursAt) return;
  try {
    const data = await api.requests({ all_history: true, limit: 1 });
    state.allHours = historyHours(data?.window?.from || null) ?? 1;
    state.allHoursAt = new Date().toISOString();
  } catch {
    // 拿不到跨度就退回 24 小时，页面仍可用（只是窗口偏短）。
    state.allHours = 24;
  }
}

function pickRange(selection) {
  state.selection = selection === ALL_HISTORY ? ALL_HISTORY : Number(selection);
  state.loading = true;
  draw();
  prepareRange().then(() => load(true));
}

async function load(force = false) {
  const spec = currentSpec();
  const key = `${state.selection}:${spec.hours}`;
  if (!force && state.data && state.dataKey === key) return;
  const current = ++token;
  try {
    const data = await api.workspaceUsage({
      hours: spec.hours,
      allHistory: state.selection === ALL_HISTORY,
    });
    if (current !== token) return;
    state.error = null;
    state.data = data;
    state.dataKey = key;
    state.at = new Date().toISOString();
  } catch (error) {
    if (current !== token) return;
    state.error = errorText(error);
    state.data = null;
  }
  state.loading = false;
  draw();
}

// —— KPI ——
function kpiTiles(data) {
  const workspaces = data.workspaces || [];
  const unattributed = data.unattributed || {};
  const totals = workspaces.reduce((acc, item) => ({
    requests: acc.requests + (item.stats.requests || 0),
    successes: acc.successes + (item.stats.successes || 0),
    failures: acc.failures + (item.stats.failures || 0),
    retries: acc.retries + (item.stats.retries || 0),
    prompt_tokens: acc.prompt_tokens + (item.stats.prompt_tokens || 0),
    total_tokens: acc.total_tokens + (item.stats.total_tokens || 0),
    cached_tokens: acc.cached_tokens + (item.stats.cached_tokens || 0),
    duration_ms: acc.duration_ms + (item.stats.total_duration_ms || 0),
    first_token_ms: acc.first_token_ms + (item.stats.total_first_token_ms || 0),
  }), {
    requests: 0, successes: 0, failures: 0, retries: 0, prompt_tokens: 0,
    total_tokens: 0, cached_tokens: 0, duration_ms: 0, first_token_ms: 0,
  });

  const successRatio = totals.requests ? totals.successes / totals.requests : null;
  const busiest = workspaces.reduce(
    (best, item) => (best && best.stats.requests >= item.stats.requests ? best : item), null);
  // 未归属占比：升级后第一眼最需要的数字——它说明有多少历史还没归到空间上。
  const allRequests = totals.requests + (unattributed.requests || 0);
  const orphanRatio = allRequests ? (unattributed.requests || 0) / allRequests : 0;
  // 后五张与「用量统计」同一套口径：都按请求数加权，而不是把各空间的平均值再平均。
  const perRequest = (total) => (totals.requests ? Math.round(total / totals.requests) : null);
  const cacheRatio = totals.prompt_tokens ? totals.cached_tokens / totals.prompt_tokens : null;
  const retryRatio = totals.requests ? totals.retries / totals.requests : null;
  const failRatio = successRatio === null ? null : 1 - successRatio;

  // 10 张瓦片：5 列时两整行、2 列时五行、1 列时十行，每一档都排满。
  // 只放 5 张的话 2 列档会甩出一张孤儿瓦片（webui/probes/webui_layout_probe.mjs 锁这条）。
  return statGrid5(
    stat("工作空间", formatCount(workspaces.length),
      workspaces.length ? `最活跃：${busiest?.name}（${formatCount(busiest?.stats.requests)} 次）` : "窗口内没有归属记录",
      { iconName: "layers" }),
    stat("归属请求", formatCount(totals.requests), `最近 ${rangeLabel()}`, { iconName: "activity" }),
    stat("归属 Token", formatCompact(totals.total_tokens),
      `缓存 ${formatCompact(totals.cached_tokens)} Token`, { iconName: "cost" }),
    stat("成功率", successRatio === null ? "-" : formatPercentValue(successRatio, 1),
      `${formatCount(totals.successes)} 成功 · ${formatCount(totals.failures)} 失败`,
      { iconName: "check", tone: successRatio !== null && successRatio < 0.95 ? "bad" : null }),
    stat("未归属请求", formatCount(unattributed.requests || 0),
      allRequests
        ? `占窗口内全部请求的 ${formatPercentValue(orphanRatio, 1)}`
        : "窗口内无请求",
      { iconName: "alert", tone: orphanRatio > 0 ? "warn" : null }),
    stat("失败请求", formatCount(totals.failures),
      failRatio === null ? "窗口内无请求" : `占归属请求的 ${formatPercentValue(failRatio, 1)}`,
      { iconName: "alert", tone: totals.failures ? "bad" : null }),
    // 重试次数与成功率分开看：上游抖动到靠重试兜住时，成功率照样接近 100%。
    stat("重试次数", formatCount(totals.retries),
      retryRatio === null ? "窗口内无请求" : `重试率 ${formatPercentValue(retryRatio, 1)}`,
      { iconName: "refresh" }),
    stat("缓存命中率", cacheRatio === null ? "-" : formatPercentValue(cacheRatio, 1),
      `缓存 ${formatCompact(totals.cached_tokens)} / 输入 ${formatCompact(totals.prompt_tokens)} Token`,
      { iconName: "filter", tone: cacheRatio !== null && cacheRatio > 0.3 ? "good" : null }),
    stat("平均耗时", perRequest(totals.duration_ms) === null ? "-" : formatDuration(perRequest(totals.duration_ms)),
      totals.requests ? `按 ${formatCount(totals.requests)} 次归属请求加权` : "窗口内无请求",
      { iconName: "clock" }),
    stat("平均首字", perRequest(totals.first_token_ms) ? formatDuration(perRequest(totals.first_token_ms)) : "-",
      totals.requests ? `按 ${formatCount(totals.requests)} 次归属请求加权` : "窗口内无请求",
      { iconName: "bolt" }),
  );
}

// —— 流向图 ——
// 宽度口径可切：请求数与 Token 各回答一个问题，且同一份 links 里两者都有，
// 切换不需要重新取数。
function metricOf(link) {
  return state.metric === "tokens" ? Number(link.total_tokens) || 0 : Number(link.requests) || 0;
}

function flowCard(data) {
  const rawLinks = data.links || [];
  // 把宽度口径统一搬到 requests 字段上交给布局算法（它只认这一个字段名），
  // 避免在 chart-math 里再开一个口径参数。
  const links = rawLinks.map((link) => ({ ...link, requests: metricOf(link) }));
  const metricLabel = state.metric === "tokens" ? "Token" : "次请求";
  const total = links.reduce((sum, link) => sum + link.requests, 0);

  return card(
    cardHead("请求流向",
      badge(rangeLabel(), "muted"),
      h("div.head-tools", {},
        segmented(
          [{ id: "requests", label: "按请求数" }, { id: "tokens", label: "按 Token" }],
          state.metric,
          (id) => { state.metric = id; draw(); },
          { "aria-label": "选择流向宽度口径" },
        ),
      ),
    ),
    links.length
      ? h("div.stack", {},
          sankey({
            links,
            layers: data.layers || [],
            metricLabel,
            height: 440,
            formatValue: (value) => (state.metric === "tokens"
              ? formatCompactNumber(value)
              : formatCount(value)),
            ariaLabel: `请求从工作空间到上游模型的流向，最近 ${rangeLabel()}`,
          }),
          h("div.card-foot", {},
            "流带越粗表示该段承载的请求越多。同一层的节点按承载量从上到下排列；"
            + "某一端缺失（例如没有供应商归因）的请求会在此处留出缺口，不并入其它流。"),
        )
      : empty("窗口内没有可归因到工作空间的请求。", {
          icon: "activity",
          hint: "工作空间归属从本次升级后开始记录，历史行会计入「未归属」。",
        }),
  );
}

// —— 各空间明细 ——
const COLUMNS = [
  { key: "name", label: "工作空间", render: (row) => h("span.mono", { title: row.name }, row.name), sortValue: (row) => row.name },
  { key: "requests", label: "请求", numeric: true, render: (row) => formatCount(row.stats.requests), sortValue: (row) => row.stats.requests },
  { key: "share", label: "占比", numeric: true, render: (row) => formatPercentValue(row.share, 1), sortValue: (row) => row.share },
  { key: "successes", label: "成功", numeric: true, render: (row) => formatCount(row.stats.successes), sortValue: (row) => row.stats.successes },
  { key: "failures", label: "失败", numeric: true, render: (row) => (row.stats.failures
      ? h("span", { style: { color: "var(--md-error)" } }, formatCount(row.stats.failures))
      : "0"), sortValue: (row) => row.stats.failures },
  { key: "rate", label: "成功率", numeric: true, render: (row) => formatPercentValue(row.stats.requests ? row.stats.successes / row.stats.requests : null, 1), sortValue: (row) => (row.stats.requests ? row.stats.successes / row.stats.requests : -1) },
  { key: "tokens", label: "Token", numeric: true, render: (row) => formatCompact(row.stats.total_tokens), sortValue: (row) => row.stats.total_tokens },
  { key: "duration", label: "平均耗时", numeric: true, render: (row) => formatCompact(row.stats.avg_duration_ms), sortValue: (row) => row.stats.avg_duration_ms },
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

function breakdownCard(data) {
  const workspaces = data.workspaces || [];
  const unattributed = data.unattributed || {};
  const attributed = workspaces.reduce((sum, item) => sum + (item.stats.requests || 0), 0);
  const allRequests = attributed + (unattributed.requests || 0);

  const rows = workspaces
    .map((item) => ({ ...item, share: allRequests ? item.stats.requests / allRequests : 0 }))
    .sort(comparator());

  if (!rows.length) {
    return card(cardHead("空间明细", badge("0 项", "muted")),
      empty("窗口内没有归属到工作空间的请求。", { icon: "logs" }));
  }

  return card(
    cardHead("空间明细", badge(`${rows.length} 项`, "muted"), badge(rangeLabel(), "muted")),
    table(COLUMNS, rows, "窗口内没有归属数据。", {
      sort: state.sort,
      onSort: (key) => {
        const direction = state.sort.key === key && state.sort.direction === "desc" ? "asc" : "desc";
        state.sort = { key, direction };
        draw();
      },
    }),
    h("div.card-foot", {},
      h("div.row-between", {},
        h("span", "归属合计"),
        h("span.inline", {},
          h("strong", formatCount(attributed)),
          h("span.muted", " 次请求 · "),
          h("strong", formatCompact(workspaces.reduce((sum, item) => sum + (item.stats.total_tokens || 0), 0))),
          h("span.muted", " Token"),
        ),
      ),
    ),
  );
}

// —— 空间构成 ——
function rankCard(data) {
  const rows = (data.workspaces || [])
    .map((item) => ({ name: item.name, value: item.stats.requests || 0 }))
    .sort((a, b) => b.value - a.value);
  if (!rows.length) return null;
  return card(
    cardHead("请求排行", badge(rangeLabel(), "muted")),
    barList(rows, {
      format: (row) => `${formatCount(row.value)} 次`,
      emptyText: "窗口内没有归属请求。",
    }),
  );
}

// tokenRankCard 与请求排行并排，排序经常不一样：长上下文的空间在 Token 榜上更靠前。
// 与 rankCard 不同，它空数据时也返回一张卡——它和请求排行同处一排，留空会看见缺口。
function tokenRankCard(data) {
  const rows = (data.workspaces || [])
    .map((item) => ({ name: item.name, value: item.stats.total_tokens || 0 }))
    .filter((row) => row.value > 0)
    .sort((a, b) => b.value - a.value);
  return card(
    cardHead("Token 排行", badge(rangeLabel(), "muted")),
    rows.length
      ? barList(rows, {
          tone: "secondary",
          format: (row) => `${formatCompact(row.value)} Token`,
          emptyText: "窗口内没有归属 Token。",
        })
      : empty("窗口内没有归属 Token。", { icon: "cost" }),
  );
}

// —— 页面组装 ——
function draw(firstPaint = false) {
  if (!host) return;
  if (!firstPaint && !host.isConnected) return;
  const { store } = xtxRef;
  const data = state.data;

  const children = [
    h("div.page-head", {},
      h("div.page-title", {},
        h("h1", "工作空间"),
        h("p.sub", "各工作空间的用量读数，以及请求从工作空间流向上游模型的完整链路。"),
      ),
      h("div.page-actions", {},
        data && state.at ? freshness(state.at) : null,
        segmented(
          USAGE_RANGES.map((item) => ({ id: item.hours, label: item.short, title: item.label })),
          state.selection,
          pickRange,
          { "aria-label": "选择统计窗口" },
        ),
      ),
    ),
  ];

  if (store.connectionError) children.push(notice(`无法连接 AMKR 服务：${store.connectionError}`, "error"));
  if (state.error) children.push(notice(`工作空间用量读取失败：${state.error}`, "error"));
  if (state.loading && data) children.push(notice("正在读取所选窗口的数据，下方读数仍属于上一次查询的窗口。", "info"));

  if (!data) {
    // 10 张 / 5 列，与 kpiTiles 的真实网格对齐（对不上数据到位时会整块跳一下）。
    children.push(statSkeleton(10, 5));
    children.push(card(cardHead("请求流向"), skeleton("chart")));
    render(host, children);
    return;
  }

  const unattributed = data.unattributed || {};
  children.push(kpiTiles(data));
  // 未归属不是一个"次要细节"：它就是升级前的那段历史。放在流向图**之前**，
  // 否则用户会先看到一张少了一块的图，再往下才明白为什么少了。
  if (unattributed.requests) {
    children.push(notice(
      `有 ${formatCount(unattributed.requests)} 次请求没有工作空间归属（共 ${formatCompact(unattributed.total_tokens)} Token），`
      + "它们不计入任何工作空间，也不出现在流向图里。工作空间归属从本版本起才开始记录，升级前的历史行都会落在这里。",
      "info"));
  }
  children.push(h("div.grid-12", {},
    h("div.col-12", {}, flowCard(data)),
    // 明细表独占整行：它 9 列，收成半宽在 1024px 以下会挤出横向滚动条。
    h("div.col-12", {}, breakdownCard(data)),
    h("div.col-6", {}, rankCard(data) || card(cardHead("请求排行"), empty("窗口内没有归属请求。", { icon: "activity" }))),
    h("div.col-6", {}, tokenRankCard(data)),
  ));

  render(host, children);
}
