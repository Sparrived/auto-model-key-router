// 全局筛选：按访问密钥收窄整页读数。
//
// 为什么单独成一个模块：概览与用量统计两页共用同一个筛选器，各写一份迟早会在标签、
// 空态与失败回退上长得不一样。选中值挂在外壳 store 上（app.js 的 store.accessKeyFilter），
// 因此在一页选好的 key 切到另一页仍然生效——这正是「全局筛选」的意思。
//
// 与「按 Key 拆分」那几张卡的关系：筛选到一把 key 之后，按访问密钥拆分的表只剩一行，
// 按上游 Key、模型 × 上游 Key 的表也不再回答全实例的问题，所以页面在筛选态把它们收起来
// （见各页的 draw）。本模块只管「选了哪把」，不替页面决定显示什么。

import { h } from "./dom.js";
import { api } from "./api.js";
import { select, render } from "./ui.js";

const ALL = { value: "", label: "全部访问密钥" };

// options 是**上一次读到的**密钥清单，只在两次读取之间当占位用。清单是配置的投影，
// 读它只是一次本地调用；长期缓存住会让「刚新建的 key 不出现在筛选器里」一直持续到
// 整页刷新，那比每次绘制多一次请求难解释得多。
let options = [ALL];

// labelOf 是「配置里的显示名 + key_id」：显示名可以重名，key_id 才是稳定标识符。
// 没有显示名（配置里已删掉这把 key）时只给 id，不回填一个看起来像名字的东西。
function labelOf(key) {
  return key.name ? `${key.name}（${key.id}）` : key.id;
}

// loadOptions 读一次密钥清单；读取失败回 null，调用方保留上一次的清单——一次失败不该
// 让筛选器清空，更不该把用户已经选好的筛选悄悄弹回「全部」。
function loadOptions() {
  return api.accessKeys()
    .then((data) => (data?.access_keys || []).map((key) => ({ value: key.id, label: labelOf(key) })))
    .catch(() => null);
}

// selectedAccessKey 返回当前筛选的访问密钥 id（"" 表示不筛选）。
export function selectedAccessKey(store) {
  return store?.accessKeyFilter || "";
}

// filterNote 是筛选态下要显示在页面上的说明；未筛选时返回 null。
//
// 必须说清「收起来的那些卡去哪了」：Key 维度的明细在筛选态被收起，不写一句的话，
// 用户会以为是页面出了问题。
export function filterNote(store) {
  const selected = selectedAccessKey(store);
  if (!selected) return null;
  const option = options.find((item) => item.value === selected);
  return `已按访问密钥「${option ? option.label : selected}」筛选：本页读数只含这把密钥发起的调用。`;
}

// accessKeyFilterNode 画出筛选控件；用户改变选择时写回 store 并回调 onChange。
export function accessKeyFilterNode(store, onChange) {
  const box = h("div.access-key-filter");
  const paint = (items) => {
    const selected = selectedAccessKey(store);
    // 选中的 key 已从配置里删掉时仍要出现在选项里：历史用量不会因为删配置而消失，
    // 悄悄弹回「全部」会让筛选器显示的范围与页面上的数字对不上。
    const list = selected && !items.some((item) => item.value === selected)
      ? items.concat({ value: selected, label: selected })
      : items;
    render(box,
      h("span", "访问密钥"),
      select(list, {
        value: selected,
        "aria-label": "按访问密钥筛选",
        title: "只统计这把访问密钥发起的调用",
        onChange: (event) => {
          store.accessKeyFilter = event.target.value;
          onChange?.(store.accessKeyFilter);
        },
      }),
    );
  };
  // 先用上一次的清单画一次（首屏不闪空），读回来再画一次。页面整块重绘时旧节点会
  // 被换掉，这里重画那个游离节点没有副作用。
  paint(options);
  loadOptions().then((keys) => {
    if (!keys) return;
    options = [ALL].concat(keys);
    paint(options);
  });
  return box;
}
