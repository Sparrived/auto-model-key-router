// WebUI 看板栅格探针：锁住"每一排都排满"，卡片不能被剩在空行里。
//
// 为什么不用 DOM 垫片跑一遍：空轨是 CSS 栅格（display:grid + grid-template-columns）
// 的产物，而垫片里没有 CSS 引擎，量不到任何布局。所以这里直接读两处**声明**：
//
//   * webui/styles.css —— 各断点的列定义（.col-N 的 span、.stat-grid 的列数、
//     .stream-row/.cost-row 的轨道数），以及各断点把哪些列 display:none 了；
//   * 各页面模块 —— .grid-12 里按顺序声明的卡片、statGrid* 的瓦片数、
//     请求流/成本行的子元素。
//
// 再按 max-width 级联（<=860px 时 <=1024px 的规则仍在生效，不是互相独立）算出每一档
// 的真实布局，断言三件事：
//
//   1. .grid-12 在每一档的每一排（含最后一排）都正好排满 12 轨；
//   2. .stat-grid 的瓦片数在每一档都能被该档列数整除；
//   3. .stream-row/.cost-row 的轨道数等于那一档可见的子元素数。
//
// 三条都来自真实缺陷：col-8 夹在半宽卡中间会在 <=1024px 把后面的半宽卡剩成半行；
// 5 张瓦片排 4 列会甩出一张孤儿；窄屏隐藏了一列却没减轨道，金额那格会掉到第二行。
//
// 覆盖不到：.form-grid/.preset-grid/.pulse-grid —— 它们用 auto-fit/auto-fill 自带
// 折行，不存在"固定列数除不尽"这种缺陷。
//
// 用法：node webui/probes/webui_layout_probe.mjs（有失败时退出码 1）。

import { readFileSync } from "node:fs";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");
const read = (file) => readFileSync(path.join(WEBUI, file), "utf8");

// —— 页面清单 ——
// 只有这几个模块有固定列数的栅格；其余页面（设置/日志/供应商等）用的是 auto-fit。
//
// 账号资源没有 .grid-12（卡片顺着 host 堆叠），列进来是为了它的 KPI 瓦片数：
// 那个张数必须同时被宽屏的 4 列与 ≤1280px 的 2 列整除，否则窄屏会甩出半宽孤儿。
const PAGES = [
  { file: "panel.js", label: "面板" },
  { file: "pages/accounts.js", label: "账号资源" },
  { file: "pages/guest.js", label: "访客看板" },
  { file: "pages/activity.js", label: "用量统计" },
  { file: "pages/cost.js", label: "成本" },
  { file: "pages/overview.js", label: "概览" },
  { file: "pages/workspaces.js", label: "工作空间" },
];

// 每个档位取一个代表宽度。max-width 是"<= 该值生效"，所以 1000 会同时吃到
// 1280 与 1024 两个块——这正是级联，不能把断点当独立规则。
const VIEWPORTS = [
  { label: ">1280px", width: 1440 },
  { label: "1280px", width: 1280 },
  { label: "<=1024px", width: 1000 },
  { label: "<=860px", width: 860 },
  { label: "<=560px", width: 390 },
];

const TRACKS = 12;

// —— 语法层工具 ——

// 跳过注释与字符串字面量后匹配括号，返回闭合括号的下标。
// 页面源码里既有注释里的括号，也有模板字符串（`div.stream-row${...}`），
// 直接数字符会把它们算进去。
function balanced(source, open) {
  let depth = 0;
  for (let i = open; i < source.length; i += 1) {
    const ch = source[i];
    if (ch === "/" && source[i + 1] === "/") {
      const nl = source.indexOf("\n", i);
      i = nl < 0 ? source.length : nl;
      continue;
    }
    if (ch === "/" && source[i + 1] === "*") {
      const end = source.indexOf("*/", i + 2);
      i = end < 0 ? source.length : end + 1;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      for (i += 1; i < source.length; i += 1) {
        if (source[i] === "\\") { i += 1; continue; }
        if (source[i] === ch) break;
      }
      continue;
    }
    if (ch === "(") depth += 1;
    else if (ch === ")") {
      depth -= 1;
      if (depth === 0) return i;
    }
  }
  return -1;
}

// 返回调用括号**内部**的文本（不含外层括号），这样 depth 0 就是直接子元素那一层。
function callBody(source, at, what) {
  const open = source.indexOf("(", at);
  if (open < 0) throw new Error(`${what}: 找不到调用括号`);
  const close = balanced(source, open);
  if (close < 0) throw new Error(`${what}: 调用括号不闭合`);
  return source.slice(open + 1, close);
}

// 同上的花括号版本：CSS 媒体块与函数体都是 `{...}`，用括号匹配会切错。
// 依然跳过注释与字符串，避免注释里的 `}` 提前收尾。
function braceBody(source, at, what) {
  const open = source.indexOf("{", at);
  if (open < 0) throw new Error(`${what}: 找不到花括号`);
  let depth = 0;
  for (let i = open; i < source.length; i += 1) {
    const ch = source[i];
    if (ch === "/" && source[i + 1] === "/") {
      const nl = source.indexOf("\n", i);
      i = nl < 0 ? source.length : nl;
      continue;
    }
    if (ch === "/" && source[i + 1] === "*") {
      const end = source.indexOf("*/", i + 2);
      i = end < 0 ? source.length : end + 1;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === "`") {
      for (i += 1; i < source.length; i += 1) {
        if (source[i] === "\\") { i += 1; continue; }
        if (source[i] === ch) break;
      }
      continue;
    }
    if (ch === "{") depth += 1;
    else if (ch === "}") {
      depth -= 1;
      if (depth === 0) return { open, close: i, body: source.slice(open + 1, i) };
    }
  }
  throw new Error(`${what}: 花括号不闭合`);
}

// —— styles.css：解析出各断点的列定义 ——

const css = read("styles.css").replace(/\/\*[\s\S]*?\*\//g, "");

const mediaBlocks = [];
{
  const re = /@media\s*\(max-width:\s*(\d+)px\)\s*\{/g;
  let match;
  while ((match = re.exec(css))) {
    const block = braceBody(css, match.index, "styles.css 的媒体查询");
    mediaBlocks.push({ width: Number(match[1]), body: block.body, start: match.index, end: block.close + 1 });
    re.lastIndex = block.close;
  }
}
// 基准档 = 挖掉所有媒体块之后剩下的声明。
let baseCSS = "";
{
  let cursor = 0;
  for (const block of [...mediaBlocks].sort((a, b) => a.start - b.start)) {
    baseCSS += css.slice(cursor, block.start);
    cursor = block.end;
  }
  baseCSS += css.slice(cursor);
}

function rulesOf(body) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let match;
  while ((match = re.exec(body))) {
    out.push({
      selectors: match[1].split(",").map((item) => item.trim()).filter(Boolean),
      decls: match[2],
    });
  }
  return out;
}

// 数 grid-template-columns 里的轨道数，跳过 minmax(0, 1fr) 括号内的空格。
function trackCount(value) {
  const tracks = [];
  let depth = 0;
  let current = "";
  for (const ch of value.trim()) {
    if (ch === "(") depth += 1;
    else if (ch === ")") depth -= 1;
    if (/\s/.test(ch) && depth === 0) {
      if (current) tracks.push(current);
      current = "";
      continue;
    }
    current += ch;
  }
  if (current) tracks.push(current);
  return tracks.length;
}

// 把若干段声明叠起来得到一档的有效定义。
function layoutFor(viewport) {
  const state = { col: {}, statGrid: {}, statGrid5: {}, rows: {}, hidden: new Set() };
  const apply = (body) => {
    for (const rule of rulesOf(body)) {
      const tracks = /grid-template-columns:\s*([^;]+)/.exec(rule.decls);
      for (const selector of rule.selectors) {
        const col = /^\.col-(\d+)$/.exec(selector);
        if (col) {
          const span = /grid-column:\s*span\s+(\d+)/.exec(rule.decls);
          if (span) state.col[col[1]] = Number(span[1]);
        }
        // .stat-grid.cols-5（两个类）与 .stat-grid（一个类）分开记：窄档若只写了
        // 后者，压不过基准档里两个类的那一条，这是 CSS 优先级而不是笔误。
        if (selector === ".stat-grid" && tracks) {
          const repeat = /repeat\(\s*(\d+)/.exec(tracks[1]);
          state.statGrid = repeat ? Number(repeat[1]) : 1;
        }
        if (selector === ".stat-grid.cols-5" && tracks) {
          const repeat = /repeat\(\s*(\d+)/.exec(tracks[1]);
          state.statGrid5 = repeat ? Number(repeat[1]) : 1;
        }
        if (selector === ".stream-row" || selector === ".cost-row") {
          if (tracks) state.rows[selector.slice(1)] = trackCount(tracks[1]);
        }
        if (/display:\s*none/.test(rule.decls)) {
          const cls = selector.split(/\s+/).pop().replace(/^\./, "");
          if (cls) state.hidden.add(cls);
        }
      }
    }
  };
  apply(baseCSS);
  for (const block of mediaBlocks.filter((item) => item.width >= viewport).sort((a, b) => b.width - a.width)) {
    apply(block.body);
  }
  return state;
}

// —— 页面源码：解析声明出来的栅格结构 ——

function gridCards(source, file) {
  const grids = [];
  const re = /h\("div\.grid-12"/g;
  let match;
  while ((match = re.exec(source))) {
    const body = callBody(source, match.index, `${file} 的 .grid-12`);
    grids.push([...body.matchAll(/h\("div\.col-(\d+)"/g)].map((item) => Number(item[1])));
  }
  if (!grids.length) {
    // 有些页面（账号资源）只用堆叠卡片 + KPI 网格，没有固定列数的 .grid-12。
    // 这类页面没有"排不满 12 轨"的风险，但仍然有 KPI 瓦片数的风险，所以返回空清单
    // 继续跑下面那一段，而不是整个探针报错退出。
    if (/\bh\("div\.grid-12"/.test(source)) {
      throw new Error(`${file}: 找不到 .grid-12`);
    }
    return [];
  }
  // 自检：源码里所有 h("div.col-N" 都必须落在某个 .grid-12 调用内，否则探针
  // 建模不到那段布局（例如有人嵌了第二层栅格），结论不可信。
  const declared = [...source.matchAll(/h\("div\.col-(\d+)"/g)].length;
  const parsed = grids.reduce((sum, spans) => sum + spans.length, 0);
  if (declared !== parsed) {
    throw new Error(`${file}: 声明了 ${declared} 张 col-* 卡，但只有 ${parsed} 张在 .grid-12 里`);
  }
  return grids;
}

function statGrids(source, file) {
  const grids = [];
  const re = /(statGrid5|statGrid)\(/g;
  let match;
  while ((match = re.exec(source))) {
    const body = callBody(source, match.index, `${file} 的 ${match[1]}`);
    // 瓦片构造器只有两个：stat(...) 与 costTile(...)（概览最后一张是成本瓦片）。
    const tiles = (body.match(/(?:^|[^A-Za-z0-9_])stat\(|costTile\(/g) || []).length;
    grids.push({ kind: match[1], tiles });
  }
  return grids;
}

// 请求流/成本行的子元素类名（grid 项），用来和轨道数对齐。
// 每个字符所在的花括号/圆括号深度（跳过字符串字面量）。
// 用来区分"栅格的直接子元素"与"子元素里面再套的元素"：请求流的 .stream-model /
// .stream-meta 在 .stream-main 里面，它们不是 grid item，不该算进轨道数。
function depthMap(text) {
  const depths = new Array(text.length).fill(0);
  let depth = 0;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '"' || ch === "'" || ch === "`") {
      let j = i + 1;
      for (; j < text.length; j += 1) {
        if (text[j] === "\\") { j += 1; continue; }
        if (text[j] === ch) break;
      }
      for (let k = i; k <= Math.min(j, text.length - 1); k += 1) depths[k] = depth;
      i = j;
      continue;
    }
    if (ch === "(") { depths[i] = depth; depth += 1; continue; }
    if (ch === ")") { depth -= 1; depths[i] = depth; continue; }
    depths[i] = depth;
  }
  return depths;
}

// 只保留顶层（depth 0）的 h("span.x") / h("div.x") 匹配。
function directChildren(body, pattern) {
  const depths = depthMap(body);
  return [...body.matchAll(pattern)].filter((m) => depths[m.index] === 0).map((m) => m[1]);
}

function streamRowChildren(source) {
  const fn = /function streamRow\(/.exec(source);
  if (!fn) throw new Error("pages/overview.js: 找不到 streamRow");
  const fnBody = braceBody(source, fn.index, "streamRow").body;
  const at = fnBody.indexOf("h(");
  if (at < 0) throw new Error("streamRow: 找不到 h(...)");
  return directChildren(callBody(fnBody, at, "streamRow 的 h(...)"), /h\("(?:span|div)\.([a-z-]+)[."]/g);
}

function costRowChildren(source) {
  const at = source.indexOf('h("div.cost-row"');
  if (at < 0) throw new Error("pages/cost.js: 找不到 .cost-row");
  return directChildren(callBody(source, at, ".cost-row"), /h\("(?:span|div)\.([a-z-]+)[."]/g);
}

// —— 稀疏自动放置：放不下就换行，原行剩下的轨留空 ——
function rows(spans, map) {
  const out = [];
  let row = [];
  let used = 0;
  for (const span of spans) {
    const width = Math.min(TRACKS, map[span]);
    if (used + width > TRACKS) { out.push({ used, items: row }); row = []; used = 0; }
    row.push(width);
    used += width;
    if (used === TRACKS) { out.push({ used, items: row }); row = []; used = 0; }
  }
  if (row.length) out.push({ used, items: row });
  return out;
}

// —— 跑检查 ——
const failures = [];
const fail = (message) => failures.push(message);

const layouts = VIEWPORTS.map((viewport) => ({ ...viewport, layout: layoutFor(viewport.width) }));

// 自检：页面用到的 .col-N 必须在 CSS 里有定义，否则 span 会退回 auto（列错位）。
{
  const known = new Set(Object.keys(layouts[0].layout.col));
  for (const page of PAGES) {
    for (const span of gridCards(read(page.file), page.file).flat()) {
      if (!known.has(String(span))) fail(`${page.label}（${page.file}）用了 CSS 里没有定义的 col-${span}`);
    }
  }
}

// 自检：格子辅助函数与列类名的对应关系没被动过。
{
  const ui = read("ui.js");
  if (!/statGrid5/.test(ui) || !/div\.stat-grid\.cols-5/.test(ui)) {
    fail("ui.js: statGrid5 不再渲染 .stat-grid.cols-5，本探针的列数假设已失效");
  }
}

let checked = 0;

for (const page of PAGES) {
  const source = read(page.file);
  const grids = gridCards(source, page.file);
  let ok = true;

  for (const { label, layout } of layouts) {
    for (const spans of grids) {
      checked += 1;
      const placed = rows(spans, layout.col);
      const partial = placed.filter((row) => row.used !== TRACKS);
      if (partial.length) {
        ok = false;
        const shown = placed.map((row) => row.items.join("+")).join(" | ");
        fail(`${page.label}（${page.file}）在 ${label} 有 ${partial.length} 排没排满：${shown}`);
      }
    }
  }

  // KPI 网格：瓦片数必须能被该档列数整除（除不尽就会甩出孤儿瓦片）。
  const statChecks = [];
  for (const grid of statGrids(source, page.file)) {
    const columnsOf = (layout) => (grid.kind === "statGrid5" ? layout.statGrid5 : layout.statGrid);
    for (const { label, layout } of layouts) {
      checked += 1;
      const columns = columnsOf(layout);
      if (!columns || grid.tiles % columns !== 0) {
        ok = false;
        fail(`${page.label}（${page.file}）的 ${grid.kind} 有 ${grid.tiles} 张瓦片，在 ${label}（${columns} 列）除不尽`);
      }
    }
    statChecks.push(`${grid.kind} ${grid.tiles} 张 × ${columnsOf(layouts[0].layout)} 列`);
  }

  // 请求流/成本行：轨道数必须等于那一档可见的子元素数。
  const rowChecks = [];
  if (page.file === "pages/overview.js") {
    const children = streamRowChildren(source);
    for (const { label, layout } of layouts) {
      checked += 1;
      const visible = children.filter((cls) => !layout.hidden.has(cls));
      const tracks = layout.rows["stream-row"];
      if (tracks !== visible.length) {
        ok = false;
        fail(`.stream-row 在 ${label} 声明了 ${tracks} 条轨道，但有 ${visible.length} 个可见子元素（${visible.join("、")}）`);
      }
    }
    rowChecks.push(`.stream-row ${children.length} 列`);
  }
  if (page.file === "pages/cost.js") {
    const children = costRowChildren(source);
    for (const { label, layout } of layouts) {
      checked += 1;
      const visible = children.filter((cls) => !layout.hidden.has(cls));
      const tracks = layout.rows["cost-row"];
      if (tracks !== visible.length) {
        ok = false;
        fail(`.cost-row 在 ${label} 声明了 ${tracks} 条轨道，但有 ${visible.length} 个可见子元素（${visible.join("、")}）`);
      }
    }
    rowChecks.push(`.cost-row ${children.length} 列`);
  }

  const detail = [
    grids.map((spans) => `[${spans.join(", ")}]`).join(" "),
    ...statChecks,
    ...rowChecks,
  ].filter(Boolean).join(" · ");
  console.log(`${ok ? "OK  " : "FAIL"} ${page.file.padEnd(22)} ${detail}`);
}

console.log("");
console.log(`检查了 ${checked} 项，断点档位：${VIEWPORTS.map((v) => v.label).join(" / ")}`);
if (failures.length) {
  console.log("");
  for (const message of failures) console.log(`✗ ${message}`);
  console.log(`\n共 ${failures.length} 处排不满；卡片数或断点列定义改动后必须让每一排都排满。`);
  process.exit(1);
}
console.log("所有看板在四档断点下都排满了，没有空轨或孤儿瓦片。");
