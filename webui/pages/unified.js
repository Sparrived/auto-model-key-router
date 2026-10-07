// 统一模型：主/回退/分族映射与推理强度。
//
// `unified-model` 是一个**按入站端点族分派**的伪模型：对话走 default，图像、嵌入、
// 语音合成、语音识别、视频、重排各有一条可选计划；没配的族回落到 default（见
// internal/config 的 UnifiedModelConfig 与 UnifiedPlanNames）。

import { h, mount, errorText } from "../dom.js";
import { api } from "../api.js";
import { UNKNOWN_KIND, kindOf, normalizeModelKinds } from "../model-kinds.js";
import { card, cardHead, notice, badge, empty, loading, render, toast, buttonNode, select, confirmDialog, kv } from "../ui.js";

// FAMILIES 是 default 之外的分族计划；顺序即界面顺序，与服务端
// config.UnifiedPlanNames 去掉 default 之后逐字一致（default 永远在最前）。
//
// kind 只用于给这个族的下拉排序（见 webui/model-kinds.js）；endpoints 是这条计划
// 服务的入站端点族，写在编辑器顶部的说明里——「语音识别」一条计划同时服务
// /v1/audio/transcriptions 与 /v1/audio/translations，这在界面上必须说清楚，
// 否则用户会以为漏配了一条。
const FAMILIES = [
  { id: "image", label: "图像模型", keyLabel: "图像 Key", kind: "image", endpoints: "/v1/images/generations" },
  { id: "embeddings", label: "嵌入模型", keyLabel: "嵌入 Key", kind: "embedding", endpoints: "/v1/embeddings" },
  { id: "speech", label: "语音合成模型", keyLabel: "语音合成 Key", kind: "tts", endpoints: "/v1/audio/speech" },
  { id: "transcriptions", label: "语音识别模型", keyLabel: "语音识别 Key", kind: "stt", endpoints: "/v1/audio/transcriptions 与 /v1/audio/translations" },
  { id: "video", label: "视频模型", keyLabel: "视频 Key", kind: "video", endpoints: "/v1/videos" },
  { id: "rerank", label: "重排模型", keyLabel: "重排 Key", kind: "rerank", endpoints: "/v1/rerank" },
];

// ESTABLISHED_FAMILIES 是本次改动**之前**就有的两族：它们的模型与 Key 是两行固定
// 的只读摘要（未配置时也显示"未配置"），其余族只在配置了才出现。保留这条界线是为了
// 让只用了老三种计划的配置，页面与改动前逐字一致。
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

const state = { unified: null, models: [], revision: null, loading: true, error: null, editing: false, saving: false, saveError: null, kinds: null };

let host = null;

const enabledKeys = (modelId) =>
  (state.models.find((model) => model.id === modelId)?.keys || []).filter((key) => key.enabled).map((key) => key.name);

async function load() {
  const [unified, models] = await Promise.all([api.unified(), api.models()]);
  state.unified = unified.unified_model || null;
  state.revision = unified.config_revision ?? models.config_revision;
  state.models = models.models || [];
  state.kinds = await loadModelKinds();
}

// loadModelKinds 读一次模型类型，用来给各分族下拉排序（文本族在前、未分类在后）。
//
// 与模型清单**分开失败**：类型只影响下拉里的排序，问不到就按配置顺序平铺，绝不能让
// 整页读不出来——/ui 不可达时用户连主模型都选不了，那比"下拉没排序"糟得多。
async function loadModelKinds() {
  try {
    const data = await api.modelKinds(state.models.map((model) => model.id));
    return normalizeModelKinds(data);
  } catch {
    return null;
  }
}

function statusText(unified) {
  const primary = unified?.default?.primary;
  if (!primary) return "未启用";
  return primary.key ? `固定 Key · ${primary.key}` : "自动路由";
}

// knownModel 把「已不存在的模型名」折成空串。
//
// state.unified 与 state.models 是两次独立取数的结果，中间可能刚好有供应商被删掉：
// 那时 unified 仍指向一个已随供应商一起消失的模型。此时必须把它当成「没选」——
// 否则下拉里根本没有这个选项、界面显示空值，而保存时却仍会把它发给服务端，被
// 「引用了未配置的模型」顶回来（页面看起来就是保存无效）。
function knownModel(id) {
  return state.models.some((model) => model.id === id) ? id : "";
}

function editor() {
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

  // families：每个分族就地改的内存状态（模型 + 固定 Key），与上面 default 的三个变量
  // 同一套写法。knownModel 折空串的规则对每个族都适用：引用了已消失的模型时当作
  // "没选"，否则下拉显示空值、保存却仍把那个名字发出去，被服务端顶回来。
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
  const keyOptions = (modelId, includeAuto) => {
    const options = includeAuto ? [{ value: "", label: "自动路由" }] : [];
    return options.concat(enabledKeys(modelId).map((name) => ({ value: name, label: name })));
  };

  // modelGroups 把一个族的候选模型按类型分成「本族 / 其它类型 / 未分类」三组。
  //
  // 三条不变量：
  //   1. **任何模型都不会被筛掉**：判据只是排序与分组。自定义模型名在服务端没有任何
  //      档案（未分类），把它们挡在下拉之外，用户就再也选不回自己的模型；
  //   2. 类型读数取不到时平铺：给一个"全都是未分类"的下拉加分组标题，只会让人以为
  //      这些模型有问题；
  //   3. 一个已分类的模型都没有时同样平铺（同上一条）。
  const modelGroups = (kind) => {
    const names = () => state.models.map((model) => model.id);
    const classified = state.kinds
      ? names().filter((name) => kindOf(state.kinds, name) !== UNKNOWN_KIND)
      : [];
    if (!classified.length) return [{ label: "", names: names() }];
    const buckets = [
      { label: "", names: [] },
      { label: "其它类型", names: [] },
      { label: "未分类", names: [] },
    ];
    for (const name of names()) {
      const actual = kindOf(state.kinds, name);
      buckets[actual === kind ? 0 : actual === UNKNOWN_KIND ? 2 : 1].names.push(name);
    }
    return buckets.filter((bucket) => bucket.names.length);
  };

  // modelSelectFor 生成一个按类型排序的模型下拉（optgroup 分组，见 modelGroups）。
  //
  // 不用 ui.js 的 select()：它只吃平铺的 {value,label}，表达不出分组。select 的取值、
  // disabled 与 onChange 语义与它完全一致。
  //
  // emptyLabel 非空时在最前面插一个空值选项（分族的「不配置映射」）；它**不在**任何
  // optgroup 里，否则"清空这一族"会长得像某一类模型。
  const modelSelectFor = (kind, value, onChange, emptyLabel = "") => {
    const node = h("select.select", { value, disabled: state.saving, onChange });
    if (emptyLabel) node.append(h("option", { value: "", selected: !value }, emptyLabel));
    for (const group of modelGroups(kind)) {
      const options = group.names.map((name) => h("option", {
        value: name,
        selected: String(value ?? "") === name,
      }, name));
      node.append(group.label ? h("optgroup", { label: group.label }, options) : options);
    }
    return node;
  };

  // familyPlan 把某个族折成提交体里的那条计划：没选模型 = null（清空该族）。
  //
  // 与既有 image/embeddings 的写法一字不差：Key 空串折成 null（= 自动路由）。
  const familyPlan = (family) => {
    const picked = families[family.id];
    return picked.model ? { primary: { model: picked.model, key: picked.key || null } } : null;
  };

  const drawForm = () => {
    const primaryKeys = enabledKeys(primaryModel);
    if (routing === "key" && primaryKey && !primaryKeys.includes(primaryKey)) {
      primaryKey = primaryKeys[0] || "";
      if (!primaryKey) routing = "auto";
    }
    const model = state.models.find((item) => item.id === primaryModel);

    const modelSelect = modelSelectFor("text", primaryModel, (event) => {
      const next = event.target.value;
      // 选中的模型正好是当前回退时，两者互换。
      if (fallbackModel && next === fallbackModel) {
        fallbackModel = primaryModel;
        fallbackKey = routing === "key" ? primaryKey : "";
      }
      state.unified = state.unified || { default: { primary: {} }, image: null };
      state.unified.default.primary = { model: next, key: routing === "key" ? primaryKey : null };
      effort = state.models.find((item) => item.id === next)?.reasoning_effort || "";
      const keys = enabledKeys(next);
      primaryKey = keys.includes(primaryKey) ? primaryKey : keys[0] || "";
      if (routing === "key" && !keys.length) routing = "auto";
      render(host, editor());
    });

    const routingRadios = h("div.inline", {},
      ["auto", "key"].map((value) => h("label.check", {},
        h("input", {
          type: "radio", name: "routing", value, checked: routing === value, disabled: state.saving || (value === "key" && !primaryKeys.length),
          onChange: () => { routing = value; state.unified = state.unified || { default: { primary: {} }, image: null }; state.unified.default.primary = { model: primaryModel, key: value === "key" ? primaryKey || primaryKeys[0] || null : null }; primaryKey = value === "key" ? primaryKey || primaryKeys[0] || "" : primaryKey; render(host, editor()); },
        }),
        value === "auto" ? "自动路由" : "固定 Key",
      )),
    );

    mount(formHost,
      h("div.form-grid", {},
        h("label.field", h("span", "模型"), modelSelect),
        h("label.field", h("span", "推理强度"), select(EFFORTS, {
          value: effort, disabled: state.saving,
          onChange: (event) => { effort = event.target.value; },
        })),
      ),
      h("div.field", h("span", "路由方式"), routingRadios),
      routing === "key"
        ? h("label.field", h("span", "Key"), select(keyOptions(primaryModel, false), {
            value: primaryKey, disabled: state.saving,
            onChange: (event) => { primaryKey = event.target.value; state.unified.default.primary = { model: primaryModel, key: primaryKey }; },
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
        h("label.field", h("span", "回退 Key"), select(keyOptions(fallbackModel, true), {
          value: fallbackKey, disabled: state.saving || !fallbackModel,
          onChange: (event) => { fallbackKey = event.target.value; },
        })),
      ),
      // 分族计划：每族一行（模型 + Key），与既有 image/embeddings 两行完全同构。
      // 清空某一族的模型 = 该族不配置（提交体里是 null），请求回落到 default。
      h("p.muted", "上面的默认模型（文本族）服务对话端点；以下各族按入站端点分派，"
        + "没有配置的族会回落到它。这些端点族的请求体对 AMKR 不透明（只替换模型名后原样转发），"
        + "因此所选模型必须绑定到确实提供该端点的上游 Key。"),
      ...FAMILIES.map((family) => {
        const picked = families[family.id];
        return h("div.form-grid", {},
          h("label.field", h("span", family.label), modelSelectFor(family.kind, picked.model, (event) => {
            picked.model = event.target.value;
            // 换模型即丢掉旧 Key：Key 是模型级的，留着它会以"引用了未配置的模型"被拒。
            picked.key = "";
            drawForm();
          }, "不配置映射")),
          h("label.field", h("span", family.keyLabel), select(keyOptions(picked.model, true), {
            value: picked.key, disabled: state.saving || !picked.model,
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
          render(host, editor());
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
            state.editing = false;
            toast("统一模型已更新。");
            draw();
          } catch (error) {
            state.saveError = `统一模型操作失败: ${errorText(error)}`;
            state.saving = false;
            render(host, editor());
          }
        },
      }),
      buttonNode("取消", { variant: "text", disabled: state.saving, onClick: () => { state.saveError = null; state.editing = false; draw(); } }),
    );

    formHost.append(actions, errorHost);
  };
  drawForm();
  return formHost;
}

// renderUnified 每次进入页面都重新取数。
//
// 不能只在首次进入时取：模型与 config_revision 会被**别的页面**改掉（例如在供应商页
// 删掉一个供应商会连同它的模型一起删掉），而缓存下来的旧模型名既选不中、又会被服务端
// 以「引用了未配置的模型」拒绝，旧版本号更会让每次保存都撞到 409。统一模型页依赖
// 供应商/模型页的结果，因此必须与「访问密钥」页一样每次进入都重新读取。
export function renderUnified(context) {
  host = h("div.stack");
  state.loading = true;
  state.error = null;
  draw();
  (async () => {
    try { await load(); state.error = null; } catch (error) { state.error = errorText(error); }
    state.loading = false;
    draw();
  })();
  return host;
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "统一模型"), h("p.sub", "请求统一入口时使用的模型、Key 与回退顺序。")),
      h("div.spacer"),
      state.unified ? badge("已启用", "good") : badge("未启用", "muted"),
      state.revision ? badge(`版本 ${String(state.revision).slice(0, 12)}`, "muted") : null,
    ),
  ];
  if (state.error) children.push(notice(`读取失败: ${state.error}`, "error"));
  if (state.loading) {
    children.push(card(cardHead("当前配置"), loading("正在读取统一模型配置。")));
    render(host, children);
    return;
  }
  if (!state.models.length) {
    children.push(empty("尚未配置可用模型。"));
    render(host, children);
    return;
  }
  if (state.editing) {
    children.push(card(cardHead("当前配置"), editor()));
  } else {
    const unified = state.unified;
    const rows = [
      ["文本模型", unified?.default?.primary?.model || "未配置"],
      ["路由方式", statusText(unified)],
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
    children.push(card(
      cardHead("当前配置", buttonNode("编辑", { small: true, variant: "text", onClick: () => { state.saveError = null; state.editing = true; draw(); } })),
      kv(rows),
    ));
    if (unified) {
      children.push(h("div.btn-row", {},
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
      ));
    }
  }
  render(host, children);
}
