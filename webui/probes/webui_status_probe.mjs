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
//   3. **双请求**：页面每次 load 并发两次同一接口——跟随页头时间窗的页面请求（不带
//      bucket_seconds）驱动 KPI / 趋势 / 错误 / 模型明细，固定 12 小时、15 分钟桶的
//      矩阵请求（bucket_seconds=900，49 列）只驱动矩阵。矩阵窗口与页头时间窗解耦，
//      切时间窗不改矩阵徽标；切维度 / 筛选两路都要重取。
//   4. 页面行为：矩阵 3 行 × 49 列 = 147 格、每行 cells 定长 49、没有横向滚动容器、
//      列模板 49 等分、时间轴 7 个标签；健康显示切换只重着色不重取；重复 provider_id
//      透传；空库 / 仅矩阵失败 / 有请求但无法归因 / 整页失败四种空态；onTick 两路都重取。
//   5. **颜色不是唯一信息**：每个格子的 title 必须含请求数 / 成功率 / 首字 / 缓存率与
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
// 页面每次 load 并发两次同一接口：
//   - 页面请求（不带 bucket_seconds）：跟随页头时间窗，驱动 KPI / 趋势 / 错误 / 模型明细；
//   - 矩阵请求（bucket_seconds=900）：固定 12 小时 / 15 分钟一格，只驱动渠道健康矩阵。
// 这里按 bucket_seconds 是否存在分流，并把每次查询参数记下来供请求侧断言。
const requests = [];
const statusQueries = [];
const unexpected = [];
// normal | empty | matrix-error（仅矩阵 500）| matrix-rows-empty | error（两路都失败）
let mode = "normal";
// 0 正常 / 1 轮询后页面请求换成 16 次全成功 / 2 矩阵被 60 行上限截断
let version = 0;
// 0 行 A 缓存率 0%（健康分 60）/ 1 行 A 缓存率 100%（健康分 80）：供「矩阵恢复后数据已更新」断言。
let matrixRevision = 0;

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

// 页面请求的窗口以服务端回显为准：窗口小时数跟着请求走，页面才能算对 RPM 与标注。
const pageWindow = (hours) => ({
  from: "2026-07-13T04:00:00+08:00",
  to: "2026-07-14T04:00:00+08:00",
  hours,
});
const PAGE_BUCKET_SECONDS = 3600;

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

const pagePayload = ({
  groupBy, hours, total, points = seriesPoints(), models, truncated = 0,
}) => {
  const snapshot = {
    count_semantics: "upstream_attempt",
    window: pageWindow(hours),
    started_at: "2026-07-14T03:00:00+08:00",
    database_path: "/tmp/metrics.db",
    rate_window_seconds: 60,
    current_rpm: 2,
    current_tpm: total.prompt_tokens,
    router_status: "red",
    active_requests: 0,
    total,
    models: models || {
      "model-x": { requests: 6, successes: 3, failures: 3, prompt_tokens: 100, cached_tokens: 0, total_first_token_ms: 24000, total_duration_ms: 300 },
      "model-y": { requests: 2, successes: 1, failures: 1, prompt_tokens: 60, cached_tokens: 40, total_first_token_ms: 200, total_duration_ms: 100 },
    },
  };
  return {
    count_semantics: "upstream_attempt",
    window: pageWindow(hours),
    filters: { provider_ids: [], access_key_ids: [] },
    group_by: groupBy,
    bucket_seconds: PAGE_BUCKET_SECONDS,
    snapshot,
    series: { bucket_seconds: PAGE_BUCKET_SECONDS, points },
    matrix: { group_by: groupBy, rows: rowsFor(groupBy), rows_truncated: truncated },
  };
};

const normalPagePayload = (hours, groupBy, variant = version) => {
  const big = variant === 1;
  const total = big
    ? totalOf({ requests: 16, successes: 16, failures: 0, promptTokens: 320, cachedTokens: 160, avgFirstTokenMS: 1000 })
    : totalOf({ requests: 8, successes: 4, failures: 4, promptTokens: 160, cachedTokens: 40, avgFirstTokenMS: 3000 });
  if (big) total.status_codes = { "200": 16 };
  return pagePayload({
    groupBy,
    hours,
    total,
    points: seriesPoints(),
    // variant=2 模拟矩阵被 60 行上限截断：页面必须把「还有 N 行没画」说出来。
    truncated: variant === 2 ? 5 : 0,
  });
};

const emptyPagePayload = (hours, groupBy = "provider") => pagePayload({
  groupBy,
  hours,
  total: totalOf({ requests: 0, successes: 0, failures: 0, promptTokens: 0, cachedTokens: 0, avgFirstTokenMS: 0 }),
  models: {},
  points: BUCKET_STARTS.slice(0, 3).map((start, index) => ({
    started_at: start,
    ended_at: BUCKET_STARTS[index + 1],
    complete: index < 2,
    requests: 0, successes: 0, failures: 0, retries: 0,
    prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0,
  })),
});

// —— 矩阵请求 fixture：固定 12 小时 / 15 分钟桶 / 49 列 ——
// 首列对齐桶边界（16:00 是 15 分钟边界），之后每列 900 秒；列数 49 = 48 个整桶 + 首桶。
const MATRIX_HOURS = 12;
const MATRIX_BUCKET_SECONDS = 900;
const MATRIX_COLUMNS = 49;
const MATRIX_WINDOW = {
  from: "2026-07-13T16:00:00+08:00",
  to: "2026-07-14T04:00:00+08:00",
  hours: MATRIX_HOURS,
};

const pad2 = (value) => String(value).padStart(2, "0");
const beijingIso = (ms) => {
  const moment = new Date(ms + 8 * 3600 * 1000);
  return `${moment.getUTCFullYear()}-${pad2(moment.getUTCMonth() + 1)}-${pad2(moment.getUTCDate())}`
    + `T${pad2(moment.getUTCHours())}:${pad2(moment.getUTCMinutes())}:${pad2(moment.getUTCSeconds())}+08:00`;
};
const MATRIX_STARTS = Array.from({ length: MATRIX_COLUMNS + 1 },
  (_, index) => beijingIso(Date.parse(MATRIX_WINDOW.from) + index * MATRIX_BUCKET_SECONDS * 1000));

const matrixCell = (index, stats = {}) => ({
  started_at: MATRIX_STARTS[index],
  ended_at: MATRIX_STARTS[index + 1],
  complete: index < MATRIX_COLUMNS - 1,
  requests: 0, successes: 0, failures: 0,
  prompt_tokens: 0, cached_tokens: 0,
  total_first_token_ms: 0, total_duration_ms: 0,
  ...stats,
});

const matrixRowCells = (overrides = []) => {
  const cells = Array.from({ length: MATRIX_COLUMNS }, (_, index) => matrixCell(index));
  for (const [index, stats] of overrides) cells[index] = matrixCell(index, stats);
  return cells;
};

// 与页面请求同一套行身份与总量（8 次请求）：矩阵三行（含 provider_id=null 的未归属行）。
// 行 A 的 3 次请求成功率 100%、平均首字 8000ms → 综合 60 分（warn）、错误率 100 分（good）；
// 行 B 的 3 次全失败 → 0 分（bad）；行 C 只有 2 次（< 门槛）→ unknown。
const matrixRowsFor = (groupBy, updated = matrixRevision > 0) => {
  const aCells = matrixRowCells([[0, {
    requests: 3, successes: 3, failures: 0,
    prompt_tokens: 100,
    // 恢复用例：缓存率 0% → 100%，行 A 健康分 60 → 80，用来证明画出的是新响应而不是旧数据。
    cached_tokens: updated ? 100 : 0,
    total_first_token_ms: 24000, total_duration_ms: 300,
  }]]);
  const bCells = matrixRowCells([[0, {
    requests: 3, successes: 0, failures: 3,
    total_duration_ms: 0,
  }]]);
  const cCells = matrixRowCells([[1, {
    requests: 2, successes: 1, failures: 1,
    prompt_tokens: 60, cached_tokens: 40,
    total_first_token_ms: 200, total_duration_ms: 100,
  }]]);
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

const matrixSeriesPoints = ({ empty = false } = {}) => Array.from(
  { length: MATRIX_COLUMNS },
  (_, index) => {
    const stats = empty || index > 1
      ? { requests: 0, successes: 0, failures: 0, prompt_tokens: 0, cached_tokens: 0, total_first_token_ms: 0, total_duration_ms: 0 }
      : index === 0
        ? { requests: 6, successes: 3, failures: 3, prompt_tokens: 100, cached_tokens: 0, total_first_token_ms: 24000, total_duration_ms: 300 }
        : { requests: 2, successes: 1, failures: 1, prompt_tokens: 60, cached_tokens: 40, total_first_token_ms: 200, total_duration_ms: 100 };
    return {
      started_at: MATRIX_STARTS[index],
      ended_at: MATRIX_STARTS[index + 1],
      complete: index < MATRIX_COLUMNS - 1,
      retries: 0,
      ...stats,
    };
  },
);

const matrixPayload = (groupBy, { rows, empty = false } = {}) => {
  const total = empty
    ? totalOf({ requests: 0, successes: 0, failures: 0, promptTokens: 0, cachedTokens: 0, avgFirstTokenMS: 0 })
    : totalOf({ requests: 8, successes: 4, failures: 4, promptTokens: 160, cachedTokens: 40, avgFirstTokenMS: 3000 });
  const snapshot = {
    count_semantics: "upstream_attempt",
    window: MATRIX_WINDOW,
    started_at: "2026-07-14T03:00:00+08:00",
    database_path: "/tmp/metrics.db",
    rate_window_seconds: 60,
    current_rpm: 2,
    current_tpm: total.prompt_tokens,
    router_status: "red",
    active_requests: 0,
    total,
    models: {},
  };
  return {
    count_semantics: "upstream_attempt",
    window: MATRIX_WINDOW,
    filters: { provider_ids: [], access_key_ids: [] },
    group_by: groupBy,
    bucket_seconds: MATRIX_BUCKET_SECONDS,
    snapshot,
    series: { bucket_seconds: MATRIX_BUCKET_SECONDS, points: matrixSeriesPoints({ empty }) },
    matrix: {
      group_by: groupBy,
      rows: rows === undefined ? matrixRowsFor(groupBy) : rows,
      rows_truncated: version === 2 ? 5 : 0,
    },
  };
};

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
    const bucketSeconds = parsed.searchParams.get("bucket_seconds");
    const hours = parsed.searchParams.get("hours");
    const groupBy = parsed.searchParams.get("group_by") || "provider";
    statusQueries.push({
      url: target,
      params: parsed.searchParams,
      hours,
      groupBy,
      bucketSeconds,
      providerId: parsed.searchParams.getAll("provider_id"),
      accessKeyId: parsed.searchParams.getAll("access_key_id"),
    });
    const fail = (detail) => ({
      ok: false,
      status: 500,
      async text() { return JSON.stringify({ detail }); },
    });
    // 两路都失败：整页退化成错误态（矩阵卡不画）。
    if (mode === "error") return fail("boom");
    // bucket_seconds 是否存在是两路请求的稳定区分：矩阵请求显式固定 15 分钟桶。
    if (bucketSeconds !== null) {
      if (mode === "matrix-error") return fail("matrix boom");
      if (mode === "empty") return jsonResponse(matrixPayload(groupBy, { rows: [], empty: true }));
      if (mode === "matrix-rows-empty") return jsonResponse(matrixPayload(groupBy, { rows: [] }));
      return jsonResponse(matrixPayload(groupBy));
    }
    if (mode === "empty") return jsonResponse(emptyPagePayload(Number(hours) || 24, groupBy));
    return jsonResponse(normalPagePayload(Number(hours) || 24, groupBy));
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
// 页面请求不带 bucket_seconds，矩阵请求带 bucket_seconds=900：按这个特征取最近一次，
// 不依赖两路请求谁先落地。
const lastQuery = (isMatrix) => [...statusQueries].reverse()
  .find((query) => (query.bucketSeconds !== null) === isMatrix) || {};
const statTiles = () => byClass(host, "stat");
const statText = (index) => statTiles()[index]?.textContent || "";
const matrixCells = () => byClass(host, "status-cell");
const rowNames = () => byClass(host, "status-row-name").map((node) => node.textContent);
const cellTitle = (index) => String(matrixCells()[index]?.attrs?.title || "");
const cardByTitle = (title) => findAll(host, (node) => hasClass(node, "card")
  && findAll(node, (child) => child.tagName === "h3" && child.textContent === title).length > 0)[0];
// 卡片头徽标文案：用来断言「矩阵写死 12 小时 / 15 分钟，其余跟随页头时间窗」。
const cardHeadBadges = (title) => {
  const card = cardByTitle(title);
  const head = card ? findAll(card, (node) => hasClass(node, "card-head"))[0] : null;
  return head ? findAll(head, (node) => hasClass(node, "badge")).map((node) => node.textContent) : [];
};
// 卡片范围内的 notice / 脚注：矩阵刷新失败提示必须长在矩阵卡里，而不是整页顶部。
const cardNotices = (title, tone) => {
  const card = cardByTitle(title);
  return card
    ? findAll(card, (node) => hasClass(node, "notice") && hasClass(node, `tone-${tone}`))
    : [];
};
const cardFootText = (title) => {
  const card = cardByTitle(title);
  return card
    ? findAll(card, (node) => hasClass(node, "card-foot")).map((node) => node.textContent).join(" ")
    : "";
};
const matrixRows = () => byClass(host, "status-matrix-row");

// 首屏：恰好两次请求——页面请求跟随页头 24 小时，矩阵请求固定 12 小时 / 900 秒桶。
eq("page_first_fetch_count", statusCallCount(), 2);
eq("page_first_fetch_page_query",
  [statusQueries[0]?.hours, statusQueries[0]?.groupBy, statusQueries[0]?.bucketSeconds],
  ["24", "provider", null]);
eq("page_first_fetch_matrix_query",
  [statusQueries[1]?.hours, statusQueries[1]?.groupBy, statusQueries[1]?.bucketSeconds],
  ["12", "provider", "900"]);
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

// 矩阵只吃第二次响应：3 行 × 49 列、每行定长、49 等分铺满、无滚动容器。
eq("page_matrix_row_count", matrixRows().length, 3);
eq("page_matrix_cell_count_is_rows_times_columns", matrixCells().length, 3 * 49);
eq("page_matrix_cells_per_row",
  byClass(host, "status-cells").map((node) => node.children.length), [49, 49, 49]);
eq("page_matrix_no_scroll_container", byClass(host, "status-matrix-scroll").length, 0);
eq("page_matrix_cells_grid_template",
  byClass(host, "status-cells").map((node) => node.style.gridTemplateColumns),
  Array(3).fill("repeat(49, minmax(0, 1fr))"));
eq("page_matrix_axis_track_exists", byClass(host, "status-axis-track").length, 1);
eq("page_matrix_axis_label_count", byClass(host, "status-axis-label").length, 7);
// 轴标签来自**矩阵响应**的 series.points（页面请求的 4 个点从 00:00 起）：49 列步长 7，
// 标签落在 0/7/14/21/28/35/42 列，首列是桶边界 16:00，而不是页面窗口的第一个点。
eq("page_matrix_axis_labels_from_matrix_series",
  byClass(host, "status-axis-label").map((node) => node.textContent),
  ["16:00", "17:45", "19:30", "21:15", "23:00", "00:45", "02:30"]);
// 矩阵卡头徽标固定为 12 小时 / 15 分钟一格，不随页头时间窗变化。
eq("page_matrix_badges_fixed", cardHeadBadges("渠道健康矩阵"), ["最近 12 小时", "15 分钟/格"]);
check("page_matrix_hint_fixed_window",
  findText(host, "矩阵固定展示最近 12 小时、每格 15 分钟"));
eq("page_row_labels_provider", rowNames(), ["prov-a", "prov-b", "未归属"]);
check("page_row_summary_has_score",
  String(byClass(host, "status-row-meta")[0]?.textContent || "").includes("健康分 60"),
  byClass(host, "status-row-meta")[0]?.textContent);
check("page_row_summary_unknown_samples",
  String(byClass(host, "status-row-meta")[2]?.textContent || "").includes("样本不足"),
  byClass(host, "status-row-meta")[2]?.textContent);
// 分档色落位：prov-a 综合 60 → warn；prov-b 0 → bad；2 次行 → unknown；其余 144 格无流量。
const cellBands = matrixCells().map((node) =>
  ["good", "warn", "bad", "unknown", "no-traffic"].find((band) => hasClass(node, `band-${band}`)) || null);
eq("page_cell_band_distribution", {
  warn: cellBands.filter((band) => band === "warn").length,
  bad: cellBands.filter((band) => band === "bad").length,
  unknown: cellBands.filter((band) => band === "unknown").length,
  "no-traffic": cellBands.filter((band) => band === "no-traffic").length,
}, { warn: 1, bad: 1, unknown: 1, "no-traffic": 144 });
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
  String(byClass(host, "status-matrix")[0]?.attrs?.["aria-label"] || "").includes("3 行 × 49 列") &&
    String(byClass(host, "status-matrix")[0]?.attrs?.["aria-label"] || "").includes("最近 12 小时"),
  byClass(host, "status-matrix")[0]?.attrs?.["aria-label"]);
check("page_matrix_foot_states_grid",
  findText(host, "共 49 列") && findText(host, "矩阵固定最近 12 小时") &&
    findText(host, "≥ 3 次才参与着色"));
// 桶宽以服务端回声为准：矩阵固定 15 分钟/格，趋势仍跟随页面请求的 1 小时/点。
check("page_bucket_badges_from_echo",
  findText(host, "15 分钟/格") && findText(host, "1 小时/点"));
// 趋势卡徽标跟随页头时间窗（与矩阵的固定窗口形成对照）。
eq("page_trend_badges_follow_window", cardHeadBadges("健康趋势"), ["24 小时", "1 小时/点"]);
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

// 时间窗切换：页面请求改成 1 小时，矩阵请求仍固定 12 小时 / 900 秒；矩阵徽标不动，
// KPI / 趋势 / 错误 / 模型跟着新窗口走。
const fetchBeforeWindowSwitch = statusCallCount();
click(segmentButton(host, "选择统计窗口", "1h"));
await settle();
eq("page_window_switch_request_count", statusCallCount(), fetchBeforeWindowSwitch + 2);
eq("page_window_switch_page_hours", lastQuery(false).hours, "1");
eq("page_window_switch_matrix_fixed",
  [lastQuery(true).hours, lastQuery(true).bucketSeconds], ["12", "900"]);
eq("page_window_switch_matrix_badges", cardHeadBadges("渠道健康矩阵"), ["最近 12 小时", "15 分钟/格"]);
eq("page_window_switch_trend_badges", cardHeadBadges("健康趋势"), ["1 小时", "1 小时/点"]);
eq("page_window_switch_error_badges", cardHeadBadges("错误原因"), ["1 小时", "4 次失败"]);
eq("page_window_switch_model_badges", cardHeadBadges("模型明细"), ["2 个模型", "1 小时"]);
check("page_window_switch_kpi_hours", statText(3).includes("8 次请求 / 1 小时"), statText(3));
eq("page_window_switch_matrix_cells", matrixCells().length, 3 * 49);
// 切回 24 小时：矩阵仍固定 12 小时。
click(segmentButton(host, "选择统计窗口", "24h"));
await settle();
eq("page_window_switch_back", [lastQuery(false).hours, lastQuery(true).hours], ["24", "12"]);
eq("page_window_switch_back_badges", cardHeadBadges("健康趋势"), ["24 小时", "1 小时/点"]);

// 维度切换：矩阵行由服务端聚合，两路请求都必须带新 group_by。
const fetchBeforeDimension = statusCallCount();
click(segmentButton(host, "选择矩阵维度", "供应商·模型"));
await settle();
eq("page_dimension_model_request_count", statusCallCount(), fetchBeforeDimension + 2);
eq("page_dimension_model_page_group", lastQuery(false).groupBy, "provider_model");
eq("page_dimension_model_matrix_group", lastQuery(true).groupBy, "provider_model");
eq("page_dimension_model_labels", rowNames(), ["prov-a / model-x"]);
eq("page_dimension_model_cells", matrixCells().length, 1 * 49);
click(segmentButton(host, "选择矩阵维度", "供应商·Key"));
await settle();
eq("page_dimension_key_page_group", lastQuery(false).groupBy, "provider_key");
eq("page_dimension_key_matrix_group", lastQuery(true).groupBy, "provider_key");
eq("page_dimension_key_labels", rowNames(), ["prov-a / key-main"]);
eq("page_dimension_key_cells", matrixCells().length, 1 * 49);
click(segmentButton(host, "选择矩阵维度", "供应商"));
await settle();
eq("page_dimension_back_page_group", lastQuery(false).groupBy, "provider");
eq("page_dimension_back_matrix_group", lastQuery(true).groupBy, "provider");
eq("page_dimension_back_cells", matrixCells().length, 3 * 49);

// 全局筛选：两个供应商各点一次 → 两路请求里都出现两个重复的 provider_id；
// 再加一把访问密钥 → access_key_id 也两路透传，矩阵窗口不变。
click(filterChip(host, "provider", "prov-a"));
await settle();
eq("page_filter_provider_single",
  [lastQuery(false).providerId, lastQuery(true).providerId], [["prov-a"], ["prov-a"]]);
click(filterChip(host, "provider", "prov-b"));
await settle();
eq("page_filter_provider_repeated",
  [lastQuery(false).providerId, lastQuery(true).providerId],
  [["prov-a", "prov-b"], ["prov-a", "prov-b"]]);
check("page_filter_repeated_in_url",
  String(lastQuery(false).url || "").includes("provider_id=prov-a&provider_id=prov-b") &&
    String(lastQuery(true).url || "").includes("provider_id=prov-a&provider_id=prov-b"),
  `${lastQuery(false).url} / ${lastQuery(true).url}`);
click(filterChip(host, "access-key", "ak1"));
await settle();
eq("page_filter_access_key",
  [lastQuery(false).accessKeyId, lastQuery(true).accessKeyId], [["ak1"], ["ak1"]]);
eq("page_filter_provider_still_repeated", lastQuery(true).providerId, ["prov-a", "prov-b"]);
eq("page_filter_matrix_fixed_window",
  [lastQuery(true).hours, lastQuery(true).bucketSeconds], ["12", "900"]);

// 空库空态：两路都空。KPI 显示 "—"，矩阵卡说明「最近 12 小时没有请求」。
mode = "empty";
const fetchBeforeEmpty = statusCallCount();
click(buttonWithText(host, "刷新"));
await settle();
eq("page_empty_request_count", statusCallCount(), fetchBeforeEmpty + 2);
eq("page_empty_no_cells", matrixCells().length, 0);
eq("page_empty_matrix_badges", cardHeadBadges("渠道健康矩阵"), ["最近 12 小时", "15 分钟/格"]);
check("page_empty_reason",
  findText(host, "最近 12 小时没有请求，健康矩阵暂时没有可着色的格子。"));
check("page_empty_kpi_dash", statText(0).includes("窗口内无请求") && statText(0).includes("—"), statText(0));
check("page_empty_trend_reason", findText(host, "窗口内没有请求，趋势暂时没有可画的读数。"));
check("page_empty_error_card", findText(host, "窗口内没有失败请求。"));
check("page_empty_model_reason", findText(host, "窗口内没有模型调用记录。"));
// 空库没有行时列数退回 series.points（49）：脚注必须写「共 49 列」，不能是误导性的「共 0 列」。
check("empty_matrix_foot_columns",
  cardFootText("渠道健康矩阵").includes("共 49 列") &&
    !cardFootText("渠道健康矩阵").includes("共 0 列"),
  cardFootText("渠道健康矩阵"));

// 仅矩阵 500：页面其余部分正常，只有矩阵卡退化成可重试的错误空态。
// 用切维度触发（切维度会先清空两路旧读数，矩阵确实处于「没有数据」），避免拿上一轮
// 矩阵冒充失败态。
mode = "matrix-error";
const fetchBeforeMatrixError = statusCallCount();
click(segmentButton(host, "选择矩阵维度", "供应商·模型"));
await settle();
eq("page_matrix_error_request_count", statusCallCount(), fetchBeforeMatrixError + 2);
eq("page_matrix_error_matrix_group", lastQuery(true).groupBy, "provider_model");
check("page_matrix_error_no_page_error", byClass(host, "notice").length === 0,
  byClass(host, "notice").map((node) => node.className).join(" , "));
check("page_matrix_error_kpi_ok", statText(0).includes("50.0%"), statText(0));
check("page_matrix_error_empty", findText(host, "矩阵读取失败。"));
check("page_matrix_error_hint", findText(host, "AMKR 请求失败（HTTP 500）: matrix boom"));
eq("page_matrix_error_no_cells", matrixCells().length, 0);
check("page_matrix_error_frame_kept", cardHeadBadges("渠道健康矩阵"), ["最近 12 小时", "15 分钟/格"]);
check("page_matrix_error_retry", Boolean(buttonWithText(host, "重试")));
// 恢复：点矩阵卡里的重试，矩阵重绘而页面其余部分不受影响。
mode = "normal";
click(buttonWithText(host, "重试"));
await settle();
eq("page_matrix_error_recovered_cells", matrixCells().length, 1 * 49);
eq("page_matrix_error_recovered_badges", cardHeadBadges("渠道健康矩阵"), ["最近 12 小时", "15 分钟/格"]);
// 回到 provider 维度，后续空态 / 整页失败用例沿用三行矩阵。
click(segmentButton(host, "选择矩阵维度", "供应商"));
await settle();
eq("page_matrix_error_back_cells", matrixCells().length, 3 * 49);

// 有请求但矩阵没有任何行（历史行缺少归因）：空态要说清原因，而不是「没有请求」。
mode = "matrix-rows-empty";
click(buttonWithText(host, "刷新"));
await settle();
check("page_matrix_rows_empty_reason",
  findText(host, "最近 12 小时有请求，但没有任何可归因的渠道数据。"));
eq("page_matrix_rows_empty_cells", matrixCells().length, 0);
check("page_matrix_rows_empty_kpi_ok", statText(0).includes("50.0%"), statText(0));

// 整页接口 500：只出现错误通知、不抛异常；切窗口先清掉旧读数（这里没有旧读数）。
mode = "error";
const fetchBeforeError = statusCallCount();
click(segmentButton(host, "选择统计窗口", "1h"));
await settle();
eq("page_error_request_count", statusCallCount(), fetchBeforeError + 2);
eq("page_error_refetched_hours", lastQuery(false).hours, "1");
const errorNotice = findAll(host, (node) => hasClass(node, "notice") && hasClass(node, "tone-error"))[0];
check("page_error_notice", Boolean(errorNotice), byClass(host, "notice").map((node) => node.className).join(" , "));
check("page_error_notice_text",
  String(errorNotice?.textContent || "").includes("状态监控数据读取失败：AMKR 请求失败（HTTP 500）: boom"),
  errorNotice?.textContent);
eq("page_error_alert_role", errorNotice?.attrs?.role, "alert");
eq("page_error_no_cells", matrixCells().length, 0);
check("page_error_retry_entry", Boolean(buttonWithText(host, "重试") && findText(host, "暂时读不到状态监控数据。")));

// 恢复：点重试后重新取数并画出读数（页面窗口已是 1 小时，矩阵仍 12 小时）。
mode = "normal";
click(buttonWithText(host, "重试"));
await settle();
check("page_recover_fetched", lastQuery(false).hours === "1" && matrixCells().length === 3 * 49,
  `${lastQuery(false).hours} / ${matrixCells().length}`);

// onTick：页面注册的静默重取回调必须两路都重取（不叠请求、不闪骨架由代码路径保证）。
eq("page_on_tick_registered", ticks.length, 1);
version = 1; // 下一次页面响应换成 16 次请求、16 成功的读数
const fetchBeforeTick = statusCallCount();
await ticks[0]();
await settle();
eq("page_on_tick_request_count", statusCallCount(), fetchBeforeTick + 2);
eq("page_on_tick_page_hours", lastQuery(false).hours, "1");
eq("page_on_tick_matrix_fixed",
  [lastQuery(true).hours, lastQuery(true).bucketSeconds], ["12", "900"]);
check("page_on_tick_updated_kpi",
  statText(0).includes("100%") && statText(0).includes("16 成功 · 0 失败"), statText(0));
check("page_on_tick_cache_updated", statText(2).includes("50.0%"), statText(2));
check("page_on_tick_no_error_notice", byClass(host, "notice").length === 0,
  byClass(host, "notice").map((node) => node.className).join(" , "));
eq("page_on_tick_matrix_cells", matrixCells().length, 3 * 49);

// 行截断：矩阵被 60 行上限截断时，页面必须把「还有 N 行未显示」说出来，
// 否则用户会把被截掉的渠道读成「不存在」。
version = 2;
click(buttonWithText(host, "刷新"));
await settle();
check("page_rows_truncated_notice",
  findText(host, "另有 5 行未显示"), byClass(host, "status-matrix-foot")[0]?.textContent);
check("page_rows_truncated_keeps_cells", matrixCells().length === 3 * 49, String(matrixCells().length));

// 矩阵刷新失败但手里还有上一轮矩阵：卡内必须出现 warn 提示（与整页刷新失败同一口径），
// 旧矩阵保留在下方供对照，而不是静默冒充最新数据。页面请求正常 → KPI 不受影响。
version = 0;
click(buttonWithText(host, "刷新"));
await settle();
const stalePreconditionClean = matrixCells().length === 3 * 49 &&
  cardNotices("渠道健康矩阵", "warn").length === 0;
mode = "matrix-error"; // 只让矩阵请求 500，页面请求仍 200
const fetchBeforeStale = statusCallCount();
click(buttonWithText(host, "刷新"));
await settle();
const staleWarn = cardNotices("渠道健康矩阵", "warn");
const staleWarnText = String(staleWarn[0]?.textContent || "");
const staleErrorNotices = byClass(host, "notice").filter((node) => hasClass(node, "tone-error"));
check("stale_matrix_warn",
  stalePreconditionClean &&
    statusCallCount() === fetchBeforeStale + 2 &&
    staleWarn.length === 1 &&
    staleWarnText.includes("矩阵刷新失败，下方是上次成功读数：") &&
    staleWarnText.includes("AMKR 请求失败（HTTP 500）: matrix boom") &&
    statText(0).includes("50.0%") &&
    matrixCells().length === 3 * 49 &&
    staleErrorNotices.length === 0,
  `${staleWarnText} / cells=${matrixCells().length} / ${statText(0)} / errNotices=${staleErrorNotices.length}`);

// 恢复：矩阵请求成功后 warn 消失，矩阵重绘成新响应（行 A 缓存率 100% → 健康分 80），
// 证明画的是新数据而不是上一轮缓存。
mode = "normal";
matrixRevision = 1;
await ticks[0]();
await settle();
check("stale_matrix_recovered",
  cardNotices("渠道健康矩阵", "warn").length === 0 &&
    matrixCells().length === 3 * 49 &&
    cellTitle(0).includes("健康分 80") &&
    String(byClass(host, "status-row-meta")[0]?.textContent || "").includes("健康分 80"),
  `warn=${cardNotices("渠道健康矩阵", "warn").length} / cells=${matrixCells().length}`
    + ` / ${cellTitle(0)} / ${byClass(host, "status-row-meta")[0]?.textContent}`);

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
