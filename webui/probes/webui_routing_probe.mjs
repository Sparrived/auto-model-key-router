// 模型路由页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/routing.js。
// 用法：node webui_routing_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么必须用 DOM 垫片：这一页的风险大多在"渲染出来的结构"与"提交体"上——模型列表
// 是否竖排在左、切换模型时右侧详情是否跟着换、上游模型名是否作为可编辑的目标属性出现、
// 以及保存/迁移时发出去的请求体字段。纯逻辑（候选去重等）在别处覆盖不到这些。
//
// 垫片忠实地保留 null 与文本节点语义（原生 replaceChildren 会把 null 参数变成文本
// "null"），否则会把 dom.js 的回归藏起来——见 webui_tip_probe.mjs 的同款说明。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

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
  // 与 webui/dom.js 的 append() 对齐：它自己过滤 null，再交给这里。
  append(...nodes) { this.#adopt(nodes); }
  // 忠实还原浏览器：null / undefined / false 都会变成文本节点。
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
  querySelector() { return null; }
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

// 文本节点必须也是 Node（dom.js 用 `child instanceof Node` 判断），否则会被
// 当成字符串再包一层，读出来就是 "[object Object]"。
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

// —— 假的模型路由配置 ——
//
// 三条路由覆盖详情形态：多个目标 + 别名、单个目标、零目标（零目标最容易在"按顺序"与
// 空状态文案上出错）。上游模型名刻意有一条与路由名不同（gpt-5.5-2026）：那正是这次
// 改造要表达的东西——上游各叫各的，名字不等于可调用名。
const ROUTES = [
  {
    id: "gpt-5.5",
    aliases: ["gpt", "gpt-latest"],
    routing_mode: "round_robin",
    targets: [
      { provider: "openai", key: "primary", upstream_model: "gpt-5.5" },
      { provider: "openai", key: "backup", upstream_model: "gpt-5.5-2026" },
    ],
  },
  {
    id: "claude-sonnet-4-5",
    aliases: [],
    routing_mode: null,
    targets: [{ provider: "anthropic", key: "main", upstream_model: "claude-sonnet-4-5" }],
  },
  { id: "local-llama", aliases: [], routing_mode: "only_first", targets: [] },
];

const PROVIDERS = [
  {
    id: "openai",
    base_url: "https://api.openai.com",
    keys: [
      { name: "primary", capabilities: { models: ["gpt-5.5"] } },
      // secondary 也探测到 gpt-5.5 但**未绑定**：候选列表该有它、不该有 primary。
      { name: "secondary", capabilities: { models: ["gpt-5.5"] } },
    ],
  },
  {
    id: "anthropic",
    base_url: "https://api.anthropic.com",
    keys: [{ name: "main", capabilities: { models: ["claude-sonnet-4-5"] } }],
  },
];

// 记录每一次写请求：这一页有几处"两次写入"的流程（迁移目标），只看渲染结果看不出
// 写入顺序与字段，必须落到请求体上。
const CALLS = [];
global.fetch = async (url, options = {}) => {
  const target = String(url);
  const method = options.method || "GET";
  let payload = {};
  if (target.includes("/api/routes")) {
    payload = { routes: ROUTES, config_revision: "rev-000000000000" };
  } else if (target.includes("/api/providers")) {
    payload = { providers: PROVIDERS, config_revision: "rev-000000000000" };
  }
  if (method !== "GET") {
    CALLS.push({ url: target, method, body: options.body ? JSON.parse(options.body) : null });
  }
  return { ok: true, status: 200, async text() { return JSON.stringify(payload); } };
};
global.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
global.location = { pathname: "/ui/", hash: "#/routing" };
global.window = { addEventListener() {}, isSecureContext: true, location: global.location };

const { renderRouting } = await import(pathToFileURL(path.join(WEBUI, "pages", "routing.js")).href);

const checks = {};
const check = (name, ok, detail = "") => {
  checks[name] = ok ? true : `FAILED${detail ? `: ${detail}` : ""}`;
};
// 收尾：报告并决定退出码。抽成函数是因为下面有一条"结构不对就别再驱动"的早退——
// 旧实现（横排标签页）会在这里被判出来，此时若继续点按钮，第一个 undefined 会抛
// TypeError，把已经查实的布局失败淹没在栈里，读的人只看到"探针自己崩了"。
function report() {
  const failed = Object.entries(checks).filter(([, value]) => value !== true);
  console.log(JSON.stringify({ checks, failed: failed.length }, null, 2));
  if (failed.length) {
    console.error(`\n${failed.length} 项断言失败`);
    process.exit(1);
  }
  console.log(`\n全部 ${Object.keys(checks).length} 项断言通过`);
}
const findAll = (node, predicate, out = []) => {
  if (!node) return out;
  if (predicate(node)) out.push(node);
  for (const child of node.children) findAll(child, predicate, out);
  return out;
};
// SVG 节点的 class 落在属性上（SVGElement.className 只读，dom.js 走 setAttribute）。
const hasClass = (node, name) =>
  [node.className, node.attrs?.class].some((value) =>
    String(value || "").split(/\s+/).includes(name));
const byClass = (root, name) => findAll(root, (n) => hasClass(n, name));
const byTag = (root, tag) => findAll(root, (n) => n.tagName === tag);
const click = (node) => { for (const fn of node.listeners.click || []) fn({ target: node, preventDefault() {} }); };
const buttonWithText = (root, text) => byTag(root, "button").find((n) => n.textContent.trim() === text);
// 页面里的异步流程（保存、迁移）都带若干次 await；多等几轮宏任务再断言。
const settle = async () => { for (let i = 0; i < 4; i += 1) await new Promise((resolve) => setTimeout(resolve, 0)); };

// —— 渲染真实页面 ——
const host = renderRouting({});
await settle();

// —— 布局：模型列表必须竖排在左侧，横向标签页条必须消失 ——
const split = byClass(host, "rail-split")[0];
const rail = byClass(host, "rail-nav")[0];
check("split_present", Boolean(split));
check("rail_is_nav", rail?.tagName === "nav", rail?.tagName);
check("rail_is_first_column", split?.children[0] === rail);
check("detail_is_second_column", hasClass(split?.children[1] || new FakeNode("x"), "rail-detail"));
// 这条是那次改动的核心：模型路由页原来用横排标签页，必须确认它真的没了。
check("old_tabs_strip_removed", byClass(host, "tabs").length === 0);
check("no_fake_tab_semantics", byClass(host, "tab").length === 0);
check("detail_holds_card", byClass(split?.children[1] || new FakeNode("x"), "card").length === 1);

// —— 导航项：每条路由一行，名称与目标数都要在 ——
const items = byClass(rail || new FakeNode("x"), "rail-item");
check("rail_item_count", items.length === ROUTES.length, `${items.length} != ${ROUTES.length}`);
check("rail_marks_active", items[0]?.attrs["aria-current"] === "true", items[0]?.attrs["aria-current"]);
check("rail_others_not_current", items.slice(1).every((n) => n.attrs["aria-current"] === undefined));
check("rail_shows_route_id", items[0]?.textContent.includes("gpt-5.5") === true, items[0]?.textContent);
// 目标数是选模型时最该看到的读数（0 个目标的路由其实是坏的）。
check("rail_shows_target_count", items[0]?.textContent.includes("2 个目标") === true, items[0]?.textContent);
check("rail_shows_zero_targets", items[2]?.textContent.includes("0 个目标") === true, items[2]?.textContent);
check("rail_semantics_not_fake_tab", items.every((n) => n.attrs.role === undefined));
// 供应商那栏靠品牌图标区分，这里刻意不带图标：别把两类导航的差异悄悄抹平。
check("rail_has_no_duplicate_icons", byClass(rail || new FakeNode("x"), "icon").length === 0);

// 结构不对就不再往下驱动：后面的断言全部依赖 rail-item（点它才能切模型、开编辑器），
// 拿不到就没法验证，硬跑只会抛错。这里如实报出布局失败并停下。
if (!items.length) {
  report();
}

// —— 详情：对外名称与轮询目标分开呈现 ——
const detail = split?.children[1];
const detailText = detail?.textContent || "";
check("detail_is_active_route", detailText.includes("gpt-5.5"), detailText.slice(0, 80));
check("detail_shows_route_id", detailText.includes("对外名称"), detailText.slice(0, 120));
check("detail_shows_aliases", detailText.includes("gpt-latest"));
// 隐藏别名整体下线：页面不该再有这个概念（旧版有一行「隐藏别名」）。
check("detail_drops_hidden_aliases", !detailText.includes("隐藏别名"), detailText.slice(0, 200));
// 上游模型名是目标自己的属性，必须逐条显示出来——它不等于路由名这件事要能一眼看到。
check("detail_shows_upstream_model", detailText.includes("gpt-5.5-2026"), detailText.slice(0, 200));
// 目标按顺序列出：顺序即优先级，所以顺序本身是数据，不是排版细节。
check("detail_shows_targets_in_order",
  detailText.indexOf("openai / primary") < detailText.indexOf("openai / backup"), detailText.slice(0, 160));
check("detail_shows_routing_mode", detailText.includes("轮询"));
// 每条目标都要能搬到别的路由上——上游名与路由名解耦之后，这是主要的整理手段。
const moveButtons = byTag(detail, "button").filter((n) => n.textContent.includes("移到其它路由"));
check("detail_target_rows_offer_move", moveButtons.length === 2, `${moveButtons.length} != 2`);

// —— 切换模型：详情必须跟着换，不能停在原来那条上 ——
click(items[2]);
const detail2 = byClass(host, "rail-detail")[0];
check("switch_updates_current", byClass(host, "rail-nav")[0]?.children[2]?.attrs["aria-current"] === "true");
check("switch_repaints_detail", detail2?.textContent.includes("local-llama") === true, detail2?.textContent?.slice(0, 80));
// 空目标要给出明确说明，而不是把"没有目标"渲染成一片空白；还要说清"保存即删除"。
check("switch_shows_empty_targets", detail2?.textContent.includes("尚未绑定目标") === true);
check("switch_explains_empty_means_delete", detail2?.textContent.includes("保存空目标会直接删除它") === true);
check("switch_drops_old_detail", detail2?.textContent.includes("gpt-latest") !== true);

// —— 编辑态：展开后仍在右栏内，左栏的模型列表不能被整块替换掉 ——
check("edit_button_present", Boolean(buttonWithText(host, "编辑")));
click(buttonWithText(host, "编辑"));
const detail3 = byClass(host, "rail-detail")[0];
check("editor_stays_in_detail_column", detail3?.textContent.includes("轮询目标（按顺序）") === true,
  detail3?.textContent?.slice(0, 120));
check("rail_still_present_while_editing", byClass(host, "rail-nav")[0]?.children.length === ROUTES.length);
check("editor_has_cancel", Boolean(buttonWithText(host, "取消")));
click(buttonWithText(host, "取消"));
// 只读详情写的是"轮询目标（按顺序，越靠前越优先）"，编辑态写的是"轮询目标（按顺序）"：
// 这里只要确认它退回了只读那一条。
check("cancel_returns_to_readonly",
  (byClass(host, "rail-detail")[0]?.textContent || "").includes("越靠前越优先"));

// —— 候选目标去重：已绑定的 Key 不能再出现在候选里 ——
// 这条锁的是模板字符串里 `${target.key}` 曾被转义成字面量的回归：那样去重集合的键
// 与候选键永远不相等，已绑定的 Key 会被反复加进来。
//
// 必须先切回 gpt-5.5（上一步停在零目标的 local-llama 上），否则候选列表本就是空的，
// 断言会因为"没有候选"而假通过——那正是这个回归最难被发现的地方。
click(byClass(host, "rail-nav")[0].children[0]);
click(buttonWithText(host, "编辑"));
const candidateButtons = byTag(host, "button").filter((n) => n.textContent.trim().startsWith("+ "));
check("candidates_rendered", candidateButtons.length > 0,
  candidateButtons.map((n) => n.textContent.trim()).join(" | "));
// openai/primary 已绑定 gpt-5.5，openai/secondary 未绑定：候选里只能有后者。
check("bound_keys_absent_from_candidates",
  candidateButtons.every((n) => !n.textContent.includes("openai / primary")),
  candidateButtons.map((n) => n.textContent.trim()).join(" | "));
check("unbound_key_offered_as_candidate",
  candidateButtons.some((n) => n.textContent.includes("openai / secondary")),
  candidateButtons.map((n) => n.textContent.trim()).join(" | "));
// 候选是"探测到的上游名正好等于本路由某个对外名称"的快捷入口；与本路由无关的 Key
// （anthropic）不该出现。
check("candidates_cover_route_names",
  candidateButtons.every((n) => !n.textContent.includes("anthropic")),
  candidateButtons.map((n) => n.textContent.trim()).join(" | "));

// —— 目标行的上游模型名可编辑：这是"上游各叫各的"在界面上的落点 ——
const upstreamInputs = byTag(host, "input").filter((n) => n.attrs["aria-label"]?.includes("上游模型名"));
check("editor_upstream_inputs_per_target", upstreamInputs.length === 2, `${upstreamInputs.length} != 2`);
check("editor_upstream_input_keeps_value",
  upstreamInputs[1]?.value === "gpt-5.5-2026", upstreamInputs[1]?.value);

// —— 添加目标：Key 下拉必须覆盖全部 Key（不再只限"探测到本路由名"的那些） ——
const allKeyCount = PROVIDERS[0].keys.length + PROVIDERS[1].keys.length;
const keySelect = byTag(host, "select").find((n) => (n.children || []).length === allKeyCount);
check("editor_key_select_covers_all_keys", Boolean(keySelect),
  byTag(host, "select").map((n) => (n.children || []).length).join(","));
// 上游名候选来自被选 Key 的探测结果（datalist），默认值回落成路由名。
const datalist = byTag(host, "datalist")[0];
// option 的 value 是 DOM 属性（dom.js 对 value 走 el.value = ...），不是属性节点。
check("editor_upstream_datalist_from_probe",
  (datalist?.children || []).some((option) => option.value === "gpt-5.5"),
  JSON.stringify((datalist?.children || []).map((option) => option.value)));
check("editor_upstream_defaults_to_route_id",
  byTag(host, "input").some((n) => n.attrs.list === "routing-upstream-options" && n.value === "gpt-5.5"));
check("editor_has_save", Boolean(buttonWithText(host, "保存路由")));

// —— 保存：请求体形状必须与"对外名称 + 上游名"这套语义一致 ——
// 改一下别名，确认提交体里带上了它、并且**没有** hidden_aliases（该字段已被服务端移除，
// 还发过去会直接 422）。
const aliasBox = byTag(host, "input").find((n) => n.value === "gpt, gpt-latest");
check("editor_alias_input_present", Boolean(aliasBox));
if (aliasBox) aliasBox.value = "gpt, gpt-latest, gpt-2026";
CALLS.length = 0;
click(buttonWithText(host, "保存路由"));
await settle();
const saved = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/gpt-5.5"));
check("save_puts_route", Boolean(saved), JSON.stringify(CALLS.map((c) => `${c.method} ${c.url}`)));
check("save_body_has_no_hidden_aliases", saved ? !("hidden_aliases" in saved.body) : false, JSON.stringify(saved?.body));
check("save_body_keeps_upstream_names",
  saved?.body?.targets?.map((target) => target.upstream_model).join(",") === "gpt-5.5,gpt-5.5-2026",
  JSON.stringify(saved?.body?.targets));
check("save_body_carries_aliases", saved?.body?.aliases?.includes("gpt-2026") === true, JSON.stringify(saved?.body?.aliases));
check("save_body_omits_rename", saved?.body?.id === null, JSON.stringify(saved?.body?.id));

// —— 迁移目标：先给接收方追加，再从来源移除；来源空了就传空 targets（服务端据此删路由） ——
CALLS.length = 0;
click(byClass(host, "rail-nav")[0].children[0]);
click(buttonWithText(host, "编辑"));
const moveButtons2 = byTag(host, "button").filter((n) => n.textContent.includes("移到其它路由"));
check("editor_rows_offer_move", moveButtons2.length === 0);   // 编辑态改由输入框接管，不再放"移到…"
click(byClass(host, "rail-nav")[0].children[0]);
const moveButtons3 = byTag(host, "button").filter((n) => n.textContent.includes("移到其它路由"));
click(moveButtons3[1]);   // gpt-5.5 的第二条目标（backup / gpt-5.5-2026）
await settle();
const moveDialog = byClass(document.body, "dialog")[0];
check("move_dialog_opens", Boolean(moveDialog), document.body.textContent.slice(0, 120));
const moveConfirm = moveDialog && byTag(moveDialog, "button").find((n) => n.textContent.trim() === "移动");
check("move_dialog_has_confirm", Boolean(moveConfirm));
if (moveConfirm) click(moveConfirm);
await settle();
const destinationPut = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/claude-sonnet-4-5"));
const sourcePut = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/gpt-5.5"));
check("move_appends_to_destination",
  destinationPut?.body?.targets?.length === 2 &&
  destinationPut.body.targets[1].upstream_model === "gpt-5.5-2026",
  JSON.stringify(destinationPut?.body?.targets));
check("move_removes_from_source",
  sourcePut?.body?.targets?.length === 1 && sourcePut.body.targets[0].upstream_model === "gpt-5.5",
  JSON.stringify(sourcePut?.body?.targets));
// 两次写入的顺序：先追加后移除。反过来的话中途失败会直接丢掉这条目标。
check("move_appends_before_removing",
  CALLS.findIndex((call) => call.url.includes("claude-sonnet-4-5")) <
  CALLS.findIndex((call) => call.url.includes("/api/routes/gpt-5.5")),
  JSON.stringify(CALLS.map((c) => c.url)));

report();