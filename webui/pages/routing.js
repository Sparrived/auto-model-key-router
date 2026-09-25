// 模型路由：对外名称（路由 ID + 别名）与轮询目标（供应商 Key + 上游模型名）。
//
// 两个概念刻意分开呈现：
//   - 对外名称 = 路由 ID 与别名，两者都出现在 /v1/models，外部请求的 model 字段写的就是它们；
//   - 轮询目标 = 某个 Key 加上"发给这个上游的模型名"（upstream_model）。同一个模型在各上游
//     叫法不同时，就在目标行里各写各的名字，名字不会变成可调用名。
//
// 「一条路由下没有目标就不该存在」是服务端的写路径不变式（见 internal/configops），
// 因此这里的编辑器允许把目标删到一个不剩，保存时由服务端把整条路由删掉——页面上
// 用文案与二次确认明确这一点，而不是偷偷拦住用户。

import { h, errorText } from "../dom.js";
import { api } from "../api.js";
import {
  card, cardHead, notice, badge, empty, loading, render, toast, buttonNode,
  input, select, confirmDialog, dialog, field,
} from "../ui.js";

const MODES = [
  { value: "", label: "默认策略" },
  { value: "round_robin", label: "轮询" },
  { value: "priority", label: "优先级" },
  { value: "only_first", label: "首 Key" },
];

const modeLabel = (value) => (MODES.find((mode) => mode.value === (value || "")) || MODES[0]).label;

const DATALIST_ID = "routing-upstream-options";

const state = {
  routes: [],
  providers: [],
  revision: null,
  loading: true,
  error: null,
  active: "",
  editing: null,
  saving: false,
};

let host = null;

async function load() {
  const [routes, providers] = await Promise.all([api.routes(), api.providers()]);
  state.routes = routes.routes || [];
  state.revision = routes.config_revision;
  state.providers = providers.providers || [];
  if (!state.routes.some((route) => route.id === state.active)) {
    state.active = state.routes[0]?.id || "";
  }
}

// 路由的对外名称集合：路由 ID + 别名。两者等价，都会出现在 /v1/models。
const routeNames = (route) => [route.id, ...(route.aliases || [])];

// keyUpstreams 返回某个 Key 探测到的上游模型名（去重排序）。
//
// 这是"上游叫什么"的唯一权威来源：页面用它给上游名输入框做候选，不再把它当成
// 可调用名称。没探测过（capabilities 缺失）的 Key 返回空，用户仍可手填。
function keyUpstreams(key) {
  const names = (key.capabilities?.models || []).map((name) => String(name).trim()).filter(Boolean);
  return [...new Set(names)].sort();
}

const targetText = (target) => `${target.provider} / ${target.key} / ${target.upstream_model}`;

// candidates 返回"探测到的上游名正好是本路由某个对外名称、且尚未绑定"的目标。
//
// 只是一个快捷入口：上游名与路由名不一致时（同一个模型在各家叫法不同），用下面的
// 「添加目标」手选 Key 与上游名即可。
function candidates(route) {
  const names = new Set(routeNames(route));
  const bound = new Set((route.targets || []).map((target) => `${target.provider}|${target.key}|${target.upstream_model}`));
  const list = [];
  for (const provider of state.providers) {
    for (const key of provider.keys || []) {
      for (const upstream of keyUpstreams(key)) {
        if (!names.has(upstream)) continue;
        if (bound.has(`${provider.id}|${key.name}|${upstream}`)) continue;
        list.push({ provider: provider.id, key: key.name, upstream_model: upstream });
      }
    }
  }
  return list;
}

// allKeyOptions 列出全部 (供应商, Key)，供"添加目标"选择。
function allKeyOptions() {
  const options = [];
  for (const provider of state.providers) {
    for (const key of provider.keys || []) {
      options.push({
        value: `${provider.id}|${key.name}`,
        label: `${provider.id} / ${key.name}（探测到 ${keyUpstreams(key).length} 个上游模型）`,
      });
    }
  }
  return options;
}

// —— 只读详情 ——

function targetRow(route, target, index) {
  return h("div.inline", { style: { padding: "8px 12px", background: "#fafafa", borderRadius: "4px" } },
    h("span.mono", targetText(target)),
    h("span", { style: { flex: "1" } }),
    buttonNode("移到其它路由…", {
      small: true,
      variant: "text",
      disabled: state.saving || state.routes.length < 2,
      onClick: () => moveDialog(route, index),
    }),
  );
}

function routeDetail(route) {
  const targets = route.targets || [];
  return h("div.stack", {},
    h("div.stack.tight", {},
      h("div", {}, h("span.muted", "对外名称："), h("span.mono", route.id)),
      h("div", {}, h("span.muted", "别名："), (route.aliases || []).length ? h("span.mono", route.aliases.join(", ")) : "无别名"),
      h("p.muted", "路由 ID 与别名都出现在 /v1/models；上游模型名只是发给上游的名字，不会被调用。"),
    ),
    h("div.stack.tight", {},
      h("div", {}, h("span.muted", "轮询目标（按顺序，越靠前越优先）：")),
      targets.length
        ? h("ul", { "aria-label": `${route.id} 的路由目标`, style: { margin: "0", paddingLeft: "20px" } },
            targets.map((target) => h("li.mono", targetText(target))))
        : h("p.muted", "尚未绑定目标。没有目标的路由不会被调用，保存空目标会直接删除它。"),
      targets.length
        ? h("div.stack.tight", {}, targets.map((target, index) => targetRow(route, target, index)))
        : null,
    ),
  );
}

// —— 目标迁移 ——

// moveDialog 选一个接收方路由，把这条目标整条搬过去。
//
// 上游模型名随目标一起搬（它就是"发给那个上游的名字"，与落在哪条路由无关）。
function moveDialog(route, index) {
  const target = route.targets[index];
  const destinations = state.routes.filter((item) => item.id !== route.id);
  if (!destinations.length) {
    toast("没有其它路由可以接收这个目标。", "error");
    return;
  }
  const picker = select(destinations.map((item) => ({
    value: item.id,
    label: `${item.id}（${(item.targets || []).length} 个目标）`,
  })), { value: destinations[0].id });
  const ref = dialog({
    title: "移动到其它路由",
    body: h("div.stack", {},
      h("p.mono", targetText(target)),
      field("目标路由", picker),
      h("p.muted", "上游模型名保持不变。原路由若没有别的目标，会被自动删除。"),
    ),
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "移动", onClick: () => { ref.close(); void moveTarget(route, index, picker.value); } },
    ],
  });
}

async function moveTarget(route, index, destinationID) {
  const destination = state.routes.find((item) => item.id === destinationID);
  if (!destination) return;
  const target = route.targets[index];
  const remaining = (route.targets || []).filter((_, position) => position !== index);
  state.saving = true;
  draw();
  try {
    // 先给接收方追加，再从来源移除：两步之间中断也只会留下一条重复目标（可在页面上
    // 手动删掉），反过来则会直接丢目标。
    await api.updateRoute(state.revision, destination.id, [...(destination.targets || []), target],
      destination.aliases || [], destination.routing_mode || null, null);
    await load();
    // 空目标等于删除路由：这一步同时完成了"搬空即删来源"。
    await api.updateRoute(state.revision, route.id, remaining, route.aliases || [], route.routing_mode || null, null);
    await load();
    state.active = remaining.length === 0 ? destination.id : route.id;
    toast(remaining.length === 0
      ? `目标已移到 ${destination.id}，原路由 ${route.id} 已删除。`
      : `目标已移到 ${destination.id}。`);
  } catch (error) {
    toast(errorText(error), "error");
    await load();
  }
  state.saving = false;
  draw();
}

// —— 编辑态 ——

function routeEditor(route) {
  const idInput = input({ value: route.id, placeholder: "对外模型名" });
  const aliasInput = input({ value: (route.aliases || []).join(", "), placeholder: "逗号分隔，留空表示无别名" });
  const modeSelect = select(MODES, { value: route.routing_mode || "" });
  const errorHost = h("div");
  const targets = (route.targets || []).map((target) => ({ ...target }));

  const upstreamInput = (target) => input({
    value: target.upstream_model,
    placeholder: "上游模型名",
    "aria-label": `${target.provider} / ${target.key} 的上游模型名`,
    onInput: (event) => { target.upstream_model = event.target.value; },
  });

  const listHost = h("div.stack.tight");
  const drawTargets = () => {
    render(listHost,
      targets.length
        ? targets.map((target, index) => h("div.inline", { style: { padding: "8px 12px", background: "#fafafa", borderRadius: "4px" } },
            h("span.mono", `${target.provider} / ${target.key} /`),
            upstreamInput(target),
            h("span", { style: { flex: "1" } }),
            buttonNode("上移", { small: true, variant: "text", disabled: index === 0, onClick: () => { [targets[index - 1], targets[index]] = [targets[index], targets[index - 1]]; drawTargets(); } }),
            buttonNode("下移", { small: true, variant: "text", disabled: index === targets.length - 1, onClick: () => { [targets[index + 1], targets[index]] = [targets[index], targets[index + 1]]; drawTargets(); } }),
            buttonNode("移除", { small: true, variant: "text", onClick: () => { targets.splice(index, 1); drawTargets(); } }),
          ))
        : h("p.muted", "此路由没有任何目标。保存后它会连同路由一起被删除——空路由不会出现在 /v1/models 里。"),
    );
  };
  drawTargets();

  // —— 添加目标：先选 Key，再从该 Key 探测到的上游名里挑（也允许手填） ——
  const keyOptions = allKeyOptions();
  const keySelect = select(keyOptions, { value: keyOptions[0]?.value || "" });
  const upstreamField = input({ placeholder: "上游模型名", list: DATALIST_ID });
  const datalist = h("datalist", { id: DATALIST_ID });
  const refreshUpstreams = () => {
    const [providerID, keyName] = String(keySelect.value).split("|");
    const provider = state.providers.find((item) => item.id === providerID);
    const key = (provider?.keys || []).find((item) => item.name === keyName);
    render(datalist, ...keyUpstreams(key || {}).map((name) => h("option", { value: name })));
    if (!upstreamField.value) upstreamField.value = route.id;
  };
  if (keyOptions.length) {
    keySelect.addEventListener("change", refreshUpstreams);
    refreshUpstreams();
  }

  const addTarget = () => {
    const [providerID, keyName] = String(keySelect.value).split("|");
    if (!providerID || !keyName) return;
    const upstream = String(upstreamField.value || "").trim() || route.id;
    if (targets.some((target) => target.provider === providerID && target.key === keyName && target.upstream_model === upstream)) {
      render(errorHost, notice("这个目标已经在本路由里了。", "warn"));
      return;
    }
    render(errorHost);
    targets.push({ provider: providerID, key: keyName, upstream_model: upstream });
    drawTargets();
  };

  const addHost = keyOptions.length
    ? h("div.stack.tight", {},
        h("div.inline", {},
          keySelect,
          upstreamField,
          buttonNode("添加目标", { small: true, variant: "secondary", onClick: addTarget }),
        ),
        h("p.muted", "上游模型名可留空（默认与路由 ID 相同），也可以直接输入探测不到的名字。"),
        datalist,
      )
    : h("p.muted", "还没有任何供应商 Key。先在供应商页添加 Key，再回来绑定目标。");

  const options = candidates(route);
  const candidateHost = options.length
    ? h("div.btn-row", {}, options.map((candidate) => buttonNode(`+ ${targetText(candidate)}`, {
        small: true,
        variant: "secondary",
        onClick: () => {
          targets.push({ ...candidate });
          drawTargets();
          render(candidateHost);
        },
      })))
    : h("p.muted", "没有探测到本路由名称的未绑定 Key。上游名与路由名不同时，用上面的「添加目标」手选 Key 与上游名。");

  const save = async () => {
    const newID = idInput.value.trim() || route.id;
    const aliases = aliasInput.value.split(",").map((item) => item.trim()).filter(Boolean);
    const mode = modeSelect.value || null;
    // 上游名留空回落到路由名：那是"同一名字发给上游"的默认含义。
    const cleaned = targets.map((target) => ({
      provider: target.provider,
      key: target.key,
      upstream_model: String(target.upstream_model || "").trim() || newID,
    }));
    state.saving = true;
    draw();
    try {
      if (!cleaned.length) {
        await api.deleteRoute(state.revision, route.id);
        toast(`路由 ${route.id} 没有目标，已删除。`);
      } else {
        await api.updateRoute(state.revision, route.id, cleaned, aliases, mode, newID === route.id ? null : newID);
        toast("路由已保存。");
      }
      await load();
      state.editing = null;
      state.active = newID;
    } catch (error) {
      render(errorHost, notice(`保存失败: ${errorText(error)}`, "error"));
      if (error.status === 409) load().then(draw);
    }
    state.saving = false;
    draw();
  };

  return h("div.stack", {},
    h("div.form-grid", {},
      h("label.field", h("span", "对外名称（路由名）"), idInput),
      h("label.field", h("span", "别名"), aliasInput),
      h("label.field", h("span", "路由模式"), modeSelect),
    ),
    h("p.muted", "对外名称与别名都会出现在 /v1/models，外部请求的 model 字段写的就是它们；改名会同时改写 unified_model 与任务里的引用。"),
    h("div", {}, h("h4", "轮询目标（按顺序）"), listHost),
    h("div", {}, h("h4", "添加目标"), addHost),
    h("div", {}, h("h4", "探测到本路由名称的 Key"), candidateHost),
    errorHost,
    h("div.btn-row", {},
      buttonNode(state.saving ? "保存中…" : "保存路由", { disabled: state.saving, onClick: save }),
      buttonNode("取消", { variant: "text", onClick: () => { state.editing = null; draw(); } }),
      buttonNode("删除路由", {
        variant: "danger",
        disabled: state.saving,
        onClick: () => confirmDialog({
          title: "删除路由",
          message: `删除模型路由 ${route.id}？绑定该模型的 Key 会一并解除。`,
          confirmLabel: "删除",
          danger: true,
          onConfirm: async () => {
            try {
              await api.deleteRoute(state.revision, route.id);
              await load();
              state.editing = null;
              toast("路由已删除。");
              draw();
            } catch (error) { toast(errorText(error), "error"); }
          },
        }),
      }),
    ),
  );
}

export function renderRouting(context) {
  host = h("div.stack");
  if (state.loading) {
    render(host, h("div.page-head", h("h1", "模型路由")), loading("正在读取模型路由。"));
    (async () => {
      try { await load(); state.error = null; } catch (error) { state.error = errorText(error); }
      state.loading = false;
      draw();
    })();
    return host;
  }
  draw();
  return host;
}

// 模型导航：竖向排在详情左侧，与供应商页共用同一套排版（.rail-split / .rail-nav）。
//
// 模型数量只会比供应商更多（一个供应商就能贡献几十个），横排标签页尤其撑不住；
// 竖排只占一列，再多也只是这一列变长，右侧详情的位置始终不动。
//
// 不带图标：供应商那栏的品牌标志是有信息量的（一眼分辨是哪家），而这里每一行都是
// 同一个「路由」图标，重复几十次只是占宽。列只有 180px，留给模型名更有用。
//
// 语义用 nav + aria-current，不用 role="tab"：真正的 tab 需要配套的
// role="tabpanel" 与方向键 roving tabindex，这里没有实现，标成 tab 属于空头承诺。
function routeRail() {
  return h("nav.rail-nav", { "aria-label": "模型列表" },
    state.routes.map((route) => h("button.rail-item", {
      type: "button",
      "aria-current": route.id === state.active ? "true" : null,
      onClick: () => { state.active = route.id; state.editing = null; draw(); },
    },
      h("span.rail-text", {},
        h("span.rail-name", route.id),
        h("span.rail-meta", `${(route.targets || []).length} 个目标`),
      ),
    )),
  );
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "模型路由"),
        h("p.sub", "对外名称（路由 ID + 别名）与它下面的轮询目标；每个目标自带发给上游的模型名。")),
      h("div.spacer"),
      state.revision ? badge(`版本 ${String(state.revision).slice(0, 12)}`, "muted") : null,
    ),
  ];
  if (state.error) children.push(notice(`无法读取或写入模型路由: ${state.error}`, "error"));
  if (!state.routes.length) {
    children.push(empty("尚未配置模型路由。请先在供应商页添加 Key 并绑定其服务模型，路由会自动出现在这里。"));
    render(host, children);
    return;
  }

  const route = state.routes.find((item) => item.id === state.active);
  if (!route) { render(host, children); return; }

  const detail = [];
  if (state.editing === route.id) {
    detail.push(card(routeEditor(route)));
  } else {
    detail.push(card(
      cardHead(route.id,
        badge(modeLabel(route.routing_mode), "muted"),
        badge(`${(route.targets || []).length} 个目标`, (route.targets || []).length ? "muted" : "warn"),
        buttonNode("编辑", { small: true, variant: "text", onClick: () => { state.editing = route.id; draw(); } }),
      ),
      routeDetail(route),
    ));
  }

  children.push(h("div.rail-split", {},
    routeRail(),
    h("div.rail-detail", {}, detail),
  ));
  render(host, children);
}
