// 设置：运行参数、本地鉴权、服务控制、配置迁移、WebUI 开关、版本检查。

import { h, errorText, copyText } from "../dom.js";
import { api } from "../api.js";
import { card, cardHead, notice, badge, loading, render, toast, buttonNode, input, field, confirmDialog, kv, toggle } from "../ui.js";

const SERVICE_GROUPS = [
  { title: "服务控制", actions: [
    { id: "start_amkr", label: "启动服务", icon: "play", running: true },
    { id: "stop_amkr", label: "停止服务", icon: "stop" },
    { id: "restart_amkr", label: "重启服务", icon: "refresh", running: true },
    { id: "status_amkr", label: "查询任务", icon: "activity" },
  ]},
  { title: "登录自启（当前用户）", actions: [
    { id: "install_user_amkr", label: "注册登录启动", icon: "shield" },
    { id: "uninstall_amkr", label: "取消注册", icon: "trash", confirm: "取消登录启动任务？正在运行的服务也会停止。", danger: true },
  ]},
  { title: "系统服务（需要管理员授权）", actions: [
    { id: "install_system_amkr", label: "注册开机服务", icon: "shield", confirm: "注册系统级开机服务？Windows 将请求管理员授权。" },
    { id: "start_system_amkr", label: "启动", icon: "play" },
    { id: "stop_system_amkr", label: "停止", icon: "stop" },
    { id: "restart_system_amkr", label: "重启", icon: "refresh" },
    { id: "uninstall_system_amkr", label: "取消系统服务", icon: "trash", confirm: "取消系统级服务？Windows 将请求管理员授权。", danger: true },
  ]},
];

const TIMEOUT_FIELDS = [
  { key: "request_timeout", label: "请求超时", min: 0.1 },
  { key: "stream_first_byte_timeout", label: "首字节超时", min: 0.1 },
  { key: "stream_idle_timeout", label: "流式空闲超时", min: 0.1 },
];

const state = {
  settings: null,
  draft: null,
  revision: null,
  tool: null,
  update: null,
  selfUpdate: null,
  loading: true,
  error: null,
  action: null,
  newestKey: null,
  exportText: "",
  transfer: null,
  transferring: false,
};

let host = null;
let xtxRef = null;

async function load() {
  const settings = await api.settings();
  state.settings = settings.settings || null;
  state.revision = settings.config_revision;
  state.draft = settings.settings ? { ...settings.settings } : null;
}

function draftInput(key, props = {}) {
  const control = input({
    value: state.draft?.[key] ?? "",
    ...props,
    onChange: (event) => { if (state.draft) state.draft[key] = event.target.value; },
  });
  return control;
}

function runtimeCard() {
  if (!state.draft) return card(cardHead("运行设置"), notice("运行设置暂不可用。", "warn"));
  const loopback = new Set(["127.0.0.1", "localhost", "::1"]);
  const errorHost = h("div");
  const save = async () => {
    const hostValue = String(state.draft.host || "").trim();
    if (!loopback.has(hostValue.toLowerCase())) {
      confirmDialog({
        title: "确认监听地址",
        message: "监听地址不是本机回环地址。远程客户端将可能访问 AMKR 管理 API，确定继续吗？",
        confirmLabel: "继续保存",
        onConfirm: () => commit(hostValue, errorHost),
      });
      return;
    }
    commit(hostValue, errorHost);
  };
  const commit = async (hostValue, errorHost) => {
    state.action = "settings";
    draw();
    try {
      const payload = {
        host: hostValue,
        port: Number(state.draft.port),
        max_retries: Number(state.draft.max_retries),
      };
      for (const item of TIMEOUT_FIELDS) payload[item.key] = Number(state.draft[item.key]);
      const result = await api.updateSettings(state.revision, payload);
      state.settings = result.settings || state.settings;
      state.revision = result.config_revision || state.revision;
      state.draft = result.settings ? { ...result.settings } : state.draft;
      toast("运行设置已保存。监听地址变更将在服务重启后生效。");
    } catch (error) {
      render(errorHost, notice(errorText(error), "error"));
    }
    state.action = null;
    draw();
  };

  return card(
    cardHead("运行设置", badge(state.revision ? String(state.revision).slice(0, 8) : "读取中", "muted")),
    h("div.form-grid", {},
      field("监听地址", draftInput("host", { required: true })),
      field("端口", draftInput("port", { type: "number", min: "1", max: "65535", required: true })),
      ...TIMEOUT_FIELDS.map((item) => field(`${item.label}（秒）`, draftInput(item.key, { type: "number", min: String(item.min), step: "0.1", required: true }))),
      field("最大重试", draftInput("max_retries", { type: "number", min: "0", step: "1", required: true })),
    ),
    h("div", { style: { marginTop: "16px" } }, kv([
      ["本地鉴权", state.settings?.local_auth_enabled ? `指纹 ${state.settings.local_api_key_fingerprint}` : "未启用"],
    ])),
    errorHost,
    h("div.btn-row", { style: { marginTop: "16px" } },
      buttonNode(state.action === "settings" ? "正在保存" : "保存运行设置", { iconName: "check", disabled: state.action === "settings", onClick: save }),
      buttonNode("重置本地鉴权 Key", {
        variant: "secondary",
        iconName: "refresh",
        disabled: state.action === "settings",
        onClick: () => confirmDialog({
          title: "重置本地鉴权 Key",
          message: "重置本地鉴权 Key？现有客户端需要改用新 Key。",
          confirmLabel: "重置",
          danger: true,
          onConfirm: resetKey,
        }),
      }),
    ),
    state.newestKey
      ? h("div", { style: { marginTop: "16px" } },
          notice("本地鉴权 Key 已重置，请立即更新客户端配置。", "good"),
          h("div.inline", { style: { marginTop: "8px" } },
            input({ value: state.newestKey, readOnly: true, class: "mono" }),
            buttonNode("复制", { onClick: () => copyText(state.newestKey).then(() => toast("Key 已复制")).catch((error) => toast(errorText(error), "error")) }),
          ),
        )
      : null,
  );
}

async function resetKey() {
  state.action = "key";
  draw();
  try {
    const result = await api.regenerateLocalKey(state.revision);
    state.newestKey = result.local_api_key;
    state.revision = result.config_revision || state.revision;
    if (state.settings) {
      state.settings.local_auth_enabled = true;
      state.settings.local_api_key_fingerprint = result.local_api_key_fingerprint;
    }
    toast("本地鉴权 Key 已重置。");
  } catch (error) {
    toast(errorText(error), "error");
  }
  state.action = null;
  draw();
}

function serviceCard() {
  const actionHost = h("div");
  const run = async (action) => {
    state.action = action.id;
    draw();
    try {
      const result = await api.runService(action.id);
      render(actionHost, h("div.stack.tight", { style: { marginTop: "16px" } },
        notice("操作已完成。", "good"),
        h("pre.log-panel", result.text || "（无输出）"),
      ));
      if (["start_amkr", "restart_amkr", "install_user_amkr"].includes(action.id)) {
        for (let i = 0; i < 5; i += 1) {
          await new Promise((resolve) => setTimeout(resolve, 500));
          const health = await api.health().catch(() => null);
          if (health?.status === "ok") break;
        }
      }
      if (xtxRef) await xtxRef.refreshHealth();
    } catch (error) {
      render(actionHost, notice(`服务操作失败: ${errorText(error)}`, "error"));
    }
    state.action = null;
    draw();
  };

  return card(
    cardHead("服务状态与控制", state.action ? h("span.spinner") : null),
    h("p.muted", "这些动作在本机执行，需要服务进程具备相应权限。"),
    SERVICE_GROUPS.map((group) => h("div", { style: { marginTop: "16px" } },
      h("h4", group.title),
      h("div.btn-row", { style: { marginTop: "8px" } }, group.actions.map((action) => buttonNode(
        state.action === action.id ? `正在${action.label}` : action.label,
        {
          variant: action.danger ? "danger" : "secondary",
          small: true,
          iconName: action.icon,
          disabled: !!state.action,
          onClick: () => action.confirm
            ? confirmDialog({ title: action.label, message: action.confirm, confirmLabel: action.label, danger: !!action.danger, onConfirm: () => run(action) })
            : run(action),
        },
      ))),
    )),
    actionHost,
  );
}

function transferCard() {
  const textarea = h("textarea.textarea", {
    placeholder: "导出后在此显示，或粘贴可迁移配置以导入。",
    value: state.exportText,
    onChange: (event) => { state.exportText = event.target.value; },
  });
  const errorHost = h("div");
  const busy = state.transfer !== null;
  return card(
    cardHead("配置迁移", busy ? badge(state.transfer === "export" ? "正在导出" : "正在导入", "muted") : null),
    textarea,
    errorHost,
    h("div.btn-row", { style: { marginTop: "16px" } },
      buttonNode(state.transfer === "export" ? "正在导出" : "导出", {
        iconName: "download",
        disabled: busy,
        onClick: async () => {
          state.transfer = "export";
          draw();
          try {
            const data = await api.exportConfig();
            state.exportText = JSON.stringify(data.config);
            toast("已导出可迁移配置。");
          } catch (error) { toast(errorText(error), "error"); }
          state.transfer = null;
          draw();
        },
      }),
      buttonNode(state.transfer === "import" ? "正在导入" : "导入配置", {
        variant: "secondary",
        iconName: "bolt",
        disabled: busy || !state.exportText.trim(),
        onClick: () => confirmDialog({
          title: "导入配置",
          message: "导入将追加/合并供应商与路由配置，并保留本机设置。是否继续？",
          confirmLabel: "导入",
          onConfirm: async () => {
            let parsed;
            try {
              parsed = JSON.parse(state.exportText);
            } catch {
              render(errorHost, notice("配置内容不是有效 JSON。", "error"));
              return;
            }
            if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
              render(errorHost, notice("配置内容必须是 JSON 对象。", "error"));
              return;
            }
            state.transfer = "import";
            draw();
            try {
              const providers = await api.providers();
              const result = await api.importConfig(providers.config_revision, parsed);
              if (result.imported === false) throw new Error("AMKR 未确认配置导入。");
              toast("配置已导入，AMKR 已热重载。");
            } catch (error) {
              render(errorHost, notice(errorText(error), "error"));
            }
            state.transfer = null;
            draw();
          },
        }),
      }),
    ),
  );
}

// applyUpdate 执行自更新。
//
// 成功之后服务会自己关停并由收尾助手拉起新版本，因此**不能**假设还能继续正常通讯：
// 请求本身会正常返回（服务端是先把响应写出去再关停），但紧接着的连接都会失败。
// 这里给出明确的等待提示，而不是让用户面对一串看不懂的报错。
async function applyUpdate() {
  toast("正在下载并校验新版本…");
  try {
    const result = await api.applyUpdate();
    toast(result?.message || "更新完成，服务正在重启。");
    state.update = null;
    draw();
  } catch (error) {
    // 401/403 是权限问题（访客 key），503/501 是构建不带自更新能力——都如实回显。
    toast(errorText(error), "error");
  }
}

function toolCard() {
  if (!state.tool) return card(cardHead("AMKR 版本"), loading("正在检测…"));
  const enabled = !!state.tool.webui_enabled;
  return card(
    cardHead("AMKR 版本与 WebUI", state.tool.update_available ? badge("发现新版本", "warn") : badge(`v${state.tool.version}`, "muted")),
    kv([
      ["当前版本", state.tool.version || "暂不可用"],
      ["最新版本", state.tool.latest_version || "暂不可用"],
      ["WebUI 资产", state.tool.webui_available ? "已安装" : "未随包安装"],
      ["WebUI 运行中", state.tool.webui_mounted ? "是" : "否"],
    ]),
    h("div.inline", { style: { marginTop: "16px" } },
      buttonNode("检查更新", {
        variant: "secondary", small: true,
        onClick: async () => {
          try {
            const result = await api.checkUpdate();
            state.update = result;
            toast(result.update_available ? "发现新版本。" : "当前已是最新版本。");
            if (xtxRef) await xtxRef.refreshHealth();
            draw();
          } catch (error) { toast(errorText(error), "error"); }
        },
      }),
      // 「立即更新」只在两件事同时成立时出现：服务端具备自更新能力，且确实有新版本。
      // 前者由 /ui/update/status 回答（旧构建没有这个能力），后者由检查更新的结果回答。
      // 不做成常驻按钮：它换掉的是正在运行的可执行文件并重启服务，随手可点不合适。
      state.update?.update_available && state.selfUpdate?.available
        ? buttonNode("立即更新", {
            variant: "danger", small: true,
            onClick: () => confirmDialog({
              title: "立即更新",
              message: `将下载并安装 ${state.update.latest_version}，然后自动重启服务。`
                + "更新期间界面会短暂断开，请稍后刷新。",
              confirmLabel: "更新并重启",
              danger: true,
              onConfirm: () => { void applyUpdate(); },
            }),
          })
        : null,
      toggle(enabled ? "WebUI 已启用" : "WebUI 已关闭", enabled, async () => {
        try {
          const result = await api.setWebui(!enabled);
          state.tool = { ...state.tool, ...result };
          toast(result.enabled === result.webui_mounted
            ? (result.enabled ? "WebUI 已启用。" : "WebUI 已关闭。")
            : "WebUI 配置已保存，重启服务后生效。");
          draw();
        } catch (error) { toast(errorText(error), "error"); }
      }),
    ),
    state.update?.release_url
      ? h("p.muted", { style: { marginTop: "8px" } }, "发布页面：", h("a", { href: state.update.release_url, target: "_blank", rel: "noreferrer" }, state.update.release_url))
      : null,
  );
}

export function renderSettings(context) {
  xtxRef = context;
  host = h("div.stack");
  if (state.loading) {
    render(host, h("div.page-head", h("h1", "设置")), loading("正在读取设置。"));
    (async () => {
      try {
        await load();
        state.error = null;
      } catch (error) {
        state.error = errorText(error);
      }
      state.tool = await api.tool().catch(() => null);
      // 自更新能力是公开信息（不鉴权），拿不到就当"不支持"——旧构建正是如此。
      state.selfUpdate = await api.updateStatus().catch(() => null);
      state.loading = false;
      draw();
    })();
    return host;
  }
  draw();
  return host;
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "设置"), h("p.sub", "运行参数、本地鉴权、服务控制与配置迁移。")),
      h("div.spacer"),
      xtxRef?.store?.health?.base_url ? badge(xtxRef.store.health.base_url, "muted") : null,
    ),
  ];
  if (state.error) children.push(notice(`读取设置失败: ${state.error}`, "error"));
  children.push(runtimeCard(), serviceCard(), toolCard(), transferCard());
  render(host, children);
}
