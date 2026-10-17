// 状态监控页回归探针：用最小 DOM 垫片驱动真实的 webui/status-math.js 与
// webui/pages/status.js。
//
// 锁的是「坏了在界面上不容易看出来」的口径与行为：
//
//   1. 健康分的**边界**：样本门槛（<3 次一律 unknown）、80/50 两条分档线、首字 1.5s/8s
//      锚点、缓存率 30% 锚点、综合权重（成功率 60 / 首字 20 / 缓存率 20，缺读数按 0）。
//      阈值挪 1 分，界面照样"看起来正常"，只有逐格比对才能发现。
//   2. 错误归因六类：2xx 只计成功、无状态码 = failures − 非 2xx 状态码（不为负）、
//      3xx 不硬塞进「其它 4xx」。
//   3. 页面行为：矩阵格数 = 行数 × 列数；三种行身份（供应商 / 供应商·模型 /
//      供应商·Key）；健康显示切换只重着色**不重取**；维度切换必须带 group_by 重新取数；
//      全局筛选以**重复 provider_id** 透传；空库空态；接口 500 只出错误通知不抛异常；
//      onTick 静默重取。
//   4. **颜色不是唯一信息**：每个格子的 title 必须含请求数 / 成功率 / 首字 / 缓存率与
//      健康分，图例必须有文字阈值——色弱用户与只读到 title 的读屏用户都拿得到结论。
//
// 用法：node webui/probes/webui_status_probe.mjs（有失败时退出码 1）。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

// —— DOM 垫片 ——
// 保留 null 与文本节点语义（原生 replaceChildren 会把 null 变成文本 "null"），
// 否则会把 dom.js 的回归藏起来——见 webui_tip_probe.mjs 的同款说明。
class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.parent = null;
    this.value = "";
    this.checked = false;
    // 页面用 isConnected 判断节点是否已挂载（首绘之后的重绘必须挂载过才画）。
    // 浏览器里由真实 DOM 给；垫片里由探针在 renderStatus 返回后手动置真。
    this.isConnected = false;
  }
  append(...nodes) { this.#adopt(nodes); }
  replaceChildren(...nodes) { this.children = []; this.#adopt(nodes); }
  #adopt(nodes) {
    for (const node of nodes.flat(Infinity)) {
      if (node === null || node === undefined || node === false) continue;
      const child = node instanceof FakeNode ? node : new FakeText(node);
      child.parent = this;
      this.children.push(child);
    }
  }
  appendChild(node) { this.append(node); return node; }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) { return this.attrs[key]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((item) => item !== fn);
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
  }
  querySelector(selector) {
    const tags = String(selector).split(",").map((part) => part.trim());
    return findAll(this, (node) => tags.includes(node.tagName))[0] || null;
  }
  prepend(...nodes) { this.children.unshift(...nodes.flat()); }
  replaceWith() {}
  focus() {}
  get classList() {
    const self = this;
    const names = () => String(self.className).split(/\s+/).filter(Boolean);
    return {
      add: (...add) => { self.className = [...new Set([...names(), ...add])].join(" "); },
      remove: (...drop) => { self.className = names().filter((n) => !drop.includes(n)).join(" "); },
      contains: (name) => names().includes(name),
    };
  }
  get textContent() {
    return this.children
      .map((c) => (c.textContent === undefined ? String(c) : c.textContent))
      .join(" ")
      .trim();
  }
}

class FakeText extends FakeNode {
  constructor(data) { super("#text"); this.data = String(data); }
  get textContent() { return this.data; }
}

global.Node = FakeNode;
global.document = {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  body: new FakeNode("body"),
  addEventListener() {},
  removeEventListener() {},
};
global.localStorage = { getItem: () => "", setItem() {}, removeItem() {} };
global.location = { pathname: "/ui/", hash: "#/status" };
global.window = { addEventListener() {}, isSecureContext: true, location: global.location };
// 刻意不定义 ResizeObserver：charts.js 的 sizing() 会在测量可用时同步画首帧
// （探针环境没有布局引擎，走的就是这条分支）。

const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children || []) findAll(child, predicate, out);
  return out;
};
const hasClass = (node, name) =>
  [node?.className, node?.attrs?.class].some((value) =>
    String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (n) => hasClass(n, name));
const byTag = (root, tag) => findAll(root, (n) => n.tagName === tag);
const findText = (root, text) => findAll(root, (n) =>
  n.tagName === "#text" && String(n.data).includes(text)).length > 0;
const hasTitle = (root, text) => findAll(root, (n) =>
  String(n.attrs?.title || "").includes(text)).length > 0;
const buttonWithText = (root, text) =>
  findAll(root, (n) => n.tagName === "button" && n.textContent === text)[0];
const click = (node) => {
  for (const fn of node?.listeners?.click || []) fn({ target: node, preventDefault() {} });
};
// segmented 的容器带 aria-label：用它把「维度 / 健康显示 / 趋势指标 / 时间窗」四组分清，
// 否则「缓存率」同时出现在健康显示与趋势指标里，按文本找会点到另一组。
const segmentGroup = (root, ariaLabel) =>
  findAll(root, (n) => n.attrs?.["aria-label"] === ariaLabel)[0] || null;
const segmentButton = (root, ariaLabel, text) => {
  const group = segmentGroup(root, ariaLabel);
  if (!group) return null;
  return findAll(group, (n) => n.tagName === "button" && n.textContent === text)[0] || null;
};
// 全局筛选的 chip：挂在 data-dimension="provider" / "access-key" 的组里。
const filterChip = (root, dimension, value) => {
  const group = findAll(root, (n) => n.attrs?.["data-dimension"] === dimension)[0];
  if (!group) return null;
  return findAll(group, (n) => n.tagName === "button" && n.attrs?.["data-value"] === value)[0] || null;
};

// —— 假 fetch ——
// 三种响应形态：正常读数（按 group_by 给不同行身份）、空库、接口 500。
// 每次 /ui/status-monitor.json 的查询参数都记下来，断言请求侧的口径。
const requests = [];
const statusQueries = [];
const unexpected = [];
let mode = "normal"; // normal | normal2 | empty | error
let version = 0;

const jsonResponse = (payload) => ({
  ok: true,
  status: 200,
  async text() { return JSON.stringify(payload); },
});

const BUCKET_STARTS = [
  "2026-07-14T00:00:00+08:00",
  "2026-07-14T01:00:00+08:00",
  "2026-07-14T02:00:00+08:00",
  "2026-07-14T03:00:00+08:00",
  "2026-07-14T04:00:00+08:00",
];

const zeroCell = (index) => ({
  started_at: BUCKET_STARTS[index],
  ended_at: BUCKET_STARTS[index + 1],
  complete: index < 3,
  requests: 0, successes: 0, failures: 0,
  prompt_tokens: 0, cached_tokens: 0,
  total_first_token_ms: 0, total_duration_ms: 0,
});

// 一格有读数的统计，其余字段按参数补齐；格子只有契约规定的 10 个键。
const cell = (index, stats = {}) => ({
  ...zeroCell(index),
  requests: 0, successes: 0, failures: 0,
  prompt_tokens: 0, cached_tokens: 0,
  total_first_token_ms: 0, total_duration_ms: 0,
  ...stats,
  started_at: BUCKET_STARTS[index],
  ended_at: BUCKET_STARTS[index + 1],
  complete: index < 3,
});

// 窗口内共 8 次请求、4 成功 4 失败：矩阵三行（含 provider_id=null 的未归属行）。
// 行 A 的 3 次请求成功率 100%、平均首字 8000ms → 综合 60 分（warn）、错误率 100 分（good）；
// 行 B 的 3 次全失败 → 0 分（bad）；行 C 只有 2 次（< 门槛）→ unknown。
const rowsFor = (groupBy) => {
  const aCells = [cell(0, {
    requests: 3, successes: 3, failures: 0,
    prompt_tokens: 100, cached_tokens: 0,
    total_first_token_ms: 24000, total_duration_ms: 300,
  }), zeroCell(1), zeroCell(2), zeroCell(3)];
  const bCells = [cell(0, {
    requests: 3, successes: 0, failures: 3,
    total_duration_ms: 0,
  }), zeroCell(1), zeroCell(2), zeroCell(3)];
  const cCells = [zeroCell(0), cell(1, {
    requests: 2, successes: 1, failures: 1,
    prompt_tokens: 60, cached_tokens: 40,
    total_first_token_ms: 200, total_duration_ms: 100,
  }), zeroCell(2), zeroCell(3)];
  if (groupBy === "provider_model") {
    return [{ provider_id: "prov-a", upstream_model_id: "model-x", key_name: null, cells: aCells }];
  }
  if (groupBy === "provider_key") {
    return [{ provider_id: "prov-a", upstream_model_id: null, key_name: "key-main", cells: aCells }];
  }
  return [
    { provider_id: "prov-a", upstream_model_id: null, key_name: null, cells: aCells },
    { provider_id: "prov-b", upstream_model_id: null, key_name: null, cells: bCells },
    { provider_id: null, upstream_model_id: null, key_name: null, cells: cCells },
  ];
};

const WINDOW = { from: "2026-07-13T04:00:00+08:00", to: "2026-07-14T04:00:00+08:00", hours: 24 };

const seriesPoints = () => [
  {
    started_at: BUCKET_STARTS[0], ended_at: BUCKET_STARTS[1], complete: true,
    requests: 6, successes: 3, failures: 3, retries: 0,
    prompt_tokens: 100, cached_tokens: 0, total_first_token_ms: 24000, total_duration_ms: 300,
  },
  {
    started_at: BUCKET_STARTS[1], ended_at: BUCKET_STARTS[2], complete: true,
    requests: 2, successes: 1, failures: 1, retries: 0,
    prompt_tokens: 60, cached_tokens: 40, total_first_token_ms: 200, total_duration_ms: 100,
  },
  {
    started_at: BUCKET_STARTS[2], ended_at: BUCKET_STARTS[3], complete: true,
    requests: 0, successes: 0, failures: 0, retries: 0,
    prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0,
  },
  {
    started_at: BUCKET_STARTS[3], ended_at: BUCKET_STARTS[4], complete: false,
    requests: 0, successes: 0, failures: 0, retries: 0,
    prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0,
  },
];

const totalOf = ({ requests, successes, failures, promptTokens, cachedTokens, avgFirstTokenMS }) => ({
  requests,
  successes,
  failures,
  retries: 0,
  prompt_tokens: promptTokens,
  completion_tokens: 0,
  total_tokens: promptTokens,
  cached_tokens: cachedTokens,
  cache_creation_input_tokens: 0,
  cache_read_input_tokens: 0,
  cached_token_rate: promptTokens > 0 ? cachedTokens / promptTokens : 0,
  total_duration_ms: 400,
  avg_duration_ms: 50,
  min_duration_ms: 0,
  max_duration_ms: 300,
  total_first_token_ms: avgFirstTokenMS * requests,
  avg_first_token_ms: avgFirstTokenMS,
  min_first_token_ms: 0,
  max_first_token_ms: avgFirstTokenMS,
  status_codes: { "200": successes, "429": 2, "500": failures - 2 },
});

const payloadOf = ({ groupBy, total, rows, points, truncated = 0 }) => {
  const snapshot = {
    count_semantics: "upstream_attempt",
    window: WINDOW,
    started_at: "2026-07-14T03:00:00+08:00",
    database_path: "/tmp/metrics.db",
    rate_window_seconds: 60,
    current_rpm: 2,
    current_tpm: total.prompt_tokens,
    router_status: "red",
    active_requests: 0,
    total,
    models: {
      "model-x": { requests: 6, successes: 3, failures: 3, prompt_tokens: 100, cached_tokens: 0, total_first_token_ms: 24000, total_duration_ms: 300 },
      "model-y": { requests: 2, successes: 1, failures: 1, prompt_tokens: 60, cached_tokens: 40, total_first_token_ms: 200, total_duration_ms: 100 },
    },
  };
  return {
    count_semantics: "upstream_attempt",
    window: WINDOW,
    filters: { provider_ids: [], access_key_ids: [] },
    group_by: groupBy,
    bucket_seconds: 3600,
    snapshot,
    series: { bucket_seconds: 3600, points },
    matrix: { group_by: groupBy, rows, rows_truncated: truncated },
  };
};

const normalPayload = (groupBy, variant = version) => {
  const big = variant === 1;
  const total = big
    ? totalOf({ requests: 16, successes: 16, failures: 0, promptTokens: 320, cachedTokens: 160, avgFirstTokenMS: 1000 })
    : totalOf({ requests: 8, successes: 4, failures: 4, promptTokens: 160, cachedTokens: 40, avgFirstTokenMS: 3000 });
  if (big) total.status_codes = { "200": 16 };
  return payloadOf({
    groupBy,
    total,
    rows: rowsFor(groupBy),
    points: seriesPoints(),
    // variant=2 模拟后端 60 行截断：页面必须把「还有 N 行没画」说出来。
    truncated: variant === 2 ? 5 : 0,
  });
};

const emptyPayload = () => payloadOf({
  groupBy: "provider",
  total: totalOf({ requests: 0, successes: 0, failures: 0, promptTokens: 0, cachedTokens: 0, avgFirstTokenMS: 0 }),
  rows: [],
  points: [
    { started_at: BUCKET_STARTS[0], ended_at: BUCKET_STARTS[1], complete: true, requests: 0, successes: 0, failures: 0, retries: 0, prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0 },
    { started_at: BUCKET_STARTS[1], ended_at: BUCKET_STARTS[2], complete: true, requests: 0, successes: 0, failures: 0, retries: 0, prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0 },
    { started_at: BUCKET_STARTS[2], ended_at: BUCKET_STARTS[3], complete: false, requests: 0, successes: 0, failures: 0, retries: 0, prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0 },
  ],
});

global.fetch = async (url) => {
  const target = String(url);
  requests.push(target);
  if (target.startsWith("/api/providers")) {
    return jsonResponse({ providers: [{ id: "prov-a" }, { id: "prov-b" }], revision: "rev-1" });
  }
  if (target.startsWith("/api/access-keys")) {
    return jsonResponse({
      access_keys: [{ id: "ak1", name: "主密钥" }, { id: "ak2" }],
      revision: "rev-1",
    });
  }
  if (target.startsWith("/ui/status-monitor.json")) {
    const parsed = new URL(target, "http://probe.local");
    statusQueries.push({
      url: target,
      params: parsed.searchParams,
      hours: parsed.searchParams.get("hours"),
      groupBy: parsed.searchParams.get("group_by"),
      providerId: parsed.searchParams.getAll("provider_id"),
      accessKeyId: parsed.searchParams.getAll("access_key_id"),
    });
    if (mode === "error") {
      return {
        ok: false,
        status: 500,
        async text() { return JSON.stringify({ detail: "boom" }); },
      };
    }
    if (mode === "empty") return jsonResponse(emptyPayload());
    return jsonResponse(normalPayload(parsed.searchParams.get("group_by") || "provider"));
  }
  unexpected.push(target);
  return { ok: false, status: 404, async text() { return JSON.stringify({ detail: "unexpected" }); } };
};

const settle = async (rounds = 10) => {
  for (let index = 0; index < rounds; index += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
};

// —— 结果收集 ——
const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};
const eq = (name, actual, expected) =>
  check(name, JSON.stringify(actual) === JSON.stringify(expected),
    `实际=${JSON.stringify(actual)} 期望=${JSON.stringify(expected)}`);

// —— 一、status-math.js 口径 ——
const math = await import(pathToFileURL(path.join(WEBUI, "status-math.js")).href);
const {
  MIN_HEALTH_SAMPLES, HEALTH_THRESHOLDS, TTFT_GOOD_MS, TTFT_BAD_MS, CACHE_FULL_RATE,
  OVERALL_WEIGHTS, GROUP_BY_MODES, HEALTH_MODES, HEALTH_BAND_LABELS,
  cellMetrics, aggregateCells, healthScore, healthBand, healthBandLabel,
  rowLabel, formatHealthTooltip, errorReasons, ERROR_REASON_ROWS,
} = math;

const statCell = (stats) => ({
  started_at: BUCKET_STARTS[0],
  ended_at: BUCKET_STARTS[1],
  complete: true,
  ...stats,
});

eq("math_min_health_samples", MIN_HEALTH_SAMPLES, 3);
eq("math_health_thresholds", HEALTH_THRESHOLDS, { good: 80, warn: 50 });
eq("math_ttft_anchors", [TTFT_GOOD_MS, TTFT_BAD_MS], [1500, 8000]);
eq("math_cache_anchor", CACHE_FULL_RATE, 0.3);
eq("math_overall_weights", OVERALL_WEIGHTS, { success: 0.6, ttft: 0.2, cache: 0.2 });
eq("math_group_by_modes", GROUP_BY_MODES.map((item) => item.id), ["provider", "provider_model", "provider_key"]);
eq("math_health_modes", HEALTH_MODES.map((item) => item.id), ["overall", "success", "ttft", "cache"]);

// 样本门槛：2 次一律 unknown（对三种口径都成立），3 次才按分档。
const twoSamples = statCell({ requests: 2, successes: 2, total_first_token_ms: 3000, prompt_tokens: 100, cached_tokens: 50 });
eq("math_two_samples_unknown", healthBand(twoSamples, "overall"), "unknown");
eq("math_two_samples_success_unknown", healthBand(twoSamples, "success"), "unknown");
eq("math_two_samples_score_null", healthScore(twoSamples, "success"), null);
const threeSamples = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 0 });
eq("math_three_samples_enters_bands", healthBand(threeSamples, "overall"), "good");
eq("math_zero_requests_no_traffic", healthBand(statCell({ requests: 0 }), "overall"), "no-traffic");
eq("math_zero_requests_score_null", healthScore(statCell({ requests: 0 }), "overall"), null);

// 分档边界：成功率 100 / 50 / 33。
eq("math_success_100_good", healthBand(statCell({ requests: 3, successes: 3 }), "success"), "good");
eq("math_success_50_warn", healthBand(statCell({ requests: 4, successes: 2 }), "success"), "warn");
eq("math_success_33_bad", healthBand(statCell({ requests: 3, successes: 1 }), "success"), "bad");

// 综合分边界：80 是 good 的下沿、79 是 warn、50 是 warn 的下沿、49 是 bad。
// 80 = 成功率 100（60）+ 首字 1500ms 满分（20）+ 无缓存读数按 0（0）。
const score80 = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 0 });
eq("math_overall_score_80", healthScore(score80, "overall"), 80);
eq("math_overall_80_is_good", healthBand(score80, "overall"), "good");
// 79 = 60 + 首字 1825ms 的 95 分 × 0.2 = 19。
const score79 = statCell({ requests: 3, successes: 3, total_first_token_ms: 5475, prompt_tokens: 100, cached_tokens: 0 });
eq("math_overall_score_79", healthScore(score79, "overall"), 79);
eq("math_overall_79_is_warn", healthBand(score79, "overall"), "warn");
// 50 = 成功率 50%（30）+ 首字 1500ms（20）+ 缓存 0。
const score50 = statCell({ requests: 4, successes: 2, total_first_token_ms: 6000, prompt_tokens: 100, cached_tokens: 0 });
eq("math_overall_score_50", healthScore(score50, "overall"), 50);
eq("math_overall_50_is_warn", healthBand(score50, "overall"), "warn");
// 49 = 成功率 50%（30）+ 首字 1825ms（19）+ 缓存 0。
const score49 = statCell({ requests: 4, successes: 2, total_first_token_ms: 7300 });
eq("math_overall_score_49", healthScore(score49, "overall"), 49);
eq("math_overall_49_is_bad", healthBand(score49, "overall"), "bad");

// 综合权重：缺读数按 0 计入（而不是按满分或跳过）——只有成功率读数时最高 60 分。
eq("math_overall_missing_reads_are_zero",
  healthScore(statCell({ requests: 3, successes: 3 }), "overall"), 60);
// 三项都满分才是 100。
const allFull = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 30 });
eq("math_overall_all_full", healthScore(allFull, "overall"), 100);

// 首字锚点：1500ms 满分、8000ms 零分、4750ms 中点 50 分；没有首字读数 → unknown。
eq("math_ttft_1500_full", healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 1500 }), "ttft"), 100);
eq("math_ttft_8000_zero", healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 8000 }), "ttft"), 0);
eq("math_ttft_midpoint_50", healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 4750 }), "ttft"), 50);
eq("math_ttft_over_bad_zero",
  healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 20000 }), "ttft"), 0);
eq("math_ttft_no_reading_unknown", healthBand(statCell({ requests: 3, successes: 3 }), "ttft"), "unknown");
eq("math_ttft_unknown_band_warn_not_green",
  healthBand(statCell({ requests: 3, total_first_token_ms: 3 * 10000 }), "ttft"), "bad");

// 缓存率锚点：30% 满分、15% 得分 50、0% 零分；超过 30% 封顶 100；没有输入 Token → unknown。
eq("math_cache_30_full",
  healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 30 }), "cache"), 100);
eq("math_cache_15_half",
  healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 15 }), "cache"), 50);
eq("math_cache_0_zero",
  healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 0 }), "cache"), 0);
eq("math_cache_capped",
  healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 80 }), "cache"), 100);
eq("math_cache_no_prompt_unknown",
  healthBand(statCell({ requests: 3, successes: 3 }), "cache"), "unknown");
eq("math_cache_no_prompt_score_null",
  healthScore(statCell({ requests: 3, prompt_tokens: 0, cached_tokens: 0 }), "cache"), null);

// cellMetrics 的 null 口径：0/0 不是 0%。
eq("math_cell_rate_null_on_zero_requests", cellMetrics({ requests: 0 }).successRate, null);
eq("math_cell_cache_null_without_prompt", cellMetrics({ requests: 3, prompt_tokens: 0 }).cacheRate, null);
eq("math_cell_avg_ttft_null_without_reading", cellMetrics({ requests: 3, total_first_token_ms: 0 }).avgTtft, null);
eq("math_cell_avg_ttft_weighted", cellMetrics({ requests: 4, total_first_token_ms: 2000 }).avgTtft, 500);
eq("math_cell_failures_derived", cellMetrics({ requests: 5, successes: 2 }).failures, 3);
eq("math_cell_negative_clamped", cellMetrics({ requests: -1, successes: -5 }).requests, 0);

// aggregateCells：逐格相加（不是对比率再平均）、complete=false 传播。
const aggregate = aggregateCells([
  statCell({ requests: 2, successes: 1, failures: 1, prompt_tokens: 10, cached_tokens: 5 }),
  { ...statCell({ requests: 1, successes: 1, failures: 0, prompt_tokens: 20, cached_tokens: 0 }), complete: false },
]);
eq("math_aggregate_requests", aggregate.requests, 3);
eq("math_aggregate_successes", aggregate.successes, 2);
eq("math_aggregate_prompt_tokens", aggregate.prompt_tokens, 30);
eq("math_aggregate_complete_flag", aggregate.complete, false);
eq("math_aggregate_empty", aggregateCells([]).requests, 0);

// 行标签：三种身份 + 未归属。
eq("math_row_label_provider", rowLabel({ provider_id: "prov-a" }, "provider").label, "prov-a");
eq("math_row_label_null_provider", rowLabel({ provider_id: null }, "provider").label, "未归属");
eq("math_row_label_model",
  rowLabel({ provider_id: "prov-a", upstream_model_id: "model-x" }, "provider_model").label,
  "prov-a / model-x");
eq("math_row_label_model_null_detail",
  rowLabel({ provider_id: "prov-a", upstream_model_id: null }, "provider_model").label,
  "prov-a / 未归属");
eq("math_row_label_key",
  rowLabel({ provider_id: "prov-a", key_name: "key-main" }, "provider_key").label,
  "prov-a / key-main");

// 分档文案。
eq("math_band_labels", HEALTH_BAND_LABELS, {
  good: "健康", warn: "注意", bad: "异常", unknown: "样本不足", "no-traffic": "无流量",
});
eq("math_band_label_fn", healthBandLabel("warn"), "注意");

// 格子 title：颜色不是唯一信息，数值全部写出来。
const tip = formatHealthTooltip(statCell({
  requests: 3, successes: 2, failures: 1,
  prompt_tokens: 100, cached_tokens: 40,
  total_first_token_ms: 3000, total_duration_ms: 500,
}), "overall");
check("math_tooltip_requests", tip.includes("请求 3 次"), tip);
check("math_tooltip_success_rate", tip.includes("成功率 66.7%"), tip);
check("math_tooltip_ttft", tip.includes("平均首字 1.00s"), tip);
check("math_tooltip_cache", tip.includes("缓存率 40.0%"), tip);
check("math_tooltip_band_and_score", tip.includes("着色口径 综合") && tip.includes("健康分 80"), tip);
check("math_tooltip_time_range", tip.includes("时段 "), tip);
const partialTip = formatHealthTooltip(
  { ...statCell({ requests: 3, successes: 3, total_first_token_ms: 4500 }), complete: false }, "success");
check("math_tooltip_partial_flag", partialTip.includes("累加中，读数偏低"), partialTip);
check("math_tooltip_success_mode_label", partialTip.includes("着色口径 错误率"), partialTip);
check("math_tooltip_no_first_token", formatHealthTooltip(statCell({ requests: 3 }), "ttft").includes("平均首字 无读数"));

// 错误归因：六类、固定顺序、2xx 不计错、无状态码不为负、3xx 单独暴露。
const reasons = errorReasons({
  failures: 15,
  status_codes: {
    "200": 5, "401": 1, "403": 1, "429": 2,
    "400": 1, "404": 1, "413": 1, "422": 1,
    "500": 1, "502": 1, "418": 1, "302": 1,
  },
});
eq("math_error_rows_order", reasons.rows.map((row) => row.id),
  ["auth", "rate", "request", "upstream", "other4xx", "no-status"]);
eq("math_error_rows_labels", reasons.rows.map((row) => row.label),
  ["认证失败", "限流", "请求错误", "上游故障", "其它 4xx", "连接失败 / 无状态码"]);
eq("math_error_counts", reasons.rows.map((row) => row.count), [2, 2, 4, 2, 1, 3]);
eq("math_error_failures", reasons.failures, 15);
eq("math_error_coded", reasons.coded, 12);
eq("math_error_unclassified_3xx", reasons.unclassified, 1);
eq("math_error_no_status", reasons.noStatus, 3);
eq("math_error_ratio_auth", reasons.rows[0].ratio, 2 / 15);
// 2xx 只计成功：把 200 加得再多也不改变任何一档。
const reasonsWithMany2xx = errorReasons({ failures: 1, status_codes: { "200": 100, "500": 1 } });
eq("math_error_2xx_not_counted", reasonsWithMany2xx.rows.map((row) => row.count), [0, 0, 0, 1, 0, 0]);
// 无状态码 = failures − 非 2xx 状态码；状态码读数比 failures 多时也不能为负。
eq("math_error_no_status_never_negative", errorReasons({ failures: 1, status_codes: { "500": 3 } }).noStatus, 0);
eq("math_error_pure_failures", errorReasons({ failures: 2 }).rows[5].count, 2);
eq("math_error_empty_total", errorReasons({}).rows.map((row) => row.count), [0, 0, 0, 0, 0, 0]);
eq("math_error_empty_ratio_null", errorReasons({}).rows[0].ratio, null);
eq("math_error_reason_spec_ids", ERROR_REASON_ROWS.map((row) => row.id),
  ["auth", "rate", "request", "upstream", "other4xx", "no-status"]);

// —— 二、页面行为（DOM 垫片 + 可控假 fetch） ——
const { renderStatus } = await import(pathToFileURL(path.join(WEBUI, "pages", "status.js")).href);

const ticks = [];
const store = {
  health: { local_auth_enabled: true },
  authorized: true,
  providerFilters: [],
  accessKeyFilters: [],
  connectionError: null,
};
const host = renderStatus({ store, onTick: (listener) => ticks.push(listener) });
// 模拟 app.js 的挂载：页面自身在重绘前会检查 isConnected。
host.isConnected = true;
await settle();

const statusCallCount = () => statusQueries.length;
const lastStatusQuery = () => statusQueries[statusQueries.length - 1] || {};
const statTiles = () => byClass(host, "stat");
const statText = (index) => statTiles()[index]?.textContent || "";
const matrixCells = () => byClass(host, "status-cell");
const rowNames = () => byClass(host, "status-row-name").map((node) => node.textContent);
const cellTitle = (index) => String(matrixCells()[index]?.attrs?.title || "");

// 首屏：矩阵 3 行 × 4 列、行身份与行摘要、KPI 数字都对。
check("page_first_fetch", statusCallCount() === 1, String(statusCallCount()));
eq("page_first_fetch_query", [lastStatusQuery().hours, lastStatusQuery().groupBy], ["24", "provider"]);
check("page_no_unexpected_requests", unexpected.length === 0, unexpected.join(" , "));
eq("page_kpi_tile_count", statTiles().length, 4);
check("page_kpi_success_rate",
  statText(0).includes("成功率") && statText(0).includes("50.0%") && statText(0).includes("4 成功 · 4 失败"),
  statText(0));
check("page_kpi_avg_ttft",
  statText(1).includes("平均首字") && statText(1).includes("3.00s") && statText(1).includes("按 8 次请求加权"),
  statText(1));
check("page_kpi_cache_rate",
  statText(2).includes("缓存命中率") && statText(2).includes("25.0%") && statText(2).includes("缓存 40 / 输入 160 Token"),
  statText(2));
check("page_kpi_window_rpm",
  statText(3).includes("窗口平均 RPM") && statText(3).includes("0.01") &&
    statText(3).includes("8 次请求 / 24 小时") && statText(3).includes("次/分"),
  statText(3));

eq("page_matrix_row_count", byClass(host, "status-matrix-row").length, 3);
eq("page_matrix_cell_count_is_rows_times_columns", matrixCells().length, 3 * 4);
eq("page_row_labels_provider", rowNames(), ["prov-a", "prov-b", "未归属"]);
check("page_row_summary_has_score", byClass(host, "status-row-meta")[0]?.textContent.includes("健康分 60"),
  byClass(host, "status-row-meta")[0]?.textContent);
check("page_row_summary_unknown_samples",
  byClass(host, "status-row-meta")[2]?.textContent.includes("样本不足"),
  byClass(host, "status-row-meta")[2]?.textContent);
// 分档色落位：prov-a 综合 60 → warn；prov-b 0 → bad；2 次行 → unknown；空桶 → no-traffic。
const cellBands = matrixCells().map((node) =>
  ["good", "warn", "bad", "unknown", "no-traffic"].find((band) => hasClass(node, `band-${band}`)) || null);
eq("page_cell_band_distribution", {
  warn: cellBands.filter((band) => band === "warn").length,
  bad: cellBands.filter((band) => band === "bad").length,
  unknown: cellBands.filter((band) => band === "unknown").length,
  "no-traffic": cellBands.filter((band) => band === "no-traffic").length,
}, { warn: 1, bad: 1, unknown: 1, "no-traffic": 9 });
// 颜色不是唯一信息：每格 title 都含请求数 / 成功率 / 首字 / 缓存率；图例是文字阈值。
check("page_every_cell_title_has_numbers", matrixCells().every((_, index) => {
  const title = cellTitle(index);
  return title.includes("请求") && title.includes("成功率") &&
    title.includes("平均首字") && title.includes("缓存率");
}), cellTitle(0));
check("page_cell_title_has_health_score", cellTitle(0).includes("健康分 60"), cellTitle(0));
check("page_legend_is_text_not_color_only",
  findText(host, "健康（≥80）") && findText(host, "注意（50–79）") &&
    findText(host, "异常（<50）") && findText(host, "样本不足（<3 次）") &&
    findText(host, "无流量（无请求）") && findText(host, "累加中的尾桶（读数偏低）"));
check("page_matrix_aria_label_has_shape",
  String(byClass(host, "status-matrix")[0]?.attrs?.["aria-label"] || "").includes("3 行 × 4 列"),
  byClass(host, "status-matrix")[0]?.attrs?.["aria-label"]);
check("page_matrix_foot_states_grid",
  findText(host, "4 个时间桶") && findText(host, "≥ 3 次才参与着色"));
// 桶宽以服务端回声为准：矩阵按「N/格」、趋势按「N/点」标注。
check("page_bucket_badges_from_echo",
  findText(host, "1 小时/格") && findText(host, "1 小时/点"));
// 趋势图真的画了（同步首帧）：metrics 在 svg 的 aria-label 里。
check("page_trend_chart_drawn",
  findAll(host, (node) => node.tagName === "svg" && String(node.attrs?.["aria-label"] || "").includes("成功率趋势")).length === 1);
// 错误原因卡：两类失败各 50%。
check("page_error_card",
  findText(host, "错误原因") && findText(host, "限流") && findText(host, "上游故障") &&
    findText(host, "失败合计 4 次"));
// 模型明细：两个模型都列出来。
check("page_model_table",
  findText(host, "模型明细") && findText(host, "model-x") && findText(host, "model-y") &&
    findText(host, "2 个模型"));

// 健康显示切换：只重着色、不重取。同一组数据三种口径应得出不同档位。
const fetchBeforeHealthSwitch = statusCallCount();
click(segmentButton(host, "选择健康着色口径", "错误率"));
await settle(2);
eq("page_health_switch_no_refetch", statusCallCount(), fetchBeforeHealthSwitch);
check("page_health_switch_success_mode_green",
  hasClass(matrixCells()[0], "band-good") && cellTitle(0).includes("着色口径 错误率"),
  `${matrixCells()[0]?.className} / ${cellTitle(0)}`);
click(segmentButton(host, "选择健康着色口径", "首Token"));
await settle(2);
eq("page_health_switch_ttft_no_refetch", statusCallCount(), fetchBeforeHealthSwitch);
check("page_health_switch_ttft_mode_red",
  hasClass(matrixCells()[0], "band-bad") && cellTitle(0).includes("着色口径 首Token"),
  `${matrixCells()[0]?.className} / ${cellTitle(0)}`);
check("page_health_hint_text",
  findText(host, "按「首Token」着色：按平均首字延迟着色"));
click(segmentButton(host, "选择健康着色口径", "缓存率"));
await settle(2);
eq("page_health_switch_cache_no_refetch", statusCallCount(), fetchBeforeHealthSwitch);
check("page_health_switch_cache_mode_red", hasClass(matrixCells()[0], "band-bad"),
  matrixCells()[0]?.className);
click(segmentButton(host, "选择健康着色口径", "综合"));
await settle(2);
eq("page_health_switch_back_no_refetch", statusCallCount(), fetchBeforeHealthSwitch);
check("page_health_switch_back_restored", hasClass(matrixCells()[0], "band-warn"),
  matrixCells()[0]?.className);

// 维度切换：必须带 group_by 重新取数，行身份跟着变。
click(segmentButton(host, "选择矩阵维度", "供应商·模型"));
await settle();
check("page_dimension_model_refetch",
  statusCallCount() === fetchBeforeHealthSwitch + 1 && lastStatusQuery().groupBy === "provider_model",
  `${statusCallCount()} / ${lastStatusQuery().groupBy}`);
eq("page_dimension_model_labels", rowNames(), ["prov-a / model-x"]);
eq("page_dimension_model_cells", matrixCells().length, 1 * 4);
click(segmentButton(host, "选择矩阵维度", "供应商·Key"));
await settle();
check("page_dimension_key_refetch", lastStatusQuery().groupBy === "provider_key", lastStatusQuery().groupBy);
eq("page_dimension_key_labels", rowNames(), ["prov-a / key-main"]);
eq("page_dimension_key_cells", matrixCells().length, 1 * 4);
click(segmentButton(host, "选择矩阵维度", "供应商"));
await settle();
check("page_dimension_back_refetch", lastStatusQuery().groupBy === "provider", lastStatusQuery().groupBy);
eq("page_dimension_back_cells", matrixCells().length, 3 * 4);

// 全局筛选：两个供应商各点一次 → 请求里出现两个重复的 provider_id；
// 再加一把访问密钥 → access_key_id 也透传。
click(filterChip(host, "provider", "prov-a"));
await settle();
eq("page_filter_provider_single", lastStatusQuery().providerId, ["prov-a"]);
click(filterChip(host, "provider", "prov-b"));
await settle();
eq("page_filter_provider_repeated", lastStatusQuery().providerId, ["prov-a", "prov-b"]);
check("page_filter_repeated_in_url",
  lastStatusQuery().url.includes("provider_id=prov-a&provider_id=prov-b"), lastStatusQuery().url);
click(filterChip(host, "access-key", "ak1"));
await settle();
eq("page_filter_access_key", lastStatusQuery().accessKeyId, ["ak1"]);
eq("page_filter_provider_still_repeated", lastStatusQuery().providerId, ["prov-a", "prov-b"]);

// 空库空态：不再画格子，KPI 显示 "—"，并说明原因。
mode = "empty";
const fetchBeforeEmpty = statusCallCount();
click(buttonWithText(host, "刷新"));
await settle();
check("page_empty_refetched", statusCallCount() === fetchBeforeEmpty + 1, String(statusCallCount()));
eq("page_empty_no_cells", matrixCells().length, 0);
check("page_empty_reason",
  findText(host, "窗口内没有请求，健康矩阵暂时没有可着色的格子。"), "");
check("page_empty_kpi_dash", statText(0).includes("窗口内无请求") && statText(0).includes("—"), statText(0));
check("page_empty_trend_reason", findText(host, "窗口内没有请求，趋势暂时没有可画的读数。"));
check("page_empty_error_card", findText(host, "窗口内没有失败请求。"));

// 接口 500：只出现错误通知、不抛异常；切窗口先清掉旧读数（这里没有旧读数）。
mode = "error";
click(segmentButton(host, "选择统计窗口", "1h"));
await settle();
eq("page_error_refetched_hours", lastStatusQuery().hours, "1");
const errorNotice = findAll(host, (node) => hasClass(node, "notice") && hasClass(node, "tone-error"))[0];
check("page_error_notice", Boolean(errorNotice), byClass(host, "notice").map((node) => node.className).join(" , "));
check("page_error_notice_text",
  String(errorNotice?.textContent || "").includes("状态监控数据读取失败：AMKR 请求失败（HTTP 500）: boom"),
  errorNotice?.textContent);
eq("page_error_alert_role", errorNotice?.attrs?.role, "alert");
eq("page_error_no_cells", matrixCells().length, 0);
check("page_error_retry_entry", Boolean(buttonWithText(host, "重试") && findText(host, "暂时读不到状态监控数据。")));

// 恢复：点重试后重新取数并画出读数。
mode = "normal";
click(buttonWithText(host, "重试"));
await settle();
check("page_recover_fetched", lastStatusQuery().hours === "1" && matrixCells().length === 3 * 4,
  `${lastStatusQuery().hours} / ${matrixCells().length}`);

// onTick：页面注册的静默重取回调必须真的重新取数（不叠请求、不闪骨架由代码路径保证）。
eq("page_on_tick_registered", ticks.length, 1);
version = 1; // 下一次响应换成 16 次请求、16 成功的读数
const fetchBeforeTick = statusCallCount();
await ticks[0]();
await settle();
check("page_on_tick_refetched", statusCallCount() === fetchBeforeTick + 1, String(statusCallCount()));
check("page_on_tick_updated_kpi",
  statText(0).includes("100%") && statText(0).includes("16 成功 · 0 失败"), statText(0));
check("page_on_tick_cache_updated", statText(2).includes("50.0%"), statText(2));
check("page_on_tick_no_error_notice", byClass(host, "notice").length === 0,
  byClass(host, "notice").map((node) => node.className).join(" , "));

// 行截断：后端只回前 60 行时，页面必须把「还有 N 行未显示」说出来，
// 否则用户会把被截掉的渠道读成「不存在」。
version = 2;
click(buttonWithText(host, "刷新"));
await settle();
check("page_rows_truncated_notice",
  findText(host, "另有 5 行未显示"), byClass(host, "status-matrix-foot")[0]?.textContent);
check("page_rows_truncated_keeps_cells", matrixCells().length === 3 * 4, String(matrixCells().length));

// —— 结果 ——
let failures = 0;
for (const [name, value] of Object.entries(checks)) {
  if (value !== true) failures += 1;
  console.log(`${value === true ? "OK  " : "FAIL"} ${name}${value === true ? "" : `  ${value}`}`);
}
console.log("");
if (failures) {
  console.log(`✗ ${failures} / ${Object.keys(checks).length} 项失败：状态监控的健康口径或页面行为被改动了。`);
  process.exit(1);
}
console.log(`全部 ${Object.keys(checks).length} 项通过。`);
