// 全局筛选：按**供应商**与**访问密钥**收窄整页读数。
//
// 为什么单独成一个模块：概览与用量统计两页共用同一套筛选器，各写一份迟早会在标签、
// 空态与失败回退上长得不一样。选中值挂在外壳 store 上（app.js 的 store.providerFilters /
// store.accessKeyFilters），因此在一页选好的条件切到另一页仍然生效——这正是「全局筛选」
// 的意思。
//
// 语义：**维度内多选是 OR，维度间是 AND**。「供应商 = openai + azure」是这两家的量之和，
// 再加上「访问密钥 = ak1」就是「ak1 打到这两家的量」。后端把重复出现的同名参数收成一个
// IN 列表（见 /ui/filtered-metrics.json）。
//
// 控件形态：每个维度一个下拉（原生 <details>），收起时只占一行摘要——选项随配置增长
// （十几把访问密钥很常见），一排 chip 折成三四行会把页头撑得比看板还高。展开后仍是同一批
// chip（复用 .segment 的样式与按下态），第一枚「全部」= 该维度不设限。
//
// 状态不落在 DOM 上：选中值读 store，展开状态记在 openDimensions 里，因此页面每 15 秒整块
// 重绘、以及选中一项触发的整页重绘，都不会把用户正在操作的下拉合上或把已选条件抹掉。
//
// 与「按 Key 拆分」那几张卡的关系：只要**任一维度**筛选生效，按访问密钥、按上游 Key、
// 模型 × 上游 Key 这三张表就不再回答全实例的问题，由各页在筛选态收起（见各页的 draw）。
// 本模块只管「选了哪些」，不替页面决定显示什么。

import { h } from "./dom.js";
import { api } from "./api.js";
import { render } from "./ui.js";

// 每个维度最近一次读到的选项清单，给下一次绘制当占位用。清单是配置的投影，读它只是一次
// 本地调用；长期缓存住会让「刚新建的供应商 / 密钥不出现在筛选器里」一直持续到整页刷新，
// 那比每次绘制多一次读取难解释得多。并发重绘共用同一次读取（见 metricFilterBar）。
let providerOptions = [];
let accessKeyOptions = [];
// loadingOptions 表示一对选项读取正在途中（去重理由见 metricFilterBar）。
let loadingOptions = false;
// repaintLatest 是**最近一次**绘制出来的筛选器的重画入口。选项读回来时要重画当前那一块
// 而不是发起读取的那一块：读取在途时页面可能又整体重绘过，发起者已经被换掉了。
let repaintLatest = null;

// labelOf 是「配置里的显示名 + key_id」：显示名可以重名，key_id 才是稳定标识符。
// 没有显示名（配置里已删掉这把 key）时只给 id，不回填一个看起来像名字的东西。
function labelOf(key) {
  return key.name ? `${key.name}（${key.id}）` : key.id;
}

// loadOptions 读一次两个维度的选项；单个维度读取失败时回 null，调用方保留上一次的清单
// ——一次失败不该让筛选器清空，更不该把用户已经选好的条件悄悄抹掉。
//
// 两者一起并发读：它们是同一个页头的两半，谁慢谁快都该一起换上去。
function loadOptions() {
  return Promise.all([
    api.providers().then((data) => (data?.providers || []).map((item) => ({ value: item.id, label: item.id }))).catch(() => null),
    api.accessKeys().then((data) => (data?.access_keys || []).map((key) => ({ value: key.id, label: labelOf(key) }))).catch(() => null),
  ]);
}

// —— 选中值 ——
// store 上的两个数组是本模块与页面的唯一契约：页面不解析 DOM，模块不解析 URL。

export function selectedProviders(store) {
  return Array.isArray(store?.providerFilters) ? store.providerFilters : [];
}

export function selectedAccessKeys(store) {
  return Array.isArray(store?.accessKeyFilters) ? store.accessKeyFilters : [];
}

// metricFilterActive 是「有没有在筛」的唯一判据：页面据此决定收不收那三张按 Key 拆分的
// 卡、走不走 /ui/filtered-metrics.json。
export function metricFilterActive(store) {
  return selectedProviders(store).length > 0 || selectedAccessKeys(store).length > 0;
}

// filterNote 是筛选态下要显示在页面上的说明；未筛选时返回 null。
//
// 措辞必须点明这套筛选的真实语义：**同一组内选中任一项都算命中（OR），两组之间都要命中
// （AND）**。只写「条件同时成立」会把「供应商 = openai + azure」读成「同时是这两家」，
// 与页面上的数字（两家的量之和）正好相反。文案走纯文本渲染（notice 不做 markdown），
// 所以这里不能写 **强调**。
export function filterNote(store) {
  const providers = labelList(selectedProviders(store), providerOptions);
  const accessKeys = labelList(selectedAccessKeys(store), accessKeyOptions);
  if (!providers && !accessKeys) return null;
  if (providers && accessKeys) {
    return `已按供应商「${providers}」与访问密钥「${accessKeys}」筛选：`
      + "本页读数只含两组条件都命中的调用（同一组内选中任一项即可）。";
  }
  if (providers) return `已按供应商「${providers}」筛选：本页读数只含所选供应商的调用。`;
  return `已按访问密钥「${accessKeys}」筛选：本页读数只含所选密钥发起的调用。`;
}

// labelList 把选中的 id 翻成显示名；清单里已经没有的 id（配置里删掉了）直接显示 id，
// 不假装它不存在——那正是用户此刻在筛的东西。
function labelList(values, options) {
  if (!values.length) return "";
  return values
    .map((value) => options.find((item) => item.value === value)?.label || value)
    .join("、");
}

// —— 控件 ——

// openDimensions 记录哪个维度的下拉是展开的（页面重绘会重建整块筛选器，不记就会合上）。
//
// 同时只留一个：两个维度都收在操作区右侧，面板又都是从右边缘向左铺开，一起展开时后开的
// 必然盖住先开的（不选任何条件时两个摘要都很短，两个面板一定重叠）。立即互斥由 details 的
// name 交给浏览器（见下面 filterSelect），这个集合只负责把"当前开着哪个"带过重绘。
// 收起面板不影响已选条件——条件在 store 里，不在这个集合里。
const openDimensions = new Set();

// filterSelect 画一个维度的下拉。
//
// 第一枚 chip 固定为「全部」：它代表**该维度不设限**（选中值清空），所以它在选中值为空时
// 处于按下态。各选项 chip 之间互相独立，可以同时按下——这就是维度内多选。
function filterSelect(dimension, label, options, selected, onChange) {
  const chips = [
    h("button.segment", {
      type: "button",
      "data-value": "",
      "aria-pressed": String(selected.length === 0),
      title: `不按${label}筛选`,
      onClick: () => onChange([]),
    }, "全部"),
  ];
  // 选中的值即使已从清单里消失也要留一枚 chip：历史用量不会因为删配置而消失，
  // 悄悄丢掉它会让筛选器显示的范围与页面上的数字对不上，用户也没法把它取消。
  const values = options.slice();
  for (const value of selected) {
    if (!values.some((item) => item.value === value)) values.push({ value, label: value });
  }
  for (const item of values) {
    const pressed = selected.includes(item.value);
    chips.push(h("button.segment", {
      type: "button",
      "data-value": item.value,
      "aria-pressed": String(pressed),
      title: item.label,
      onClick: () => onChange(pressed
        ? selected.filter((value) => value !== item.value)
        : selected.concat(item.value)),
    }, item.label));
  }
  // 收起时的摘要与面板里同一份清单：名字再长也只占一行（CSS 省略号，完整内容在 title）。
  const summaryText = selected.length ? labelList(selected, values) : "全部";
  return h("div.filter-group", { "data-dimension": dimension },
    h("span.filter-label", label),
    h("details.filter-select", {
      // name 让浏览器自己当手风琴：点开一个会**立即**关掉另一个（同一个 name 分组），
      // 不依赖下一次重绘。没有这个属性时，另一个面板要等重绘才会收起来，而在那之前
      // 它一直盖在刚打开的面板上（后者 DOM 在后、z-index 相同），看起来像"点了没反应"。
      name: "amkr-metric-filter",
      // true 会写成 open 属性（见 dom.js 的 applyProps），false 则完全不写。
      open: openDimensions.has(dimension),
      onToggle: (event) => {
        const node = event.currentTarget;
        if (!node?.open) { openDimensions.delete(dimension); return; }
        // 浏览器把这个维度打开时也会给被关掉的那个派发 toggle（自己的 handler 会删掉自己），
        // 这里的 clear() 兜住不认 name 的浏览器：那种情况下互斥要等下一次重绘才生效。
        openDimensions.clear();
        openDimensions.add(dimension);
      },
    },
      h("summary.filter-summary", { title: `按${label}筛选：${summaryText}` },
        h("span.filter-summary-text", {}, summaryText)),
      h("div.segmented", { role: "group", "aria-label": `按${label}筛选` }, chips),
    ),
  );
}

// metricFilterBar 画出两个维度的筛选控件；用户改变选择时写回 store 并回调 onChange。
//
// 每次绘制都重读选项（理由见 loadOptions）：先用上一次的清单画一次（首屏不闪空），
// 读回来再画一次。页面整块重绘时旧节点会被换掉，这里重画那个游离节点没有副作用。
export function metricFilterBar(store, onChange) {
  const box = h("div.metric-filters");
  const apply = (nextProviders, nextAccessKeys) => {
    store.providerFilters = nextProviders;
    store.accessKeyFilters = nextAccessKeys;
    onChange?.();
  };
  const paint = () => {
    render(box,
      filterSelect("provider", "供应商", providerOptions, selectedProviders(store),
        (values) => apply(values, selectedAccessKeys(store))),
      filterSelect("access-key", "访问密钥", accessKeyOptions, selectedAccessKeys(store),
        (values) => apply(selectedProviders(store), values)),
    );
  };
  paint();
  repaintLatest = paint;
  // 在途时跳过这一轮的读取：页面首屏会连续重绘多次（实测开机到首个快照之间 10 次），
  // 不去重就是 10 对本地配置读取。不补发的理由——结果回来时用的就是那一刻的清单，
  // 中间那几次重绘要的正是这份数据；下一次绘制（轮询或用户操作）自会再读一次，
  // 新键值最多晚一个绘制周期出现。
  if (!loadingOptions) {
    loadingOptions = true;
    loadOptions()
      .then(([providers, accessKeys]) => {
        if (providers) providerOptions = providers;
        if (accessKeys) accessKeyOptions = accessKeys;
        if (providers || accessKeys) repaintLatest?.();
      })
      .finally(() => { loadingOptions = false; });
  }
  return box;
}
