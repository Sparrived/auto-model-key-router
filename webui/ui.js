// 共享 UI 组件：卡片、KPI 瓦片、数据表、分段控件、对话框、提示条。
// 全部为无状态工厂函数，调用方负责重新渲染。

import { h, mount, formatRelative, formatCount } from "./dom.js";
import { icon } from "./icons.js";
import { sparkline } from "./charts.js";

let toastHost = null;

export function installToastHost(root) {
  toastHost = h("div.toast-host", { role: "status", "aria-live": "polite" });
  root.append(toastHost);
  return toastHost;
}

export function toast(message, tone = "info") {
  if (!toastHost) return;
  const node = h("div.toast", { class: `tone-${tone}` },
    icon(tone === "error" ? "alert" : tone === "good" ? "check" : "info", { size: 16 }),
    h("span", message),
  );
  toastHost.append(node);
  setTimeout(() => {
    node.classList.add("is-leaving");
    setTimeout(() => node.remove(), 220);
  }, 3600);
}

// —— 模态对话框 ——
// closeOnBackdrop=false 用于"操作进行中不允许关闭"的场景。
export function dialog({ title, body, actions = [], onClose, closeOnBackdrop = true }) {
  const backdrop = h("div.backdrop", { role: "presentation" });
  const close = () => {
    backdrop.remove();
    document.removeEventListener("keydown", onKey);
    if (onClose) onClose();
  };
  const onKey = (event) => {
    if (event.key === "Escape" && closeOnBackdrop) close();
  };
  const panel = h(
    "div.dialog",
    { role: "dialog", "aria-modal": "true", "aria-label": typeof title === "string" ? title : "对话框" },
    h("div.dialog-head", {}, h("h3", title), buttonNode("", {
      variant: "text", small: true, "aria-label": "关闭", onClick: close,
    }, icon("close", { size: 16 }))),
    h("div.dialog-body", body),
    actions.length ? h("div.dialog-actions", actions.map((action) => button(action))) : null,
  );
  if (closeOnBackdrop) {
    backdrop.addEventListener("click", (event) => { if (event.target === backdrop) close(); });
  }
  document.addEventListener("keydown", onKey);
  backdrop.append(panel);
  document.body.append(backdrop);
  const focusable = panel.querySelector("input, select, textarea, button");
  if (focusable) focusable.focus();
  return { close, panel };
}

function button(spec) {
  return buttonNode(
    spec.label,
    { variant: spec.variant || "text", small: spec.small, disabled: spec.disabled, onClick: spec.onClick },
    spec.icon ? icon(spec.icon, { size: 16 }) : null,
  );
}

// confirmDialog 的 message 既可以是字符串（包一层 <p>），也可以是节点。
//
// 节点形态是给"正文不是一句话"的场景用的：连带影响清单要分节、要列点，塞进 <p> 里
// 会被浏览器按非法嵌套修正掉（块级元素会把段落截断）。判断用 typeof 而不是
// instanceof Node：探针的 DOM 垫片里 Node 是另一个类。
export function confirmDialog({ title = "请确认", message, confirmLabel = "确认", danger = false, onConfirm }) {
  const ref = dialog({
    title,
    body: typeof message === "string" ? h("p", message) : message,
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      {
        label: confirmLabel,
        variant: danger ? "danger" : "",
        onClick: () => { ref.close(); onConfirm(); },
      },
    ],
  });
  return ref;
}

// —— 基础块 ——
export function notice(text, tone = "info") {
  if (!text) return null;
  const glyph = tone === "error" ? "alert" : tone === "warn" ? "alert" : tone === "good" ? "check" : "info";
  return h(`div.notice.tone-${tone}`, { role: tone === "error" ? "alert" : "status" },
    icon(glyph, { size: 18 }),
    h("div.notice-text", text),
  );
}

export function badge(text, tone = "muted", extra) {
  return h(`span.badge.tone-${tone}`, extra, text);
}

export function card(...children) {
  return h("div.card", children);
}

export function cardHead(title, ...rest) {
  return h("div.card-head", h("h3", title), ...rest);
}

// KPI 瓦片：值 + 单位 + 线索 +（可选）迷你趋势与环比。
// 强调"读数"而不是"卡片"，因此不自带海拔，由父级 grid 统一对齐。
//
// trendPolarity 决定环比颜色的含义，必须由调用方明确指定：
//   "inverse"（默认）—— 涨 = 红。适用于耗时、错误率这类"越低越好"的指标。
//   "neutral"        —— 涨跌都用中性灰。适用于吞吐量这类"高低都正常"的指标，
//                       给它们上红色会把正常波动误报成故障。
//   "direct"         —— 涨 = 绿。适用于成功率、缓存命中率这类"越高越好"的指标。
export function stat(label, value, hint, options = {}) {
  const { unit, tone, points, metricId, bucketSeconds, trendChange, iconName, trendPolarity = "inverse" } = options;
  const direction = trendChange === null || trendChange === undefined ? null
    : trendChange > 0.001 ? "up" : trendChange < -0.001 ? "down" : "flat";
  const trendTone = direction === null ? null
    : direction === "flat" || trendPolarity === "neutral" ? "flat"
      : trendPolarity === "direct"
        ? (direction === "up" ? "down" : "up")   // 复用 tone-down（绿）表示"变好"
        : (direction === "up" ? "up" : "down");
  return h("div.stat", {},
    h("div.stat-head", {},
      iconName ? icon(iconName, { size: 15, class: "stat-icon" }) : null,
      h("span.stat-label", label),
      direction
        ? h(`span.stat-trend.tone-${trendTone}`, {},
            icon(direction === "down" ? "arrowDown" : direction === "up" ? "arrowUp" : "activity", { size: 12 }),
            `${Math.abs(Math.round((trendChange || 0) * 100))}%`)
        : null,
    ),
    h("div.stat-value", { class: tone ? `tone-${tone}` : null },
      h("span", value),
      unit ? h("small", unit) : null,
    ),
    hint ? h("div.stat-hint", hint) : null,
    points && points.length
      ? h("div.stat-spark", {}, sparkline({ points, metricId, bucketSeconds, tone: tone || "primary" }))
      : null,
  );
}

// KPI 网格：列数由 CSS 决定（默认 4 列），窄屏逐级塌缩。
export function statGrid(...tiles) {
  return h("div.stat-grid", {}, tiles.flat().filter(Boolean));
}

// 5 列变体：给瓦片数能被 5 整除的看板用（概览 10 张、工作空间 5 张）。
// 单开一个函数而不是给 statGrid 加参数：它是变参的，插一个列数参数会把另外三处
// 调用（用量统计 8 张、成本页 4 张、面板 4 张）一起卷进改动，而那几处都靠默认的
// 4 列正好排满。
export function statGrid5(...tiles) {
  return h("div.stat-grid.cols-5", {}, tiles.flat().filter(Boolean));
}

export function empty(text, options = {}) {
  return h("div.empty", {},
    options.icon ? icon(options.icon, { size: 28, class: "empty-icon" }) : null,
    h("p", text),
    options.hint ? h("p.empty-hint", options.hint) : null,
    options.action || null,
  );
}

export function field(label, control) {
  return h("label.field", h("span", label), control);
}

export function input(props) {
  return h("input.input", props);
}

export function select(options, props = {}) {
  const node = h("select.select", props);
  for (const option of options) {
    const spec = typeof option === "string" ? { value: option, label: option } : option;
    node.append(h("option", {
      value: spec.value,
      selected: String(props.value ?? "") === String(spec.value),
      disabled: spec.disabled,
    }, spec.label));
  }
  return node;
}

export function buttonNode(label, props = {}, ...children) {
  const { variant = "", small = false, iconName, ...rest } = props;
  const classes = ["btn", variant, small ? "small" : ""].filter(Boolean).join(".");
  return h(`button.${classes}`, { type: "button", ...rest },
    iconName ? icon(iconName, { size: small ? 14 : 16 }) : null,
    ...children,
    label || null,
  );
}

export function toggle(label, pressed, onClick, props = {}) {
  return h("button.toggle", { type: "button", "aria-pressed": String(!!pressed), onClick, ...props }, label);
}

// 分段控件（Material segmented button）：用于时间范围/指标切换。
export function segmented(items, activeId, onSelect, props = {}) {
  return h("div.segmented", { role: "group", ...props },
    items.map((item) => h("button.segment", {
      type: "button",
      "aria-pressed": String(item.id === activeId),
      title: item.title || null,
      onClick: () => onSelect(item.id),
    }, item.label)));
}

// —— 数据表 ——
// columns: { label, render(row), numeric, width, sortValue(row), sortable, align }
// options: { sort: { key, direction }, onSort(key), caption, dense }
export function table(columns, rows, emptyText, options = {}) {
  if (!rows.length) return empty(emptyText || "暂无数据。", { icon: "logs" });
  const sort = options.sort;
  const head = h("tr", {}, columns.map((column) => {
    const active = sort && sort.key === column.key;
    const sortable = options.onSort && column.sortValue;
    const th = h(`th${column.numeric ? ".num" : ""}${active ? ".is-sorted" : ""}`, {
      style: column.width ? { width: column.width } : null,
      "aria-sort": active ? (sort.direction === "asc" ? "ascending" : "descending") : null,
    }, sortable
      ? h("button.th-sort", {
          type: "button",
          onClick: () => options.onSort(column.key),
        }, column.label, icon(active && sort.direction === "asc" ? "arrowUp" : "arrowDown", { size: 12 }))
      : column.label);
    return th;
  }));
  const body = rows.map((row) =>
    h("tr", {}, columns.map((column) => {
      const value = column.render(row);
      return h(`td${column.numeric ? ".num" : ""}`, value === null || value === undefined ? "-" : value);
    })),
  );
  const node = h("table.table", { class: options.dense ? "is-dense" : null },
    options.caption ? h("caption", options.caption) : null,
    h("thead", head),
    h("tbody", body),
  );
  return options.scroll === false ? node : h("div.table-scroll", {}, node);
}

export function kv(pairs) {
  const list = h("dl.kv");
  for (const [key, value] of pairs) {
    if (value === null || value === undefined) continue;
    list.append(h("dt", key), h("dd", value));
  }
  return list;
}

// KPI 骨架：瓦片数与列数必须与真实网格一致，否则数据到位时会整块跳一下。
export function statSkeleton(count = 4, cols = 4) {
  return h(cols === 5 ? "div.stat-grid.cols-5" : "div.stat-grid", {},
    Array.from({ length: count }, () =>
      h("div.stat.is-loading", {},
        h("div.skel.skel-sm"), h("div.skel.skel-lg"), h("div.skel.skel-sm"))));
}

// —— 骨架屏 ——
// 结构形状贴近真实内容（读数块 + 图区 + 行），比转圈更能说明"将要出现什么"。
export function skeleton(kind = "chart", rows = 5) {
  if (kind === "stats") return statSkeleton(4);
  if (kind === "table") {
    return h("div.table-scroll", {}, h("div.skeleton-table", {},
      Array.from({ length: rows }, (_, index) =>
        h("div.skel-row", { class: index === 0 ? "is-head" : null },
          h("div.skel.skel-sm"), h("div.skel.skel-sm"), h("div.skel.skel-sm"), h("div.skel.skel-md")))));
  }
  return h("div.skeleton-chart", {}, h("div.skel.skel-chart"));
}

export function loading(text = "正在读取…") {
  return h("div.inline", h("span.spinner"), h("span.muted", text));
}

// 内容区：统一"页头 + 栅格"骨架，页面只填内容。
export function pageHead(title, subtitle, ...actions) {
  return h("div.page-head", {},
    h("div.page-title", {}, h("h1", title), subtitle ? h("p.sub", subtitle) : null),
    h("div.page-actions", {}, actions.filter(Boolean)),
  );
}

export function section(title, subtitle, ...children) {
  return h("section.section", {},
    title ? h("div.section-head", {}, h("h2", title), subtitle ? h("p.muted", subtitle) : null) : null,
    ...children,
  );
}

export function progressBar(percent, tone = "primary") {
  const value = Math.max(0, Math.min(100, percent ?? 0));
  return h("div.progress", { role: "progressbar", "aria-valuenow": Math.round(value), "aria-valuemin": "0", "aria-valuemax": "100" },
    h("div.progress-fill", { class: `tone-${tone}`, style: { width: `${value}%` } }),
  );
}

// 有时间戳的"最后更新"提示：轮询界面必须让用户知道数据有多新。
export function freshness(at, { prefix = "更新于" } = {}) {
  if (!at) return null;
  return h("span.freshness", { title: new Date(at).toLocaleString("zh-CN") },
    icon("clock", { size: 13 }),
    `${prefix} ${formatRelative(at)}`,
  );
}

export function render(target, ...children) {
  mount(target, ...children);
}

export { formatCount };
