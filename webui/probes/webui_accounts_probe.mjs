// 账号资源页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/accounts.js。
//
// 锁的是四件"代码里看不出来、坏了却很难发现"的事：
//
//   1. 浏览器**不直接访问 CPA**，也**不直接访问订阅厂商**，而且进页面时**不把管理密钥
//      拉下来**——管理密钥只在打开实例编辑框时才取。所有请求都必须落在 AMKR 自己的
//      /api/ 下。订阅那部分的读数由服务端扇出，页面只拿结果。
//   2. 这一页**不轮询**：进页面只发一次 /api/cpa-accounts（一次刷新要替每个实例问一遍
//      账号、逐账号问额度，还要挨家问订阅用量，轮询会把对端与 AMKR 一起拖住）。
//   3. 进度条画的是**剩余**比例，20% / 5% 两档颜色与读数一致——显示已用会把 18% 剩余
//      读成"还早"，这一页的结论就全反了。
//   4. 订阅条目**只读、按端点派生**：页面里没有"添加订阅"这种入口，删供应商就等于删条目。
//
// 另外钉住故障隔离：一个实例连不上时，它的错误只出现在自己那块卡片上，另一个实例的
// 账号照常列出；一条订阅读失败也不影响别的条目。
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

// 订阅条目：两条正常 + 一条读取失败。
//
// 这些是**派生**出来的（配了订阅端点就出现），所以探针里也照派生结果的形状给：
// 没有"添加订阅"的配置面，页面只读。
const SUBSCRIPTIONS = [
  {
    id: "go-a/key-1", kind: "opencode-go", vendor: "OpenCode Go",
    provider_id: "go-a", key_name: "key-1", base_url: "https://opencode.ai/zen/go/v1",
    ok: true, observed_at: new Date().toISOString(), account: "xiaoming",
    plan: "Go", tier_id: "individual-go",
    // 三个窗口：一个健康、一个告警（≤20%）、一个已用尽（rejected + 0）。
    // percent 是上游的**已用**，这里给的 remaining 是后端翻好的剩余比例。
    windows: [
      { key: "opencode-go/rolling", label: "5 小时", window: "5h", remaining: 0.88, reset_at: resetAt, source: "quota" },
      { key: "opencode-go/weekly", label: "7 天", window: "weekly", remaining: 0.12, source: "quota" },
      { key: "opencode-go/monthly", label: "30 天", window: "monthly", remaining: 0, source: "quota", status: "rejected" },
    ],
    summary: [{ key: "monthlyCredits", label: "订阅积分", value: 7.5, unit: "credit" }],
    signals: { status: "active" },
  },
  {
    id: "cc-a/key-1", kind: "commandcode", vendor: "Command Code",
    provider_id: "cc-a", key_name: "key-1", base_url: "https://api.commandcode.ai",
    ok: true, observed_at: new Date().toISOString(), account: "xiaoming",
    plan: "GOAT", tier_id: "individual-goat",
    windows: [
      { key: "commandcode/five_hour", label: "5 小时", window: "5h", remaining: 0.75, reset_at: resetAt, source: "quota" },
    ],
    summary: [
      { key: "monthlyCredits", label: "订阅积分", value: 5, unit: "credit" },
      { key: "totalCount", label: "请求数", value: 120, unit: "次" },
    ],
    signals: { 读取失败: "usage/summary 读取失败" },
  },
  {
    // 端点通了、但上游这次没回窗口：要说明"没有可读额度"，不能留白，也不能编个 0%。
    id: "cc-b/key-1", kind: "commandcode", vendor: "Command Code",
    provider_id: "cc-b", key_name: "key-1", base_url: "https://api.commandcode.ai",
    ok: true, observed_at: new Date().toISOString(),
    plan: "Ultra", tier_id: "individual-ultra", status: "active", windows: [],
  },
  {
    // 一条 key 不对的订阅：错误只落在自己这张卡片上。
    id: "go-b/key-1", kind: "opencode-go", vendor: "OpenCode Go",
    provider_id: "go-b", key_name: "key-1", base_url: "https://opencode.ai/zen/go/v1",
    ok: false, error: "凭据无效（401）——这把 key 不是 OpenCode Go 的订阅 key", windows: [],
  },
];

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
  subscriptions: SUBSCRIPTIONS,
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
// 张数同时被宽屏 4 列与 ≤1280px 的 2 列整除（5 张在 2 列下会甩出半宽孤儿，
// 所以「可用账号」并进了「账号」的线索里）。
const stats = byClass(host, "stat");
const statText = (index) => stats[index]?.textContent || "";
check("kpi_tile_count", stats.length === 4, String(stats.length));
check("kpi_instances_counts_broken",
  statText(0).includes("2") && statText(0).includes("1 个读取失败"), statText(0));
check("kpi_subscriptions_counts_readable",
  statText(1).includes("4") && statText(1).includes("3 条可读"), statText(1));
// 「账号」只数 CPA 账号（3 个），不把订阅并进来：订阅没有启用/停用这回事，
// 合并之后"可用 / 总数"就减不出所以然了。可用账号（2）并进线索里。
check("kpi_accounts_total", statText(2).includes("3") && !statText(2).includes("7"), statText(2));
check("kpi_accounts_hint_has_usable_count",
  statText(2).includes("2 可用") && statText(2).includes("1 停用/冷却"), statText(2));
// 告警是 CPA 账号与订阅的**合集**：CPA 侧 1 个（claude-1 最紧的窗口 3%）+
// 订阅侧 1 个（go-a 的 30 天窗口已归零）= 2，两个都 ≤5% 所以都已用尽。
check("kpi_low_quota_merges_both_sides",
  statText(3).includes("2") && statText(3).includes("其中 2 个已用尽"), statText(3));

// —— 圆环画的是剩余比例：角度、颜色、环里的数字三者同源 ——
// 8 个环 = 订阅 4 个窗口（OpenCode Go 3 个 + Command Code 1 个，排在前）
//        + 账号级 3 个窗口（5h / 7d / 7d_oi）+ 折叠区里那个模型的 1 个窗口。
// 逐模型额度用的就是同一套环，数量把两处都算上，才能保证没有窗口被漏画。
const rings = byClass(host, "quota-ring");
const ringValues = byClass(host, "quota-ring-value").map((node) => node.textContent);
check("one_ring_per_window", rings.length === 8, String(rings.length));
check("ring_degrees_match_percent",
  rings[0]?.style.background.includes("316.8deg") && ringValues[0] === "88%",
  `${rings[0]?.style.background} / ${ringValues[0]}`);
// 订阅窗口的环色与 CPA 账号走同一套阈值：12% 橙、0% 红、75% 主色。
check("subscription_rings_use_same_thresholds",
  rings[1]?.style.background.includes("#f9a825") &&
    rings[2]?.style.background.includes("var(--md-error)") &&
    rings[3]?.style.background.includes("var(--md-primary)"),
  `${rings[1]?.style.background} / ${rings[2]?.style.background} / ${rings[3]?.style.background}`);
// 账号侧的 18% 与 3% 仍按老规矩着色（0.18 与 0.03）。
check("ring_warn_and_drained_colors",
  rings[4]?.style.background.includes("#f9a825") && rings[5]?.style.background.includes("var(--md-error)"),
  `${rings[4]?.style.background} / ${rings[5]?.style.background}`);
check("ring_healthy_has_no_alert_color",
  ringValues[6] === "90%" && rings[6]?.style.background.includes("var(--md-primary)") &&
    !rings[6]?.style.background.includes("#f9a825") && !rings[6]?.style.background.includes("--md-error"),
  `${ringValues[6]} / ${rings[6]?.style.background}`);
// 来源、完整倒计时、上游那句说明都进 title；版面上只留极短的重置提示（"↻ 3 小时"）。
check("ring_marks_source_and_reset",
  hasTitle(host, "CPA 采集") && hasTitle(host, "已用尽") &&
    hasTitle(host, "3 小时 1 分钟后重置") && findText(host, "↻ 3 小时"));
// 订阅窗口的来源写"现场查询"（CPA 那半边才有"CPA 采集"这个来源）。
check("subscription_ring_source_is_live_query", hasTitle(host, "现场查询"));

// —— 订阅卡片：出处、档位、数值项、原始信号 ——
// 同一家可能配了多把 key，每把是独立的额度池，所以出处（provider · key）必须显示，
// 否则读到 12% 时没法判断是哪把快用完了。
check("subscription_shows_vendor_and_origin",
  findText(host, "OpenCode Go") && findText(host, "Command Code") &&
    findText(host, "go-a · key-1") && findText(host, "cc-a · key-1"));
check("subscription_shows_plan_and_account",
  byClass(host, "badge").some((node) => node.textContent === "Go") &&
    byClass(host, "badge").some((node) => node.textContent === "GOAT") &&
    findText(host, "账号 xiaoming"));
check("subscription_summary_metrics",
  findText(host, "订阅积分 7.5 credit") && findText(host, "请求数 120 次"));
check("subscription_signals_kept",
  findText(host, "原始信号") && findText(host, "usage/summary 读取失败"));
// 端点通了但上游这次没回窗口：要说清"没有可读额度"，不能留白，也不能编一个 0%。
check("subscription_without_windows_explains",
  findText(host, "上游这次没回可读的额度窗口") && findText(host, "档位 Ultra"));

// —— 额度细节：这些都是 CPA 已经给了、AMKR 以前丢掉的东西 ——
check("window_group_heading", findText(host, "Claude 与 GPT 模型"));

// —— 排版：额度组一行两个，组内环也两个一行 ——
// 垫片不算布局，所以这里只能钉结构、钉不了像素。但它挡得住"退回竖着叠"这类改动：组容器
// 必须是 grid（两个组并排），组内的环行必须是固定两列（否则 flex-wrap 会把三四个环挤成
// 一排，百分比与窗口名连成一串）。这正是表格行被撑到近三百像素、宽列一片空白的成因。
//
// 只按 auto-fit 认会连订阅卡片一起数进来（订阅卡片用的是 minmax(64px) 的那一份网格），
// 所以这里连 minmax 的宽度一起匹配，钉住的才是 CPA 那个分组容器。
const groupRows = findAll(host, (node) =>
  node.style?.gridTemplateColumns === "repeat(auto-fit, minmax(200px, 1fr))");
check("quota_groups_side_by_side",
  groupRows.length === 1 && groupRows[0].style.display === "grid",
  `${groupRows.length} / ${groupRows[0]?.style.display}`);
const ringRows = findAll(host, (node) =>
  node.style?.gridTemplateColumns === "repeat(2, minmax(0, 1fr))");
check("rings_two_per_row", ringRows.length >= 2, String(ringRows.length));
// 订阅卡片的窗口用另一份网格（卡片比表格行宽，三四个环一行排开更好读），
// 但它同样得是 grid——落回 flex-wrap 就会在窄屏把环挤成一条。
const subscriptionGrids = findAll(host, (node) =>
  node.style?.gridTemplateColumns === "repeat(auto-fit, minmax(64px, 1fr))");
check("subscription_windows_use_own_grid", subscriptionGrids.length === 2, String(subscriptionGrids.length));
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

// —— 故障隔离：坏实例与坏订阅的错都只留在自己那块 ——
const brokenSubCard = byClass(host, "card").find((card) =>
  card.textContent.includes("go-b · key-1"));
check("broken_subscription_shows_own_error",
  brokenSubCard?.textContent.includes("不是 OpenCode Go 的订阅 key") === true,
  brokenSubCard?.textContent);
check("broken_subscription_has_no_rings",
  byClass(brokenSubCard || new FakeNode("x"), "quota-ring").length === 0);

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
