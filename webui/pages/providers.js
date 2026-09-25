// 供应商 / Key：CRUD、能力探测（单个 + 批量）、Key 的模型绑定。

import { h, errorText } from "../dom.js";
import { api } from "../api.js";
import { PROVIDER_PRESETS, providerIcon, brandIcon } from "../brand-icons.js";
import { card, cardHead, notice, badge, empty, loading, table, render, toast, buttonNode, toggle, input, field, dialog, confirmDialog, kv } from "../ui.js";

const ROUTE_MODES = [
  { id: "openai", label: "OpenAI 路径" },
  { id: "anthropic", label: "Anthropic 路径" },
  { id: "responses", label: "Responses 路径" },
  { id: "images", label: "Images 路径" },
  { id: "embeddings", label: "Embeddings 路径" },
];

const state = {
  providers: [],
  // boundModels：`${provider}|${key}` → 该 Key 正在服务的模型 ID（升序）。
  boundModels: new Map(),
  revision: null,
  active: "",
  loading: true,
  error: null,
  expanded: null,
  editing: null,
  keyEditing: null,
  modelEditor: null,
  probe: null,
  probeBusy: false,
  probeError: null,
  keyProbeState: {},
};

let host = null;
let xtxRef = null;
let probeTimer = null;
let probeGeneration = 0;

export async function reloadProviders() {
  // routes 与 providers 一起取：Key 行下方要显示的是「这个 Key 正在服务哪些模型」，
  // 而那是模型 targets 里的绑定关系，只读 providers 看不到。
  const [data, routes] = await Promise.all([api.providers(), api.routes()]);
  state.providers = data.providers || [];
  state.boundModels = boundModelsByKey(routes.routes || []);
  state.revision = data.config_revision;
  if (!state.providers.some((provider) => provider.id === state.active)) {
    state.active = state.providers[0]?.id || "";
  }
  return data;
}

// boundModelsByKey 把路由表反过来索引成 `供应商|Key` → 它服务的模型 ID（升序）。
function boundModelsByKey(routes) {
  const index = new Map();
  for (const route of routes) {
    for (const target of route.targets || []) {
      const id = `${target.provider}|${target.key}`;
      if (!index.has(id)) index.set(id, new Set());
      index.get(id).add(route.id);
    }
  }
  for (const [id, models] of index) index.set(id, [...models].sort());
  return index;
}

function activeProvider() {
  return state.providers.find((provider) => provider.id === state.active) || null;
}

function capabilityBadge(key) {
  const capabilities = key.capabilities;
  if (!capabilities) return badge("未探测", "muted");
  const models = capabilities.models || [];
  if (models.length) return badge(`${models.length} 个模型`, "good");
  const errors = Object.values(capabilities.errors || {}).filter(Boolean);
  if (errors.length) return badge("探测有错误", "warn");
  return badge("未发现模型", "muted");
}

// capabilityDetail 渲染 Key 名称下方的那行模型：只列**这个 Key 正在服务**的模型。
//
// 不列探测到的全部模型：上游 /v1/models 常常一次返回几百个，那串字既读不完也不代表
// 这个 Key 真的对外提供它们——真正生效的是模型 targets 里的绑定。
// 探测错误仍然照常显示，否则「这个 Key 探测炸了」会因为没绑定模型而看不出来。
function capabilityDetail(provider, key) {
  const models = state.boundModels.get(`${provider.id}|${key.name}`) || [];
  const errors = Object.entries(key.capabilities?.errors || {}).filter(([, message]) => message);
  if (!models.length && !errors.length) return null;
  return h("div.stack.tight",
    models.length ? h("div.mono", models.join(", ")) : null,
    errors.length ? h("div.muted", errors.map(([mode, message]) => `${mode}: ${message}`).join(" · ")) : null,
  );
}

// —— 添加供应商 ——
//
// 常见供应商预置：点一下把名称与地址填进表单，用户仍可改。预置只填不提交，
// 所以万一地址不对，用户也看得见（就摆在输入框里），不会静默生效。
function presetPicker({ onPick, currentUrl }) {
  const nodes = [];
  // 高亮已匹配的那一个：说明"当前填的就是这家"，也避免用户重复点。
  const mark = () => {
    const current = String(currentUrl() || "").replace(/\/+$/, "").toLowerCase();
    PROVIDER_PRESETS.forEach((preset, index) => {
      nodes[index].setAttribute("aria-pressed", String(preset.baseUrl.toLowerCase() === current));
    });
  };
  for (const preset of PROVIDER_PRESETS) {
    nodes.push(h("button.preset", {
      type: "button",
      title: `${preset.label} · ${preset.baseUrl}`,
      "aria-label": `使用 ${preset.label} 预置`,
      onClick: () => { onPick(preset); mark(); },
    },
      brandIcon(preset.brand, { size: 24 }),
      h("span.preset-label", preset.label),
    ));
  }
  mark();
  return h("div.preset-grid", { role: "group", "aria-label": "常见供应商" }, nodes);
}

function openCreateProvider() {
  const nameInput = input({ placeholder: "例如 openai", required: true });
  const urlInput = input({ type: "url", placeholder: "https://api.example.com", required: true });
  const errorHost = h("div");
  const presets = presetPicker({
    currentUrl: () => urlInput.value,
    onPick: (preset) => {
      nameInput.value = preset.id;
      urlInput.value = preset.baseUrl;
    },
  });
  let ref = null;
  const submit = async () => {
    const id = nameInput.value.trim();
    const baseUrl = urlInput.value.trim();
    if (!id || !baseUrl) return;
    try {
      await api.createProvider(state.revision, id, baseUrl);
      await reloadProviders();
      state.active = id;
      ref.close();
      toast("供应商已添加。");
      draw();
    } catch (error) {
      render(errorHost, notice(`添加失败: ${errorText(error)}`, "error"));
      if (error.status === 409) reloadProviders().then(draw);
    }
  };
  const body = h("div.stack", {},
    // 不用 field()：它渲染的是 <label>，而一个 label 只能标注一个控件，
    // 里面塞一排按钮会让点按语义变得含混（辅助技术也读不出这是什么）。
    h("div.field", {}, h("span", "常见供应商"), presets),
    field("名称", nameInput),
    field("地址", urlInput),
    errorHost,
  );
  ref = dialog({
    title: "添加供应商",
    body,
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "添加供应商", onClick: submit },
    ],
  });
}

// —— 编辑供应商 ——
function providerForm(provider) {
  const nameInput = input({ value: provider.id, required: true });
  const urlInput = input({ value: provider.base_url, required: true });
  const routeInputs = {};
  const errorHost = h("div");
  const save = async () => {
    const routes = {};
    let routesChanged = false;
    for (const mode of ROUTE_MODES) {
      const value = routeInputs[mode.id].value.trim();
      const original = provider.routes?.[mode.id] || "";
      if (value !== original) routesChanged = true;
      if (value) routes[mode.id] = value;
      else if (original) routes[mode.id] = null;
    }
    try {
      const nextId = nameInput.value.trim();
      await api.updateProvider(state.revision, provider.id, nextId, urlInput.value.trim(), routesChanged ? routes : provider.routes || {});
      await reloadProviders();
      state.active = nextId;
      state.editing = null;
      toast("配置已更新。");
      draw();
    } catch (error) {
      render(errorHost, notice(`操作失败: ${errorText(error)}`, "error"));
      if (error.status === 409) reloadProviders();
    }
  };
  return h("div.stack", {},
    h("div.form-grid", {},
      field("供应商名称", nameInput),
      field("供应商地址", urlInput),
    ),
    h("details", {}, h("summary.muted", "高级路径设置"),
      h("div.form-grid", { style: { marginTop: "8px" } },
        ROUTE_MODES.map((mode) => {
          const control = input({ value: provider.routes?.[mode.id] || "", placeholder: "留空使用默认路径" });
          routeInputs[mode.id] = control;
          return field(mode.label, control);
        }),
      ),
    ),
    errorHost,
    h("div.btn-row", {},
      buttonNode("保存", { onClick: save }),
      buttonNode("取消", { variant: "text", onClick: () => { state.editing = null; draw(); } }),
      buttonNode("删除供应商", {
        variant: "danger",
        onClick: () => confirmDialog({
          title: "删除供应商",
          message: `删除供应商 ${provider.id} 及其全部 Key？`,
          confirmLabel: "删除",
          danger: true,
          onConfirm: async () => {
            try {
              await api.deleteProvider(state.revision, provider.id);
              await reloadProviders();
              toast("供应商已删除。");
              draw();
            } catch (error) { toast(errorText(error), "error"); }
          },
        }),
      }),
    ),
  );
}

// —— 添加 Key（保存 + 自动探测两步）——
function openCreateKey(provider) {
  const nameInput = input({ placeholder: "例如 primary", required: true });
  const secretInput = input({ type: "password", required: true, autocomplete: "new-password" });
  const statusHost = h("div");
  const errorHost = h("div");
  let ref = null;

  const setStatus = (step, message) => {
    render(statusHost, h("div.inline", {},
      step === "done" ? badge("完成", "good") : h("span.spinner"),
      h("span.muted", message),
    ));
  };

  const submit = async () => {
    const name = nameInput.value.trim();
    const secret = secretInput.value;
    if (!name || !secret) return;
    setStatus("saving", "正在保存 Key…");
    let revision = state.revision;
    try {
      await api.createProviderKey(revision, provider.id, name, secret);
      revision = (await reloadProviders()).config_revision;
    } catch (error) {
      render(errorHost, notice("Key 保存失败。", "error"));
      setStatus("error", errorText(error));
      return;
    }
    setStatus("probing", "正在探测可用模型…");
    try {
      const result = await api.probeKey(revision, provider.id, name);
      revision = result.config_revision || revision;
      await reloadProviders();
    } catch (error) {
      // 保存成功但探测失败：不回滚 Key，保持表单可编辑。
      await reloadProviders();
      setStatus("error", `Key 已保存，但自动探测失败: ${errorText(error)}`);
      draw();
      return;
    }
    setStatus("done", "探测完成");
    await new Promise((resolve) => setTimeout(resolve, 450));
    ref.close();
    toast(`Key ${name} 已添加。`);
    // 加完立刻展开这个 Key 的「服务模型」编辑器（与点「管理模型」同一条路径）。
    //
    // 这里曾经是裸的 `{ provider, key }`：modelEditor() 会直接展开 models / selected，
    // 缺字段就在重画时抛 TypeError，而 draw() 抛错的后果是**整页一个字都不更新**——
    // Key 其实已经存进服务端了，界面却还停在加之前的样子，只能手动刷新。
    state.modelEditor = modelEditorState(provider.id, name);
    draw();
    loadKeyModels(provider.id, name);
  };

  const body = h("div.stack", {},
    field("Key 名称", nameInput),
    field("API Key", secretInput),
    h("p.muted", "保存后会自动探测此 Key 的可用模型与路由能力。密钥只写入服务端，不会被回显。"),
    statusHost,
    errorHost,
  );
  ref = dialog({
    title: "添加 Key",
    body,
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "添加 Key", onClick: submit },
    ],
  });
}

function keyRow(provider, key) {
  if (state.keyEditing === key.name) {
    const nameInput = input({ value: key.name, required: true });
    const secretInput = input({ type: "password", placeholder: "留空保持不变", autocomplete: "new-password" });
    const errorHost = h("div");
    return h("tr", {}, h("td", { colspan: "5" },
      h("div.stack", {},
        h("div.form-grid", {}, field("Key 名称", nameInput), field("替换 API Key", secretInput)),
        errorHost,
        h("div.btn-row", {},
          buttonNode("保存", {
            onClick: async () => {
              const patch = { name: nameInput.value.trim(), enabled: key.enabled };
              if (secretInput.value.trim()) patch.api_key = secretInput.value.trim();
              try {
                await api.updateProviderKey(state.revision, provider.id, key.name, patch);
                await reloadProviders();
                state.keyEditing = null;
                toast("Key 已更新。");
                draw();
              } catch (error) {
                render(errorHost, notice(`操作失败: ${errorText(error)}`, "error"));
                if (error.status === 409) reloadProviders();
              }
            },
          }),
          buttonNode("取消", { variant: "text", onClick: () => { state.keyEditing = null; draw(); } }),
        ),
      ),
    ));
  }

  const probeState = state.keyProbeState[key.name] || "idle";
  const probeLabel = probeState === "pending" ? "探测中" : probeState === "success" ? "已探测" : "探测";
  return h("tr", {},
    h("td", {},
      h("div.stack.tight", {},
        h("strong", key.name),
        capabilityDetail(provider, key),
      ),
    ),
    h("td", {}, h("div.stack.tight", {}, capabilityBadge(key),
      h("code.mono", key.api_key_fingerprint || "-"))),
    h("td", {}, h("div.btn-row", {},
      toggle(key.enabled ? "已启用" : "已停用", key.enabled, () => patchKey(provider, key, { enabled: !key.enabled })),
    )),
    h("td", {}, h("div.btn-row", {},
      buttonNode(probeLabel, {
        small: true, variant: "secondary",
        disabled: probeState === "pending" || probeState === "success",
        onClick: () => probeOne(provider, key),
      }),
      buttonNode(
        state.modelEditor && state.modelEditor.key === key.name && state.modelEditor.provider === provider.id ? "收起" : "管理模型",
        { small: true, variant: "text", onClick: () => toggleModelEditor(provider, key) },
      ),
      buttonNode("编辑", { small: true, variant: "text", onClick: () => { state.keyEditing = key.name; draw(); } }),
      buttonNode("删除", {
        small: true, variant: "text",
        onClick: () => confirmDialog({
          title: "删除 Key",
          message: `删除 Key ${key.name}？`,
          confirmLabel: "删除",
          danger: true,
          onConfirm: async () => {
            try {
              await api.deleteProviderKey(state.revision, provider.id, key.name);
              await reloadProviders();
              toast("Key 已删除。");
              draw();
            } catch (error) { toast(errorText(error), "error"); }
          },
        }),
      }),
    )),
  );
}

async function patchKey(provider, key, patch) {
  try {
    await api.updateProviderKey(state.revision, provider.id, key.name, {
      enabled: key.enabled, ...patch,
    });
    await reloadProviders();
    draw();
  } catch (error) {
    toast(errorText(error), "error");
    if (error.status === 409) reloadProviders().then(draw);
  }
}

async function probeOne(provider, key) {
  state.keyProbeState[key.name] = "pending";
  draw();
  try {
    await api.probeKey(state.revision, provider.id, key.name);
    await reloadProviders();
    state.keyProbeState[key.name] = "success";
    const fresh = activeProvider()?.keys.find((item) => item.name === key.name) || key;
    const count = fresh.capabilities?.models?.length || 0;
    toast(count ? `Key ${key.name} 探测成功，发现 ${count} 个模型。` : `Key ${key.name} 探测完成（未发现模型）。`);
    draw();
    setTimeout(() => {
      if (state.keyProbeState[key.name] === "success") { state.keyProbeState[key.name] = "idle"; draw(); }
    }, 2400);
  } catch (error) {
    state.keyProbeState[key.name] = "error";
    toast(`Key ${key.name} 探测失败: ${errorText(error)}`, "error");
    draw();
  }
}

// —— Key 的模型绑定 ——
// modelEditorState 造一份完整的「Key 服务模型」编辑器状态。
//
// 字段必须一次给全：modelEditor() 会直接展开 models / selected，缺任何一个都是在重画时
// 抛 TypeError，而 draw() 抛错的后果是整页不更新（新增 Key 之后界面不刷新就是这个原因）。
// 建状态的入口只留这一个，避免以后再有人漏掉字段。
function modelEditorState(providerId, keyName) {
  return { provider: providerId, key: keyName, loading: true, models: [], selected: new Set(), error: null };
}

function toggleModelEditor(provider, key) {
  const same = state.modelEditor && state.modelEditor.provider === provider.id && state.modelEditor.key === key.name;
  state.modelEditor = same ? null : modelEditorState(provider.id, key.name);
  draw();
  if (!same) loadKeyModels(provider.id, key.name);
}

async function loadKeyModels(providerId, keyName) {
  const editor = state.modelEditor;
  if (!editor) return;
  try {
    const data = await api.keyModels(providerId, keyName);
    const bound = [...new Set((data.models || []).map((m) => String(m).trim()).filter(Boolean))].sort();
    editor.models = bound;
    editor.selected = new Set(bound);
    editor.loading = false;
  } catch (error) {
    editor.loading = false;
    editor.error = errorText(error);
  }
  draw();
}

function modelEditor(provider, key) {
  const editor = state.modelEditor;
  if (!editor || editor.provider !== provider.id || editor.key !== key.name) return null;
  if (editor.loading) return h("div", { style: { marginTop: "16px" } }, loading("正在读取 Key 绑定…"));

  const discovered = new Set(key.capabilities?.models || []);
  const all = [...new Set([...editor.models, ...discovered, ...editor.selected])].sort();
  const chipHost = h("div.chips");
  const errorHost = h("div", editor.error ? notice(`读取 Key 绑定失败: ${editor.error}`, "warn") : null);

  const drawChips = () => {
    render(chipHost, ...[...new Set([...all, ...editor.selected])].sort().map((model) => {
      const selected = editor.selected.has(model);
      const missing = selected && !discovered.has(model) && !editor.models.includes(model);
      return h(`button.chip${missing ? ".probe-missing" : ""}`, {
        type: "button",
        "aria-pressed": String(selected),
        "aria-label": `${selected ? "关闭" : "打开"}模型 ${model}`,
        title: missing ? "黄色卡片表示已启用但当前探测未发现" : null,
        onClick: () => {
          if (editor.selected.has(model)) editor.selected.delete(model);
          else editor.selected.add(model);
          drawChips();
        },
      }, model);
    }),
    h("button.chip", {
      type: "button",
      onClick: () => {
        const custom = input({ placeholder: "自定义模型名称" });
        const ref = dialog({
          title: "添加自定义模型",
          body: field("模型名称", custom),
          actions: [
            { label: "取消", variant: "text", onClick: () => ref.close() },
            { label: "添加", onClick: () => {
              const value = custom.value.trim();
              if (!value) return;
              editor.selected.add(value);
              if (!all.includes(value)) all.push(value);
              ref.close();
              drawChips();
            }},
          ],
        });
      },
    }, "+ 自定义"));
  };
  drawChips();

  return h("div", { style: { marginTop: "16px", padding: "16px", background: "#fafafa", borderRadius: "4px" } },
    h("div.card-head", h("h4", `Key ${key.name} 的服务模型`), h("span.muted", `${editor.selected.size} 个已选`)),
    h("p.muted", "只显示该 Key 对外提供的模型；黄色卡片表示已启用但当前探测未发现。"),
    chipHost,
    errorHost,
    h("div.btn-row", { style: { marginTop: "16px" } },
      buttonNode("保存模型", {
        onClick: async () => {
          try {
            await api.setKeyModels(state.revision, provider.id, key.name, [...editor.selected]);
            await reloadProviders();
            state.modelEditor = null;
            toast(`Key ${key.name} 已保存 ${editor.selected.size} 个模型。`);
            draw();
          } catch (error) {
            render(errorHost, notice(`保存失败: ${errorText(error)}`, "error"));
            if (error.status === 409) reloadProviders();
          }
        },
      }),
      buttonNode("取消", { variant: "text", onClick: () => { state.modelEditor = null; draw(); } }),
    ),
  );
}

// —— 批量探测 ——
function probePanel(provider) {
  const timeoutInput = input({ type: "number", min: "0.1", max: "120", step: "0.1", value: "15", "aria-label": "探测超时（秒）" });
  const messageHost = h("div");
  const probe = state.probe;
  const statusLabels = { pending: "排队中", running: "正在探测", complete: "已完成", failed: "失败", cancelled: "已取消" };
  const statusTone = probe?.status === "complete" ? "good" : probe?.status === "failed" ? "bad" : probe?.status === "cancelled" ? "warn" : "muted";

  const resultTable = probe && probe.results?.length
    ? table([
        { label: "Key", render: (row) => row.key_name },
        { label: "端点", render: (row) => h("span.mono", sanitizeEndpoint(row.endpoint)) },
        { label: "模型", render: (row) => (row.models || []).join(", ") || "未发现模型" },
        { label: "延迟", numeric: true, render: (row) => (row.latency_ms === null || row.latency_ms === undefined ? "延迟未知" : `${row.latency_ms} ms`) },
        { label: "错误", render: (row) => row.error || "-" },
      ], probe.results)
    : null;

  return h("div.card", {},
    cardHead("批量探测",
      probe ? badge(statusLabels[probe.status] || probe.status, statusTone) : badge("未运行", "muted"),
      state.probeBusy ? h("span.spinner") : null,
    ),
    h("div.inline", {},
      h("label.field", { style: { maxWidth: "200px" } }, h("span", "探测超时（秒）"), timeoutInput),
      buttonNode("探测全部 Key", { disabled: state.probeBusy || !(provider.keys || []).length, onClick: () => startProbe(provider, timeoutInput, messageHost) }),
      probe && state.probeBusy ? buttonNode("取消探测", { variant: "danger", onClick: () => cancelProbe() }) : null,
    ),
    h("p.muted", "批量探测只做展示，不会写回 Key 的能力缓存。"),
    state.probeError ? h("div", { style: { marginTop: "16px" } }, notice(`探测失败: ${state.probeError}`, "error")) : null,
    messageHost,
    resultTable ? h("div", { style: { marginTop: "16px" } }, resultTable) : null,
  );
}

async function startProbe(provider, timeoutInput, messageHost) {
  const timeout = Number(timeoutInput.value);
  if (!Number.isFinite(timeout) || timeout <= 0 || timeout > 120) {
    render(messageHost, notice("探测超时必须在 0 到 120 秒之间。", "error"));
    return;
  }
  clearTimeout(probeTimer);
  probeGeneration += 1;
  const generation = probeGeneration;
  state.probeBusy = true;
  state.probeError = null;
  state.probe = { probe_id: null, status: "pending", results: [] };
  draw();
  try {
    const created = await api.startProbe(provider.id, [], timeout);
    if (generation !== probeGeneration) { api.cancelProbe(created.probe_id).catch(() => {}); return; }
    state.probe = { ...state.probe, ...created };
    pollProbe(created.probe_id, generation);
  } catch (error) {
    state.probeBusy = false;
    state.probeError = errorText(error);
    draw();
  }
}

async function pollProbe(probeId, generation) {
  if (generation !== probeGeneration) return;
  try {
    const data = await api.getProbe(probeId);
    if (generation !== probeGeneration) return;
    state.probe = { ...state.probe, ...data };
    if (["complete", "failed", "cancelled"].includes(data.status)) { state.probeBusy = false; draw(); return; }
    draw();
    probeTimer = setTimeout(() => pollProbe(probeId, generation), 750);
  } catch (error) {
    // 轮询失败即停止，但不自动取消服务端探测。
    state.probeError = errorText(error);
    state.probeBusy = false;
    draw();
  }
}

async function cancelProbe() {
  if (!state.probe?.probe_id) return;
  try {
    const data = await api.cancelProbe(state.probe.probe_id);
    state.probe = { ...state.probe, ...data };
    if (["complete", "failed", "cancelled"].includes(data.status)) {
      state.probeBusy = false;
      clearTimeout(probeTimer);
    }
    draw();
  } catch (error) {
    state.probeError = errorText(error);
    draw();
  }
}

function sanitizeEndpoint(endpoint) {
  if (!endpoint) return "端点地址不可用";
  try {
    const url = new URL(endpoint);
    return `${url.origin}${url.pathname}`;
  } catch {
    return "端点地址不可用";
  }
}

export function renderProviders(context) {
  xtxRef = context;
  host = h("div.stack");
  if (state.loading) {
    render(host, h("div.page-head", h("h1", "供应商")), loading("正在读取供应商配置。"));
    (async () => {
      try {
        await reloadProviders();
        state.error = null;
      } catch (error) {
        state.error = errorText(error);
      }
      state.loading = false;
      draw();
    })();
    return host;
  }
  draw();
  return host;
}

// 供应商导航：竖向排在详情左侧，与模型路由页共用同一套排版（.rail-split / .rail-nav）。
//
// 为什么竖排而不是沿用顶部的横向标签页：供应商数量随使用增长，十几家很常见。
// 横排标签页要么换行、要么横向滚动，把页头撑成两三行；竖排只占一列，再多也只是
// 这一列变长，右侧详情的位置始终不变。
//
// 语义用 nav + aria-current，不用 role="tab"：真正的 tab 需要配套的
// role="tabpanel" 与方向键 roving tabindex，这里没有实现，标成 tab 属于空头承诺。
function providerRail() {
  return h("nav.rail-nav", { "aria-label": "供应商列表" },
    state.providers.map((item) => {
      const count = (item.keys || []).length;
      return h("button.rail-item", {
        type: "button",
        "aria-current": item.id === state.active ? "true" : null,
        onClick: () => {
          state.active = item.id;
          state.editing = null;
          state.modelEditor = null;
          draw();
        },
      },
        providerIcon(item.id, item.base_url, { size: 20, class: "rail-icon" }),
        h("span.rail-text", {},
          h("span.rail-name", item.id),
          h("span.rail-meta", count ? `${count} Key` : "无 Key"),
        ),
      );
    }),
  );
}

function draw() {
  if (!host) return;
  const provider = activeProvider();
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "供应商"), h("p.sub", "管理上游供应商、Key 与它们的模型能力。")),
      h("div.spacer"),
      state.revision ? badge(`版本 ${String(state.revision).slice(0, 12)}`, "muted") : null,
      buttonNode("添加供应商", { onClick: openCreateProvider }),
    ),
  ];

  if (state.error) {
    children.push(notice(`无法读取或写入供应商配置: ${state.error}`, "error"));
  }
  if (!state.providers.length) {
    children.push(empty("尚未配置供应商。", {
      icon: "providers",
      hint: "点右上角「添加供应商」，从常见供应商里挑一个，或手动填名称与地址。",
      action: buttonNode("添加供应商", { onClick: openCreateProvider }),
    }));
    render(host, children);
    return;
  }
  if (!provider) { render(host, children); return; }

  const detail = [];

  detail.push(state.editing === provider.id
    ? card(providerForm(provider))
    : card(
        cardHead(provider.id,
          badge(provider.base_url, "muted"),
          buttonNode(state.editing === provider.id ? "收起" : "编辑", {
            small: true, variant: "text",
            onClick: () => { state.editing = state.editing === provider.id ? null : provider.id; draw(); },
          }),
        ),
        kv([["路由路径", ROUTE_MODES.map((mode) => `${mode.label}: ${provider.routes?.[mode.id] || "默认"}`).join(" · ")]]),
      ));

  const keys = provider.keys || [];
  detail.push(h("div.card", {},
    cardHead(`Key（${keys.length}）`, buttonNode("添加 Key", { small: true, onClick: () => openCreateKey(provider) })),
    keys.length
      ? h("table.table", {},
          h("thead", h("tr", {}, ["Key", "能力 / 指纹", "状态", "操作"].map((label) => h("th", label)))),
          h("tbody", {}, keys.map((key) => keyRow(provider, key))),
        )
      : empty("尚无 Key。"),
  ));

  const editorKey = state.modelEditor && state.modelEditor.provider === provider.id
    ? keys.find((key) => key.name === state.modelEditor.key)
    : null;
  if (editorKey) detail.push(card(modelEditor(provider, editorKey)));

  detail.push(probePanel(provider));

  children.push(h("div.rail-split", {},
    providerRail(),
    h("div.rail-detail", {}, detail),
  ));
  render(host, children);
}