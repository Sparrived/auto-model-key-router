// 账号资源：把多个 CLIProxyAPI 实例的账号与额度汇总到一块看板。
//
// 三条设计约定，改动前先读：
//
//  1. **浏览器不直接访问 CPA**。CPA 的管理接口既不在 AMKR 同源内、也没有 CORS 头，
//     而且 management_key 是能改 CPA 配置的凭据，不该长期留在页面里。所有读数都走
//     AMKR 的服务端扇出（/api/cpa-accounts）。
//  2. **这一页不轮询**。刷新一次要替每个实例问账号、有额度插件的还要逐账号问额度，
//     轮询会把对端与 AMKR 一起拖住。数据只在进入页面与点「刷新」时取。
//  3. **显示的是剩余额度**，不是已用。这一页回答"还能用多久"，显示已用会把 18% 剩余
//     读成"还早"。

import { h, errorText, formatCount } from "../dom.js";
import { api } from "../api.js";
import {
  badge, buttonNode, card, cardHead, dialog, empty, field, freshness, input, loading,
  notice, pageHead, render, stat, statGrid, table, toast,
} from "../ui.js";

const state = { loading: true, error: null, report: null };

let host = null;

export function renderAccounts() {
  host = h("div.stack");
  void refresh();
  return host;
}

async function refresh() {
  state.loading = true;
  draw();
  try {
    state.report = await api.cpaAccounts();
    state.error = null;
  } catch (error) {
    state.error = errorText(error);
  }
  state.loading = false;
  draw();
}

// —— 渲染 ——

function draw() {
  if (!host) return;
  const children = [
    pageHead("账号资源", "把多个 CLIProxyAPI 实例的账号与额度汇总到一块看板。",
      state.loading && state.report ? h("span.muted", "正在刷新…")
        : state.report ? freshness(state.report.fetched_at, { prefix: "读取于" }) : null,
      buttonNode("刷新", {
        variant: "secondary", small: true, iconName: "refresh",
        disabled: state.loading,
        onClick: () => { void refresh(); },
      }),
      buttonNode("管理实例", {
        small: true, iconName: "settings",
        onClick: () => { void openInstanceEditor(); },
      }),
    ),
  ];
  if (state.error) children.push(notice(`读取账号资源失败：${state.error}`, "error"));

  if (state.loading && !state.report) {
    children.push(card(loading("正在读取各实例的账号与额度…")));
    render(host, children);
    return;
  }

  const instances = state.report?.instances || [];
  if (!instances.length) {
    children.push(empty("还没有 CPA 实例。", {
      icon: "gauge",
      hint: "填一个 CLIProxyAPI 的地址与管理密钥，这里就会列出它的账号与各家额度。",
      action: buttonNode("添加实例", { onClick: () => { void openInstanceEditor(); } }),
    }));
    render(host, children);
    return;
  }

  children.push(...kpis(instances));
  for (const instance of instances) children.push(instanceCard(instance));
  render(host, children);
}

// KPI 四张瓦片：与 stat-grid 的 4 列一致。列数除不尽会在窄屏甩出孤儿瓦片，
// webui_layout_probe.mjs 就是盯这件事的。
function kpis(instances) {
  const accounts = instances.flatMap((instance) => instance.accounts || []);
  const readable = instances.filter((instance) => instance.ok).length;
  const usable = accounts.filter((account) => !account.disabled && !account.unavailable).length;
  const low = accounts.filter((account) => {
    const lowest = lowestRemaining(account);
    return lowest !== null && lowest <= 0.2;
  });
  const drained = low.filter((account) => lowestRemaining(account) <= 0.05).length;
  const broken = instances.length - readable;

  return [statGrid(
    stat("实例", String(instances.length), broken ? `${broken} 个读取失败` : "全部可读",
      { iconName: "providers", tone: broken ? "bad" : undefined }),
    stat("账号", String(accounts.length), `来自 ${readable} 个可读实例`, { iconName: "key" }),
    stat("可用账号", String(usable), `${accounts.length - usable} 个停用或冷却中`, { iconName: "check" }),
    stat("额度告警", String(low.length), low.length ? `其中 ${drained} 个已用尽` : "都还有 20% 以上",
      { iconName: "alert", tone: drained ? "bad" : undefined }),
  )];
}

function instanceCard(instance) {
  const accounts = instance.accounts || [];
  const head = cardHead(
    instance.label || instance.id,
    badge(instance.ok ? `${accounts.length} 个账号` : "读取失败", instance.ok ? "muted" : "bad"),
    instance.observed_at
      ? freshness(instance.observed_at, { prefix: "CPA 观测于" })
      : null,
  );
  if (instance.error) {
    return card(head, h("p.muted", instance.base_url || "未填写地址"), notice(instance.error, "error"));
  }
  if (!accounts.length) {
    return card(head, h("p.muted", instance.base_url), empty("这个实例上还没有账号。", { icon: "key" }));
  }
  return card(
    head,
    h("p.muted", instance.base_url),
    table(accountColumns(), accounts, "这个实例上还没有账号。"),
  );
}

function accountColumns() {
  return [
    { key: "account", label: "账号", render: accountCell },
    { key: "provider", label: "供应商", render: (account) => badge(account.provider || "未知", "info") },
    { key: "status", label: "状态", render: statusBadge },
    // 额度列占掉近一半宽度：进度条挤在窄列里就看不出"还剩多少"了。
    { key: "quota", label: "剩余额度", width: "44%", render: quotaCell },
    {
      key: "calls", label: "成功 / 失败", numeric: true,
      render: (account) => `${formatCount(account.success)} / ${formatCount(account.failed)}`,
    },
  ];
}

// 账号标题退回文件名：CPA 的 label 常常是空的，只显示 label 会出现一整列空白。
function accountCell(account) {
  const title = account.label || account.name || "未命名账号";
  const detail = account.label && account.email ? `${account.email} · ${account.name}` : (account.email || account.name);
  return h("div.stack.tight", {},
    h("div.account-title", title),
    detail && detail !== title ? h("span.muted", detail) : null,
  );
}

// 状态徽标只解释 disabled 与 unavailable：这两个在 CPA 侧有确定的调度含义。
// 其余 status 原样显示——翻译它反而会把"CPA 到底说了什么"这层信息抹掉。
function statusBadge(account) {
  if (account.disabled) return badge("已停用", "muted", { title: account.status_message || null });
  if (account.unavailable) return badge("冷却中", "warn", { title: account.status_message || null });
  if (account.status && account.status !== "active") return badge(account.status, "muted");
  return badge("可用", "good");
}

function quotaCell(account) {
  const windows = account.windows || [];
  if (!windows.length) {
    return h("div.stack.tight", {},
      h("span.muted", quotaHint(account)),
      signalDetails(account.signals),
    );
  }
  return h("div.stack.tight", {},
    h("div.bar-list", {}, windows.map(windowRow)),
    signalDetails(account.signals),
  );
}

function windowRow(window) {
  // clampPercent 给的是 0..1 的比例，进度条要的是百分数——先乘 100 再取整，
  // 直接 round 比例只会得到 0 或 1（本文件的第一版就这么错过）。
  const percent = Math.round(clampPercent(window.remaining) * 100);
  const notes = [
    window.source === "passive" ? "CPA 采集" : "现场查询",
    window.status === "rejected" ? "已用尽" : null,
    countdownText(window.reset_at),
  ].filter(Boolean);
  return h("div.bar-row", {},
    h("div.bar-head", {},
      h("span.bar-name", window.label),
      h("span.bar-value", `${percent}%`),
    ),
    h("div.bar-track", {},
      h("div.bar-fill", { class: toneClass(window.remaining), style: { width: `${percent}%` } })),
    notes.length ? h("span.bar-value", notes.join(" · ")) : null,
  );
}

// 没有窗口时说明原因。501 是最常见的一种（对端没装额度查询插件），直接说人话；
// 其余（超时、连不上）原样给短文本——那是需要去查的故障，不该被润色掉。
function quotaHint(account) {
  if (!account.quota_error) return "上游未提供额度信号";
  if (account.quota_error.includes("501")) return "对端未配置额度查询";
  return account.quota_error;
}

// 原始信号兜底。AMKR 只解析了 claude / codex 两族的窗口，其余头（限额状态、credits、
// representative claim）原样列出：上游新加了什么头，不必等 AMKR 适配就能看见。
function signalDetails(signals) {
  const names = Object.keys(signals || {});
  if (!names.length) return null;
  return h("details", {},
    h("summary.muted", `原始信号 · ${names.length} 项`),
    h("div.stack.tight", { style: { marginTop: "8px" } },
      names.sort().map((name) => h("div.mono", `${name}: ${signals[name]}`))),
  );
}

// —— 读数口径 ——

// 剩余比例夹到 0..1：CPA 与上游都可能给出超界值，进度条宽度按这个前提算。
function clampPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.max(0, Math.min(1, number));
}

// 剩余比例 → 进度条颜色。20% 以下告警、5% 以下算耗尽：这是"该去加号了"与
// "已经不好使了"的分界，与 CPA 自己的冷却阈值无关——它只是给人看的。
function toneClass(remaining) {
  const value = clampPercent(remaining);
  if (value <= 0.05) return "tone-bad";
  if (value <= 0.2) return "tone-warn";
  return null;
}

// 一个账号里最紧的那个窗口；没有窗口时给 null（"没有数据"与"剩 0%"是两件事）。
function lowestRemaining(account) {
  const values = (account.windows || []).map((window) => clampPercent(window.remaining));
  return values.length ? Math.min(...values) : null;
}

// 重置倒计时。不复用 dom.js 的 formatDuration：那个是给"耗时多少毫秒"用的（"1.23s"），
// 而这里的量级是小时到天，而且看板要回答的是"还要等多久"，绝对时间得让人自己算。
function countdownText(resetAt) {
  if (!resetAt) return "";
  const at = new Date(resetAt).getTime();
  if (!Number.isFinite(at)) return "";
  const remaining = at - Date.now();
  if (remaining <= 0) return "即将重置";
  const minutes = Math.round(remaining / 60000);
  if (minutes < 60) return `${minutes} 分钟后重置`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时 ${minutes % 60} 分钟后重置`;
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时后重置`;
}

// —— 实例编辑 ——

// 编辑实例清单。
//
// 打开时先读一次清单拿 config_revision：PUT 是**整体替换**，编辑期间配置若被别处改动，
// 保存会以 409 拒绝，而不是静默把对方的改动覆盖掉。因此对话框里的草稿与版本号是同时
// 取到的，两者的时间差只有一个对话框的打开时长。
async function openInstanceEditor() {
  let listed;
  try {
    listed = await api.cpaInstances();
  } catch (error) {
    toast(errorText(error), "error");
    return;
  }
  const revision = listed.config_revision;
  const rows = Object.entries(listed.instances || {}).map(([id, item]) => ({
    id,
    label: item.label || "",
    base_url: item.base_url || "",
    management_key: item.management_key || "",
  }));

  const rowsHost = h("div.stack");
  const drawRows = () => {
    if (!rows.length) {
      render(rowsHost, notice("还没有实例。填一个 CPA 的地址与管理密钥，保存后看板就会列出它的账号。", "info"));
      return;
    }
    render(rowsHost, rows.map((row, index) => instanceRow(row, () => {
      rows.splice(index, 1);
      drawRows();
    })));
  };
  drawRows();

  let saving = false;
  const save = async () => {
    if (saving) return;
    const instances = {};
    for (const row of rows) {
      const id = row.id.trim();
      if (!id) {
        toast("实例 ID 不能为空。", "error");
        return;
      }
      if (instances[id]) {
        toast(`实例 ID 重复：${id}`, "error");
        return;
      }
      instances[id] = {
        label: row.label.trim(),
        base_url: row.base_url.trim(),
        management_key: row.management_key.trim(),
      };
    }
    saving = true;
    try {
      await api.saveCPAInstances(revision, instances);
      ref.close();
      toast("实例已保存。");
      await refresh();
    } catch (error) {
      // 409 是"配置已被别处改动"：重开一次对话框就能拿到新版本号，比让用户对着
      // "HTTP 409" 自己猜要好。
      toast(error?.status === 409
        ? "配置已被其它改动更新，请重新打开本窗口后再保存。"
        : errorText(error), "error");
    } finally {
      saving = false;
    }
  };

  const ref = dialog({
    title: "管理 CPA 实例",
    body: h("div.stack", {},
      h("p.muted", "实例保存在 AMKR 配置文件的 cpa_instances 里。管理密钥是 CPA 管理面（/v0/management）的密钥，不是模型调用 key。"),
      rowsHost,
      h("div.btn-row", {},
        buttonNode("添加实例", {
          variant: "secondary", small: true,
          onClick: () => {
            rows.push({ id: "", label: "", base_url: "", management_key: "" });
            drawRows();
          },
        }),
      ),
    ),
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "保存", onClick: () => { void save(); } },
    ],
  });
}

function instanceRow(row, onRemove) {
  return h("div.instance-editor", {},
    h("div.form-grid", {},
      field("实例 ID", input({
        value: row.id, placeholder: "cpa-a",
        onInput: (event) => { row.id = event.target.value; },
      })),
      field("名称", input({
        value: row.label, placeholder: "主力 CPA",
        onInput: (event) => { row.label = event.target.value; },
      })),
      field("地址", input({
        value: row.base_url, placeholder: "http://127.0.0.1:8317",
        onInput: (event) => { row.base_url = event.target.value; },
      })),
      field("管理密钥", input({
        value: row.management_key, type: "password", autocomplete: "off",
        onInput: (event) => { row.management_key = event.target.value; },
      })),
    ),
    h("div.btn-row", {}, buttonNode("移除", { variant: "text", small: true, onClick: onRemove })),
  );
}
