// 模型路由页回归探针：用最小 DOM 垫片驱动真实的 webui/pages/routing.js。
// 用法：node webui_routing_probe.mjs，结果以 JSON 打到 stdout，失败时退出码 1。
//
// 为什么必须用 DOM 垫片：这一页的风险大多在"渲染出来的结构"与"提交体"上——模型列表
// 是否竖排在左、切换模型时右侧详情是否跟着换、上游模型名是否作为可编辑的目标属性出现、
// 以及保存/迁移时发出去的请求体字段。纯逻辑（候选去重等）在别处覆盖不到这些。
//
// 垫片忠实地保留 null 与文本节点语义（原生 replaceChildren 会把 null 参数变成文本
// "null"），否则会把 dom.js 的回归藏起来——见 webui_tip_probe.mjs 的同款说明。select 的
// 取值与 append(数组) 也照浏览器还原（详见 FakeNode 上的两处注释）：这两处早先都"比浏览器
// 宽松"，端点计划下拉因此藏了两个只有真机能看见的问题——配置好的族显示成「不覆盖」且再也
// 清不掉，以及下拉里恰好少掉本族那一组模型（真机复核抓到的，随后连同这层垫片一起锁住）。

import { pathToFileURL } from "node:url";
import path from "node:path";

const WEBUI = path.resolve(import.meta.dirname, "..");

class FakeNode {
  // select 之外的元素：value 就是普通属性。select 另有语义，见下面的 get/set value。
  #plainValue = "";
  // select 上被显式赋过的值；undefined 表示从没赋过（浏览器会默认选中第一个选项）。
  #selectedValue;
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.listeners = {};
    this.className = "";
    this.style = {};
    this.parent = null;
    this.checked = false;
  }
  // 与 webui/dom.js 的 append() 对齐：它自己过滤 null，再交给这里。
  append(...nodes) { this.#adopt(nodes); }
  // 忠实还原浏览器：null / undefined / false 都会变成文本节点。
  replaceChildren(...nodes) { this.children = []; this.#adopt(nodes); }
  #adopt(nodes) {
    // **不摊平数组**：真实元素上的 append() 只认节点与字符串，数组会被 String() 成一个文本
    // 节点——对一个 <select> 来说等于什么都没挂。dom.js 里那个会摊平数组的 append 只服务于
    // h()/mount() 自己，页面代码写 node.append(选项数组) 在真机上就是丢内容。垫片早先跟着
    // 摊平，于是「端点计划下拉恰好少掉本族那一组模型」在探针里全绿，只有真机能看出来。
    for (const node of nodes) {
      const child = node instanceof FakeNode ? node : new FakeText(node);
      child.parent = this;
      this.children.push(child);
    }
  }
  // select 的取值照浏览器还原（见下面 #options）。把它当普通属性存，会让「先赋 value 再挂
  // 选项」这类写法永远通过：真机上那个 select 的选中项会停在第一个选项上，配置好的族显示成
  // 「不覆盖」，用户还想清空它——点的正是当前项，change 不触发，于是那一族既看不出来也清不掉。
  get value() {
    if (this.tagName !== "select") return this.#plainValue;
    const options = this.#options();
    if (this.#selectedValue === undefined) return options[0]?.value ?? "";
    // 赋的值不在选项里时浏览器把 selectedIndex 置 -1：取值空串、界面空白。
    return options.find((option) => option.value === this.#selectedValue)?.value ?? "";
  }
  set value(next) {
    if (this.tagName !== "select") { this.#plainValue = next; return; }
    this.#selectedValue = String(next);
  }
  #options() {
    const found = [];
    const walk = (node) => {
      for (const child of node.children || []) {
        if (child.tagName === "option") found.push(child);
        else if (child.tagName === "optgroup") walk(child);
      }
    };
    walk(this);
    return found;
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
    // 一条已配置的图像端点计划：同一个名称下，图像端点改发给 dall-e-3。
    endpoints: { image: { primary: { model: "dall-e-3", key: null } } },
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

// —— 假的统一模型与模型清单 ——
//
// unified-model 如今是这一页的**第一个固定名称**（原来有自己的页面），端点计划编辑又
// 需要模型清单与各模型的 Key，因此这两份读数必须一起给出来。gpt-5.5 自带一条图像端点
// 计划（image → dall-e-3）：它是"同一个名称下各端点用不同模型"的最小样本。
const UNIFIED = {
  default: { primary: { model: "gpt-5.5", key: null } },
  image: { primary: { model: "dall-e-3", key: null } },
};
UNIFIED.config_revision = "rev-000000000000";

const MODELS = {
  models: [
    { id: "gpt-5.5", aliases: ["gpt", "gpt-latest"], routing_mode: "round_robin", reasoning_effort: "", keys: [
      { name: "primary", enabled: true }, { name: "backup", enabled: true },
    ] },
    { id: "claude-sonnet-4-5", aliases: [], keys: [{ name: "main", enabled: true }] },
    { id: "local-llama", aliases: [], keys: [] },
    { id: "dall-e-3", aliases: [], keys: [{ name: "image-key", enabled: true }] },
  ],
  config_revision: "rev-000000000000",
};

// 图像族只探测到 dall-e-3：用来验证端点计划下拉的分组与"不筛掉任何模型"。
const MODEL_KINDS = {
  catalog_available: true,
  models: {
    "gpt-5.5": { kinds: ["text"], primary: "text", endpoints: ["chat"] },
    "dall-e-3": { kinds: ["image"], primary: "image", endpoints: ["images"] },
  },
};

// 记录每一次写请求：这一页有几处"两次写入"的流程（迁移目标），只看渲染结果看不出
// 写入顺序与字段，必须落到请求体上。
const CALLS = [];
// 可以让下一次 POST /api/routes 失败（例如名称已存在时服务端返 409）：用来验证失败提示
// 是不是留在对话框里、以及失败之后页面还能不能用。
let POST_FAILURE = null;
global.fetch = async (url, options = {}) => {
  const target = String(url);
  const method = options.method || "GET";
  let payload = {};
  if (target.includes("/api/routes")) {
    payload = { routes: ROUTES, config_revision: "rev-000000000000" };
  } else if (target.includes("/api/providers")) {
    payload = { providers: PROVIDERS, config_revision: "rev-000000000000" };
  } else if (target.includes("/api/models")) {
    payload = MODELS;
  } else if (target.includes("/api/unified-model")) {
    payload = { unified_model: UNIFIED, config_revision: UNIFIED.config_revision };
  } else if (target.includes("/ui/model-kinds.json")) {
    payload = MODEL_KINDS;
  }
  if (method !== "GET") {
    CALLS.push({ url: target, method, body: options.body ? JSON.parse(options.body) : null });
  }
  if (POST_FAILURE && method === "POST" && target.includes("/api/routes")) {
    const failure = POST_FAILURE;
    return {
      ok: false,
      status: failure.status,
      async text() { return JSON.stringify(failure.body); },
    };
  }
  // 新建的路由要真的进列表：建完页面会重新取数并直接进编辑态，桩若不给它，
  // 断言就只能在"详情为空"上打转。
  if (method === "POST" && target.endsWith("/api/routes") && options.body) {
    const created = JSON.parse(options.body);
    ROUTES.push({
      id: created.id, aliases: created.aliases || [], routing_mode: created.routing_mode,
      targets: created.targets || [], endpoints: created.endpoints || undefined,
    });
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

// —— 导航项：第一项固定是 unified-model，其后每条路由一行 ——
const items = byClass(rail || new FakeNode("x"), "rail-item");
check("rail_item_count", items.length === ROUTES.length + 1, `${items.length} != ${ROUTES.length + 1}`);
// unified-model 是固定的第一个名称：它原来有自己的页面，现在就在这里配置。
check("rail_starts_with_unified", items[0]?.textContent.includes("unified-model") === true, items[0]?.textContent);
check("rail_unified_is_selected_first", items[0]?.attrs["aria-current"] === "true", items[0]?.attrs["aria-current"]);
check("rail_unified_shows_plan_count", items[0]?.textContent.includes("2 个端点计划") === true, items[0]?.textContent);
check("rail_marks_active", items[1]?.attrs["aria-current"] === undefined, items[1]?.attrs["aria-current"]);
check("rail_others_not_current", items.slice(2).every((n) => n.attrs["aria-current"] === undefined));
check("rail_shows_route_id", items[1]?.textContent.includes("gpt-5.5") === true, items[1]?.textContent);
// 目标数是选模型时最该看到的读数（0 个目标的路由其实是坏的）。
check("rail_shows_target_count", items[1]?.textContent.includes("2 个目标") === true, items[1]?.textContent);
check("rail_shows_zero_targets", items[3]?.textContent.includes("0 个目标") === true, items[3]?.textContent);
check("rail_semantics_not_fake_tab", items.every((n) => n.attrs.role === undefined));
// 供应商那栏靠品牌图标区分，这里刻意不带图标：别把两类导航的差异悄悄抹平。
check("rail_has_no_duplicate_icons", byClass(rail || new FakeNode("x"), "icon").length === 0);

// 结构不对就不再往下驱动：后面的断言全部依赖 rail-item（点它才能切模型、开编辑器），
// 拿不到就没法验证，硬跑只会抛错。这里如实报出布局失败并停下。
if (!items.length) {
  report();
}

// —— 第一屏：unified-model 的只读详情 ——
//
// 这一页默认落在固定项上，因此"第一屏能不能看懂统一入口现在指向什么"必须是断言的一部分：
// 原来它由独立页面承担，合并之后漏掉就等于把统一模型的现状藏进了一次点击里。
const firstDetail = split?.children[1]?.textContent || "";
check("unified_detail_is_first_screen", firstDetail.includes("文本模型") && firstDetail.includes("gpt-5.5"), firstDetail.slice(0, 120));
check("unified_detail_shows_established_families",
  firstDetail.includes("图像模型") && firstDetail.includes("dall-e-3"), firstDetail.slice(0, 200));
check("unified_detail_explains_dispatch", firstDetail.includes("按入站端点族分派"), firstDetail.slice(0, 300));

// —— 切到一条路由：对外名称与轮询目标分开呈现 ——
// 点完之后必须重新取节点：切换会重画右栏，点之前抓到的那棵子树已经脱离文档。
click(items[1]);
await settle();
const detail = byClass(host, "rail-detail")[0];
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
// 端点计划在只读详情里也要看得见：它是"这个名称下各端点各用各的模型"的唯一读数。
check("detail_shows_endpoint_plans", detailText.includes("图像模型 → dall-e-3"), detailText.slice(0, 400));

// —— 切换模型：详情必须跟着换，不能停在原来那条上 ——
click(items[3]);
const detail2 = byClass(host, "rail-detail")[0];
check("switch_updates_current", byClass(host, "rail-nav")[0]?.children[3]?.attrs["aria-current"] === "true");
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
check("rail_still_present_while_editing", byClass(host, "rail-nav")[0]?.children.length === ROUTES.length + 1);
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
click(byClass(host, "rail-nav")[0].children[1]);
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
click(byClass(host, "rail-nav")[0].children[1]);
click(buttonWithText(host, "编辑"));
const moveButtons2 = byTag(host, "button").filter((n) => n.textContent.includes("移到其它路由"));
check("editor_rows_offer_move", moveButtons2.length === 0);   // 编辑态改由输入框接管，不再放"移到…"
click(byClass(host, "rail-nav")[0].children[1]);
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
// 迁移只搬目标：两次写入都不许带 endpoints，否则会把接收方（或来源）自己配好的端点计划
// 顺手清掉——那是一次看不见的配置丢失。
check("move_does_not_touch_endpoint_plans",
  destinationPut ? !("endpoints" in destinationPut.body) : false &&
  sourcePut ? !("endpoints" in sourcePut.body) : false,
  JSON.stringify([destinationPut?.body?.endpoints, sourcePut?.body?.endpoints]));

// ═══ 端点计划：同一个名称下各端点可以各用各的模型 ═══
//
// 这是这次改造的核心。断言分两头：编辑器里七个族都要有编辑位（漏一族的表现是这一族的
// 请求悄悄回落到轮询目标——页面看起来毫无问题），提交体只带配置了模型的族（服务端是
// **整体替换**这张表，多写一个族就等于改掉用户的计划）。
const FAMILY_LABELS = ["文本（对话）", "图像模型", "嵌入模型", "语音合成模型", "语音识别模型", "视频模型", "重排模型"];
const optionValues = (node) =>
  findAll(node || new FakeNode("select"), (child) => child.tagName === "option").map((option) => option.value);
// changeTo 驱动一个下拉：写值再触发 change（垫片不会自己派发事件）。
const changeTo = (node, value) => {
  if (!node) { check("change_target_missing", false, "要改动的下拉不存在"); return; }
  node.value = value;
  for (const fn of node.listeners.change || []) fn({ target: node });
};

CALLS.length = 0;
click(byClass(host, "rail-nav")[0].children[1]);
await settle();
click(buttonWithText(host, "编辑"));
await settle();
check("editor_renders_every_endpoint_family", FAMILY_LABELS.every((label) => host.textContent.includes(label)),
  FAMILY_LABELS.filter((label) => !host.textContent.includes(label)).join(",") || "全部都在");
// 已配置的图像计划要回显成"选中的模型"，而不是空着——空着的下一步就是被保存清掉。
const imagePlanSelect = byTag(host, "select").find((n) => n.value === "dall-e-3");
check("endpoint_plan_echoes_configured_model", Boolean(imagePlanSelect),
  byTag(host, "select").map((n) => n.value).join(","));
// 下拉不许筛掉任何模型：把自定义模型名挡在外面，用户就再也选不回自己的模型。
check("endpoint_plan_lists_every_model",
  ["gpt-5.5", "claude-sonnet-4-5", "local-llama", "dall-e-3"].every((id) => optionValues(imagePlanSelect).includes(id)),
  optionValues(imagePlanSelect).join(","));
check("endpoint_plan_leads_with_clear_option", optionValues(imagePlanSelect)[0] === "",
  optionValues(imagePlanSelect).join(","));

changeTo(imagePlanSelect, "local-llama");
click(buttonWithText(host, "保存路由"));
await settle();
const planPut = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/gpt-5.5"));
check("endpoint_plan_saved", planPut?.body?.endpoints?.image?.primary?.model === "local-llama",
  JSON.stringify(planPut?.body?.endpoints));
check("endpoint_plan_sends_only_configured_families",
  Object.keys(planPut?.body?.endpoints || {}).join(",") === "image",
  Object.keys(planPut?.body?.endpoints || {}).join(","));
// Key 留空 = 自动路由，折成 null（与既有 image/embeddings 的写法一致）。
check("endpoint_plan_folds_auto_key_to_null", planPut?.body?.endpoints?.image?.primary?.key === null,
  JSON.stringify(planPut?.body?.endpoints?.image?.primary));

// 清空一族 = 提交体里不再出现它。这一条最容易做错：把"清空"发成 null 之外的形状，
// 服务端会当成非法计划直接拒绝，用户看到的是"保存不了"，而表单上一切正常。
CALLS.length = 0;
click(buttonWithText(host, "编辑"));
await settle();
const imagePlanSelect2 = byTag(host, "select").find((n) => n.value === "dall-e-3");
changeTo(imagePlanSelect2, "");
click(buttonWithText(host, "保存路由"));
await settle();
const clearedPut = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/gpt-5.5"));
check("endpoint_plan_clear_removes_family", clearedPut?.body?.endpoints !== undefined
  && !("image" in clearedPut.body.endpoints), JSON.stringify(clearedPut?.body?.endpoints));

// ═══ 添加模型名称：新建必须同时绑定第一条目标 ═══
//
// 服务端的写路径不变式是"没有 target 的模型不存在"，因此新建对话框必须一次要齐名称与
// 第一条目标；否则只会在提交时被顶回来，用户还得重填一遍。
CALLS.length = 0;
click(buttonWithText(host, "添加模型名称"));
await settle();
const addDialog = byClass(document.body, "dialog").at(-1);
check("add_model_dialog_opens", Boolean(addDialog), document.body.textContent.slice(0, 120));
const nameBox = byTag(addDialog || new FakeNode("div"), "input")
  .find((n) => String(n.attrs.placeholder || "").includes("my-model"));
check("add_model_dialog_asks_name", Boolean(nameBox), JSON.stringify(byTag(addDialog || new FakeNode("div"), "input").map((n) => n.attrs.placeholder)));
if (nameBox) nameBox.value = "probe-model";
click(byTag(addDialog || new FakeNode("div"), "button").find((n) => n.textContent.trim() === "创建"));
await settle();
const created = CALLS.find((call) => call.method === "POST" && call.url.includes("/api/routes"));
check("add_model_posts_route", created?.body?.id === "probe-model", JSON.stringify(created?.body));
check("add_model_binds_first_target",
  created?.body?.targets?.length === 1 && created.body.targets[0].upstream_model === "probe-model",
  JSON.stringify(created?.body?.targets));
// 建完直接进编辑态：端点计划通常就是接下来要配的东西；而且必须是**可用**的编辑态
// （saving 没复位的话，按钮写着「保存中…」且全表单禁用）。disabled 为 false 时 dom.js
// 根本不会写这个属性，因此判据是"不为 true"。
check("add_model_enters_editor", Boolean(buttonWithText(host, "保存路由")), host.textContent.slice(0, 160));
check("add_model_editor_is_usable", buttonWithText(host, "保存路由")?.disabled !== true,
  String(buttonWithText(host, "保存路由")?.disabled));

// ═══ 新建失败（如名称已存在）：原因必须留在对话框里，页面不能卡住 ═══
//
// 名称重复是最常见的创建失败（409）。这时对话框不能关闭——它一关，用户看到的就是
// "点了创建、什么都没发生"；创建的 busy 状态也必须复位，否则对话框留在页面上而按钮
// 永远禁着，只能刷新浏览器。
POST_FAILURE = { status: 409, body: { detail: "模型 gpt-5.5 已存在" } };
click(buttonWithText(host, "添加模型名称"));
await settle();
const failedDialog = byClass(document.body, "dialog").at(-1);
const collisionName = byTag(failedDialog || new FakeNode("div"), "input")
  .find((n) => String(n.attrs.placeholder || "").includes("my-model"));
if (collisionName) collisionName.value = "gpt-5.5";
click(byTag(failedDialog || new FakeNode("div"), "button").find((n) => n.textContent.trim() === "创建"));
await settle();
check("add_model_failure_visible", String(failedDialog?.textContent || "").includes("已存在"),
  failedDialog?.textContent?.slice(0, 200));
check("add_model_failure_keeps_dialog", byClass(document.body, "dialog").includes(failedDialog),
  JSON.stringify(byClass(document.body, "dialog").length));
check("add_model_failure_reenables_create",
  byTag(failedDialog || new FakeNode("div"), "button").find((n) => n.textContent.trim() === "创建")?.disabled !== true,
  String(byTag(failedDialog || new FakeNode("div"), "button").find((n) => n.textContent.trim() === "创建")?.disabled));
POST_FAILURE = null;
const closeFailedDialog = byTag(failedDialog || new FakeNode("div"), "button")
  .find((n) => n.textContent.trim() === "取消");
if (closeFailedDialog) click(closeFailedDialog);
await settle();
check("add_model_failure_leaves_page_usable",
  buttonWithText(host, "添加模型名称")?.disabled !== true,
  String(buttonWithText(host, "添加模型名称")?.disabled));

// ═══ 端点计划引用的模型已消失（供应商被删）═══
//
// 配置与模型清单是两次独立取数，中间可能刚好有供应商被删：计划仍指着已随供应商消失的模型。
// 此时必须当成"不覆盖"——否则下拉显示空值，保存却仍把那个名字发出去，被服务端的
// 「引用了未配置的模型」顶回来（用户看到的是"保存无效"，而表单上一切正常）。
MODELS.models = MODELS.models.filter((model) => model.id !== "dall-e-3");
ROUTES[0].endpoints = { image: { primary: { model: "dall-e-3", key: null } } };
// 重新渲染一页（模拟"离开再回来"）：这一页每次进入都重新取数，取到的就是上面这份清单。
const revisit = renderRouting({});
await settle();
click(byClass(revisit, "rail-nav")[0].children[1]);
await settle();
click(buttonWithText(revisit, "编辑"));
await settle();
check("vanished_plan_model_not_shown", !byTag(revisit, "select").some((n) => n.value === "dall-e-3"),
  byTag(revisit, "select").map((n) => n.value).join(","));
CALLS.length = 0;
click(buttonWithText(revisit, "保存路由"));
await settle();
const afterVanish = CALLS.find((call) => call.method === "PUT" && call.url.includes("/api/routes/gpt-5.5"));
check("vanished_plan_model_not_resubmitted",
  afterVanish?.body?.endpoints !== undefined && Object.keys(afterVanish.body.endpoints).length === 0,
  JSON.stringify(afterVanish?.body?.endpoints));

report();