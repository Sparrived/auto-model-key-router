// AMKR 工作空间面板 —— 可被其它应用以 iframe 嵌入的独立精简页。
//
// 与主 WebUI 的关系：同一份静态资源、同一套样式与组件，但是**另一个入口**
// （webui/panel.html），不经过 app.js 的登录卡与导航壳。这样做的理由是权限模型：
// 面板拿的是工作空间 key，只有那一个空间的读写权，把整壳管理界面配上一个受限凭据
// 会给用户"我能管整个实例"的错觉，而每个按钮都 401。
//
// 三条边界（都有对应的服务端强制，不是约定）：
//
//  1. 空间由 key 决定，不接受任何指定空间的入口——服务端忽略请求头里的空间
//     （internal/api/server.go 的 authorizedTaskConfig）。
//  2. 只能读写本空间的任务与读数：模型、供应商、设置等管理面接口一律 401。
//  3. 不碰 localStorage（见 panel-api.js 的说明），凭据只从 URL fragment 来。

import {
  h, formatCount, formatCompact, formatDateTime, errorText, copyText,
} from "./dom.js";
import { ApiError } from "./api.js";
import { panelCredential, createPanelApi } from "./panel-api.js";
import {
  installToastHost, toast, card, cardHead, stat, statGrid, notice, badge, empty,
  skeleton, render, segmented, table, freshness, buttonNode, field, input, select,
} from "./ui.js";
import { sankey, barList } from "./charts.js";
import {
  USAGE_RANGES, ALL_HISTORY, rangeSpec, formatPercentValue, formatCompactNumber,
} from "./chart-math.js";

// 六层流向的中文名，与 internal/metrics/workspace.go 的 workspaceFlowLayers 一一对应。
// 那份是英文列名（它们同时是 SQL 列），这里只做展示翻译。
const LAYER_LABELS = {
  workspace: "工作空间",
  requested_model_id: "任务/别名",
  model_id: "模型",
  provider_id: "供应商",
  key_name: "上游 Key",
  upstream_model_id: "上游模型",
};

const state = {
  selection: 24,
  metric: "requests",
  usage: null,
  tasks: [],
  revision: null,
  key: "",
  // unauthorized 与 error 分开：前者是"凭据不对"，界面该请人重新给 key；后者是
  // "请求出错了"，界面该显示错误并让人重试。混成一个会让 401 看起来像服务故障。
  unauthorized: false,
  error: null,
  loading: true,
  editing: null,
  at: null,
};

let api = null;
let credential = null;
let host = null;

export function bootPanel() {
  const root = document.getElementById("root");
  host = h("div.panel");
  root.append(host);
  installToastHost(root);
  // 没有 key 就先问人要，而不是直接打接口——否则用户看到的是一个 401 错误，
  // 会以为 key 填错了（其实还没填）。
  credential = panelCredential();
  if (!credential.key) {
    draw();
    return;
  }
  api = createPanelApi(credential);
  state.key = credential.key;
  load();
}

// —— 数据 ——

function spec() {
  return rangeSpec(state.selection);
}

async function load() {
  if (!api) return;
  const current = spec();
  try {
    const [usage, tasks] = await Promise.all([
      api.usage({ hours: current.hours, allHistory: state.selection === ALL_HISTORY }),
      api.tasks(),
    ]);
    state.usage = usage;
    state.tasks = tasks.tasks || [];
    state.revision = tasks.config_revision ?? null;
    state.unauthorized = false;
    state.error = null;
    state.at = new Date().toISOString();
  } catch (error) {
    state.usage = null;
    if (error instanceof ApiError && error.status === 401) {
      state.unauthorized = true;
      state.error = null;
    } else {
      state.error = errorText(error);
    }
  }
  state.loading = false;
  draw();
}

// —— 派生读数 ——

function statsOf(usage) {
  const entry = (usage?.workspaces || [])[0];
  return entry?.stats || null;
}

function rangeLabel() {
  if (state.selection !== ALL_HISTORY) return spec().label;
  // 「全部历史」的真实跨度只有服务端知道。面板 key 读不了 /metrics/requests（那是
  // 完整权限的接口），因此不能像用量统计页那样先探最早一条；直接用读数响应里的
  // window.from —— 它本来就是同一个窗口的下界。
  const from = state.usage?.window?.from;
  return from ? `全部历史（自 ${formatDateTime(from)}）` : "全部历史";
}

// flowChain 把桑基图的六层折成一行文字链路。
//
// 存在的意义有两条，都不是装饰：
//   - 桑基图在窄 iframe 里会挤成一团，文字链路是它在任何宽度下的可读摘要；
//   - 它是这幅图的文字替代（图上有 aria-label，但读屏用户拿到的是"一条链路"，
//     这里给出真正的数值）。
// 第一层（工作空间）不出现：面板本身就是"某个工作空间"的视图，重复一遍没有信息量。
function flowChain(usage) {
  const links = usage?.links || [];
  const layers = usage?.layers || [];
  if (!links.length) return null;
  const nodeSets = layers.map(() => new Set());
  for (const link of links) {
    nodeSets[link.source_layer]?.add(link.source);
    nodeSets[link.target_layer]?.add(link.target);
  }
  const segments = [];
  for (let index = 1; index < layers.length; index += 1) {
    const count = nodeSets[index]?.size || 0;
    if (!count) continue;
    segments.push({ label: LAYER_LABELS[layers[index]] || layers[index], count });
  }
  if (!segments.length) return null;
  return h("div.chain", { role: "group", "aria-label": "请求流经的层与各层节点数" },
    h("span.chain-seg", {}, h("span.chain-count", "1"), h("span.chain-label", "工作空间")),
    ...segments.map((segment) => [
      h("span.chain-arrow", "→"),
      h("span.chain-seg", {},
        h("span.chain-count", formatCount(segment.count)),
        h("span.chain-label", segment.label)),
    ]).flat(),
  );
}

// —— 组件 ——

function keyForm() {
  const box = input({
    type: "password",
    placeholder: "amkr_ws_…",
    autocomplete: "off",
    "aria-label": "工作空间 key",
  });
  const submit = () => {
    const value = box.value.trim();
    if (!value) {
      toast("请填写工作空间 key。", "error");
      return;
    }
    // 写回 fragment 而不是 localStorage：刷新后仍然可用，但不与同源的管理面共享
    // 任何存储（见 panel-api.js）。replaceState 避免在历史里堆一串带凭据的地址。
    const url = new URL(location.href);
    url.hash = `k=${encodeURIComponent(value)}`;
    history.replaceState(null, "", url);
    credential = panelCredential();
    api = createPanelApi(credential);
    state.key = credential.key;
    state.loading = true;
    state.unauthorized = false;
    draw();
    load();
  };
  box.addEventListener("keydown", (event) => { if (event.key === "Enter") submit(); });
  return card(
    cardHead("工作空间面板"),
    h("p.muted", "这个面板需要一个工作空间 key。它由 AMKR 在创建该工作空间时给出，"
      + "只能读写这一个空间的任务与用量。"),
    field("工作空间 key", box),
    h("div.inline", {}, buttonNode("打开面板", { variant: "primary", onClick: submit })),
  );
}

function kpiTiles(usage) {
  const stats = statsOf(usage);
  const requests = stats?.requests || 0;
  const successes = stats?.successes || 0;
  return statGrid(
    stat("请求", formatCount(requests), rangeLabel(), { iconName: "activity", trendPolarity: "neutral" }),
    stat("成功率", requests ? formatPercentValue(successes / requests, 1) : "-",
      `${formatCount(successes)} 成功 · ${formatCount(stats?.failures || 0)} 失败`,
      { iconName: "check", tone: requests && successes / requests < 0.95 ? "bad" : null }),
    stat("Token", formatCompact(stats?.total_tokens || 0),
      `缓存 ${formatCompact(stats?.cached_tokens || 0)} Token`, { iconName: "cost" }),
    stat("平均耗时", formatCompact(stats?.avg_duration_ms || 0), "毫秒", { iconName: "clock" }),
  );
}

function flowCard(usage) {
  const links = (usage?.links || []).map((link) => ({
    ...link,
    requests: state.metric === "tokens"
      ? Number(link.total_tokens) || 0
      : Number(link.requests) || 0,
  }));
  const metricLabel = state.metric === "tokens" ? "Token" : "次请求";
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
      ? sankey({
          links,
          layers: usage.layers || [],
          metricLabel,
          // 面板常被嵌在窄栏里，给一个比管理面更矮的画布；层数多时靠横向滚动兜底。
          height: 360,
          formatValue: (value) => (state.metric === "tokens"
            ? formatCompactNumber(value)
            : formatCount(value)),
          ariaLabel: `本工作空间的请求流向，${rangeLabel()}`,
        })
      : empty("窗口内没有可归因的请求。", {
          icon: "activity",
          hint: "归属从本版本起开始记录，升级前的历史行不会计入。",
        }),
  );
}

// layerIndexOf 按层名查下标（找不到给 -1）。
//
// 层级顺序由服务端给（usage.layers），因此**必须按层名定位**而不是写死数字：这次在
// 供应商与上游模型之间插入「上游 Key」层时，原先写死的 3→4 段就从"供应商 → 上游模型"
// 变成了"供应商 → Key"，那张卡会安静地把 Key 名当成上游模型名列出来。
function layerIndexOf(usage, name) {
  return (usage?.layers || []).indexOf(name);
}

// sumLinks 把某一段连边按一端汇总成排行行。side 决定取起点还是终点：
// 同一段"任务→模型"的连边，取起点是任务排行、取终点是模型排行，两者别混。
function sumLinks(usage, layer, side) {
  const totals = new Map();
  for (const link of (usage?.links || []).filter((item) => item.source_layer === layer)) {
    const key = link[side];
    totals.set(key, (totals.get(key) || 0) + (Number(link.requests) || 0));
  }
  return [...totals.entries()]
    .map(([name, value]) => ({ name, value }))
    .sort((a, b) => b.value - a.value);
}

// 任务用量：取 requested_model_id 那一层的**起点连边**才是任务名（调用方传
// TASK_XXXXXX，它以 requested_model_id 的身份出现，见 internal/metrics/workspace.go
// 的层级注释）。取终点会画成「任务用量」里列一串模型名，和卡头说的不是一回事。
function taskUsageCard(usage) {
  const layer = layerIndexOf(usage, "requested_model_id");
  const rows = layer < 0 ? [] : sumLinks(usage, layer, "source");
  return card(
    cardHead("任务用量", badge(rangeLabel(), "muted")),
    rows.length
      ? barList(rows, { format: (row) => `${formatCount(row.value)} 次`, emptyText: "窗口内没有请求。" })
      : empty("窗口内没有请求。", { icon: "activity" }),
  );
}

// 上游模型用量：取**终点是上游模型**的那一段（它的起点是 Key 层）。面板要看清这个
// 空间实际打到哪些厂商模型上——本地路由名（model_id）与上游模型名是两回事。
// 空数据时也返回一张卡：它和任务用量同处一排，留空会看见缺口。
function upstreamUsageCard(usage) {
  const layer = layerIndexOf(usage, "upstream_model_id") - 1;
  const rows = layer < 0 ? [] : sumLinks(usage, layer, "target");
  return card(
    cardHead("上游模型用量", badge(rangeLabel(), "muted")),
    rows.length
      ? barList(rows, {
          tone: "secondary",
          format: (row) => `${formatCount(row.value)} 次`,
          emptyText: "窗口内没有请求。",
        })
      : empty("窗口内没有可归因的上游模型。", {
          icon: "activity",
          hint: "上游模型归因从本版本起开始记录，升级前的历史行不会计入。",
        }),
  );
}

// —— 任务管理 ——
// 刻意只给「模型 + 显示名」两个字段：任务的 params（温度、stop、推理强度等）属于
// 调优，归管理面；嵌入方的面板管的是"这个空间有哪些任务"。
// 更新时不发 params，服务端的 UpdateParams=false 会原样保留既有参数，不会清空。

const TASK_COLUMNS = [
  { key: "name", label: "任务名", render: (row) => h("span.mono", {}, row.name) },
  { key: "display_name", label: "显示名", render: (row) => row.display_name || h("span.muted", "—") },
  { key: "model", label: "模型", render: (row) => (row.model
      ? h("span.mono", {}, row.model)
      : h("span.muted", "未指定")) },
  // 行内动作占一列，空表头（动作列不需要标题）。
  { key: "actions", label: "", render: (row) => h("div.inline", {},
      buttonNode("编辑", {
        small: true,
        variant: "text",
        disabled: state.editing !== null,
        onClick: () => { state.editing = { task: row }; draw(); },
      }),
      buttonNode("删除", {
        small: true,
        variant: "text",
        disabled: state.editing !== null,
        onClick: () => removeTask(row),
      }),
    ) },
];

function tasksCard() {
  const rows = state.tasks;
  const actions = h("div.inline", {},
    buttonNode("新建任务", {
      variant: "primary",
      small: true,
      disabled: state.editing !== null,
      onClick: () => { state.editing = { new: true }; draw(); },
    }),
  );
  return card(
    cardHead(`任务（${rows.length}）`, actions),
    state.editing ? taskForm() : null,
    table(TASK_COLUMNS, rows, "这个工作空间还没有任务。"),
  );
}

function taskForm() {
  const editing = state.editing;
  const isNew = !!editing.new;
  const task = editing.task || { name: "", display_name: "", model: "" };
  const models = state.usage?.models || [];

  const nameInput = input({
    value: task.name,
    disabled: !isNew,
    placeholder: "例如 TASK_000001",
    "aria-label": "任务名",
  });
  const displayInput = input({
    value: task.display_name || "",
    placeholder: "可选",
    "aria-label": "显示名",
  });
  const modelOptions = [{ value: "", label: "（未指定）" }]
    .concat(models.map((id) => ({ value: id, label: id })));
  const modelSelect = select(modelOptions, {
    value: task.model || "",
    "aria-label": "模型",
  });

  const errorHost = h("div");
  const close = () => { state.editing = null; draw(); };
  const save = buttonNode(isNew ? "创建任务" : "保存修改", {
    variant: "primary",
    onClick: async () => {
      const name = isNew ? nameInput.value.trim() : task.name;
      if (!name) { render(errorHost, notice("请填写任务名。", "error")); return; }
      const payload = {
        model: modelSelect.value || null,
        display_name: displayInput.value.trim() || null,
      };
      save.disabled = true;
      render(errorHost);
      try {
        if (isNew) await api.createTask(state.revision, { name, ...payload });
        else await api.updateTask(state.revision, name, payload);
        state.editing = null;
        await load();
        toast(isNew ? "任务已创建。" : "任务已更新。");
        return;
      } catch (error) {
        render(errorHost, notice(errorText(error), "error"));
        // 版本冲突说明手上的配置已过期，重新读一次让人重试。
        if (error.status === 409) await load().catch(() => {});
      }
      save.disabled = false;
    },
  });

  return h("div.editor", {},
    h("div.form-grid", {},
      field("任务名", nameInput),
      field("显示名", displayInput),
      field("模型", modelSelect),
    ),
    errorHost,
    h("div.inline", {}, save, buttonNode("取消", { variant: "text", onClick: close })),
    isNew ? null : h("p.muted", "任务的参数（温度、停止词、推理强度等）请在 AMKR 管理界面调整，这里不会改动它们。"),
  );
}

async function removeTask(task) {
  // window.confirm 而不是 ui.js 的 confirmDialog：面板可能被嵌在窄 iframe 里，
  // 模态框的遮罩只覆盖本 iframe 的视口，宿主页面上的其它内容仍可点击——那会让人
  // 以为"确认框没拦住操作"。原生 confirm 由浏览器保证是真的模态。
  if (!window.confirm(`删除任务「${task.name}」？使用它的调用方之后会收到 404。`)) return;
  try {
    await api.deleteTask(state.revision, task.name);
    await load();
    toast("任务已删除。");
  } catch (error) {
    toast(errorText(error), "error");
    if (error.status === 409) await load().catch(() => {});
  }
}

// —— 组装 ——

function draw() {
  if (!host) return;

  if (!state.key) {
    render(host, keyForm());
    return;
  }

  const usage = state.usage;
  const workspace = usage?.workspace || "工作空间";
  const children = [
    h("div.panel-bar", {},
      h("div.panel-id", {},
        h("span.panel-eyebrow", "工作空间面板"),
        h("h1.panel-name", { class: "mono" }, workspace),
      ),
      h("div.panel-tools", {},
        state.at ? freshness(state.at) : null,
        segmented(
          USAGE_RANGES.map((item) => ({ id: item.hours, label: item.short, title: item.label })),
          state.selection,
          (id) => {
            state.selection = id === ALL_HISTORY ? ALL_HISTORY : Number(id);
            state.loading = true;
            draw();
            load();
          },
          { "aria-label": "选择统计窗口" },
        ),
        buttonNode("", {
          small: true,
          variant: "text",
          "aria-label": "刷新",
          title: "刷新",
          onClick: () => { state.loading = true; draw(); load(); },
        }, "刷新"),
        buttonNode("", {
          small: true,
          variant: "text",
          "aria-label": "复制面板地址",
          title: "复制带 key 的面板地址，便于再次打开",
          onClick: () => copyText(panelURL())
            .then(() => toast("面板地址已复制。"))
            .catch((error) => toast(errorText(error), "error")),
        }, "复制地址"),
      ),
    ),
  ];

  if (state.unauthorized) {
    children.push(notice("这个 key 无效，或对应的空间已被删除。请在下方重新填写。", "error"));
    children.push(keyForm());
    render(host, children);
    return;
  }
  if (state.error) {
    children.push(notice(`无法读取面板数据：${state.error}`, "error"));
    children.push(flowCard(null));
    render(host, children);
    return;
  }
  if (state.loading || !usage) {
    children.push(skeleton("stats"));
    children.push(card(cardHead("请求流向"), skeleton("chart")));
    render(host, children);
    return;
  }

  const chain = flowChain(usage);
  if (chain) children.push(chain);
  children.push(kpiTiles(usage));
  children.push(flowCard(usage));
  children.push(h("div.grid-12", {},
    // 任务表独占整行：它 4 列，收成半宽后窄屏会把「模型」和行内动作一起挤掉。
    h("div.col-12", {}, tasksCard()),
    // 两张半宽卡成一排。6+6 在四档断点下都排满；col-8 + col-4 在 <=1024px 会让
    // 第二张剩成半行（详见 webui/probes/webui_layout_probe.mjs）。
    h("div.col-6", {}, taskUsageCard(usage)),
    h("div.col-6", {}, upstreamUsageCard(usage)),
  ));
  if (!state.tasks.length) {
    children.push(notice("这个工作空间还没有任务。调用方要指定任务名（或模型别名）才能用到它。", "info"));
  }
  render(host, children);
}

// panelURL 拼出带当前 key 的面板地址，供嵌入方复制保存。
//
// 用当前页面地址而不是拼一个绝对路径：面板可能挂在子路径下（/amkr/ui/panel.html），
// 也可能被宿主用自定义 api 基址引入，能把 key 交回去的只有它自己手上的地址。
function panelURL() {
  const url = new URL(location.href);
  url.hash = `k=${encodeURIComponent(state.key)}`;
  return url.toString();
}
