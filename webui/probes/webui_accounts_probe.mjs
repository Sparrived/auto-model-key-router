// 账号资源页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/accounts.js。
//
// 锁的是三件"代码里看不出来、坏了却很难发现"的事：
//
//   1. 浏览器**不直接访问 CPA**，而且进页面时**不把管理密钥拉下来**——管理密钥只在
//      打开实例编辑框时才取。所有请求都必须落在 AMKR 自己的 /api/ 下。
//   2. 这一页**不轮询**：进页面只发一次 /api/cpa-accounts（一次刷新要替每个实例问一遍
//      账号、逐账号问额度，轮询会把对端与 AMKR 一起拖住）。
//   3. 进度条画的是**剩余**比例，20% / 5% 两档颜色与读数一致——显示已用会把 18% 剩余
//      读成"还早"，这一页的结论就全反了。
//
// 另外钉住故障隔离：一个实例连不上时，它的错误只出现在自己那块卡片上，另一个实例的
// 账号照常列出。
//
// 用法：node webui/probes/webui_accounts_probe.mjs（有失败时退出码 1）。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

// —— DOM 垫片 ——
// 保留 null 与文本节点语义（原生 replaceChildren 会把 null 变成文本 "null"），
// 否则会把 dom.js 的回归藏起来——见 webui_tip_probe.mjs 的同款说明。
class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.parent = null;
    this.value = "";
    this.checked = false;
  }
  append(...nodes) { this.#adopt(nodes); }
  replaceChildren(...nodes) { this.children = []; this.#adopt(nodes); }
  #adopt(nodes) {
    for (const node of nodes.flat()) {
      const child = node instanceof FakeNode ? node : new FakeText(node);
      child.parent = this;
      this.children.push(child);
    }
  }
  appendChild(node) { this.append(node); return node; }
  setAttribute(key, value) { this.attrs[key] = String(value); }
  getAttribute(key) { return this.attrs[key]; }
  addEventListener(type, fn) { (this.listeners[type] ||= []).push(fn); }
  removeEventListener(type, fn) {
    this.listeners[type] = (this.listeners[type] || []).filter((item) => item !== fn);
  }
  remove() {
    if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this);
  }
  querySelector(selector) {
    const tags = String(selector).split(",").map((part) => part.trim());
    return findAll(this, (node) => tags.includes(node.tagName))[0] || null;
  }
  prepend(...nodes) { this.children.unshift(...nodes.flat()); }
  replaceWith() {}
  focus() {}
  get classList() {
    const self = this;
    const names = () => String(self.className).split(/\s+/).filter(Boolean);
    return {
      add: (...add) => { self.className = [...new Set([...names(), ...add])].join(" "); },
      remove: (...drop) => { self.className = names().filter((n) => !drop.includes(n)).join(" "); },
      contains: (name) => names().includes(name),
    };
  }
  get textContent() {
    return this.children
      .map((c) => (c.textContent === undefined ? String(c) : c.textContent))
      .join(" ");
  }
}

class FakeText extends FakeNode {
  constructor(data) { super("#text"); this.data = String(data); }
  get textContent() { return this.data; }
}

global.Node = FakeNode;
global.document = {
  createElement: (tag) => new FakeNode(tag),
  createElementNS: (_ns, tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  body: new FakeNode("body"),
  addEventListener() {},
  removeEventListener() {},
};
global.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
global.location = { pathname: "/ui/", hash: "#/accounts" };
global.window = { addEventListener() {}, isSecureContext: true, location: global.location };

const findAll = (node, predicate, out = []) => {
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
const hasClass = (node, name) =>
  [node.className, node.attrs?.class].some((value) =>
    String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (n) => hasClass(n, name));
const byTag = (root, tag) => findAll(root, (n) => n.tagName === tag);
const findText = (root, text) => findAll(root, (n) =>
  n.tagName === "#text" && String(n.data).includes(text)).length > 0;
const hasTitle = (root, text) => findAll(root, (n) =>
  String(n.attrs?.title || "").includes(text)).length > 0;
const buttonWithText = (root, text) =>
  findAll(root, (n) => n.tagName === "button" && n.textContent.includes(text))[0];
const click = (node) => { for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} }); };

// —— 假响应：两个实例，四种账号形态 ——
// 重置时间放在未来 3 小时，用来断言倒计时那一行真的渲染出来了。
const resetAt = new Date(Date.now() + 3 * 3600 * 1000).toISOString();

const ACCOUNTS = {
  fetched_at: new Date().toISOString(),
  instances: [
    {
      id: "cpa-a", label: "主力 CPA", base_url: "http://127.0.0.1:8317",
      ok: true, observed_at: new Date().toISOString(),
      accounts: [
        {
          auth_index: "0", name: "claude-1.json", provider: "claude", email: "a@example.com",
          status: "active", success: 12, failed: 1,
          // 账号形态与订阅档位：plan 给人看，tier_id 是上游标识，两者都要出现。
          plan: "Pro", tier_id: "pro-tier", account_type: "oauth", project_id: "proj-1",
          // 上游钟比本地慢 1 分钟：倒计时必须据此校正（否则显示 3 小时 0 分）。
          server_time_offset_ms: -60000,
          // 同一组里三个窗口 + 上游给的一句说明；说明要单独成行，不能顶掉窗口名。
          windows: [
            { key: "claude/5h", label: "5 小时", window: "5h", group: "Claude 与 GPT 模型", remaining: 0.18, reset_at: resetAt, source: "passive" },
            { key: "claude/7d", label: "7 天", window: "7d", group: "Claude 与 GPT 模型", remaining: 0.03, source: "passive", status: "rejected", description: "本周额度已用去大部分" },
            { key: "claude/7d_oi", label: "7 天（含超额）", window: "7d_oi", group: "Claude 与 GPT 模型", remaining: 0.9, source: "passive" },
          ],
          signals: { "Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour" },
          // 不成窗口的数值项（余额/积分）：值与单位一起显示。
          summary: [{ key: "credits", label: "剩余积分", value: 12.5, unit: "credit" }],
          // 逐模型额度与十分钟请求桶：都折叠在账号行里，但必须真的渲染出来。
          model_quotas: {
            "gpt-6-luna": {
              observed_at: new Date().toISOString(),
              windows: [{ key: "claude/5h", label: "5 小时", remaining: 0.4, source: "passive" }],
            },
          },
          recent_requests: [
            { time: "13:20-13:30", success: 3, failed: 1 },
            { time: "13:30-13:40", success: 0, failed: 0 },
          ],
        },
        {
          auth_index: "1", name: "gemini-1.json", provider: "gemini", status: "active",
          success: 0, failed: 0, windows: [],
          quota_error: "HTTP 501: no quota provider available for credential",
        },
        {
          auth_index: "2", name: "claude-old.json", provider: "claude", status: "disabled",
          disabled: true, success: 3, failed: 0, windows: [],
        },
      ],
    },
    {
      id: "cpa-b", label: "备用 CPA", base_url: "http://10.0.0.9:8317",
      ok: false, error: "无法连接 CPA: dial tcp 10.0.0.9:8317: connect: connection refused",
      accounts: [],
    },
  ],
};

const INSTANCES = {
  "cpa-a": { label: "主力 CPA", base_url: "http://127.0.0.1:8317", management_key: "mk-a" },
  "cpa-b": { label: "备用 CPA", base_url: "http://10.0.0.9:8317", management_key: "mk-b" },
};

// 记录每一次请求：整份探针最关键的一条就是"请求都打到哪了"。
const calls = [];
global.fetch = async (url) => {
  const target = String(url);
  calls.push(target);
  let payload = {};
  if (target.includes("/api/cpa-accounts")) payload = ACCOUNTS;
  else if (target.includes("/api/cpa-instances")) payload = { instances: INSTANCES, config_revision: "rev-1" };
  return { ok: true, status: 200, async text() { return JSON.stringify(payload); } };
};

const settle = async () => {
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
};

// —— 渲染真实页面 ——
const { renderAccounts } = await import(pathToFileURL(path.join(WEBUI, "pages", "accounts.js")).href);
const host = renderAccounts({});
await settle();

const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};

// —— 取数路径：只走 AMKR，且进页面只发一次 ——
check("loads_accounts_once",
  calls.length === 1 && calls[0] === "/api/cpa-accounts", calls.join(" , "));
check("no_instances_request_on_load",
  !calls.some((url) => url.includes("/api/cpa-instances")), calls.join(" , "));

// —— KPI 四张瓦片：读数与线索都要对 ——
const stats = byClass(host, "stat");
const statText = (index) => stats[index]?.textContent || "";
check("kpi_tile_count", stats.length === 4, String(stats.length));
check("kpi_instances_counts_broken",
  statText(0).includes("2") && statText(0).includes("1 个读取失败"), statText(0));
check("kpi_accounts_total", statText(1).includes("3"), statText(1));
check("kpi_usable_excludes_disabled",
  statText(2).includes("2") && statText(2).includes("1 个停用或冷却中"), statText(2));
check("kpi_low_quota_counts_drained",
  statText(3).includes("1") && statText(3).includes("其中 1 个已用尽"), statText(3));

// —— 圆环画的是剩余比例：角度、颜色、环里的数字三者同源 ——
// 4 个环 = 账号级 3 个窗口（5h / 7d / 7d_oi）+ 折叠区里那个模型的 1 个窗口：
// 逐模型额度用的就是同一套环，数量把两处都算上，才能保证没有窗口被漏画。
const rings = byClass(host, "quota-ring");
const ringValues = byClass(host, "quota-ring-value").map((node) => node.textContent);
check("one_ring_per_window", rings.length === 4, String(rings.length));
check("ring_degrees_match_percent",
  rings[0]?.style.background.includes("64.8deg") && ringValues[0] === "18%",
  `${rings[0]?.style.background} / ${ringValues[0]}`);
check("ring_warn_and_drained_colors",
  rings[0]?.style.background.includes("#f9a825") && rings[1]?.style.background.includes("var(--md-error)"),
  `${rings[0]?.style.background} / ${rings[1]?.style.background}`);
check("ring_healthy_has_no_alert_color",
  ringValues[2] === "90%" && rings[2]?.style.background.includes("var(--md-primary)") &&
    !rings[2]?.style.background.includes("#f9a825") && !rings[2]?.style.background.includes("--md-error"),
  `${ringValues[2]} / ${rings[2]?.style.background}`);
// 来源、完整倒计时、上游那句说明都进 title；版面上只留极短的重置提示（"↻ 3 小时"）。
check("ring_marks_source_and_reset",
  hasTitle(host, "CPA 采集") && hasTitle(host, "已用尽") &&
    hasTitle(host, "3 小时 1 分钟后重置") && findText(host, "↻ 3 小时"));

// —— 额度细节：这些都是 CPA 已经给了、AMKR 以前丢掉的东西 ——
check("window_group_heading", findText(host, "Claude 与 GPT 模型"));

// —— 排版：额度组一行两个，组内环也两个一行 ——
// 垫片不算布局，所以这里只能钉结构、钉不了像素。但它挡得住"退回竖着叠"这类改动：组容器
// 必须是 grid（两个组并排），组内的环行必须是固定两列（否则 flex-wrap 会把三四个环挤成
// 一排，百分比与窗口名连成一串）。这正是表格行被撑到近三百像素、宽列一片空白的成因。
const groupRows = findAll(host, (node) =>
  String(node.style?.gridTemplateColumns || "").includes("auto-fit"));
check("quota_groups_side_by_side",
  groupRows.length === 1 && groupRows[0].style.display === "grid",
  `${groupRows.length} / ${groupRows[0]?.style.display}`);
const ringRows = findAll(host, (node) =>
  node.style?.gridTemplateColumns === "repeat(2, minmax(0, 1fr))");
check("rings_two_per_row", ringRows.length >= 2, String(ringRows.length));
// 上游那句说明（"You have used some of your weekly limit…"）只进悬停提示，不占版面：
// 旧版把它当窗口名画在进度条旁，于是看板上出现了"某个窗口叫这么长一句话"的怪状。
check("window_description_only_in_tooltip",
  hasTitle(host, "本周额度已用去大部分") && !findText(host, "本周额度已用去大部分"));
check("countdown_uses_server_offset", hasTitle(host, "3 小时 1 分钟后重置"));
check("plan_tier_and_project_shown", findText(host, "Pro") && findText(host, "proj-1"));
check("summary_metric_with_unit", findText(host, "剩余积分 12.5 credit"));
check("model_quota_section", findText(host, "逐模型额度") && findText(host, "gpt-6-luna"));
check("recent_request_bars", hasTitle(host, "成功 3 / 失败 1"));

// —— 没有额度的账号要说清原因，而不是留白 ——
// 501 是"对端没有额度提供者"，文案要同时点出两条补法（装额度插件 / 给账号配
// quota_probe），否则看到这行的人不知道该去哪儿改。
check("quota_hint_explains_501",
  findText(host, "对端没有额度提供者") && findText(host, "quota_probe"));
check("raw_signals_kept_as_fallback",
  findText(host, "原始信号") && findText(host, "Anthropic-Ratelimit-Unified-Representative-Claim"));
check("disabled_account_badged", findText(host, "已停用"));

// —— 故障隔离：坏实例的错只留在自己那块 ——
const brokenCard = byClass(host, "card").find((card) => card.textContent.includes("备用 CPA"));
check("broken_instance_shows_own_error",
  brokenCard?.textContent.includes("connection refused") === true, brokenCard?.textContent);
check("broken_instance_has_no_table", byTag(brokenCard || new FakeNode("x"), "table").length === 0);
const healthyTables = byClass(host, "table");
check("healthy_instance_still_lists_accounts",
  healthyTables.length === 1 && healthyTables[0].textContent.includes("claude-1.json"),
  String(healthyTables.length));

// —— 管理密钥只在实例编辑框里出现，且是密码框 ——
click(buttonWithText(host, "管理实例"));
await settle();
check("editor_requests_instances",
  calls.some((url) => url.includes("/api/cpa-instances")), calls.join(" , "));
const dialogNode = byClass(document.body, "dialog")[0];
check("editor_dialog_opened", Boolean(dialogNode));
const dialogInputs = byTag(dialogNode || new FakeNode("x"), "input");
check("editor_lists_every_instance",
  byClass(dialogNode || new FakeNode("x"), "instance-editor").length === 2,
  String(byClass(dialogNode || new FakeNode("x"), "instance-editor").length));
check("management_key_is_password_field",
  dialogInputs.filter((node) => node.attrs.type === "password").length === 2,
  String(dialogInputs.filter((node) => node.attrs.type === "password").length));
check("editor_prefills_base_url",
  dialogInputs.some((node) => node.value === "http://127.0.0.1:8317"));

// —— 全局：从头到尾没有一个请求打到 CPA 自己身上 ——
check("only_amkr_api_called",
  calls.length > 0 && calls.every((url) => url.startsWith("/api/")), calls.join(" , "));

// —— 结果 ——
let failures = 0;
for (const [name, value] of Object.entries(checks)) {
  if (value !== true) failures += 1;
  console.log(`${value === true ? "OK  " : "FAIL"} ${name}${value === true ? "" : `  ${value}`}`);
}
console.log("");
if (failures) {
  console.log(`✗ ${failures} / ${Object.keys(checks).length} 项失败：账号资源页的取数路径或额度口径被改动了。`);
  process.exit(1);
}
console.log(`全部 ${Object.keys(checks).length} 项通过。`);
