// 账号资源：把 CPA 实例的账号、以及按订阅计费的供应商（OpenCode Go、Command Code…）
// 的剩余额度汇总到一块看板。
//
// 四条设计约定，改动前先读：
//
//  1. **浏览器不直接访问 CPA**。CPA 的管理接口既不在 AMKR 同源内、也没有 CORS 头，
//     而且 management_key 是能改 CPA 配置的凭据，不该长期留在页面里。所有读数都走
//     AMKR 的服务端扇出（/api/cpa-accounts）。
//  2. **订阅条目不是配置出来的，是派生出来的**。这一页没有「添加订阅」这回事：只要
//     供应商里配了某家的订阅端点（OpenCode Go、Command Code），它的每个启用 key 就
//     自动出现在这里，删掉供应商就自动消失。页面因此只读不写——唯一的写入口是 CPA
//     实例编辑器（那是「一台机器」，凭据与端点都没法从别处推出来）。
//  3. **这一页不轮询**。刷新一次要替每个实例问账号、有额度插件的还要逐账号问额度，
//     还要挨家问订阅用量；轮询会把对端与 AMKR 一起拖住。数据只在进页面与点「刷新」时取。
//  4. **显示的是剩余额度**，不是已用。这一页回答"还能用多久"，显示已用会把 18% 剩余
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
    pageHead("账号资源", "CPA 实例的账号，以及 OpenCode Go、Command Code 这类订阅的剩余额度。",
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
  const subscriptions = state.report?.subscriptions || [];
  if (!instances.length && !subscriptions.length) {
    children.push(empty("还没有账号资源。", {
      icon: "gauge",
      hint: "填一个 CLIProxyAPI 的地址与管理密钥，或者配一个 OpenCode Go / Command Code 供应商——订阅会按端点自动出现在这里。",
      action: buttonNode("添加实例", { onClick: () => { void openInstanceEditor(); } }),
    }));
    render(host, children);
    return;
  }

  children.push(...kpis(instances, subscriptions));

  // 订阅排在前：它回答的是"我自己花的钱还剩多少"，而实例那一摞是"我管的机器上还有哪些号"。
  // 不新加一层网格容器：这一页的卡片本来就顺着 host 的 stack 排下去，实例卡片就是这么
  // 放的，订阅另起一套排版只会让两种卡片在窄屏塌缩的时机不一致。
  for (const entry of subscriptions) children.push(subscriptionCard(entry));
  for (const instance of instances) children.push(instanceCard(instance));
  render(host, children);
}

// KPI 四张瓦片：与 stat-grid 的 4 列一致。张数必须**同时**被 4 列（宽屏）与 2 列
// （≤1280px 塌缩）整除，否则窄屏会把最后一张甩成半宽孤儿。5 张看着更全，但它在 2 列下
// 除不尽，所以「可用账号」并进「账号」的线索里，把位置让给「订阅」。
// webui_layout_probe.mjs 的 PAGES 里有这一页，改张数会在那里失败。
//
// 「账号」只数 CPA 的账号，不把订阅条目并进来：订阅没有"启用/停用"这回事，
// 合并计数会让"可用 / 总数"减不出所以然。「额度告警」则是两边的合集——它回答的是
// 同一个问题（还剩多少），阈值必须同源，否则同一个 18% 在订阅卡片上报警、在账号行里
// 不报警。
function kpis(instances, subscriptions) {
  const accounts = instances.flatMap((instance) => instance.accounts || []);
  const readable = instances.filter((instance) => instance.ok).length;
  const usable = accounts.filter((account) => !account.disabled && !account.unavailable).length;
  const low = accounts.filter((account) => {
    const lowest = lowestRemaining(account);
    return lowest !== null && lowest <= 0.2;
  });
  const broken = instances.length - readable;

  const readableSubs = subscriptions.filter((entry) => entry.ok);
  const lowSubs = subscriptions.filter((entry) => {
    const lowest = lowestRemaining(entry);
    return lowest !== null && lowest <= 0.2;
  });
  const failedSubs = subscriptions.length - readableSubs.length;
  const alerts = low.length + lowSubs.length;
  const drained = low.filter((account) => lowestRemaining(account) <= 0.05).length
    + lowSubs.filter((entry) => lowestRemaining(entry) <= 0.05).length;

  return [statGrid(
    stat("实例", String(instances.length), broken ? `${broken} 个读取失败` : "全部可读",
      { iconName: "providers", tone: broken ? "bad" : undefined }),
    stat("订阅", String(subscriptions.length),
      subscriptions.length ? `${readableSubs.length} 条可读` : "未配置订阅端点",
      { iconName: "activity", tone: failedSubs ? "bad" : undefined }),
    stat("账号", String(accounts.length),
      `${usable} 可用 · ${accounts.length - usable} 停用/冷却`, { iconName: "key" }),
    stat("额度告警", String(alerts), alerts ? `其中 ${drained} 个已用尽` : "都还有 20% 以上",
      { iconName: "alert", tone: drained ? "bad" : undefined }),
  )];
}

// 一条订阅 = 一张卡片。
//
// 出处（供应商 / key 名）必须显示：同一家可能配了多把 key，每把是独立的额度池，只说
// "OpenCode Go" 的话读到 20% 时没法判断是哪把快用完了。
function subscriptionCard(entry) {
  const head = cardHead(
    entry.vendor || entry.kind,
    badge(entry.key_name ? `${entry.provider_id} · ${entry.key_name}` : entry.provider_id, "muted"),
    entry.plan ? badge(entry.plan, "info", { title: entry.tier_id || null }) : null,
    entry.ok ? freshness(entry.observed_at, { prefix: "查询于" }) : null,
  );
  const source = h("p.muted", [
    entry.base_url,
    entry.account ? `账号 ${entry.account}` : null,
  ].filter(Boolean).join(" · "));

  if (!entry.ok) {
    return card(head, source, notice(entry.error || "读取失败", "error"));
  }

  const windows = entry.windows || [];
  // 窗口的重置倒计时传 0 偏移，而不是像 CPA 那样减一个时钟差：CPA 会主动汇报它与上游的
  // 时差（server_time_offset_ms），而这两家没有这样的字段——凭空造一个偏移只会让倒计时
  // 更不准。剩下的偏差就是浏览器本机时钟与对端之差，正常机器上是秒级。
  const body = windows.length
    ? h("div", { style: quotaGridStyle }, windows.map((window) => quotaRing(window, 0)))
    : notice(subscriptionHint(entry), "info");

  return card(
    head,
    source,
    body,
    summaryBadges(entry.summary),
    signalDetails(entry.signals),
  );
}

// 没有窗口时说明原因。订阅这一侧的"没有窗口"不是故障：供应商没被认出来、或者上游这次
// 只回了账号信息没回用量，都可能。这里把剩下那点信息（状态、档位）讲出来，让人知道
// 端点通了、只是没额度可读。
function subscriptionHint(entry) {
  const parts = ["上游这次没回可读的额度窗口"];
  if (entry.status) parts.push(`状态 ${entry.status}`);
  if (entry.plan) parts.push(`档位 ${entry.plan}`);
  return `${parts.join("，")}。`;
}

// 订阅卡片里的环用 grid 而不是 CPA 表格行的 ringRowStyle（固定两列）：卡片比表格行宽，
// 三个窗口（OpenCode Go 的 5 小时/7 天/30 天）用 auto-fit 一行排开更好读，窄屏也会
// 自己落回两列、一列。
const quotaGridStyle = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(64px, 1fr))",
  gap: "10px 12px",
  justifyItems: "start",
};

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
    // 额度列不钉死宽度：内容是「两组 × 两环」，本身有宽度。钉成 44% 后，宽屏上额度列
    // 多出来的那几百像素全是空白（环挤在左端），而账号列的长文件名反被挤成两行。
    { key: "quota", label: "剩余额度", render: quotaCell },
    {
      key: "calls", label: "成功 / 失败", numeric: true,
      render: (account) => h("div.stack.tight", { style: { alignItems: "flex-end" } },
        h("span", `${formatCount(account.success)} / ${formatCount(account.failed)}`),
        recentBars(account.recent_requests),
      ),
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
    accountMeta(account),
  );
}

// 订阅档位与账号形态。
//
// plan 与 tier_id 两个都显示：plan 是给人看的档位名（CPA 可能按语言的叫法不同），
// tier_id 是上游的稳定标识（如 `free-tier`）。只留一个的话，前者对不上号、后者得去查。
// account_type/project_id 是排查用的——额度算到哪个项目上，出问题时第一个要看的就是它。
function accountMeta(account) {
  const items = [];
  if (account.plan) {
    items.push(badge(account.plan, "info", { title: account.tier_id || null }));
  } else if (account.tier_id) {
    items.push(badge(account.tier_id, "info"));
  }
  const meta = [account.account_type, account.project_id].filter(Boolean);
  if (meta.length) items.push(h("span.muted", meta.join(" · ")));
  if (!items.length) return null;
  return h("div", { style: { display: "flex", gap: "6px", flexWrap: "wrap", alignItems: "center" } }, ...items);
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
  const groups = groupWindows(account.windows || []);
  // 对端时钟与本地可能差一截（CPA 会把它算在 serverTimeOffsetMs 里），倒计时按本地钟
  // 算就会整体偏；把偏移减掉之后，"还有多久重置"才对得上上游的 resetTime。
  const offset = Number(account.server_time_offset_ms) || 0;
  const extras = [
    summaryBadges(account.summary),
    modelQuotaDetails(account, offset),
    signalDetails(account.signals),
  ];
  if (!groups.length) {
    return h("div.stack.tight", {}, h("span.muted", quotaHint(account)), ...extras);
  }
  return h("div.stack.tight", {},
    h("div", { style: groupGridStyle }, groups.map((group) => h("div.stack.tight", {},
      // 组名用内联样式而不是新加一个 CSS 类：这一页的样式表是公共资产，为一行小标题
      // 去改它（并让别处的改动跟着一起动）不划算。
      group.name
        ? h("div", {
          style: { fontSize: "12px", fontWeight: "500", color: "var(--md-on-surface-variant)" },
        }, group.name)
        : null,
      h("div", { style: ringRowStyle }, ...group.windows.map((window) => quotaRing(window, offset))),
    ))),
    ...extras,
  );
}

// 额度组的排版：一行放两个（列宽不够时自动落回一个）。
//
// Antigravity 这类 provider 的额度天然是两组（Gemini 一组、Claude 与 GPT 一组），两组竖着
// 叠会各占一整行：表格行被撑到近三百像素，而每组的环只占列宽的一小截，于是又高又空。
// 并排之后组与组对齐、行高减半，宽列也不再留一大片空白。
//
// 用 auto-fit 而不是写死两列：写死两列在窄屏会把每个组压到环都放不下（环有 52px 的
// min-width，压不下就溢出到表格外），auto-fit 在窄屏落回一列。
const groupGridStyle = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
  gap: "10px 20px",
  alignItems: "start",
};

// 一组里的环固定两个一行（grid 而不是 flex-wrap）。
//
// flex-wrap 是按列宽走的：列一宽，三个四个窗口就挤成一排，百分比与窗口名连成一串小数；
// 固定两列之后每组的节奏一致，读数也排得整齐。
const ringRowStyle = {
  display: "grid",
  gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
  gap: "8px 12px",
  justifyItems: "start",
};

// 按「模型组」切分窗口。
//
// Antigravity 这类 provider 的额度天然是两维的：Gemini 与 Claude/GPT **各有**一套
// 5 小时 + 周期额度，两组的窗口名一模一样（weekly / 5h）。不分区时看板上就是四条
// 看起来重复的条，谁也说不清哪条管哪些模型。
//
// 只比较相邻项而不是用 Map 归并：CPA 的 groups 按顺序给出，同组窗口必然相邻；用 Map
// 会把两个同名但不相邻的组悄悄合成一个，那是把上游的顺序信息吃掉。
function groupWindows(windows) {
  const groups = [];
  for (const window of windows) {
    const name = (window.group || "").trim();
    const last = groups[groups.length - 1];
    if (last && last.name === name) last.windows.push(window);
    else groups.push({ name, windows: [window] });
  }
  return groups;
}

// 一个窗口 = 一个圆环 + 环里的百分比 + 窗口名 + 极短的重置提示。
//
// 为什么不用横条：一行账号常有四个窗口（Antigravity 就是两组 × 两个窗口），横条每个占满
// 一整行，四条就把这块撑到大半屏；圆环把这些压进两行，百分比数字直接写在环里，也不必再
// 把读数对齐到右端。
//
// 上游那句说明（"You have used some of your weekly limit, it will fully refresh in
// 5 days, 9 hours."）只进 title 提示、**不占版面**：它是"为什么只剩这么多"的解释，不是
// 窗口名——旧版把它当标题画上去，看板上就出现了"某个窗口叫这么长一句话"的怪状，而它说的
// 重置时间又与提示行里的倒计时重复了一遍。
function quotaRing(window, offsetMs) {
  // clampPercent 给的是 0..1 的比例，环里要的是百分数——先乘 100 再取整，
  // 直接 round 比例只会得到 0 或 1（本文件的第一版就这么错过）。
  //
  // 取整用 floor 而不是 round：round 会把「只剩 99.6%」显示成 100%，等于替上游保证
  // 一个它没说的满额（上面那句 "You have used some of your weekly limit" 正是反例）。
  // 但纯 floor 会踩浮点：0.29*100 在 IEEE754 里是 28.999999999999996，floor 就成了 28%，
  // 所以要加一个 1e-9 的台阶把这类误差抬回整数。
  const percent = Math.min(100, Math.floor(clampPercent(window.remaining) * 100 + 1e-9));
  const details = [
    window.source === "passive" ? "CPA 采集" : "现场查询",
    window.status === "rejected" ? "已用尽" : null,
    countdownText(window.reset_at, offsetMs),
    window.description || null,
  ].filter(Boolean);
  const hint = resetHintText(window.reset_at, offsetMs);
  return h("div.ring-item", {
    title: details.join("\n"),
    style: { display: "flex", flexDirection: "column", alignItems: "center", gap: "3px", minWidth: "52px" },
  },
    // conic-gradient 画环：实色占 percent 对应的角度，其余用描边色。内圈盖住中心，
    // 百分比写在里面——不引 SVG（页面别处也没用），一个 div 就够。
    h("div.quota-ring", {
      style: {
        width: "46px",
        height: "46px",
        borderRadius: "50%",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: `conic-gradient(${ringColor(window.remaining)} ${(percent * 3.6).toFixed(1)}deg, var(--md-outline) 0)`,
      },
    },
      h("span.quota-ring-value", {
        style: {
          width: "34px",
          height: "34px",
          borderRadius: "50%",
          background: "var(--md-surface)",
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          fontSize: "12px",
          fontWeight: "500",
          fontVariantNumeric: "tabular-nums",
        },
      }, `${percent}%`),
    ),
    h("span.ring-label", {
      style: { fontSize: "12px", color: "var(--md-on-surface-variant)" },
    }, window.label),
    hint
      ? h("span.ring-reset", {
        style: { fontSize: "11px", color: "var(--md-on-surface-variant)" },
      }, hint)
      : null,
  );
}

// 环色分界与横条时期的 toneClass 完全一致：≤5% 红（已经不好使了）、≤20% 橙（该去加号了）、
// 其余主色。两处必须同源，否则"什么时候该报警"会随呈现形式变。
function ringColor(remaining) {
  const value = clampPercent(remaining);
  if (value <= 0.05) return "var(--md-error)";
  if (value <= 0.2) return "#f9a825";
  return "var(--md-primary)";
}

// 圆环下面只留一个能扫的量级（"↻ 3 小时"），完整说法在 title 里。
// 前缀 ↻ 不能省：光写"3 小时"读者得自己猜这是"还剩 3 小时"还是"还有 3 小时重置"。
function resetHintText(resetAt, offsetMs) {
  if (!resetAt) return "";
  const at = new Date(resetAt).getTime();
  if (!Number.isFinite(at)) return "";
  const remaining = at - (Number(offsetMs) || 0) - Date.now();
  if (remaining <= 0) return "↻ 即将重置";
  const minutes = Math.round(remaining / 60000);
  if (minutes < 60) return `↻ ${minutes} 分`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `↻ ${hours} 小时`;
  return `↻ ${Math.floor(hours / 24)} 天`;
}

// 数值项：CPA 的 summary[] 装的是「不成窗口的量」（余额、积分、计费系数…），单位各异，
// 所以值与单位一起原样显示，不做归一——猜错了比不显示更糟。
function summaryBadges(summary) {
  const metrics = (summary || []).filter((metric) => metric && (metric.label || metric.key));
  if (!metrics.length) return null;
  return h("div", { style: { display: "flex", gap: "6px", flexWrap: "wrap", alignItems: "center" } },
    metrics.map((metric) => badge(`${metric.label || metric.key} ${formatMetric(metric)}`, "muted",
      { title: metric.currency ? `币种 ${metric.currency}` : null })));
}

// 值与单位拼一起：12.5 与 12.5 USD 是两件事，只显示数字等于把单位丢了。
function formatMetric(metric) {
  const value = Number(metric.value);
  let text = "";
  if (Number.isFinite(value)) {
    text = String(Math.round(value * 100) / 100);
  } else if (metric.value !== undefined && metric.value !== null) {
    text = String(metric.value);
  }
  return [text, metric.unit].filter(Boolean).join(" ");
}

// 逐模型额度：解析口径与汇总行共用后端一份代码，因此标签、颜色、倒计时规则完全一致。
// 这里只把它折叠起来——几十个模型全铺开会把账号行撑到屏幕外。
function modelQuotaDetails(account, offsetMs) {
  const models = Object.keys(account.model_quotas || {}).sort();
  if (!models.length) return null;
  return h("details", {},
    h("summary.muted", `逐模型额度 · ${models.length} 个模型`),
    h("div.stack.tight", { style: { marginTop: "8px" } },
      models.map((model) => {
        const windows = (account.model_quotas[model] || {}).windows || [];
        return h("div.stack.tight", {},
          h("div.bar-head", {},
            h("span.bar-name", model),
            windows.length ? null : h("span.bar-value", "无信号"),
          ),
          h("div", { style: ringRowStyle }, ...windows.map((window) => quotaRing(window, offsetMs))),
        );
      })),
  );
}

// 最近请求：CPA 自己维护的十分钟桶，随 auth-files 一起回来，不需要额外请求。
//
// 画成迷你柱看的是"节奏"（有没有在打、有没有连续失败），绝对值就在上面的成功/失败里，
// 所以高度按本账号的峰值归一。失败单独着色：一眼能看出哪一段在报错。
function recentBars(buckets) {
  const list = (buckets || []).filter((bucket) => bucket && ((bucket.success || 0) + (bucket.failed || 0)) > 0);
  if (!list.length) return null;
  const totals = list.map((bucket) => (bucket.success || 0) + (bucket.failed || 0));
  const peak = Math.max(...totals, 1);
  return h("div", { style: { display: "flex", alignItems: "flex-end", gap: "2px", height: "16px" } },
    list.map((bucket) => {
      const success = bucket.success || 0;
      const failed = bucket.failed || 0;
      const height = Math.max(2, Math.round(((success + failed) / peak) * 16));
      return h("div", {
        title: `${bucket.time}：成功 ${success} / 失败 ${failed}`,
        style: {
          width: "5px",
          height: `${height}px`,
          borderRadius: "1px",
          background: failed ? "var(--md-error)" : "var(--md-primary)",
        },
      });
    }));
}

// 没有窗口时说明原因。501 是最常见的一种（对端既没装额度插件、也没给账号配声明式
// 探测 quota_probe），直接说人话并给出下一步；其余（超时、连不上）原样给短文本——
// 那是需要去查的故障，不该被润色掉。
function quotaHint(account) {
  if (!account.quota_error) {
    return account.supports_quota ? "对端没有可读的额度" : "上游未提供额度信号";
  }
  if (account.quota_error.includes("501")) {
    return "对端没有额度提供者（未装额度插件，也没配 quota_probe）";
  }
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

// 剩余比例夹到 0..1：CPA 与上游都可能给出超界值，圆环的角度与告警分界都按这个前提算。
function clampPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.max(0, Math.min(1, number));
}

// 一个账号里最紧的那个窗口；没有窗口时给 null（"没有数据"与"剩 0%"是两件事）。
function lowestRemaining(account) {
  const values = (account.windows || []).map((window) => clampPercent(window.remaining));
  return values.length ? Math.min(...values) : null;
}

// 重置倒计时。不复用 dom.js 的 formatDuration：那个是给"耗时多少毫秒"用的（"1.23s"），
// 而这里的量级是小时到天，而且看板要回答的是"还要等多久"，绝对时间得让人自己算。
//
// offsetMs 是对端时钟与本地时钟的差（CPA 的 serverTimeOffsetMs = 对端 − 本地）：
// resetTime 是按对端时钟写的，不把偏移减掉，倒计时就会整体偏一段——对端差几分钟，
// 看板上就多算几分钟。
function countdownText(resetAt, offsetMs) {
  if (!resetAt) return "";
  const at = new Date(resetAt).getTime();
  if (!Number.isFinite(at)) return "";
  const offset = Number(offsetMs) || 0;
  const remaining = at - offset - Date.now();
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
