// 离线图标集：内置几何图形，随包发布，不依赖图标库或网络字体。
//
// 为什么不用 emoji / Unicode 字形（◎ ≣ ⛁）：这些码位在不同系统上会被替换成
// 彩色 emoji 或缺失方块，同一份资产在 Windows / macOS / Linux 上不一致。
//
// 统一规范：24×24 网格，stroke 描边 1.75，round 端点与拐角，currentColor 继承文字色。

import { svg } from "./dom.js";

const GRID = { viewBox: "0 0 24 24", fill: "none", "stroke-width": "1.75", "stroke-linecap": "round", "stroke-linejoin": "round" };

// 每条记录是 [标签, 属性] 序列；描边色统一取 currentColor。
const SHAPES = {
  // 看板：四个不等高方块（Material dashboard 语义）
  overview: [
    ["rect", { x: 3, y: 3, width: 7.5, height: 9, rx: 2 }],
    ["rect", { x: 13.5, y: 3, width: 7.5, height: 5.5, rx: 2 }],
    ["rect", { x: 13.5, y: 11.5, width: 7.5, height: 9.5, rx: 2 }],
    ["rect", { x: 3, y: 15, width: 7.5, height: 6, rx: 2 }],
  ],
  // 脉搏：请求速率
  activity: [["polyline", { points: "2.5 12.5 6 12.5 8.5 6 12 18.5 14.5 12.5 21.5 12.5" }]],
  // 供应商：服务器机架
  providers: [
    ["rect", { x: 3, y: 4, width: 18, height: 6.5, rx: 2 }],
    ["rect", { x: 3, y: 13.5, width: 18, height: 6.5, rx: 2 }],
    ["line", { x1: 7, y1: 7.25, x2: 7.01, y2: 7.25 }],
    ["line", { x1: 7, y1: 16.75, x2: 7.01, y2: 16.75 }],
  ],
  // 路由：一个入口分叉到两个出口
  routing: [
    ["circle", { cx: 4, cy: 12, r: 2 }],
    ["circle", { cx: 20, cy: 6, r: 2 }],
    ["circle", { cx: 20, cy: 18, r: 2 }],
    ["polyline", { points: "6 12 10 12 14 6 18 6" }],
    ["polyline", { points: "6 12 10 12 14 18 18 18" }],
  ],
  // 统一模型：四角星
  unified: [["path", { d: "M12 2.5 14.6 9.4 21.5 12 14.6 14.6 12 21.5 9.4 14.6 2.5 12 9.4 9.4Z" }]],
  // 任务路由：带勾选的清单
  task: [
    ["polyline", { points: "3.5 7 5.5 9 9 5.5" }],
    ["polyline", { points: "3.5 16 5.5 18 9 14.5" }],
    ["line", { x1: 12.5, y1: 7.5, x2: 20.5, y2: 7.5 }],
    ["line", { x1: 12.5, y1: 16.5, x2: 20.5, y2: 16.5 }],
  ],
  // 集成：插头
  integrations: [
    ["rect", { x: 6, y: 8, width: 12, height: 7, rx: 2 }],
    ["line", { x1: 9.5, y1: 8, x2: 9.5, y2: 3 }],
    ["line", { x1: 14.5, y1: 8, x2: 14.5, y2: 3 }],
    ["line", { x1: 12, y1: 15, x2: 12, y2: 21 }],
  ],
  // 设置：滑杆
  settings: [
    ["line", { x1: 3, y1: 7, x2: 21, y2: 7 }],
    ["line", { x1: 3, y1: 12, x2: 21, y2: 12 }],
    ["line", { x1: 3, y1: 17, x2: 21, y2: 17 }],
    ["circle", { cx: 8, cy: 7, r: 2.25, class: "knob" }],
    ["circle", { cx: 16, cy: 12, r: 2.25, class: "knob" }],
    ["circle", { cx: 10, cy: 17, r: 2.25, class: "knob" }],
  ],
  refresh: [
    ["path", { d: "M20.5 12a8.5 8.5 0 1 1-2.9-6.4" }],
    ["polyline", { points: "17.6 2.6 18.2 8.2 12.7 8.9" }],
  ],
  search: [
    ["circle", { cx: 10.5, cy: 10.5, r: 6.5 }],
    ["line", { x1: 19.5, y1: 19.5, x2: 15.2, y2: 15.2 }],
  ],
  copy: [
    ["rect", { x: 9, y: 9, width: 12, height: 12, rx: 2 }],
    ["path", { d: "M5.5 15H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v.5" }],
  ],
  external: [
    ["path", { d: "M14.5 3H21v6.5" }],
    ["line", { x1: 10.5, y1: 13.5, x2: 21, y2: 3 }],
    ["path", { d: "M18.5 13.5V19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2h5.5" }],
  ],
  check: [["polyline", { points: "20 6.5 9.5 17 4 11.5" }]],
  close: [
    ["line", { x1: 18, y1: 6, x2: 6, y2: 18 }],
    ["line", { x1: 6, y1: 6, x2: 18, y2: 18 }],
  ],
  alert: [
    ["path", { d: "M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" }],
    ["line", { x1: 12, y1: 9.5, x2: 12, y2: 14 }],
    ["line", { x1: 12, y1: 17.3, x2: 12.01, y2: 17.3 }],
  ],
  info: [
    ["circle", { cx: 12, cy: 12, r: 9.25 }],
    ["line", { x1: 12, y1: 16.5, x2: 12, y2: 11 }],
    ["line", { x1: 12, y1: 7.75, x2: 12.01, y2: 7.75 }],
  ],
  chevron: [["polyline", { points: "6 9.5 12 15.5 18 9.5" }]],
  arrowUp: [["polyline", { points: "12 19 12 5" }], ["polyline", { points: "5.5 11.5 12 5 18.5 11.5" }]],
  arrowDown: [["polyline", { points: "12 5 12 19" }], ["polyline", { points: "5.5 12.5 12 19 18.5 12.5" }]],
  clock: [
    ["circle", { cx: 12, cy: 12, r: 9.25 }],
    ["polyline", { points: "12 6.75 12 12 16 14.25" }],
  ],
  bolt: [["path", { d: "M13.5 2.5 4 13.5h7l-.5 8 9.5-11h-7Z" }]],
  filter: [["path", { d: "M3.5 5h17l-6.5 8v6l-4 2v-8Z" }]],
  download: [
    ["path", { d: "M20.5 15.5V19a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2v-3.5" }],
    ["polyline", { points: "7.5 10.5 12 15 16.5 10.5" }],
    ["line", { x1: 12, y1: 15, x2: 12, y2: 3 }],
  ],
  play: [["path", { d: "M6.5 3.5 20 12 6.5 20.5Z" }]],
  stop: [["rect", { x: 5.5, y: 5.5, width: 13, height: 13, rx: 2 }]],
  power: [
    ["path", { d: "M18.4 6.6a9 9 0 1 1-12.8 0" }],
    ["line", { x1: 12, y1: 3, x2: 12, y2: 11.5 }],
  ],
  shield: [["path", { d: "M12 2.5 20 6v6c0 5-3.4 8.4-8 9.5-4.6-1.1-8-4.5-8-9.5V6Z" }]],
  key: [
    ["circle", { cx: 7.5, cy: 15.5, r: 4 }],
    ["polyline", { points: "10.4 12.6 20.5 2.5" }],
    ["polyline", { points: "17.5 5.5 20.5 8.5" }],
    ["polyline", { points: "15 8 18 11" }],
  ],
  logs: [
    ["rect", { x: 3, y: 3.5, width: 18, height: 17, rx: 2 }],
    ["line", { x1: 7, y1: 9, x2: 17, y2: 9 }],
    ["line", { x1: 7, y1: 13, x2: 17, y2: 13 }],
    ["line", { x1: 7, y1: 17, x2: 12.5, y2: 17 }],
  ],
  layers: [
    ["path", { d: "M12 2.5 21.5 7.5 12 12.5 2.5 7.5Z" }],
    ["polyline", { points: "2.5 12.5 12 17.5 21.5 12.5" }],
    ["polyline", { points: "2.5 17 12 22 21.5 17" }],
  ],
  // 成本：硬币（圆 + 美元符号的竖线与 S 形）
  cost: [
    ["circle", { cx: 12, cy: 12, r: 9 }],
    ["path", { d: "M14.6 8.8c-.7-.8-1.6-1.2-2.6-1.2-1.7 0-2.7 1-2.7 2 0 1.2 1.1 1.7 2.7 2.1 1.7.4 3 .9 3 2.3 0 1.2-1.2 2.2-3 2.2-1.2 0-2.2-.4-2.9-1.3" }],
    ["line", { x1: 12, y1: 5.6, x2: 12, y2: 7.4 }],
    ["line", { x1: 12, y1: 16.6, x2: 12, y2: 18.4 }],
  ],
  // 额度：仪表盘（半圆刻度 + 指针）。账号资源看板看的是"还剩多少"，与概览的方块、
  // 成本的硬币都要能一眼分开。
  gauge: [
    ["path", { d: "M3.5 18.5a8.5 8.5 0 0 1 17 0" }],
    ["path", { d: "M12 18.5 16.2 11.8" }],
    ["circle", { cx: 12, cy: 18.5, r: 1.6 }],
  ],
};

export const ICON_NAMES = Object.keys(SHAPES);

// icon("overview", { class: "nav-icon", size: 20 })
export function icon(name, props = {}) {
  const shapes = SHAPES[name];
  if (!shapes) return null;
  const { size = 20, class: className, title, ...rest } = props;
  const node = svg("svg", {
    ...GRID,
    width: size,
    height: size,
    stroke: "currentColor",
    class: `icon${className ? ` ${className}` : ""}`,
    "aria-hidden": title ? null : "true",
    role: title ? "img" : null,
    "aria-label": title || null,
    ...rest,
  });
  if (title) node.append(svg("title", {}, title));
  for (const [tag, attrs] of shapes) node.append(svg(tag, attrs));
  return node;
}
