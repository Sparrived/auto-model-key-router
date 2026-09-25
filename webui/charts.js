// SVG 图表层：折线（含十字准线读数）、堆叠柱、环形、迷你趋势、横向排行。
//
// 设计约束：
// - 零依赖、零构建，随包发布，离线可用；
// - 所有取值都走 chart-math.js 的口径函数，"画"和"算"分离，方便单测；
// - 图表以真实像素宽度渲染（不用 preserveAspectRatio="none" 拉伸），
//   因为拉伸会把描边和文字一起压扁，读数就不准了。

import { h, svg, mount, formatClockSeconds, formatDateTime, formatAxisTime, clamp } from "./dom.js";
import {
  METRIC_MAP, axisScale, timeTicks, metricValue, windowSums,
  heatmapCells, heatmapScale, heatmapLevel, WEEKDAY_LABELS, HEATMAP_SLOTS, HEAT_LEVELS,
  slotLabel, fitLabel, sankeyLabelBudget,
  readable, gapBridges, sankeyLayout, sankeyLinkPath,
  formatNumber, formatCompactNumber, formatPercentValue, formatDurationValue,
} from "./chart-math.js";

// 图表内边距（px）。左侧留给 Y 轴刻度文字，底部留给时间标签。
const PAD = { top: 16, right: 16, bottom: 30, left: 60 };

// 统一的描边宽度与配色语义色名。
const TONE_VAR = {
  primary: "var(--md-primary)",
  secondary: "var(--md-secondary-variant)",
  error: "var(--md-error)",
  neutral: "var(--md-on-surface-variant)",
};

// 轴标签字体走 CSS（.chart .axis-label），不在这里写内联样式：
// SVG 表现属性不支持 var()，写成 font-family="var(--font)" 会被当作字面字族名而失效。
const AXIS_TEXT = { "font-size": "10" };

function sizing(host, draw) {
  const render = () => {
    const width = Math.max(240, Math.round(host.clientWidth || host.parentElement?.clientWidth || 720));
    host.replaceChildren(draw(width));
  };
  // 探针环境没有 ResizeObserver：按测量值同步画一次，随后无处可等。
  if (typeof ResizeObserver === "undefined") {
    render();
    return host;
  }
  // 有 ResizeObserver 时**不在这里**画首帧。此刻 host 还没被挂进文档（页面都是先建
  // 节点、后 mount），clientWidth 为 0，只能落到 720 的兜底宽度；而挂载后 CSS 会把
  // <svg> 拉满容器宽度，viewBox 却还是 720 宽，preserveAspectRatio 于是把整幅图等比
  // 居中缩到中间 —— 下一帧按真实宽度重画才「啪」地弹回满宽。每 10 秒一次轮询重绘都会
  // 重演，这就是肉眼看到的"图表往中间挤一下又复原"。
  // 首绘交给 RO 回调：它在布局之后、绘制之前触发，一帧都不会画错。
  const observer = new ResizeObserver(() => {
    // 页面轮询会整块替换图表节点。观察已脱离文档的节点既能防止泄漏，
    // 也不会再为空节点做无用的重绘。
    if (!host.isConnected) { observer.disconnect(); return; }
    render();
  });
  observer.observe(host);
  return host;
}

// 数值 → 展示文本，按指标类型分派。
function formatValue(metric, value) {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  if (metric.percent) return formatPercentValue(value);
  if (metric.unit === "ms") return formatDurationValue(value);
  return metric.format(value);
}

// 时间轴标签的格式由**整段跨度**决定（见 dom.js 的 formatAxisTime）：长窗口下
// 固定 "HH:MM" 会退化成每个点都是 "00:00" 的噪声。跨度优先用真实的首尾时间戳，
// 拿不到再退回「点数 × 桶宽」。
function axisTimeFormatter(points, bucketSeconds) {
  const list = points || [];
  const at = (point) => point?.at ?? point?.started_at;
  const first = at(list[0]);
  const last = at(list[list.length - 1]);
  let span = 0;
  if (first && last) {
    const seconds = (Date.parse(last) - Date.parse(first)) / 1000;
    if (Number.isFinite(seconds) && seconds > 0) span = seconds;
  }
  if (!span) span = Math.max(0, list.length - 1) * (Number(bucketSeconds) || 0);
  return (value) => formatAxisTime(value, span);
}

function withUnit(metric, value) {
  const text = formatValue(metric, value);
  if (text === "—") return text;
  if (metric.percent) return text;
  return metric.unit ? `${text} ${metric.unit}` : text;
}

// 把数据点切成"连续非空"的段；0 是有效读数，只有 null 才断线。
function segments(points) {
  const out = [];
  let current = null;
  points.forEach((point, index) => {
    if (!readable(point)) {
      current = null;
      return;
    }
    if (!current) { current = []; out.push(current); }
    current.push(index);
  });
  return out;
}

// 缺口桥：用虚线把"两侧都有读数、中间缺采样"的两点连起来。
//
// 均值型指标（耗时、首字、成功率）在无请求的桶上没有读数，而 15 秒一桶的稀疏
// 流量下这种空桶非常密集 —— 不桥接的话曲线会碎成几十段加一地孤立圆点，读起来
// 像图表坏了，而不是"这些时刻没有请求"。虚线是刻意的：它表示这一段没有采样，
// 只是示意走势延续，与实线的实测段、以及"累加中"的尾桶虚线都不是一回事。
function appendGapBridges(node, series, x, y) {
  for (const bridge of gapBridges(series)) {
    const from = [x(bridge.from), y(series[bridge.from].value)];
    const to = [x(bridge.to), y(series[bridge.to].value)];
    node.append(svg("polyline", {
      class: "series-gap",
      points: `${from[0].toFixed(2)},${from[1].toFixed(2)} ${to[0].toFixed(2)},${to[1].toFixed(2)}`,
    }));
  }
}

// —— 折线 / 面积图 ——
export function lineChart({
  points,
  metricId = "rpm",
  bucketSeconds = 60,
  height = 240,
  showArea = true,
  ariaLabel,
  cumulative = false,
}) {
  const metric = METRIC_MAP[metricId] || METRIC_MAP.rpm;
  const host = h("div.chart-host", { style: { height: `${height}px` } });
  const series = (points || []).map((point, index) => ({
    index,
    at: point.started_at,
    endedAt: point.ended_at,
    complete: point.complete !== false,
    value: metricValue(point, metric, bucketSeconds),
    raw: point,
  }));
  // 累计模式：把每桶读数改写成"到此为止的总和"，用来画"总量涨到多少"。
  // 缺失的桶（null）不参与累加也不算断点——它只是没采样，不是总量归零。
  if (cumulative) {
    let running = 0;
    for (const point of series) {
      if (readable(point)) running += point.value;
      point.value = running;
    }
  }

  sizing(host, (width) => {
    const innerW = Math.max(10, width - PAD.left - PAD.right);
    const innerH = Math.max(10, height - PAD.top - PAD.bottom);
    const scale = axisScale(series.map((point) => point.value), { percent: metric.percent });
    const count = series.length;
    const step = count > 1 ? innerW / (count - 1) : 0;
    const x = (index) => (count === 1 ? PAD.left + innerW / 2 : PAD.left + index * step);
    const y = (value) => PAD.top + innerH - ((clamp(value, scale.min, scale.max) - scale.min) / (scale.max - scale.min || 1)) * innerH;

    const node = svg("svg", {
      width, height, viewBox: `0 0 ${width} ${height}`,
      class: "chart", role: "img",
      "aria-label": ariaLabel || `${metric.label}趋势图`,
    });

    // 横向网格 + Y 轴刻度。percent 恒为 0-100，其余按 nice 阶梯。
    for (const tick of scale.ticks) {
      const ty = y(tick);
      node.append(svg("line", {
        class: "grid-line", x1: PAD.left, x2: width - PAD.right, y1: ty, y2: ty,
      }));
      node.append(svg("text", {
        class: "axis-label", x: PAD.left - 8, y: ty + 3.5, "text-anchor": "end", ...AXIS_TEXT,
      }, metric.percent ? `${Math.round(tick)}%` : formatCompactNumber(tick)));
    }

    // 时间轴标签
    const formatAxis = axisTimeFormatter(series, bucketSeconds);
    for (const tick of timeTicks(series, Math.max(2, Math.floor(innerW / 92)))) {
      node.append(svg("text", {
        class: "axis-label", x: x(tick.index), y: height - PAD.bottom + 18,
        "text-anchor": "middle", ...AXIS_TEXT,
      }, formatAxis(tick.at)));
    }

    const baseline = PAD.top + innerH;
    const runs = segments(series);

    // 先画缺口桥再接实测段：实线叠在虚线之上，"哪一段是真的"一眼可辨。
    appendGapBridges(node, series, x, y);

    for (const run of runs) {
      const coords = run.map((index) => [x(index), y(series[index].value)]);
      const line = coords.map(([px, py]) => `${px.toFixed(2)},${py.toFixed(2)}`).join(" ");
      if (showArea && coords.length > 1) {
        const first = coords[0];
        const last = coords[coords.length - 1];
        node.append(svg("path", {
          class: "area",
          d: `M ${first[0].toFixed(2)},${baseline.toFixed(2)} L ${line.replace(/ /g, " L ")} L ${last[0].toFixed(2)},${baseline.toFixed(2)} Z`,
        }));
      }
      if (coords.length === 1) {
        node.append(svg("circle", { class: "point", cx: coords[0][0], cy: coords[0][1], r: 2.6 }));
      } else {
        node.append(svg("polyline", { class: "series-line", points: line }));
        // 仍在累加中的尾桶用虚线：它天然偏低，不能被读成流量骤降。
        const lastIndex = run[run.length - 1];
        if (series[lastIndex]?.complete === false && run.length > 1) {
          const prev = coords[coords.length - 2];
          const last = coords[coords.length - 1];
          node.append(svg("polyline", {
            class: "series-line is-partial",
            points: `${prev[0].toFixed(2)},${prev[1].toFixed(2)} ${last[0].toFixed(2)},${last[1].toFixed(2)}`,
          }));
        }
      }
    }

    // 交互层：十字准线 + 最近点高亮 + 读数气泡
    const crosshair = svg("line", { class: "crosshair", y1: PAD.top, y2: PAD.top + innerH, x1: 0, x2: 0, opacity: 0 });
    const marker = svg("circle", { class: "marker", r: 4.5, cx: 0, cy: 0, opacity: 0 });
    node.append(crosshair, marker);

    const overlay = svg("rect", {
      x: PAD.left - step / 2, y: PAD.top, width: Math.max(innerW + step, 1), height: innerH,
      fill: "transparent", style: { cursor: "crosshair" },
    });
    node.append(overlay);

    const tooltip = h("div.chart-tip", { role: "status" });
    const frame = h("div.chart-frame", {}, node, tooltip);

    const showAt = (index) => {
      const point = series[index];
      if (!point || point.value === null) { hide(); return; }
      const px = x(index);
      const py = y(point.value);
      crosshair.setAttribute("x1", px); crosshair.setAttribute("x2", px); crosshair.setAttribute("opacity", 1);
      marker.setAttribute("cx", px); marker.setAttribute("cy", py); marker.setAttribute("opacity", 1);
      // 必须走 mount 而不是原生 replaceChildren：原生 API 会把 null 子项
      // 字符串化成文本节点"null"，让每个已完结的点都多出一行 null。
      mount(tooltip,
        h("span.tip-time", formatDateTime(point.at)),
        h("span.tip-value", withUnit(metric, point.value)),
        point.complete === false ? h("span.tip-flag", "累加中") : null,
      );
      tooltip.classList.add("is-visible");
      // 气泡跟随但夹在容器内，避免右端溢出被裁。
      const tipWidth = tooltip.offsetWidth || 150;
      const left = clamp(px - tipWidth / 2, 4, Math.max(4, width - tipWidth - 4));
      tooltip.style.left = `${left}px`;
      tooltip.style.top = `${clamp(py - 46, 4, height - 20)}px`;
    };
    const hide = () => {
      crosshair.setAttribute("opacity", 0);
      marker.setAttribute("opacity", 0);
      tooltip.classList.remove("is-visible");
    };

    overlay.addEventListener("pointermove", (event) => {
      const rect = node.getBoundingClientRect();
      const ratio = rect.width ? (event.clientX - rect.left) / rect.width : 0;
      const localX = ratio * width;
      const index = count <= 1 ? 0 : Math.round((localX - PAD.left) / (step || 1));
      showAt(clamp(index, 0, count - 1));
    });
    overlay.addEventListener("pointerleave", hide);

    if (!series.some((point) => point.value !== null)) {
      frame.append(h("div.chart-empty", "该时间窗内没有请求"));
    }
    return frame;
  });
  return host;
}

// —— 堆叠柱：输入 / 输出 Token 随时间构成 ——
export function stackedBars({ points, height = 200, series: seriesSpec, bucketSeconds = 60, ariaLabel = "Token 构成随时间变化", emptyText = "该时间窗内没有 Token 用量" }) {
  const specs = seriesSpec || [
    { id: "prompt", label: "输入", tone: "primary", pick: (point) => point.prompt_tokens },
    { id: "completion", label: "输出", tone: "secondary", pick: (point) => point.completion_tokens },
  ];
  const host = h("div.chart-host", { style: { height: `${height}px` } });

  sizing(host, (width) => {
    const innerW = Math.max(10, width - PAD.left - PAD.right);
    const innerH = Math.max(10, height - PAD.top - PAD.bottom);
    const totals = (points || []).map((point) =>
      specs.reduce((sum, spec) => sum + (Number(spec.pick(point)) || 0), 0));
    const scale = axisScale(totals, { ticks: 4 });
    const count = (points || []).length;
    const slot = count ? innerW / count : innerW;
    // 1px 间隙保证相邻柱不会糊成一片色块。
    const barWidth = Math.max(1, slot - Math.max(1, slot * 0.18));

    const node = svg("svg", {
      width, height, viewBox: `0 0 ${width} ${height}`,
      class: "chart stacked", role: "img", "aria-label": ariaLabel,
    });

    for (const tick of scale.ticks) {
      const ty = PAD.top + innerH - (tick / (scale.max || 1)) * innerH;
      node.append(svg("line", { class: "grid-line", x1: PAD.left, x2: width - PAD.right, y1: ty, y2: ty }));
      node.append(svg("text", {
        class: "axis-label", x: PAD.left - 8, y: ty + 3.5, "text-anchor": "end",
        ...AXIS_TEXT,
      }, formatCompactNumber(tick)));
    }

    const labelPoints = (points || []).map((point) => ({ started_at: point.started_at }));
    const formatAxis = axisTimeFormatter(points, bucketSeconds);
    for (const tick of timeTicks(labelPoints, Math.max(2, Math.floor(innerW / 92)))) {
      node.append(svg("text", {
        class: "axis-label", x: PAD.left + tick.index * slot + barWidth / 2, y: height - PAD.bottom + 18,
        "text-anchor": "middle", ...AXIS_TEXT,
      }, formatAxis(tick.at)));
    }

    const baseline = PAD.top + innerH;
    const tip = h("div.chart-tip", { role: "status" });
    const frame = h("div.chart-frame", {}, node, tip);

    (points || []).forEach((point, index) => {
      let cursor = baseline;
      const values = specs.map((spec) => ({ spec, value: Number(spec.pick(point)) || 0 }));
      for (const { spec, value } of values) {
        if (value <= 0) continue;
        const barHeight = (value / (scale.max || 1)) * innerH;
        cursor -= barHeight;
        const rect = svg("rect", {
          class: "bar", x: PAD.left + index * slot, y: cursor,
          width: barWidth, height: Math.max(barHeight, 0.5),
          fill: TONE_VAR[spec.tone] || TONE_VAR.primary,
        });
        rect.addEventListener("pointerenter", () => {
          const total = values.reduce((sum, item) => sum + item.value, 0);
          mount(tip,
            h("span.tip-time", formatDateTime(point.started_at)),
            ...values.map(({ spec: inner, value: innerValue }) =>
              h("span.tip-value", `${inner.label} ${formatCompactNumber(innerValue)}`)),
            h("span.tip-total", `合计 ${formatCompactNumber(total)}`),
            point.complete === false ? h("span.tip-flag", "累加中") : null,
          );
          tip.classList.add("is-visible");
          const tipWidth = tip.offsetWidth || 150;
          tip.style.left = `${clamp(PAD.left + index * slot - tipWidth / 2, 4, Math.max(4, width - tipWidth - 4))}px`;
          tip.style.top = `${PAD.top}px`;
        });
        rect.addEventListener("pointerleave", () => tip.classList.remove("is-visible"));
        node.append(rect);
      }
    });

    if (!totals.some((value) => value > 0)) frame.append(h("div.chart-empty", emptyText));
    return frame;
  });
  return host;
}

// —— 环形图：比率型指标的单一读数 ——
export function donut({ ratio, label, caption, size = 168, tone = "primary", emptyText = "无数据" }) {
  const value = Number.isFinite(ratio) ? clamp(ratio, 0, 1) : null;
  const stroke = 14;
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const center = size / 2;

  const track = svg("circle", {
    cx: center, cy: center, r: radius, fill: "none",
    stroke: "var(--md-outline)", "stroke-width": stroke,
  });
  const arc = svg("circle", {
    cx: center, cy: center, r: radius, fill: "none",
    stroke: TONE_VAR[tone] || TONE_VAR.primary, "stroke-width": stroke,
    "stroke-linecap": "round",
    "stroke-dasharray": `${(value ?? 0) * circumference} ${circumference}`,
    transform: `rotate(-90 ${center} ${center})`,
  });
  const node = svg("svg", {
    width: size, height: size, viewBox: `0 0 ${size} ${size}`,
    class: "donut", role: "img",
    "aria-label": `${label} ${value === null ? emptyText : formatPercentValue(value)}`,
  }, track, arc);

  return h("div.donut-wrap", {},
    node,
    h("div.donut-center", {},
      h("strong", value === null ? emptyText : formatPercentValue(value, 1)),
      h("span", label),
    ),
    caption ? h("p.donut-caption", caption) : null,
  );
}

// —— 迷你趋势（KPI 瓦片内联）——
export function sparkline({ points, metricId = "rpm", bucketSeconds = 60, width = 96, height = 28, tone = "primary" }) {
  const metric = METRIC_MAP[metricId] || METRIC_MAP.rpm;
  const values = (points || []).map((point) => metricValue(point, metric, bucketSeconds));
  if (!values.some((value) => Number.isFinite(value))) return h("div.spark-empty");

  const scale = axisScale(values, { percent: metric.percent });
  const count = values.length;
  const step = count > 1 ? width / (count - 1) : 0;
  const x = (index) => (count === 1 ? width / 2 : index * step);
  const y = (value) => height - 2 - ((clamp(value, scale.min, scale.max) - scale.min) / (scale.max - scale.min || 1)) * (height - 4);

  const node = svg("svg", {
    width, height, viewBox: `0 0 ${width} ${height}`, class: `spark tone-${tone}`,
    "aria-hidden": "true", focusable: "false",
  });
  const sparse = values.map((value, index) => ({ value, index }));
  // 与主折线同一口径：稀疏指标的缺口用虚线桥接，否则迷你图会被切成碎点。
  for (const bridge of gapBridges(sparse)) {
    node.append(svg("polyline", {
      class: "spark-gap",
      points: `${x(bridge.from).toFixed(1)},${y(values[bridge.from]).toFixed(1)} ` +
        `${x(bridge.to).toFixed(1)},${y(values[bridge.to]).toFixed(1)}`,
    }));
  }
  for (const run of segments(sparse)) {
    const coords = run.map((index) => `${x(index).toFixed(1)},${y(values[index]).toFixed(1)}`);
    if (coords.length === 1) {
      node.append(svg("circle", { class: "point", cx: x(run[0]), cy: y(values[run[0]]), r: 1.6 }));
    } else {
      node.append(svg("polyline", { class: "spark-line", points: coords.join(" ") }));
    }
  }
  return node;
}

// —— 横向排行：把"谁在消耗"读成一句话 ——
export function barList(rows, { format = (row) => formatNumber(row.value, 0), tone = "primary", emptyText = "暂无数据" } = {}) {
  if (!rows.length) return h("div.bar-list-empty", emptyText);
  const max = Math.max(...rows.map((row) => row.value), 1);
  const list = h("div.bar-list", { role: "list" });
  for (const row of rows) {
    list.append(h("div.bar-row", { role: "listitem" },
      h("div.bar-head", {},
        h("span.bar-name", { title: row.name }, row.name),
        h("span.bar-value", format(row)),
      ),
      h("div.bar-track", {}, h("div", {
        class: `bar-fill tone-${tone}`,
        style: { width: `${Math.max(2, (row.value / max) * 100)}%` },
      })),
    ));
  }
  return list;
}

// —— 热力图：星期 × 半小时 ——
// 用 CSS 栅格而不是 SVG：矩阵是 7×48 的规则格子，栅格天然处理"每格等宽"，
// 而 SVG 得自己算 336 个矩形的坐标，宽度变化还要重算一遍。
// 行容器设 display:contents，让行列的子元素直接落进同一个栅格：
// 这样表头、星期标签与格子共享同一套列宽，不需要手算 span。
export function heatmap({ points, metric, ariaLabel = "用量热力图" }) {
  const cells = heatmapCells(points, { value: metric.pick });
  const max = heatmapScale(cells);
  const grid = h("div.heatmap", { role: "img", "aria-label": ariaLabel });

  // 表头每 2 小时标一个（半小时粒度下是每 4 格）：48 个标签在窄屏会糊成一片。
  const head = h("div.heat-row", {}, h("span.heat-corner"));
  for (let slot = 0; slot < HEATMAP_SLOTS; slot += 1) {
    head.append(h("span.heat-hour", {}, slot % 4 === 0 ? slotLabel(slot) : ""));
  }
  grid.append(head);

  const tip = h("div.chart-tip", { role: "status" });
  const frame = h("div.heat-frame", {}, grid, tip);
  const show = (cell, weekday, slot) => {
    mount(tip,
      h("span.tip-time", `${WEEKDAY_LABELS[weekday]} ${slotLabel(slot)}`),
      // "窗口未覆盖"与"这一格确实是 0"必须分开说：前者是没数据，后者是没流量。
      h("span.tip-value", cell.buckets ? metric.format(cell.value) : "窗口未覆盖"),
      cell.partial ? h("span.tip-flag", "累加中") : null,
    );
    // 先显示再测量：display:none 时 getBoundingClientRect 全是 0，气泡会被钉在左上角。
    tip.classList.add("is-visible");
    const spot = cell.node.getBoundingClientRect();
    const host = frame.getBoundingClientRect();
    const box = tip.getBoundingClientRect();
    tip.style.left = `${clamp(spot.left - host.left + spot.width / 2 - box.width / 2, 4, Math.max(4, host.width - box.width - 4))}px`;
    tip.style.top = `${clamp(spot.top - host.top - box.height - 6, 0, Math.max(0, host.height - box.height))}px`;
  };

  cells.forEach((row, weekday) => {
    const line = h("div.heat-row", {}, h("span.heat-day", WEEKDAY_LABELS[weekday]));
    row.forEach((cell, slot) => {
      cell.node = h("span.heat-cell", {
        class: heatCellClass(cell, max),
        // 格子本身不进无障碍树：整块矩阵有一个 aria-label，336 个无标签格子
        // 只会把读屏淹没。
        "aria-hidden": "true",
      });
      cell.node.addEventListener("pointerenter", () => show(cell, weekday, slot));
      line.append(cell.node);
    });
    grid.append(line);
  });
  grid.addEventListener("pointerleave", () => tip.classList.remove("is-visible"));

  const legendLevels = h("span.heat-legend-scale");
  for (let level = 0; level <= HEAT_LEVELS; level += 1) {
    legendLevels.append(h("i", { class: `heat-cell level-${level}`, "aria-hidden": "true" }));
  }

  return h("div.stack", {},
    frame,
    h("div.heat-legend", {},
      h("span.muted", "少"),
      legendLevels,
      h("span.muted", "多"),
      max > 0 ? h("span.muted", `· 峰值 ${metric.format(max)}`) : h("span.muted", "· 窗口内无数据"),
      h("span.spacer"),
      h("span.muted", { title: "斜纹格表示该时段不在统计窗口内" }, "斜纹 = 窗口未覆盖"),
    ),
  );
}

function heatCellClass(cell, max) {
  return [
    "heat-cell",
    `level-${cell.buckets ? heatmapLevel(cell.value, max, HEAT_LEVELS) : 0}`,
    cell.buckets ? "" : "is-coverless",
    cell.partial ? "is-partial" : "",
  ].filter(Boolean).join(" ");
}

// —— 桑基流向图：请求在各层之间怎么流动 ——
//
// 与其它图表的差别：这里没有时间轴，纵轴也不是刻度，而是**量的堆叠**。每层节点
// 的高度与该节点承载的请求数成正比，流带的粗细同样，因此"哪条流最粗"就是"哪条
// 流向承载最多请求"，一眼可读。
//
// 只画 SVG 不做交互式下钻：流带本身已经带 title（悬停能看到精确数值），而真正的
// 下钻要跳到明细页、带上逐层过滤条件（现在是六层，见 layers），那不是这张图该承担
// 的职责。
export function sankey({ links, layers, metricLabel, height = 420, formatValue: format = formatCompactNumber, ariaLabel = "请求流向桑基图" }) {
  const host = h("div.chart-host", { style: { height: `${height}px` } });

  // 层标题单独占一条顶带。标题画在 y=12，而满高的列从 y=0 起就顶到画布上沿，
  // 不把图整体下移的话列标题必然被自己的节点压住——这就是"文字叠加、看不清"的
  // 一半原因（另一半是流带上的标签，见下面的文字描边）。
  const TOP = 22;
  const CANVAS_BOTTOM = 6;

  sizing(host, (width) => {
    const layout = sankeyLayout(links, { width: Math.max(320, width - 2), height: height - TOP - CANVAS_BOTTOM });
    const node = svg("svg", {
      width, height, viewBox: `0 0 ${width} ${height}`,
      class: "chart sankey", role: "img", "aria-label": ariaLabel,
    });

    if (!layout.edges.length) {
      node.append(svg("text", {
        class: "axis-label", x: width / 2, y: height / 2, "text-anchor": "middle", ...AXIS_TEXT,
      }, "该时间窗内没有可归因到工作空间的请求"));
      return node;
    }

    // 流带先画，节点后画：节点盖在流带端点上，接缝更干净。
    const ribbonLayer = svg("g", { class: "sankey-ribbons" });
    const nodeLayer = svg("g", { class: "sankey-nodes" });
    // 整体下移一条顶带的高度，把画布上方让给列标题。用 transform 而不是给每个
    // 坐标加偏移：布局算出的坐标保持"从 0 开始"的原样，探针断言不用跟着改口径。
    node.append(svg("g", { class: "sankey-plot", transform: `translate(0, ${TOP})` }, ribbonLayer, nodeLayer));

    // 颜色按**起点节点名**分配：同一个工作空间/模型的流出保持同色，方便顺着
    // 一条流看下去。色板只用 styles.css 里真实定义的变量——写一个不存在的
    // var() 会让 fill 变成 none（流带整片消失），而不是回退到某个默认色。
    const palette = ["var(--md-primary)", "var(--md-secondary-variant)", "var(--md-error)", "var(--md-on-surface-variant)", "var(--md-primary-variant)"];
    const colorOf = new Map();
    const colorFor = (name) => {
      if (!colorOf.has(name)) colorOf.set(name, palette[colorOf.size % palette.length]);
      return colorOf.get(name);
    };

    for (const edge of layout.edges) {
      const ribbon = svg("path", {
        class: "sankey-ribbon",
        d: sankeyLinkPath(edge),
        fill: colorFor(edge.sourceName),
      });
      // 悬停提示：精确数值 + 两端名字。title 必须是 path 的**子节点**，写成兄弟
      // 节点会被浏览器丢掉、悬停什么都不显示。
      ribbon.append(svg("title", {}, `${edge.sourceName} → ${edge.targetName}：${format(edge.value)} ${metricLabel}`));
      ribbonLayer.append(ribbon);
    }

    layout.columns.forEach((column, layerIndex) => {
      const last = layerIndex === layout.columns.length - 1;
      // 标签一律写在**列与列之间的走廊**里，而最右那条走廊里挤着两个标签
      // （倒数第二列从左往右写、末列从右往左写），所以末列只能分到走廊的一半；
      // 其余列各自独占一条走廊，可以写满。不按走廊收窄的话，窄窗口下最右那两个
      // 标签会贴到一起糊成一片。减掉的 12px + 节点宽是两端各 6px 偏移与节点占位。
      const step = layout.columns.length > 1 ? layout.columns[1].x - layout.columns[0].x : width;
      const labelBudget = sankeyLabelBudget({ step, nodeWidth: layout.nodeWidth, last });
      for (const item of column.nodes) {
        const rect = svg("rect", {
          class: "sankey-node",
          x: column.x, y: item.y, width: column.width, height: Math.max(1, item.height),
          rx: 2, fill: colorFor(item.name),
        });
        rect.append(svg("title", {}, `${item.name}：${format(item.value)} ${metricLabel}`));
        nodeLayer.append(rect);
        // 标签一律留在**列与列之间的空隙**里：首列也写在节点右侧（写成左侧会落到
        // x=-6，整段跑出画布外），末列改为右对齐写在节点左侧（写成右侧同样会跑出
        // 右边界）。这样五列的标签都不会溢出画布，顶多压在流带上——流带是半透明的，
        // 靠 .sankey-label 的底色描边（paint-order）保证字仍然清楚。
        nodeLayer.append(svg("text", {
          class: "sankey-label",
          x: last ? column.x - 6 : column.x + column.width + 6,
          y: item.y + item.height / 2 + 3.5,
          "text-anchor": last ? "end" : "start", ...AXIS_TEXT,
        }, fitLabel(item.name, labelBudget)));
      }
    });

    // 层标题：说明每一列是什么，否则读者不知道第 3 列为什么是 provider。
    // 画在顶带里（y=12），首列左对齐、末列右对齐，中间列居中，都不会越出画布。
    if (layers && layers.length) {
      const labels = { workspace: "工作空间", requested_model_id: "请求模型", model_id: "实际模型", provider_id: "供应商", key_name: "上游 Key", upstream_model_id: "上游模型" };
      layout.columns.forEach((column, index) => {
        const key = layers[index];
        const anchor = index === 0 ? "start" : index === layout.columns.length - 1 ? "end" : "middle";
        node.append(svg("text", {
          class: "axis-label sankey-column-label",
          x: column.x, y: 12, "text-anchor": anchor, ...AXIS_TEXT,
        }, labels[key] || key || ""));
      });
    }

    return node;
  });

  return host;
}

// 节点标签的截断走 chart-math.js 的 fitLabel：那里不碰 DOM，探针能直接断言
// 截断结果塞得进可用宽度，而不是只靠肉眼在浏览器里看。

// —— 图例 ——
export function legend(items) {
  return h("div.legend", {}, items.map((item) =>
    h("span.legend-item", {},
      h("i", { class: `swatch tone-${item.tone || "primary"}` }),
      h("span", item.label),
      item.value !== undefined ? h("strong", item.value) : null,
    )));
}

// —— 窗口摘要（图表右上角的读数区）——
export function chartSummary({ points, metricId, bucketSeconds, windowHours }) {
  const metric = METRIC_MAP[metricId] || METRIC_MAP.rpm;
  const sums = windowSums(points || []);
  const summary = {
    ...sums,
    hours: windowHours || 0,
    // 速率口径：窗口总量 / 实际小时数换算到每分钟。
    requests: sums.requests,
  };
  const total = metric.total(summary);
  const latest = [...(points || [])].reverse()
    .map((point) => metricValue(point, metric, bucketSeconds))
    .find((value) => value !== null && value !== undefined);
  return { total, latest, sums, metric };
}

// 复用的格式化出口，避免页面各自拼字符串导致口径不一。
export const formatters = { formatNumber, formatCompactNumber, formatPercentValue, formatDurationValue, formatClockSeconds };
