// 模型路由：一个对外名称（路由 ID + 别名）与它下面的两类配置。
//
// 两类配置刻意分开呈现：
//   - 轮询目标 = 某个 Key 加上"发给这个上游的模型名"（upstream_model）。同一个模型在各
//     上游叫法不同时，就在目标行里各写各的名字，名字不会变成可调用名；
//   - 端点计划 = 「端点族 → 转给哪个模型」的覆盖表。直呼这个名称时，落在配置过的族上的
//     请求改发给那一族的模型；没配置的族照旧用它的轮询目标。
//
// `unified-model` 是**固定的第一个名称**：它不是 models 里的一条，而是一张全局计划表
// （配置写在 unified_model 段）。放在这里，是因为它与普通名称的端点计划本来就是同一件
// 事——一族指一个模型；区别只有两点：它没有自己的轮询目标，也不能被删除（只能停用）。
// 于是「配统一模型」不再需要另一个页面，而是这个列表里的第一项。
//
// 「一条路由下没有目标就不该存在」是服务端的写路径不变式（见 internal/configops），
// 因此这里的编辑器允许把目标删到一个不剩，保存时由服务端把整条路由删掉——页面上用文案
// 与二次确认明确这一点，而不是偷偷拦住用户。二次确认里的连带影响清单由服务端预演
// （`dry_run=1`）算出，见 webui/model-impact.js。

import { h, mount, errorText } from "../dom.js";
import { api } from "../api.js";
import { writeWithImpactConfirm } from "../model-impact.js";
import { normalizeModelKinds } from "../model-kinds.js";
import {
  FAMILIES, PLAN_FAMILIES, TEXT_FAMILY,
  enabledKeys, keySelect, modelSelect, readEndpointPlans, endpointPlanPayload,
} from "./endpoint-plans.js";
import {
  card, cardHead, notice, badge, empty, loading, render, toast, buttonNode,
  input, select, confirmDialog, dialog, field, kv,
} from "../ui.js";

// UNIFIED_ID 是统一模型的固定名称。它不是模型列表里的一条，而是服务端按入站端点族分派
// 的伪模型；在界面上它与普通名称同处一个列表，且永远排第一。
const UNIFIED_ID = "unified-model";

const MODES = [
  { value: "", label: "默认策略" },
  { value: "round_robin", label: "轮询" },
  { value: "priority", label: "优先级" },
  { value: "only_first", label: "首 Key" },
];

const modeLabel = (value) => (MODES.find((mode) => mode.value === (value || "")) || MODES[0]).label;

const DATALIST_ID = "routing-upstream-options";
const ADD_DATALIST_ID = "routing-add-upstream-options";

// ESTABLISHED_FAMILIES 是历史上就有的两族：它们的模型在只读摘要里固定占一行（未配置时
// 也显示"未配置"），其余族只在配置了才出现。保留这条界线，是为了让只用了老三种计划的
// 配置，页面与改动前逐字一致。
const ESTABLISHED_FAMILY_IDS = ["image", "embeddings"];

const EFFORTS = [
  { value: "", label: "默认" },
  { value: "none", label: "none" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

const state = {
  routes: [],
  providers: [],
  models: [],
  kinds: null,
  unified: null,
  revision: null,
  loading: true,
  error: null,
  // active 是左栏选中的名字：UNIFIED_ID 或某条路由的 id。
  active: UNIFIED_ID,
  editing: null,
  saveError: null,
  saving: false,
};

let host = null;

async function load() {
  const [routes, providers, models, unified] = await Promise.all([
    api.routes(), api.providers(), api.models(), api.unified(),
  ]);
  state.routes = routes.routes || [];
  state.revision = routes.config_revision;
  state.providers = providers.providers || [];
  state.models = models.models || [];
  state.unified = unified.unified_model || null;
  state.kinds = await loadModelKinds();
  if (state.active !== UNIFIED_ID && !state.routes.some((route) => route.id === state.active)) {
    state.active = UNIFIED_ID;
  }
}

// loadModelKinds 读一次模型类型，用来给各端点下拉排序（本族在前、未分类在后）。
//
// 与模型清单**分开失败**：类型只影响下拉里的排序，问不到就按配置顺序平铺，绝不能让整页
// 读不出来——/ui 不可达时用户连模型都选不了，那比"下拉没排序"糟得多。
async function loadModelKinds() {
  try {
    const data = await api.modelKinds(state.models.map((model) => model.id));
    return normalizeModelKinds(data);
  } catch {
    return null;
  }
}

// knownModel 把「已不存在的模型名」折成空串（见 endpoint-plans.js 的同名说明）。
function knownModel(id) {
  return state.models.some((model) => model.id === id) ? id : "";
}

// keyUpstreams 返回某个 Key 探测到的上游模型名（去重排序）。
//
// 这是"上游叫什么"的唯一权威来源：页面用它给上游名输入框做候选，不再把它当成可调用名称。
// 没探测过（capabilities 缺失）的 Key 返回空，用户仍可手填。
function keyUpstreams(key) {
  const names = (key.capabilities?.models || []).map((name) => String(name).trim()).filter(Boolean);
  return [...new Set(names)].sort();
}

// allKeyOptions 列出全部 (供应商, Key)，供"添加目标"与"添加模型"选择。
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

// —— 路由的对外名称 ——

// routeNames 返回一条路由的对外名称集合：路由 ID + 别名。两者等价，都会出现在 /v1/models。
const routeNames = (route) => [route.id, ...(route.aliases || [])];

const targetText = (target) => `${target.provider} / ${target.key} / ${target.upstream_model}`;

// planText 把一族端点计划写成一行读数。
const planText = (family, plan) => {
  const model = plan?.primary?.model;
  if (!model) return null;
  const key = plan.primary?.key;
  return `${family.label} → ${model}${key ? ` · 固定 Key ${key}` : ""}`;
};

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

// —— 只读详情：普通路由 ——

function targetRow(route, target, index) {
  return h("div.target-item-row", {},
    h("span.mono.target-text", targetText(target)),
    h("span", { style: { flex: "1" } }),
    buttonNode("移到其它路由…", {
      small: true,
      variant: "secondary",
      iconName: "external",
      disabled: state.saving || state.routes.length < 2,
      onClick: () => moveDialog(route, index),
    }),
  );
}

function routeDetail(route) {
  const targets = route.targets || [];
  const plans = PLAN_FAMILIES
    .map((family) => planText(family, route.endpoints?.[family.id]))
    .filter(Boolean);
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
    h("div.stack.tight", {},
      h("div", {}, h("span.muted", "端点计划（按入站端点族分派）：")),
      plans.length
        ? h("ul", { "aria-label": `${route.id} 的端点计划`, style: { margin: "0", paddingLeft: "20px" } },
            plans.map((text) => h("li.mono", text)))
        : h("p.muted", "未按端点分派：所有端点都用上面的轮询目标。"),
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
    //
    // 两次调用都**不带 endpoints**：搬的是目标，接收方自己的端点计划不受影响。
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

// —— 编辑态：普通路由 ——

// endpointPlanEditor 渲染某条路由的端点计划编辑区（七族，一族一行）。
//
// 草稿与提交体的折法都在 endpoint-plans.js：那边是"只写配置了模型的族"，与服务端的
// 「整体替换这张表」正好对上。
function endpointPlanEditor(draft, redraw) {
  return h("div.stack.tight", {}, PLAN_FAMILIES.map((family) => {
    const picked = draft[family.id];
    return h("div.form-grid", {},
      h("label.field", h("span", family.label), modelSelect({
        models: state.models,
        kinds: state.kinds,
        kind: family.kind,
        value: picked.model,
        emptyLabel: "不覆盖（用本模型的 targets）",
        disabled: state.saving,
        onChange: (event) => {
          // 换模型即丢掉旧 Key：Key 是模型级的，留着它会被服务端以"未配置可用 key"拒绝。
          picked.model = event.target.value;
          picked.key = "";
          redraw();
        },
      })),
      h("label.field", h("span", family.keyLabel), keySelect({
        models: state.models,
        modelId: picked.model,
        value: picked.key,
        includeAuto: true,
        disabled: state.saving,
        onChange: (event) => { picked.key = event.target.value; },
      })),
    );
  }));
}

function routeEditor(route) {
  const idInput = input({ value: route.id, placeholder: "对外模型名" });
  const aliasInput = input({ value: (route.aliases || []).join(", "), placeholder: "逗号分隔，留空表示无别名" });
  const modeSelect = select(MODES, { value: route.routing_mode || "" });
  const errorHost = h("div");
  const targets = (route.targets || []).map((target) => ({ ...target }));
  const endpoints = readEndpointPlans(route.endpoints, state.models);

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
        ? targets.map((target, index) => h("div.target-item-row", {},
            h("div.target-info-wrap", {},
              h("span.target-provider-key", `${target.provider} / ${target.key} /`),
              upstreamInput(target),
            ),
            h("span", { style: { flex: "1" } }),
            h("div.action-button-group", {},
              buttonNode("上移", { small: true, variant: "secondary", iconName: "arrowUp", disabled: index === 0, onClick: () => { [targets[index - 1], targets[index]] = [targets[index], targets[index - 1]]; drawTargets(); } }),
              buttonNode("下移", { small: true, variant: "secondary", iconName: "arrowDown", disabled: index === targets.length - 1, onClick: () => { [targets[index + 1], targets[index]] = [targets[index], targets[index + 1]]; drawTargets(); } }),
              buttonNode("移除", { small: true, variant: "text", class: "btn-action-danger", iconName: "trash", onClick: () => { targets.splice(index, 1); drawTargets(); } }),
            ),
          ))
        : h("p.muted", "此路由没有任何目标。保存后它会连同路由一起被删除——空路由不会出现在 /v1/models 里。"),
    );
  };
  drawTargets();

  const endpointHost = h("div.stack.tight");
  const drawEndpoints = () => render(endpointHost, endpointPlanEditor(endpoints, drawEndpoints));
  drawEndpoints();

  // —— 添加目标：先选 Key，再从该 Key 探测到的上游名里挑（也允许手填） ——
  const keyOptions = allKeyOptions();
  const keySelectNode = select(keyOptions, { value: keyOptions[0]?.value || "" });
  const upstreamField = input({ placeholder: "上游模型名", list: DATALIST_ID });
  const datalist = h("datalist", { id: DATALIST_ID });
  const refreshUpstreams = () => {
    const [providerID, keyName] = String(keySelectNode.value).split("|");
    const provider = state.providers.find((item) => item.id === providerID);
    const key = (provider?.keys || []).find((item) => item.name === keyName);
    render(datalist, ...keyUpstreams(key || {}).map((name) => h("option", { value: name })));
    if (!upstreamField.value) upstreamField.value = route.id;
  };
  if (keyOptions.length) {
    keySelectNode.addEventListener("change", refreshUpstreams);
    refreshUpstreams();
  }

  const addTarget = () => {
    const [providerID, keyName] = String(keySelectNode.value).split("|");
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
          keySelectNode,
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
        // 把目标删到一个不剩再保存，等于删掉这条路由（服务端的写路径不变式）。
        // 这条路和「删除路由」按钮是同一个后果，因此同样要先把连带变动说清楚。
        const { confirmed } = await writeWithImpactConfirm({
          title: "删除模型路由",
          message: `路由 ${route.id} 的目标已清空，保存会删掉这条路由。`,
          confirmLabel: "删除路由",
          alwaysConfirm: true,
          preview: () => api.deleteRoute(state.revision, route.id, { dryRun: true }),
          commit: () => api.deleteRoute(state.revision, route.id),
        });
        // 用户取消：停在编辑态（目标已经删空，他想反悔仍可以再加回来）。
        if (!confirmed) { state.saving = false; draw(); return; }
        toast(`路由 ${route.id} 没有目标，已删除。`);
      } else {
        await api.updateRoute(state.revision, route.id, cleaned, aliases, mode,
          newID === route.id ? null : newID, endpointPlanPayload(endpoints));
        toast("路由已保存。");
      }
      await load();
      state.editing = null;
      state.active = state.routes.some((item) => item.id === newID) ? newID : UNIFIED_ID;
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
    h("p.muted", "对外名称与别名都会出现在 /v1/models，外部请求的 model 字段写的就是它们；改名会同时改写 unified_model、模型的端点计划、任务、工作空间与访问密钥清单里指向它的名字。"),
    h("div", {}, h("h4", "轮询目标（按顺序）"), listHost),
    h("div", {}, h("h4", "添加目标"), addHost),
    h("div", {}, h("h4", "探测到本路由名称的 Key"), candidateHost),
    h("div", {},
      h("h4", "端点计划"),
      h("p.muted", "给这个名称指定各端点族改发给哪个模型：直呼它时，配了计划的端点走那一族的模型，"
        + "没配的端点仍用上面的轮询目标。目标模型必须是模型列表里已有的名称（可先在「添加模型名称」里建，"
        + "或由供应商页的 Key 绑定自动产生）。"),
      endpointHost,
    ),
    errorHost,
    h("div.btn-row", {},
      buttonNode(state.saving ? "保存中…" : "保存路由", { disabled: state.saving, onClick: save }),
      buttonNode("取消", { variant: "text", onClick: () => { state.editing = null; draw(); } }),
      buttonNode("删除路由", {
        variant: "danger",
        disabled: state.saving,
        onClick: () => removeRoute(route),
      }),
    ),
  );
}

// removeRoute 删路由：先让服务端预演连带变动，用户确认后再删。
//
// 路由就是模型本身，删它会让引用它的访问密钥少一项授权、工作空间少一个能直呼的模型、
// 引用它的任务被删掉、unified_model 与别的模型的端点计划被改写。这些必须成"删之前能看见"，
// 否则删除按钮就只是把一次配置大改藏在了"路由已删除"这句提示后面。
async function removeRoute(route) {
  try {
    const { confirmed } = await writeWithImpactConfirm({
      title: "删除模型路由",
      message: `删除模型路由 ${route.id}？绑定该模型的 Key 会一并解除。`,
      confirmLabel: "删除",
      alwaysConfirm: true,
      preview: () => api.deleteRoute(state.revision, route.id, { dryRun: true }),
      commit: () => api.deleteRoute(state.revision, route.id),
    });
    if (!confirmed) return;
    await load();
    state.editing = null;
    toast("路由已删除。");
    draw();
  } catch (error) { toast(errorText(error), "error"); }
}

// —— 统一模型（固定的第一个名称）——

// unifiedStatusText 返回默认族的当前去向。
function unifiedStatusText(unified) {
  const primary = unified?.default?.primary;
  if (!primary) return "未启用";
  return primary.key ? `固定 Key · ${primary.key}` : "自动路由";
}

// unifiedEditor 是 unified-model 的计划表编辑器。
//
// 它与普通路由的端点计划区是同一件事（一族指一个模型 + 可选的固定 Key），差别只有两处：
// default 那一族是**必经项**（没有它整个 unified-model 不成立），以及它还能配回退模型与
// 推理强度。
function unifiedEditor() {
  const storedDefault = state.unified?.default || {};
  const storedPrimary = knownModel(storedDefault.primary?.model || "");
  const storedFallback = knownModel(storedDefault.fallback?.model || "");
  // 模型被折成空串时，挂在它上面的固定 Key 也必须一起丢掉：Key 是模型级的，
  // 留着它只会让保存继续被拒。
  const primaryModel = storedPrimary || state.models[0]?.id || "";
  let primaryKey = storedPrimary ? storedDefault.primary?.key || "" : "";
  let routing = primaryKey ? "key" : "auto";
  let fallbackModel = storedFallback;
  let fallbackKey = storedFallback ? storedDefault.fallback?.key || "" : "";
  let effort = state.models.find((model) => model.id === primaryModel)?.reasoning_effort || "";

  // families：每个分族就地改的内存状态（模型 + 固定 Key）。
  const families = {};
  for (const family of FAMILIES) {
    const plan = state.unified?.[family.id];
    const model = knownModel(plan?.primary?.model || "");
    families[family.id] = { model, key: model ? plan?.primary?.key || "" : "" };
  }

  // 错误必须画在**当前**这棵 DOM 里：保存失败时会重画整个表单，写进旧节点的提示
  // 早已脱离文档，用户看到的就是「按钮点了没反应」。
  const errorHost = h("div", state.saveError ? notice(state.saveError, "error") : null);
  const formHost = h("div.stack");

  const modelOptions = () => state.models.map((model) => ({ value: model.id, label: model.id }));

  // stagedDefault 返回"编辑中"的 default 计划容器，必要时空造一个。
  //
  // 这张表单在提交前会就地重画（见下面的两条 onChange），重画时 New 出来的编辑器要从
  // state 里读回当前选择，所以默认族的选择必须**先落进 state**；还没有 unified_model
  // 时就在这里占一个空壳——页面此时显示的仍是"未启用"，直到保存成功、服务端真的写出来。
  const stagedDefault = () => {
    if (!state.unified) state.unified = { default: { primary: {} } };
    return state.unified.default;
  };

  // familyPlan 把某个族折成提交体里的那条计划：没选模型 = 该族不配置（null）。
  const familyPlan = (family) => {
    const picked = families[family.id];
    return picked.model ? { primary: { model: picked.model, key: picked.key || null } } : null;
  };

  const drawForm = () => {
    const primaryKeys = enabledKeys(state.models, primaryModel);
    if (routing === "key" && primaryKey && !primaryKeys.includes(primaryKey)) {
      primaryKey = primaryKeys[0] || "";
      if (!primaryKey) routing = "auto";
    }
    const model = state.models.find((item) => item.id === primaryModel);

    const modelSelectNode = modelSelect({
      models: state.models,
      kinds: state.kinds,
      kind: TEXT_FAMILY.kind,
      value: primaryModel,
      disabled: state.saving,
      onChange: (event) => {
        const next = event.target.value;
        // 选中的模型正好是当前回退时，两者互换。
        if (fallbackModel && next === fallbackModel) {
          fallbackModel = primaryModel;
          fallbackKey = routing === "key" ? primaryKey : "";
        }
        stagedDefault().primary = { model: next, key: routing === "key" ? primaryKey : null };
        effort = state.models.find((item) => item.id === next)?.reasoning_effort || "";
        const keys = enabledKeys(state.models, next);
        primaryKey = keys.includes(primaryKey) ? primaryKey : keys[0] || "";
        if (routing === "key" && !keys.length) routing = "auto";
        draw();
      },
    });

    const routingRadios = h("div.inline", {},
      ["auto", "key"].map((value) => h("label.check", {},
        h("input", {
          type: "radio", name: "routing", value, checked: routing === value,
          disabled: state.saving || (value === "key" && !primaryKeys.length),
          onChange: () => {
            routing = value;
            stagedDefault().primary = { model: primaryModel, key: value === "key" ? primaryKey || primaryKeys[0] || null : null };
            primaryKey = value === "key" ? primaryKey || primaryKeys[0] || "" : primaryKey;
            draw();
          },
        }),
        value === "auto" ? "自动路由" : "固定 Key",
      )),
    );

    mount(formHost,
      h("div.form-grid", {},
        h("label.field", h("span", "模型"), modelSelectNode),
        h("label.field", h("span", "推理强度"), select(EFFORTS, {
          value: effort, disabled: state.saving,
          onChange: (event) => { effort = event.target.value; },
        })),
      ),
      h("div.field", h("span", "路由方式"), routingRadios),
      routing === "key"
        ? h("label.field", h("span", "Key"), keySelect({
            models: state.models, modelId: primaryModel, value: primaryKey, disabled: state.saving,
            onChange: (event) => {
              primaryKey = event.target.value;
              state.unified.default.primary = { model: primaryModel, key: primaryKey };
            },
          }))
        : null,
      h("div", { style: { marginTop: "8px" } }, kv([
        ["路由策略", model?.routing_mode || "round_robin"],
        ["推理强度", effort || "默认"],
        ["启用 Key", String(primaryKeys.length)],
        ["别名", (model?.aliases || []).join(", ") || "无"],
      ])),
      h("hr.divider"),
      h("div.form-grid", {},
        h("label.field", h("span", "回退模型"), select([{ value: "", label: "不启用回退" }].concat(modelOptions().filter((option) => option.value !== primaryModel)), {
          value: fallbackModel, disabled: state.saving,
          onChange: (event) => { fallbackModel = event.target.value; fallbackKey = ""; drawForm(); },
        })),
        h("label.field", h("span", "回退 Key"), keySelect({
          models: state.models, modelId: fallbackModel, value: fallbackKey,
          includeAuto: true, disabled: state.saving,
          onChange: (event) => { fallbackKey = event.target.value; },
        })),
      ),
      // 分族计划：每族一行（模型 + Key）。
      // 清空某一族的模型 = 该族不配置（提交体里是 null），请求回落到 default。
      h("p.muted", "上面的默认模型（文本族）服务对话端点；以下各族按入站端点分派，"
        + "没有配置的族会回落到它。这些端点族的请求体对 AMKR 不透明（只替换模型名后原样转发），"
        + "因此所选模型必须绑定到确实提供该端点的上游 Key。"),
      ...FAMILIES.map((family) => {
        const picked = families[family.id];
        return h("div.form-grid", {},
          h("label.field", h("span", family.label), modelSelect({
            models: state.models, kinds: state.kinds, kind: family.kind,
            value: picked.model, emptyLabel: "不配置映射", disabled: state.saving,
            onChange: (event) => {
              picked.model = event.target.value;
              // 换模型即丢掉旧 Key：Key 是模型级的，留着它会以"引用了未配置的模型"被拒。
              picked.key = "";
              drawForm();
            },
          })),
          h("label.field", h("span", family.keyLabel), keySelect({
            models: state.models, modelId: picked.model, value: picked.key,
            includeAuto: true, disabled: state.saving,
            onChange: (event) => { picked.key = event.target.value; },
          })),
        );
      }),
    );

    const validate = () => {
      if (!primaryModel) return "请先选择模型。";
      if (routing === "key" && !primaryKey) return "当前模型没有可用的启用 Key。";
      if (fallbackModel && fallbackModel === primaryModel) return "回退模型不能与主模型相同。";
      // 分族的 Key 是模型级的：只留 Key 不留模型，服务端只会以"引用了未配置的模型"
      // 拒绝，而用户看到的是一个填好的 Key 框——必须在这里就说清楚。
      for (const family of FAMILIES) {
        const picked = families[family.id];
        if (picked.key && !picked.model) return `${family.label}不能只指定 Key 而不指定模型。`;
      }
      return null;
    };

    const actions = h("div.btn-row", {},
      buttonNode(state.saving ? "正在保存" : "保存", {
        disabled: state.saving,
        onClick: async () => {
          const problem = validate();
          if (problem) { render(errorHost, notice(problem, "error")); return; }
          state.saveError = null;
          state.saving = true;
          draw();
          try {
            let revision = state.revision;
            const stored = state.models.find((item) => item.id === primaryModel)?.reasoning_effort || "";
            if ((effort || "") !== (stored || "")) {
              const updated = await api.updateModelEffort(revision, primaryModel, effort || null);
              revision = updated.config_revision || revision;
            }
            const payload = {
              default: {
                primary: { model: primaryModel, key: routing === "key" ? primaryKey : null },
                fallback: fallbackModel ? { model: fallbackModel, key: fallbackKey || null } : null,
              },
            };
            // 七个计划键**每次都写全**：没配置的族是 null（= 清空该族）。这与既有
            // image/embeddings 的写法一致——"这一族不存在"与"这一族是空的"在配置里
            // 本来就是同一件事，而少写一个键在服务端是"这次不动它"，会留下用户以为
            // 已经清掉的旧计划。
            for (const family of FAMILIES) payload[family.id] = familyPlan(family);
            await api.updateUnified(revision, payload);
            // saving 必须在成功路径上复位：它同时控制着下拉/单选的 disabled 与保存
            // 按钮的文案。漏掉这一步，下一次点「编辑」拿到的是一整个禁用、按钮写着
            // 「正在保存」的表单——页面从此再也存不进任何改动，直到刷新浏览器。
            state.saving = false;
            await load();
            state.editing = null;
            toast("统一模型已更新。");
            draw();
          } catch (error) {
            state.saveError = `统一模型操作失败: ${errorText(error)}`;
            state.saving = false;
            draw();
          }
        },
      }),
      buttonNode("取消", { variant: "text", disabled: state.saving, onClick: () => { state.saveError = null; state.editing = null; draw(); } }),
    );

    formHost.append(actions, errorHost);
  };
  drawForm();
  return formHost;
}

// unifiedDetail 渲染 unified-model 的只读视图。
function unifiedDetail() {
  const unified = state.unified;
  const rows = [
    ["文本模型", unified?.default?.primary?.model || "未配置"],
    ["路由方式", unifiedStatusText(unified)],
    ["回退模型", unified?.default?.fallback?.model || "未配置"],
    ["图像模型", unified?.image?.primary?.model || "未配置"],
    ["嵌入模型", unified?.embeddings?.primary?.model || "未配置"],
  ];
  // 新增的四族**只在配置了才出现**：服务端的响应也只返回已配置的计划，给它补一行
  // "未配置"会让人以为这些族占着什么位置。既有两族保持原样（那也是改动前的渲染）。
  for (const family of FAMILIES) {
    if (ESTABLISHED_FAMILY_IDS.includes(family.id)) continue;
    const model = unified?.[family.id]?.primary?.model;
    if (model) rows.push([family.label, model]);
  }
  return h("div.stack", {},
    card(cardHead(UNIFIED_ID,
      badge(unified ? "已启用" : "未启用", unified ? "good" : "muted"),
      buttonNode("编辑", { small: true, variant: "text", onClick: () => { state.saveError = null; state.editing = UNIFIED_ID; draw(); } }),
    ), kv(rows)),
    h("p.muted", "unified-model 是一个按入站端点族分派的固定名称：调用方把 model 写成它，"
      + "AMKR 就按入站路径挑一条计划（/v1/embeddings → 嵌入，/v1/audio/speech → 语音合成，"
      + "以此类推），再按该计划选真实模型与 Key。上面的文本模型（default）是所有族的回落项。"),
    unified
      ? h("div.btn-row", {},
          buttonNode("停用统一模型", {
            variant: "danger",
            onClick: () => confirmDialog({
              title: "停用统一模型",
              message: "停用统一模型后，统一入口将不再接管请求。是否继续？",
              confirmLabel: "停用",
              danger: true,
              onConfirm: async () => {
                try {
                  await api.deleteUnified(state.revision);
                  await load();
                  toast("统一模型已停用。");
                  draw();
                } catch (error) { toast(errorText(error), "error"); }
              },
            }),
          }),
        )
      : null,
  );
}

// —— 添加模型名称 ——

// addModelDialog 新建一个模型名称。
//
// 服务端的写路径不变式是「没有 target 的模型不存在」，因此新建时必须同时绑定第一条目标
// （供应商 Key + 发给它的上游模型名）。建好之后直接进入编辑态，接着配端点计划。
function addModelDialog() {
  const keyOptions = allKeyOptions();
  if (!keyOptions.length) {
    toast("还没有任何供应商 Key。请先在供应商页添加 Key，再回来新建模型。", "error");
    return;
  }
  const nameInput = input({ placeholder: "对外模型名，例如 my-model" });
  const aliasInput = input({ placeholder: "逗号分隔，留空表示无别名" });
  const modeSelect = select(MODES, { value: "" });
  const keySelectNode = select(keyOptions, { value: keyOptions[0].value });
  const upstreamField = input({ placeholder: "上游模型名（留空 = 与模型名相同）", list: ADD_DATALIST_ID });
  const datalist = h("datalist", { id: ADD_DATALIST_ID });
  const errorHost = h("div");
  const refreshUpstreams = () => {
    const [providerID, keyName] = String(keySelectNode.value).split("|");
    const provider = state.providers.find((item) => item.id === providerID);
    const key = (provider?.keys || []).find((item) => item.name === keyName);
    render(datalist, ...keyUpstreams(key || {}).map((name) => h("option", { value: name })));
  };
  keySelectNode.addEventListener("change", refreshUpstreams);
  refreshUpstreams();

  const create = async () => {
    const id = nameInput.value.trim();
    if (!id) { render(errorHost, notice("请填写模型名称。", "error")); return; }
    const [providerID, keyName] = String(keySelectNode.value).split("|");
    const target = {
      provider: providerID,
      key: keyName,
      upstream_model: upstreamField.value.trim() || id,
    };
    state.saving = true;
    let created = false;
    try {
      await api.createRoute(state.revision, id, [target],
        aliasInput.value.split(",").map((item) => item.trim()).filter(Boolean),
        modeSelect.value || null);
      await load();
      created = true;
    } catch (error) {
      render(errorHost, notice(`创建失败: ${errorText(error)}`, "error"));
    }
    // saving 必须在重画**之前**复位：它同时控制着保存按钮的文案与各个下拉的 disabled，
    // 带着它重画出来的是"一进去就卡在保存中"的编辑器。
    state.saving = false;
    if (created) {
      // 建完直接进编辑态：新名称的端点计划通常正是接下来要配的东西。
      state.active = id;
      state.editing = id;
      ref.close();
      toast(`模型 ${id} 已创建。`);
      draw();
    }
  };

  const ref = dialog({
    title: "添加模型名称",
    body: h("div.stack", {},
      h("div.form-grid", {},
        h("label.field", h("span", "模型名称"), nameInput),
        h("label.field", h("span", "别名"), aliasInput),
        h("label.field", h("span", "路由模式"), modeSelect),
        h("label.field", h("span", "供应商 / Key"), keySelectNode),
      ),
      h("p.muted", "第一条目标（必填）：名称必须绑定到至少一个上游 Key 才能存在。"
        + "建好后可以在右侧继续添加目标与端点计划。"),
      upstreamField,
      datalist,
      errorHost,
    ),
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "创建", onClick: () => { void create(); } },
    ],
  });
}

// —— 左栏 ——

// 模型导航：竖向排在详情左侧，与供应商页共用同一套排版（.rail-split / .rail-nav）。
//
// unified-model 固定排第一——它是调用方最常写的那个名字，也是唯一"没有 targets 也能存在"
// 的名称。其余每条路由一行，名称只列路由 ID（别名在详情里）。
//
// 不带图标：供应商那栏的品牌标志是有信息量的（一眼分辨是哪家），而这里每一行都是
// 同一个「路由」图标，重复几十次只是占宽。列只有 180px，留给模型名更有用。
//
// 语义用 nav + aria-current，不用 role="tab"：真正的 tab 需要配套的
// role="tabpanel" 与方向键 roving tabindex，这里没有实现，标成 tab 属于空头承诺。
function railItem(id, name, meta) {
  return h("button.rail-item", {
    type: "button",
    "aria-current": id === state.active ? "true" : null,
    onClick: () => { state.active = id; state.editing = null; state.saveError = null; draw(); },
  },
    h("span.rail-text", {},
      h("span.rail-name", name),
      h("span.rail-meta", meta),
    ),
  );
}

function routeRail() {
  const planCount = state.unified
    ? PLAN_FAMILIES.filter((family) => state.unified[family.id]?.primary?.model).length
    : 0;
  return h("nav.rail-nav", { "aria-label": "模型列表" },
    railItem(UNIFIED_ID, UNIFIED_ID, `统一入口 · ${state.unified ? `${planCount} 个端点计划` : "未启用"}`),
    state.routes.map((route) => railItem(route.id, route.id, `${(route.targets || []).length} 个目标`)),
  );
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "模型路由"),
        h("p.sub", "对外名称、轮询目标与端点计划；unified-model 是固定的第一个名称。")),
      h("div.spacer"),
      buttonNode("添加模型名称", { small: true, variant: "secondary", disabled: state.saving, onClick: addModelDialog }),
      state.revision ? badge(`版本 ${String(state.revision).slice(0, 12)}`, "muted") : null,
    ),
  ];
  if (state.error) children.push(notice(`无法读取或写入模型路由: ${state.error}`, "error"));
  if (state.loading) {
    children.push(card(cardHead("当前配置"), loading("正在读取模型路由与统一模型。")));
    render(host, children);
    return;
  }

  const detail = [];
  if (state.active === UNIFIED_ID) {
    detail.push(state.editing === UNIFIED_ID
      ? card(cardHead(UNIFIED_ID, badge("统一入口", "muted")), unifiedEditor())
      : unifiedDetail());
  } else {
    const route = state.routes.find((item) => item.id === state.active);
    if (route) {
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
    }
  }

  if (!detail.length) {
    children.push(empty("尚未配置模型路由。请先在供应商页添加 Key 并绑定其服务模型，或点右上角「添加模型名称」新建一个。"));
    render(host, children);
    return;
  }

  children.push(h("div.rail-split", {},
    routeRail(),
    h("div.rail-detail", {}, detail),
  ));
  if (!state.models.length) {
    children.push(notice("尚未配置可用模型。请先在供应商页添加 Key 并绑定服务模型。", "warn"));
  }
  render(host, children);
}

export function renderRouting(context) {
  host = h("div.stack");
  state.loading = true;
  state.error = null;
  state.saveError = null;
  draw();
  (async () => {
    try { await load(); state.error = null; } catch (error) { state.error = errorText(error); }
    state.loading = false;
    draw();
  })();
  return host;
}
