// 访问密钥：分发给外部使用者的受限推理凭据。
//
// 这一页取代了原先的「访客模式」。那把固定 key（`amkr-visitor`）的权限是「所有
// allow_visitor 为真的上游 key」的并集：全局共享、无法按人收窄，出了问题也查不到是
// 谁。访问密钥把这件事变成每把 key 自己的两份清单——它能用哪些供应商、能调哪些模型。
//
// 明文 key 只在本页的**两个动作**后出现：新建与轮换。之后列表只显示指纹；要看旧 key
// 只能翻配置文件。凭据随列表一起发出去，等于每次打开这一页都重新泄漏一遍。

import { h, errorText, copyText, truncate } from "../dom.js";
import { api } from "../api.js";
import {
  card, cardHead, notice, badge, empty, loading, render, toast, buttonNode,
  input, field, dialog, confirmDialog, kv,
} from "../ui.js";

const state = {
  keys: [],
  // 可选项来源：供应商 ID 与被授权模型名。清单校验由服务端最终把关（引用不存在的
  // 目标会被 422 拒掉），这里拉全量是为了让界面能**直接勾选**而不是让人手写名字。
  providerIds: [],
  modelOptions: [],
  revision: null,
  loading: true,
  error: null,
  // editing 为密钥 ID 时该行展开成编辑态；creator 为真时弹新建对话框。
  editing: null,
  saving: false,
};

let host = null;

async function load() {
  const [keys, providers, models] = await Promise.all([
    api.accessKeys(), api.providers(), api.models(),
  ]);
  state.keys = keys.access_keys || [];
  state.revision = keys.config_revision;
  state.providerIds = (providers.providers || []).map((provider) => provider.id);
  // 候选模型名 = 真实 ID + 别名，两者必须能**分别**勾选：清单按调用方写的原始名字逐字
  // 比对（见 config.AccessKeyConfig 的 AllowsModel），选了真实 ID 并不等于放行别名
  // 写法。别名分第二轮加，且撞上某个真实 ID 时不加 note——那个名字本来就指向一个模型。
  // note 是必需的：一屏名字看不出谁是谁的别名。
  const catalog = new Map();
  for (const model of models.models || []) catalog.set(model.id, "");
  for (const model of models.models || []) {
    for (const alias of model.aliases || []) {
      if (!catalog.has(alias)) catalog.set(alias, `别名 · ${model.id}`);
    }
  }
  state.modelOptions = [...catalog].map(([value, note]) => ({ value, note }));
}

// —— 清单选择器 ——
// 供应商与模型都从配置里**现有的名字**里勾选，不再让人手写逗号分隔的字符串。
//
// 为什么手写不可取：清单按调用方写的原始名字逐字比对，拼错一个字符就是某把已经发出去
// 的 key 静默少一项权限，而调用方只看到 403；服务端在写盘时又会逐个校验存在性，所以
// 手写能带来的只有 422。
//
// 为什么必须有一个显式的「不限制」开关：三态里 `[]`（一个都不许）与「没有这份清单」
// （不限制）是两个**相反**的授权，而空的勾选状态同时长得像这两者。把「全部取消勾选」
// 这个明显的收紧动作解读成放开一切，是这类界面里最危险的一种默认（工作空间的模型授权
// 出于同一理由也用了显式开关，见 tasks.js 的 promptWorkspaceModels）。
function scopePicker({ options, selected = [], unrestricted = true, emptyText = "配置里还没有可选项。" }) {
  const chosen = new Set(selected);
  // 候选 = 现有候选项 + 本行已选。已选里可能出现候选之外的名字（模型被改名或删除），
  // 悄悄丢掉它们等于在运维没看见的情况下削减权限，所以并进来并单独标出来。
  const catalog = new Map();
  for (const option of options) {
    const spec = typeof option === "string" ? { value: option } : option;
    if (!catalog.has(spec.value)) catalog.set(spec.value, spec.note || "");
  }
  for (const name of selected) if (!catalog.has(name)) catalog.set(name, "");
  const candidates = [...catalog.keys()].sort((a, b) => a.localeCompare(b, "zh-CN"));
  const existing = new Set(options.map((option) => (typeof option === "string" ? option : option.value)));

  const unrestrictedBox = h("input", { type: "checkbox", checked: unrestricted, onChange: () => drawList() });
  const filterInput = input({
    type: "search", placeholder: "输入关键字筛选", "aria-label": "筛选候选项",
    onInput: () => drawList(),
  });
  const listHost = h("div.chips.picker-list");
  const summary = h("span.muted");
  const allButton = buttonNode("全选", { small: true, variant: "text", onClick: () => setAll(true) });
  const clearButton = buttonNode("清空", { small: true, variant: "text", onClick: () => setAll(false) });

  function setAll(on) {
    if (on) for (const name of candidates) chosen.add(name);
    else chosen.clear();
    drawList();
  }

  function drawList() {
    const locked = unrestrictedBox.checked;
    const keyword = filterInput.value.trim().toLowerCase();
    const visible = candidates.filter((name) => !keyword || name.toLowerCase().includes(keyword));
    listHost.style.opacity = locked ? "0.5" : "1";
    allButton.disabled = locked;
    clearButton.disabled = locked;
    if (!candidates.length) {
      render(listHost, h("span.muted", emptyText));
    } else if (!visible.length) {
      render(listHost, h("span.muted", "没有匹配的候选项。"));
    } else {
      render(listHost, ...visible.map((name) => h(`button.chip${existing.has(name) ? "" : ".is-unknown"}`, {
        type: "button",
        disabled: locked,
        "aria-pressed": String(chosen.has(name)),
        title: existing.has(name) ? (catalog.get(name) || null) : "当前配置里已经没有这个名字",
        onClick: () => {
          if (chosen.has(name)) chosen.delete(name);
          else chosen.add(name);
          drawList();
        },
      }, name)));
    }
    // 摘要走 render 而不是 textContent：探针的 DOM 垫片把 textContent 做成只读的。
    render(summary, locked ? "不限制" : chosen.size ? `已选 ${chosen.size} 项` : "一个都不许");
  }
  drawList();

  return {
    node: h("div.picker", {},
      h("div.picker-head", {},
        h("label.check", {}, unrestrictedBox, "不限制（允许全部）"),
        h("span.spacer"),
        summary,
        allButton,
        clearButton,
      ),
      candidates.length > 8 ? filterInput : null,
      listHost,
    ),
    // 提交值：null = 清除这份清单（不限制）；数组 = 限定为这些（可能为空 = 一个都不许）。
    // 排序只为让写盘结果稳定：这是权限清单，勾选顺序没有含义，而配置 diff 有。
    read: () => (unrestrictedBox.checked ? null : [...chosen].sort()),
  };
}

// scopeText 把一份清单渲染成人读的一行。
//
// 三态必须能区分出来，否则「一个都不许」会被显示成「不限制」——那正好是把一条禁令
// 读反。list 为 undefined 表示配置里没写这个字段。
function scopeText(list) {
  if (list === undefined || list === null) return h("span.muted", "不限制");
  if (!list.length) return badge("一个都不许", "bad");
  return h("span.mono", { title: list.join(", ") }, truncate(list.join(", "), 48));
}

// —— 新建 ——
function openCreate() {
  const nameInput = input({ placeholder: "例如 试用账号 A", required: true });
  const keyInput = input({ placeholder: "留空由服务端生成", autocomplete: "off" });
  const providerPicker = scopePicker({
    options: state.providerIds,
    emptyText: "还没有配置供应商，先到「供应商」页添加。",
  });
  const modelPicker = scopePicker({
    options: state.modelOptions,
    emptyText: "还没有配置模型，先到「供应商」页绑定 Key 的模型。",
  });
  const errorHost = h("div");
  let ref = null;
  let created = null;

  const submit = async () => {
    const name = nameInput.value.trim();
    if (!name) return;
    state.saving = true;
    errorHost.replaceChildren();
    try {
      const result = await api.createAccessKey(state.revision, name, {
        key: keyInput.value.trim() || undefined,
        // 新建时「不传」与「传 null」等价（见 specAccessKeyCreate），这里把 read() 的 null
        // 转成 undefined，让「不限制」在请求体里干脆就是不出现这个字段。
        providers: providerPicker.read() ?? undefined,
        models: modelPicker.read() ?? undefined,
      });
      created = result;
      await load();
      toast(`访问密钥 ${name} 已创建。`);
    } catch (error) {
      render(errorHost, notice(`创建失败: ${errorText(error)}`, "error"));
      state.saving = false;
      return;
    }
    state.saving = false;
    // 建好后换一屏显示明文：这是**唯一**能拿到它的时刻，直接用 toast 一闪而过
    // 等于让用户永远拿不到钥匙。
    ref.close();
    showPlaintext(created, "访问密钥已创建");
    draw();
  };

  const body = h("div.stack", {},
    field("名称", nameInput),
    field("密钥（留空自动生成）", keyInput),
    h("div.field", {}, h("span", "允许的供应商"), providerPicker.node),
    h("div.field", {}, h("span", "允许的模型"), modelPicker.node),
    h("p.muted", "两份清单都在配置里现有的名字里勾选。模型按调用方写的原始名字逐字比对"
      + "（别名解析之前），所以勾了模型 ID 并不等于放行它的别名——调用方会写别名时请一并勾上。"
      + "密钥只写入服务端，之后列表只显示指纹。"),
    errorHost,
  );
  ref = dialog({
    title: "新建访问密钥",
    body,
    actions: [
      { label: "取消", variant: "text", onClick: () => ref.close() },
      { label: "创建", onClick: submit },
    ],
  });
}

// —— 明文展示 ——
// 新建与轮换共用：两者的共同点是「刚生成的明文只在这一刻可见」。
function showPlaintext(result, title) {
  const value = result?.key || "";
  const area = h("textarea.copy-area", { readonly: true, rows: 3 }, value);
  const ref = dialog({
    title,
    closeOnBackdrop: false,
    body: h("div.stack", {},
      notice("这是唯一一次显示明文密钥。关闭后只能看到指纹，需要再取请轮换。", "warn"),
      area,
      kv([
        ["名称", result?.name || "-"],
        ["ID", h("code.mono", result?.id || "-")],
      ]),
    ),
    actions: [
      {
        label: "复制",
        onClick: async () => {
          try {
            await copyText(value);
            toast("已复制到剪贴板。");
          } catch (error) {
            toast(errorText(error), "error");
          }
        },
      },
      { label: "完成", onClick: () => ref.close() },
    ],
  });
}

// —— 编辑（名字 / 启停 / 两份清单）——
function editorRow(key) {
  const nameInput = input({ value: key.name, required: true });
  const enabledInput = h("input", { type: "checkbox", checked: key.enabled });
  const errorHost = h("div");

  // 两份清单交给选择器：字段缺席=「不限制」（选择器里的开关勾着），字段存在（哪怕是
  // 空数组）=「显式写了清单」。这与 specAccessKeyUpdate 的必填三态一一对应——字段不传
  // 会被当成「漏传」而被拒，所以保存时一律显式提交 read() 的结果。
  const providerPicker = scopePicker({
    options: state.providerIds,
    selected: key.providers || [],
    unrestricted: key.providers === undefined,
    emptyText: "还没有配置供应商，先到「供应商」页添加。",
  });
  const modelPicker = scopePicker({
    options: state.modelOptions,
    selected: key.models || [],
    unrestricted: key.models === undefined,
    emptyText: "还没有配置模型，先到「供应商」页绑定 Key 的模型。",
  });

  const save = async () => {
    state.saving = true;
    errorHost.replaceChildren();
    try {
      await api.updateAccessKey(state.revision, key.id, {
        name: nameInput.value.trim(),
        enabled: enabledInput.checked,
        providers: providerPicker.read(),
        models: modelPicker.read(),
      });
      await load();
      state.editing = null;
      toast("访问密钥已更新。");
      draw();
    } catch (error) {
      render(errorHost, notice(`保存失败: ${errorText(error)}`, "error"));
      // 409 是配置版本过期：重新取一遍，让用户看到当前状态再改。
      if (error.status === 409) { await load().catch(() => {}); draw(); }
    }
    state.saving = false;
  };

  return h("tr", {}, h("td", { colspan: "5" },
    h("div.stack", {},
      h("div.form-grid", {},
        field("名称", nameInput),
        field("启用", h("label.check", enabledInput, enabledInput.checked ? "已启用" : "已停用")),
      ),
      h("div.field", {}, h("span", "允许的供应商"), providerPicker.node),
      h("div.field", {}, h("span", "允许的模型"), modelPicker.node),
      h("p.muted", "开着「不限制」= 清除这份清单；关掉它再勾选 = 限定为这些"
        + "（一个都不勾 = 一个都不许）。模型按调用方写的原始名字逐字比对"
        + "（别名解析之前），调用方会写别名时请把别名一并勾上。"),
      errorHost,
      h("div.btn-row", {},
        buttonNode("保存", { disabled: state.saving, onClick: save }),
        buttonNode("取消", { variant: "text", onClick: () => { state.editing = null; draw(); } }),
      ),
    ),
  ));
}

// —— 行 ——
// 手写表格而不是用 table()：编辑态要把**整行**换成一张表单（colspan 铺满），而
// table() 的列渲染是逐单元格的，给不出跨列的行。手写这点代价换来的是一处结构清晰。
function keyRow(key) {
  if (state.editing === key.id) return editorRow(key);
  return h("tr", {},
    h("td", {},
      h("div.stack.tight", {},
        h("strong", key.name),
        h("code.mono", { title: key.id }, truncate(key.id, 16)),
      ),
    ),
    h("td", {}, h("span.inline", {},
      badge(key.enabled ? "已启用" : "已停用", key.enabled ? "good" : "muted"),
      h("code.mono", key.key_fingerprint || "-"),
    )),
    h("td", {}, scopeText(key.providers)),
    h("td", {}, scopeText(key.models)),
    h("td", {}, actionButtons(key)),
  );
}

function actionButtons(key) {
  return h("div.btn-row", {},
    buttonNode("编辑", {
      small: true, variant: "secondary",
      onClick: () => { state.editing = key.id; draw(); },
    }),
    buttonNode("轮换", {
      small: true, variant: "secondary",
      onClick: () => confirmDialog({
        title: "轮换访问密钥",
        message: `将换掉「${key.name}」的密钥，旧密钥立刻失效。请准备把新密钥交给使用者。`,
        confirmLabel: "轮换",
        onConfirm: () => rotate(key),
      }),
    }),
    buttonNode("删除", {
      small: true, variant: "danger",
      onClick: () => confirmDialog({
        title: "删除访问密钥",
        message: `将删除「${key.name}」，使用它的调用方会立刻收到 401。此操作不可撤销。`,
        confirmLabel: "删除",
        danger: true,
        onConfirm: () => remove(key),
      }),
    }),
  );
}

// keyTable 渲染整张表（含表头），行来自 state.keys。
function keyTable() {
  return h("div.table-scroll", {},
    h("table.table", {},
      h("thead", {}, h("tr", {},
        h("th", "名称"),
        h("th", "状态 / 指纹"),
        h("th", "允许的供应商"),
        h("th", "允许的模型"),
        h("th", "操作"),
      )),
      h("tbody", {}, state.keys.map((key) => keyRow(key))),
    ),
  );
}

async function rotate(key) {
  try {
    const result = await api.rotateAccessKey(state.revision, key.id);
    await load();
    showPlaintext({ ...result, name: key.name }, "访问密钥已轮换");
    draw();
  } catch (error) {
    toast(`轮换失败: ${errorText(error)}`, "error");
    if (error.status === 409) { await load().catch(() => {}); draw(); }
  }
}

async function remove(key) {
  try {
    await api.deleteAccessKey(state.revision, key.id);
    await load();
    toast(`访问密钥 ${key.name} 已删除。`);
    draw();
  } catch (error) {
    toast(`删除失败: ${errorText(error)}`, "error");
    if (error.status === 409) { await load().catch(() => {}); draw(); }
  }
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div.page-title", {},
        h("h1", "访问密钥"),
        h("p.sub", "分发给外部使用者的受限凭据。每把密钥限定可用哪些供应商与哪些模型。"),
      ),
      h("div.page-actions", {},
        buttonNode("新建访问密钥", { iconName: "key", onClick: openCreate }),
      ),
    ),
  ];

  if (state.error) children.push(notice(`读取失败：${state.error}`, "error"));

  if (state.loading) {
    children.push(card(cardHead("访问密钥"), loading("正在读取…")));
    render(host, children);
    return;
  }

  children.push(card(
    cardHead("访问密钥",
      badge(`${state.keys.length} 把`, "muted"),
    ),
    state.keys.length
      ? keyTable()
      : empty("还没有访问密钥。", {
          icon: "key",
          hint: "新建一把并把它交给使用者，它只能调用你在这把密钥上列出的供应商与模型。",
        }),
    h("div.card-foot", {},
      "明文密钥只在创建与轮换时显示一次。需要重新获取请轮换——旧密钥会立刻失效。"
      + "未配置清单表示不限制；配成空清单表示一个都不许。"),
  ));

  render(host, children);
}

// render 必须是**同步**的：app.js 直接把它返回的节点 mount 进 #content，写成 async
// 就是返回一个 Promise，会被当成子节点渲染成整页的 "[object Promise]"。取数在后台
// 完成后自行重绘，与其它页面同一约定。
export function renderAccessKeys(context) {
  host = h("div.stack");
  state.loading = true;
  state.error = null;
  draw();
  load()
    .catch((error) => { state.error = errorText(error); })
    .finally(() => { state.loading = false; draw(); });
  return host;
}
