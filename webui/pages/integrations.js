// 集成：把 Claude Code / Codex / Pi Agent 接到本机 AMKR。

import { h, errorText } from "../dom.js";
import { api } from "../api.js";
import { card, cardHead, notice, badge, loading, render, toast, buttonNode, confirmDialog } from "../ui.js";

const AGENTS = [
  { id: "claude-code", name: "Claude Code" },
  { id: "codex", name: "Codex" },
  { id: "pi-agent", name: "Pi Agent" },
];

// 变更字段预览：与各 Agent 的目标文件结构一一对应。
//
// 集成不再有「统一模型 / 原生模型」之分，三个客户端一律写入 AMKR 当前可用的全部
// 模型（模型 ID、别名，以及配置了统一模型时的 unified-model），并清理旧实现写入的
// 单模型覆盖键——留着会把客户端钉死在一个模型上。清理只针对 AMKR 自己写过的键：
// 集成前就存在的键会恢复用户原值，不是无条件删除。
const PREVIEW = {
  "claude-code": [
    "env.ANTHROPIC_BASE_URL",
    "env.ANTHROPIC_AUTH_TOKEN",
    "清理旧版写入的 env.ANTHROPIC_MODEL 等单模型覆盖",
    "modelPicker.options（全部可用模型）",
  ],
  codex: [
    "model_provider",
    "清理旧版写入的 model / review_model / model_reasoning_effort",
    "model_catalog_json（全部可用模型）",
    "model_providers.OpenAI",
    "auth.json",
  ],
  "pi-agent": [
    "providers.amkr",
    "providers.amkr.baseUrl",
    "providers.amkr.apiKey",
    "providers.amkr.models（全部可用模型）",
  ],
};

const state = { items: [], settings: null, loading: true, refreshing: false, error: null, busy: null, errors: {} };

let host = null;
let xtxRef = null;

function itemFor(agent) {
  return state.items.find((item) => item.agent === agent) || null;
}

function statusBadge(item) {
  if (!item) return badge("正在读取", "muted");
  if (item.error) return badge("操作失败", "bad");
  if (item.current_is_applied) return badge("已接管", "good");
  if (item.backup_available) return badge("配置已变更 · 可回退", "warn");
  if (item.target_exists) return badge("检测到配置", "muted");
  return badge("未找到配置", "muted");
}

async function apply(agent) {
  state.busy = agent;
  state.errors[agent] = null;
  draw();
  try {
    await api.applyIntegration(agent);
    await reload();
    toast(`${itemFor(agent)?.display_name || agent} 已接管配置。`);
  } catch (error) {
    state.errors[agent] = errorText(error);
  }
  state.busy = null;
  draw();
}

async function reload() {
  const [data, settings] = await Promise.all([api.integrations(), api.settings()]);
  state.items = data.integrations || [];
  state.settings = settings.settings || null;
  state.error = null;
}

// /health 的公开契约没有 base_url，不能以一个不存在的字段判断连接是否成功。
function connected() {
  return xtxRef?.store?.health?.status === "ok" && !xtxRef?.store?.connectionError;
}

// 集成由服务端写入本机客户端，目标地址与 agentconfig.RouterOrigin 的规则保持一致。
// 不能用浏览器地址代替：WebUI 可能经过反向代理或挂在宿主的子路径下。
function routerOrigin() {
  const settings = state.settings;
  if (!settings?.host || !settings?.port) return null;
  let host = settings.host;
  if (["0.0.0.0", "::", "[::]"].includes(host)) host = "127.0.0.1";
  if (host.includes(":") && !host.startsWith("[")) host = `[${host}]`;
  return `http://${host}:${settings.port}`;
}

async function refreshStatus(refreshHealth = false) {
  if (state.refreshing || state.busy) return;
  state.refreshing = true;
  draw();
  try {
    if (refreshHealth) await xtxRef?.refreshHealth?.();
    await reload();
  } catch (error) {
    state.error = errorText(error);
  }
  state.loading = false;
  state.refreshing = false;
  draw();
}

export function renderIntegrations(context) {
  xtxRef = context;
  host = h("div.stack");
  // 只在会影响操作的健康状态变化时重绘，正常轮询不打断折叠详情。
  const healthState = () => JSON.stringify([
    context.store.health?.status, context.store.health?.local_auth_enabled,
    context.store.health?.ops_enabled, context.store.connectionError,
  ]);
  let previousHealth = healthState();
  let wasConnected = connected();
  context.onTick?.(() => {
    const nextHealth = healthState();
    if (previousHealth === nextHealth) return;
    previousHealth = nextHealth;
    const recovered = !wasConnected && connected();
    wasConnected = connected();
    if (recovered) void refreshStatus();
    else if (!state.loading) draw();
  });
  draw();
  // 每次进入都重新读取运行设置与客户端状态；强制重绘重入时复用在途刷新。
  void refreshStatus();
  return host;
}

function draw() {
  if (!host) return;
  if (state.loading) {
    render(host, h("div.page-head", h("h1", "集成")), loading("正在读取集成状态。"));
    return;
  }
  const health = xtxRef?.store?.health;
  const isConnected = connected();
  const baseUrl = routerOrigin();
  const authEnabled = health?.local_auth_enabled;
  const opsDisabled = health?.ops_enabled === false;
  const unavailable = !isConnected || opsDisabled || !!state.error;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "集成"), h("p.sub", "让本机的 CLI 客户端通过 AMKR 统一入口发请求。")),
      h("div.spacer"),
      buttonNode(state.refreshing ? "正在刷新" : "刷新状态", {
        small: true, variant: "secondary", disabled: state.refreshing || !!state.busy, onClick: () => refreshStatus(true),
      }),
    ),
  ];
  if (state.error) children.push(notice(`读取集成状态失败: ${state.error}`, "error"));
  if (!isConnected) children.push(notice("尚未连接到 AMKR 服务，无法应用集成。", "warn"));
  else if (opsDisabled) children.push(notice("运维接口已关闭，无法应用或回退集成。", "warn"));
  else if (!authEnabled) children.push(notice("本地鉴权未启用：集成需要本地鉴权 Key 才能写入客户端配置。", "warn"));

  for (const agent of AGENTS) {
    const item = itemFor(agent.id);
    const busy = state.busy === agent.id;
    const preview = PREVIEW[agent.id] || [];
    children.push(card(
      cardHead(item?.display_name || agent.name,
        statusBadge(item),
        busy ? h("span.spinner") : null,
      ),
      h("p.muted", item?.target_path ? `目标文件 ${item.target_path}` : "正在读取配置状态。"),
      h("p.muted", "模型：写入 AMKR 当前可用的全部模型（至少有一把启用 Key 的模型与别名，含已配置的 unified-model），在客户端自己的模型选择器里切换。"),
      state.errors[agent.id] ? notice(state.errors[agent.id], "error") : null,
      item?.error ? notice(item.error, "error") : null,
      h("details", {}, h("summary.muted", `变更字段 · ${preview.length} 项`),
        h("div.stack.tight", { style: { marginTop: "8px" } },
          preview.map((name) => h("div.mono", name))),
      ),
      h("div.btn-row", { style: { marginTop: "16px" } },
        buttonNode(busy ? "正在处理" : "应用", {
          disabled: !!state.busy || state.refreshing || unavailable || !authEnabled,
          onClick: () => apply(agent.id),
        }),
        buttonNode("回退", {
          variant: "secondary",
          disabled: !!state.busy || state.refreshing || unavailable || !item?.backup_available,
          onClick: () => confirmDialog({
            title: "回退集成",
            message: `回退 ${item?.display_name || agent.name} 的原配置？`,
            confirmLabel: "回退",
            danger: true,
            onConfirm: async () => {
              state.busy = agent.id;
              draw();
              try {
                await api.rollbackIntegration(agent.id);
                await reload();
                toast("已回退到原配置。");
              } catch (error) {
                state.errors[agent.id] = errorText(error);
              }
              state.busy = null;
              draw();
            },
          }),
        }),
      ),
    ));
  }

  if (baseUrl) {
    children.push(h("p.muted", `目标地址 ${baseUrl}。${authEnabled ? "本地鉴权已启用。" : "本地鉴权未启用。"}`));
  }
  render(host, children);
}
