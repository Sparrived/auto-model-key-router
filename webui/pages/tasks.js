// 任务路由：把「模型 + 固定采样参数」打包成一个可直接当 model 传的任务名。
//
// 调用方传 model: "TASK_XXXXXX" 即可命中；因为参数由任务固定，调用方再传
// temperature 这类采样参数会被服务端拒绝（reasoning_effort 也一样，不是例外）。
//
// 任务名只在**工作空间**内唯一：不同空间可以各有一个同名任务。界面因此始终工作在
// 一个具体空间里（默认空间即「不带 X-AMKR-Workspace 头」的那个），顶部下拉切换。

import { h, mount, errorText, copyText } from "../dom.js";
import { api } from "../api.js";
import { card, cardHead, notice, badge, empty, loading, render, toast, buttonNode, input, select, confirmDialog, dialog, kv } from "../ui.js";

const EFFORTS = [
  { value: "", label: "不固定" },
  { value: "none", label: "none" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

// 数值型固定参数：标签 + 是否整数（top_k / seed / max_tokens 必须是整数）。
const NUMERIC_PARAMS = [
  { key: "temperature", label: "temperature" },
  { key: "top_p", label: "top_p" },
  { key: "top_k", label: "top_k", integer: true },
  { key: "frequency_penalty", label: "frequency_penalty" },
  { key: "presence_penalty", label: "presence_penalty" },
  { key: "seed", label: "seed", integer: true },
  { key: "max_tokens", label: "max_tokens", integer: true },
];

const PARAM_LABELS = Object.fromEntries([
  ...NUMERIC_PARAMS.map((param) => [param.key, param.label]),
  ["stop", "stop"],
  ["reasoning_effort", "reasoning_effort"],
]);

// DEFAULT_WORKSPACE 与 config.DefaultWorkspace 一致：调用方不带 X-AMKR-Workspace
// 头时命中的那个空间。界面上必须能选到它，哪怕它当前一个任务都没有。
const DEFAULT_WORKSPACE = "default";

// 记住用户选的空间：刷新或切页回来时不必重新选。任务路由是团队各自维护的，
// 每次回到页面都被重置成默认空间会很烦人。
const WORKSPACE_STORAGE = "amkr.workspace";

function storedWorkspace() {
  try {
    return localStorage.getItem(WORKSPACE_STORAGE) || DEFAULT_WORKSPACE;
  } catch {
    // 隐私模式下 localStorage 可能直接抛异常；退回默认空间即可，不影响功能。
    return DEFAULT_WORKSPACE;
  }
}

function rememberWorkspace(workspace) {
  try {
    localStorage.setItem(WORKSPACE_STORAGE, workspace);
  } catch {
    // 存不下就算了：只影响下次进页面时的默认选中项。
  }
}

const state = {
  tasks: [],
  models: [],
  // workspaces 是 {name, task_count} 的列表，来自 GET /api/workspaces。
  workspaces: [],
  workspace: storedWorkspace(),
  revision: null,
  loading: true,
  error: null,
  editing: null,
  saving: false,
};

let host = null;
// 首屏会被连续渲染两次（挂载 + 首次指标刷新），于是有两个 load 在途。没有这个
// 令牌的话，先发的请求晚回来时会重绘整个页面：用户此时若已经点开编辑器，正在
// 手填的参数就被悄悄冲掉了。
let paintToken = 0;
// workspacePinned 表示当前空间是用户**自己选的**（或刚输入的新名字），不是从
// localStorage 恢复的。只有恢复来的名字才需要「它还在不在」的校验：用户输入的新
// 空间名天然不在目录里（空空间不进目录），拿目录去纠正它会把刚输入的名字吃掉。
let workspacePinned = false;

async function load() {
  // 目录要先于任务取：任务列表是**按空间过滤**的，若恢复出来的空间已经被删空，
  // 列表会是空的，得先把选择纠正回一个真实存在的空间。
  const workspaces = await api.workspaces();
  state.workspaces = workspaces.workspaces || [];
  if (!workspacePinned) {
    const names = state.workspaces.map((item) => item.name);
    // 默认空间永远在清单里，因此这里一定落到一个真实存在的空间上。
    if (!names.includes(state.workspace)) {
      state.workspace = names.includes(DEFAULT_WORKSPACE) ? DEFAULT_WORKSPACE : (names[0] || DEFAULT_WORKSPACE);
      rememberWorkspace(state.workspace);
    }
  }

  const [tasks, models] = await Promise.all([api.tasks(state.workspace), api.models()]);
  state.tasks = tasks.tasks || [];
  state.revision = tasks.config_revision ?? models.config_revision;
  state.models = models.models || [];
}

// switchWorkspace 换到另一个空间：清掉编辑态，重新读该空间的任务。
//
// editing 必须清掉：编辑器里那个任务属于**原来**的空间，留着它再点保存会打到
// 新空间去（甚至因为重名而覆盖新空间里的同名任务）。
async function switchWorkspace(workspace) {
  if (workspace === state.workspace) return;
  state.workspace = workspace;
  workspacePinned = true;
  rememberWorkspace(workspace);
  state.editing = null;
  // 先把旧任务清空再重绘：否则在新空间的名字底下会短暂列着上一个空间的任务，
  // 而每张卡片都带「删除」——看错空间按下去删的就是另一个空间的任务。
  state.tasks = [];
  state.error = null;
  draw();
  try {
    await load();
  } catch (error) {
    state.error = errorText(error);
  }
  draw();
}

function summary(task) {
  const names = Object.keys(task.params || {});
  return names.length ? names.map((name) => PARAM_LABELS[name] || name).join("、") : "全部透传";
}

// 逗号分隔的 stop 序列；空字符串表示不固定该参数。
// task 为 null 表示"新建"（taskEditor(null)），因此必须对 task 本身做可选链：
// 只写 task.params?.stop 会在新建时抛 TypeError，让整个编辑器画不出来。
const stopText = (task) => (task?.params?.stop || []).join(", ");

function taskEditor(task) {
  const isNew = !task;
  const nameInput = input({
    value: task?.name || "",
    placeholder: "TASK_000001",
    disabled: !isNew || state.saving,
  });
  // 显示名是可选的、纯展示用的中文名；留空时界面回落到任务名。
  const displayInput = input({
    value: task?.display_name || "",
    placeholder: "例如：长文摘要",
    disabled: state.saving,
  });
  // 既有任务没有模型时**不能**回落到第一个模型：那会在用户没碰下拉的情况下把
  // 一个占位任务悄悄绑上某个模型。只有新建任务才预选第一个模型。
  let model = task ? task.model || "" : state.models[0]?.id || "";
  let fallback = task?.fallback_model || "";

  // 首选可以为空（尚未指定模型）。空值必须是一个真实选项：没有它，用户就没法把
  // 既有任务改回占位状态，新建时也被迫先挑一个模型。
  const UNSET = { value: "", label: "（尚未指定）" };
  const modelOptions = () => [UNSET].concat(state.models.map((item) => ({ value: item.id, label: item.id })));
  const fallbackOptions = (value) => [{ value: "", label: "不启用备选" }].concat(
    modelOptions().filter((option) => option.value !== value && option.value !== ""),
  );

  const fallbackSelect = select(fallbackOptions(model), {
    value: fallback,
    // 未指定首选时备选无处可退（服务端也会拒绝），因此直接锁掉。
    disabled: state.saving || !model,
  });
  const primarySelect = select(modelOptions(), {
    value: model,
    disabled: state.saving,
    onChange: (event) => {
      model = event.target.value;
      // 首选换成了当前的备选时清掉备选，避免两者相同。
      if (fallback === model) fallback = "";
      // 首选清空后备选失去意义，一并清掉并锁住下拉。
      if (!model) { fallback = ""; fallbackSelect.disabled = true; } else { fallbackSelect.disabled = state.saving; }
      mount(fallbackSelect, ...fallbackOptions(model).map((option) =>
        h("option", { value: option.value, selected: option.value === fallback }, option.label)));
    },
  });
  fallbackSelect.addEventListener("change", (event) => { fallback = event.target.value; });

  const numberInputs = {};
  for (const param of NUMERIC_PARAMS) {
    const value = task?.params?.[param.key];
    numberInputs[param.key] = input({
      value: value === undefined || value === null ? "" : String(value),
      placeholder: "留空表示不固定",
      inputmode: "decimal",
      disabled: state.saving,
    });
  }
  const stopInput = input({
    value: stopText(task),
    placeholder: "逗号分隔，留空表示不固定",
    disabled: state.saving,
  });
  const effortSelect = select(EFFORTS, { value: task?.params?.reasoning_effort || "", disabled: state.saving });

  const errorHost = h("div");
  // 保存期间要锁住的控件：请求发出后用户再改也不会被带上，与其让人以为改了，
  // 不如先禁用。任务名不在其中——编辑既有任务时它本来就一直是只读的。
  const lockable = [displayInput, primarySelect, fallbackSelect, effortSelect, stopInput, ...Object.values(numberInputs)];

  // 只读取用户填过的参数；留空 = 不写进配置，调用方可以自己传。
  const collectParams = () => {
    const params = {};
    for (const param of NUMERIC_PARAMS) {
      const raw = numberInputs[param.key].value.trim();
      if (!raw) continue;
      const value = Number(raw);
      if (!Number.isFinite(value)) throw new Error(`${param.label} 必须是数字`);
      if (param.integer && !Number.isInteger(value)) throw new Error(`${param.label} 必须是整数`);
      params[param.key] = value;
    }
    const stop = stopInput.value.split(",").map((item) => item.trim()).filter(Boolean);
    if (stop.length) params.stop = stop;
    if (effortSelect.value) params.reasoning_effort = effortSelect.value;
    return params;
  };

  // 保存时只改按钮自己的状态，不重绘表单：重绘会丢掉用户刚填的参数，也会把
  // 下面的 errorHost 从文档里摘掉，于是报错写进一个没人看得见的节点。
  const cancelButton = buttonNode("取消", {
    variant: "text",
    onClick: () => { state.editing = null; draw(); },
  });
  const saveButton = buttonNode("保存任务", {
    variant: "primary",
    onClick: async () => {
      let params;
      try {
        params = collectParams();
      } catch (error) {
        render(errorHost, notice(error.message, "error"));
        return;
      }
      const name = isNew ? nameInput.value.trim() : task.name;
      if (!name) { render(errorHost, notice("请填写任务名。", "error")); return; }
      if (fallback && fallback === model) { render(errorHost, notice("备选模型不能与首选模型相同。", "error")); return; }
      if (fallback && !model) { render(errorHost, notice("未指定首选模型时不能设置备选模型。", "error")); return; }
      const displayName = displayInput.value.trim();

      render(errorHost);
      state.saving = true;
      saveButton.disabled = true;
      // 保存途中不许取消或改表单：请求已经在路上，此时丢掉编辑器只会让人以为没保存。
      cancelButton.disabled = true;
      for (const node of lockable) node.disabled = true;
      mount(saveButton, "保存中…");
      try {
        // 清空的字段要发 null（而不是省略）：服务端把「键存在 + null」当作清空，
        // 省略则是不改。任务名不在其中——它不是可更新的字段。
        const payload = {
          model: model || null,
          display_name: displayName || null,
          fallback_model: fallback || null,
          params,
        };
        if (isNew) {
          await api.createTask(state.revision, state.workspace, { name, ...payload });
        } else {
          await api.updateTask(state.revision, state.workspace, name, payload);
        }
        await load();
        state.editing = null;
        state.saving = false;
        toast("任务路由已保存。");
        draw();
        return;
      } catch (error) {
        render(errorHost, notice(`保存失败: ${errorText(error)}`, "error"));
        // 版本冲突说明手上的配置已过期，重新读一次再让用户重试。
        if (error.status === 409) await load().catch(() => {});
      }
      state.saving = false;
      saveButton.disabled = false;
      cancelButton.disabled = false;
      for (const node of lockable) node.disabled = false;
      mount(saveButton, "保存任务");
    },
  });

  return h("div.stack", {},
    h("div.form-grid", {},
      h("label.field", h("span", "任务名"), nameInput),
      h("label.field", h("span", "显示名称（可选）"), displayInput),
      h("label.field", h("span", "首选模型"), primarySelect),
      h("label.field", h("span", "备选模型"), fallbackSelect),
      h("label.field", h("span", "推理强度"), effortSelect),
    ),
    h("p.muted", "任务名就是调用方传的 model；显示名称只用于在这个页面上辨认任务，不影响调用。" +
      "任务名不能与模型 ID 或别名撞名。"),
    h("p.muted", "首选模型可以先留空：任务会先作为占位存在，此时调用它会被明确拒绝并提示尚未指定模型，而不是落到别的模型上。"),
    h("h4", "固定采样参数"),
    h("p.muted", "填了的参数由任务说了算：调用方再传同名参数（含 reasoning_effort）会被直接拒绝。留空的参数照常透传，不在这个列表里的参数也始终透传。"),
    h("div.form-grid", {}, NUMERIC_PARAMS.map((param) => h("label.field", h("span", param.label), numberInputs[param.key]))),
    h("label.field", h("span", "stop（多个用逗号分隔）"), stopInput),
    errorHost,
    h("div.btn-row", {},
      saveButton,
      cancelButton,
    ),
  );
}

export function renderTasks(context) {
  host = h("div.stack");
  if (state.loading) {
    render(host, h("div.page-head", h("h1", "任务路由")), loading("正在读取任务路由。"));
    const token = ++paintToken;
    (async () => {
      try { await load(); state.error = null; } catch (error) { state.error = errorText(error); }
      // 已有更新的一轮在读，让那轮负责落笔。
      if (token !== paintToken) return;
      state.loading = false;
      draw();
    })();
    return host;
  }
  draw();
  return host;
}

// workspaceSwitcher 是页面顶部的空间切换下拉 + 「新建空间」入口 + 改名/删除。
//
// 空间有两条产生路径，界面上都在这里：
//   - 「＋ 新建工作空间…」是**显式创建**（POST /api/workspaces）：空间立刻写进配置、
//     服务端生成一个面板 key 并**只在那一次响应里**回明文，本页会当场把它显示出来
//     让人复制（关掉就再也拿不到了）。
//   - 任务也可以在别的空间里隐式产生，因此下拉始终跟着 GET /api/workspaces 的目录走。
//
// 改名与删除是**空间自身**的操作（PUT/DELETE /api/workspaces/{name}），与任务无关：
// 改名把整组任务（连同 api_key）一起搬到新名字下，删除连同组内任务一起删。两者对
// 默认空间都不可用：默认空间是不带 X-AMKR-Workspace 头的调用方命中的那个，删掉或
// 改名会让所有人落空。
function workspaceSwitcher() {
  const options = state.workspaces.map((item) => ({
    value: item.name,
    // 带上任务数：空空间与有任务的空间在下拉里长得一样会让人选错。
    label: item.name === DEFAULT_WORKSPACE
      ? `${item.name}（默认 · ${item.task_count}）`
      : `${item.name}（${item.task_count}）`,
  }));
  const current = state.workspace;
  // 当前空间不在目录里，说明它是刚被删空/改名，或本地记着一个早已不存在的名字。
  // 补一项进去，否则下拉会显示成默认空间、与页面上的空列表对不上。
  const saved = options.some((option) => option.value === current);
  if (!saved) {
    options.push({ value: current, label: `${current}（未写入配置）` });
  }
  // 显式创建入口就挂在下拉里：选中它即弹名字输入框，创建成功后当场显示面板 key。
  options.push({ value: NEW_WORKSPACE, label: "＋ 新建工作空间…" });

  // 没写进配置的空间（未落地的名字）没有可改可删的分组，两个动作都禁用。
  const actionable = saved && current !== DEFAULT_WORKSPACE;

  return h("div.inline", {},
    select(options, {
      value: current,
      disabled: state.saving,
      // 带一个可识别的类名：编辑器里还有首选/备选/推理强度三个下拉，探针需要一条
      // 可靠的方式区分「页面级的工作空间切换」与「编辑器里的字段」。
      class: "workspace-select",
      onChange: (event) => {
        if (event.target.value === NEW_WORKSPACE) {
          promptNewWorkspace();
          return;
        }
        switchWorkspace(event.target.value);
      },
    }),
    buttonNode("模型授权", {
      small: true,
      variant: "text",
      class: "workspace-models",
      disabled: state.saving || !actionable,
      title: actionable
        ? "设定这个空间允许直呼哪些模型"
        : "默认空间不能配置模型清单（它没有作用域凭据）",
      onClick: () => promptWorkspaceModels(current),
    }),
    buttonNode("改名", {
      small: true,
      variant: "text",
      class: "workspace-rename",
      disabled: state.saving || !actionable,
      title: current === DEFAULT_WORKSPACE ? "默认工作空间不能改名" : null,
      onClick: () => promptRenameWorkspace(current),
    }),
    buttonNode("删除", {
      small: true,
      variant: "text",
      class: "workspace-delete",
      disabled: state.saving || !actionable,
      title: current === DEFAULT_WORKSPACE ? "默认工作空间不能删除" : null,
      onClick: () => confirmDeleteWorkspace(current),
    }),
  );
}

// NEW_WORKSPACE 是下拉里「新建工作空间…」那一项的哨兵值。
//
// 不可能与真实空间名冲突：空间名来自配置的 workspaces 键，而它不可能为空串——
// config 层对空名直接报「工作空间名不能为空」。
const NEW_WORKSPACE = "";

// promptNewWorkspace 询问一个新空间名并**显式创建**它，随后显示面板 key。
//
// 用 dialog 而不是 window.prompt：创建会返回一个只能看一次的凭据，把它放在一个
// 不能被复制的系统弹窗里没有意义。
function promptNewWorkspace() {
  const nameInput = input({
    placeholder: "例如 teamA",
    "aria-label": "工作空间名",
  });
  const errorHost = h("div");
  const ref = dialog({
    title: "新建工作空间",
    body: h("div.stack", {},
      h("p.muted", "空间创建后会立刻写入配置，并生成两把只属于它的 key："
        + "面板 key 交给要嵌入工作空间面板的应用，推理 key 交给要调用 /v1 的项目。"),
      nameInput,
      errorHost,
    ),
    actions: [{
      label: "创建",
      variant: "primary",
      onClick: async () => {
        const trimmed = nameInput.value.trim();
        if (!trimmed) { render(errorHost, notice("工作空间名不能为空。", "error")); return; }
        if (trimmed === DEFAULT_WORKSPACE) {
          render(errorHost, notice(`「${DEFAULT_WORKSPACE}」是默认工作空间，不能重名。`, "error"));
          return;
        }
        try {
          const created = await api.createWorkspace(state.revision, trimmed);
          ref.close();
          await load();
          await switchWorkspace(trimmed);
          showWorkspaceKey(created);
        } catch (error) {
          render(errorHost, notice(errorText(error), "error"));
          // 版本冲突说明手上的配置已过期，重新读一次再让人重试。
          if (error.status === 409) await load().catch(() => {});
        }
      },
    }],
  });
}

// promptWorkspaceModels 设定某个空间**允许直呼**的模型清单（运维侧的模型维度隔离）。
//
// 为什么需要它：任务名天然按空间隔离，但**真实模型名在配置里是全局的**——没有这份
// 清单，任何一把推理 key 都能直呼全部模型。共用网关要能「这个项目的 key 只能用这几个
// 模型」，就得在这里收窄。
//
// 三态在界面上用两个控件表达，而不是一个多选框列表：
//   - 「不限制」勾上 -> 提交 null（清除清单）；
//   - 否则提交勾选的模型数组（可能为空数组 = 一个都不许直呼）。
//
// 用「不限制」这个显式开关而不是「一个都不勾 = 不限制」：后者会让「全部取消勾选」这个
// 明显的收紧动作变成放开一切，是这类界面里最危险的一种默认。
function promptWorkspaceModels(workspace) {
  const entry = state.workspaces.find((item) => item.name === workspace) || {};
  const restricted = Array.isArray(entry.models);
  const selected = new Set(restricted ? entry.models : []);

  const errorHost = h("div");
  const boxes = [];
  const listHost = h("div.stack.models-list");
  // 模型清单来自 /api/models（含别名），与任务编辑器里可选的是同一份。
  if (!state.models.length) {
    listHost.appendChild(h("p.muted", "当前没有可用的模型。"));
  }
  for (const model of state.models) {
    const box = h("input", { type: "checkbox", value: model.id });
    box.checked = selected.has(model.id);
    boxes.push(box);
    listHost.appendChild(h("label.check.models-item", {}, box, model.id));
  }

  const unrestrictedBox = h("input", { type: "checkbox" });
  unrestrictedBox.checked = !restricted;
  const syncDisabled = () => {
    for (const box of boxes) box.disabled = unrestrictedBox.checked;
    listHost.style.opacity = unrestrictedBox.checked ? "0.5" : "1";
  };
  syncDisabled();
  unrestrictedBox.addEventListener("change", syncDisabled);

  const ref = dialog({
    title: `模型授权 · ${workspace}`,
    body: h("div.stack", {},
      h("p.muted", "这里选中的模型允许被这个空间的**推理 key 直呼**。任务名不受限制——"
        + "任务自己固定的模型就是该空间被授权用的。"),
      h("label.check", {}, unrestrictedBox, "不限制（允许直呼全部模型）"),
      listHost,
      h("p.muted", "一个都不选 = 这个空间只能通过任务名调用，不能直呼任何模型。"),
      errorHost,
    ),
    actions: [
      {
        label: "保存",
        variant: "primary",
        onClick: async () => {
          const models = unrestrictedBox.checked
            ? null
            : boxes.filter((box) => box.checked).map((box) => box.value);
          try {
            await api.setWorkspaceModels(state.revision, workspace, models);
            ref.close();
            await load();
            toast(models === null
              ? `已取消 ${workspace} 的模型限制。`
              : `已更新 ${workspace} 的模型授权（${models.length} 个）。`);
          } catch (error) {
            render(errorHost, notice(errorText(error), "error"));
            if (error.status === 409) await load().catch(() => {});
          }
        },
      },
      {
        label: "轮换推理 key",
        variant: "secondary",
        onClick: async () => {
          try {
            const rotated = await api.rotateInferenceKey(state.revision, workspace);
            ref.close();
            await load();
            showInferenceKey(workspace, rotated.inference_key);
          } catch (error) {
            render(errorHost, notice(errorText(error), "error"));
            if (error.status === 409) await load().catch(() => {});
          }
        },
      },
    ],
  });
}

// showInferenceKey 显示刚轮换出来的推理 key（明文只出现这一次）。
function showInferenceKey(workspace, key) {
  if (!key) return;
  dialog({
    title: "推理 key 已轮换",
    closeOnBackdrop: false,
    body: h("div.stack", {},
      notice("旧 key 已经失效。新 key 只显示这一次，请立即更新到各个项目的环境变量里。", "warn"),
      kv([["工作空间", workspace], ["推理 key", h("code.mono", {}, key)]]),
      h("div.inline", {},
        buttonNode("复制推理 key", {
          variant: "primary",
          small: true,
          onClick: () => copyText(key)
            .then(() => toast("推理 key 已复制。"))
            .catch((error) => toast(errorText(error), "error")),
        }),
      ),
    ),
    actions: [{ label: "我已保存", variant: "primary" }],
  });
}

// showWorkspaceKey 显示新建空间的两把 key，并给出可复制的嵌入片段。
//
// 这是明文 key 唯一出现的地方（目录接口刻意不含它们，配置文件里那两份不会再回到界面上），
// 因此文案必须把"现在就复制"讲清楚，否则用户关掉弹窗就永久失去了它——只能删掉空间
// 重建。key 本身不给输入框（readonly input 反而像是可编辑的），用 code + 复制按钮。
//
// 两把 key 都列出来且**分开说明用途**：它们权限不同，混在一句"这是 key"里最容易
// 被拿错——把面板 key 配进项目环境变量会调不通 /v1，把推理 key 嵌进页面则读不到面板。
function showWorkspaceKey(created) {
  const panelKey = created?.api_key || "";
  const inferenceKey = created?.inference_key || "";
  if (!panelKey && !inferenceKey) return;
  const embed = panelEmbedURL(panelKey);
  const snippet = `<iframe src="${embed}" width="100%" height="720" style="border:0" title="AMKR 工作空间面板"></iframe>`;
  const rows = [["工作空间", created.name]];
  if (panelKey) rows.push(["面板 key", h("code.mono", {}, panelKey)]);
  if (inferenceKey) rows.push(["推理 key", h("code.mono", {}, inferenceKey)]);
  const copyButtons = [];
  if (panelKey) {
    copyButtons.push(buttonNode("复制面板 key", {
      variant: "primary",
      small: true,
      onClick: () => copyText(panelKey)
        .then(() => toast("面板 key 已复制。"))
        .catch((error) => toast(errorText(error), "error")),
    }));
    copyButtons.push(buttonNode("复制嵌入片段", {
      small: true,
      variant: "secondary",
      onClick: () => copyText(snippet)
        .then(() => toast("嵌入片段已复制。"))
        .catch((error) => toast(errorText(error), "error")),
    }));
  }
  if (inferenceKey) {
    copyButtons.push(buttonNode("复制推理 key", {
      small: true,
      variant: "secondary",
      onClick: () => copyText(inferenceKey)
        .then(() => toast("推理 key 已复制。"))
        .catch((error) => toast(errorText(error), "error")),
    }));
  }
  dialog({
    title: "工作空间已创建",
    closeOnBackdrop: false,
    body: h("div.stack", {},
      notice("这两把 key 只显示这一次。请立即复制并妥善保存；关闭后无法再查看，"
        + "只能轮换推理 key，或删除该工作空间后重建面板 key。", "warn"),
      kv(rows),
      h("div.inline", {}, ...copyButtons),
      h("details", {},
        h("summary", "两把 key 的分工"),
        h("p.muted", "面板 key —— 给嵌入工作空间面板的应用：它只能读写这一个空间的任务与用量，"
          + "支持被 iframe 直接嵌入（key 放在 fragment 里，不进 Referer 与访问日志）。"),
        h("p.muted", "推理 key —— 给各个项目做 /v1 调用：把它配到项目的环境变量里作为 "
          + "Authorization: Bearer。空间由这把 key 决定，调用方无法用 "
          + "X-AMKR-Workspace 头换到别的空间。"),
        h("p.muted", "面板地址与嵌入片段："),
        h("pre.snippet", {}, snippet),
      ),
    ),
    actions: [{ label: "我已保存", variant: "primary" }],
  });
}

// panelEmbedURL 拼出面板页的地址，带上 key 的 fragment。
//
// key 放 fragment（#）而不是查询串：fragment 不会进 Referer、不进服务端访问日志，
// 而查询串两处都会留下明文凭据（见 webui/panel-api.js）。
function panelEmbedURL(key) {
  return `${location.origin}${webuiBase()}/panel.html#k=${encodeURIComponent(key)}`;
}

// webuiBase 返回当前 WebUI 的挂载前缀（独立运行是 /ui，嵌入宿主是 /amkr/ui）。
//
// 与 api.js 的 apiBase 同源：面板页与主界面在同一个前缀下，写死 /ui 会让挂了子路径
// 的部署复制到一个打不开的地址。
function webuiBase() {
  const path = String(location.pathname || "");
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) return `${path.slice(0, index)}/ui`;
  if (path.endsWith("/ui")) return path;
  return "/ui";
}

// promptRenameWorkspace 把当前空间改名，整组任务跟着走。
//
// 改名之后必须把 state.workspace 指到新名字上：页面还停在这个空间里（只是换了名字），
// 若不同步，下一次 load 会拿旧名字去查任务，得到一个空列表，看起来像任务全丢了。
async function promptRenameWorkspace(workspace) {
  const name = window.prompt(`把工作空间「${workspace}」改名为：其中 ${countOf(workspace)} 个任务会一起搬过去。`, workspace);
  if (name === null) return;
  const trimmed = name.trim();
  if (!trimmed || trimmed === workspace) return;
  if (trimmed === DEFAULT_WORKSPACE) {
    toast(`「${DEFAULT_WORKSPACE}」是默认工作空间，不能重名。`, "error");
    return;
  }
  state.saving = true;
  draw();
  try {
    await api.renameWorkspace(state.revision, workspace, trimmed);
    // 改名后旧名字不复存在，localStorage 里记的也得跟着换。
    state.workspace = trimmed;
    workspacePinned = true;
    rememberWorkspace(trimmed);
    state.editing = null;
    state.tasks = [];
    await load();
    toast(`工作空间已改名为 ${trimmed}。`);
  } catch (error) {
    toast(errorText(error), "error");
    if (error.status === 409) await load().catch(() => {});
  }
  state.saving = false;
  draw();
}

// confirmDeleteWorkspace 删除当前空间连同其中的全部任务。
function confirmDeleteWorkspace(workspace) {
  const count = countOf(workspace);
  confirmDialog({
    title: "删除工作空间",
    // 把任务数写进确认文案：删空间会连带删掉里面的任务，这是不可逆的。
    message: count
      ? `删除工作空间 ${workspace}？其中 ${count} 个任务会一起被删除。`
      : `删除工作空间 ${workspace}？`,
    confirmLabel: "删除",
    danger: true,
    onConfirm: async () => {
      try {
        await api.deleteWorkspace(state.revision, workspace);
        // 删完之后这个空间不存在了，留在原地会让页面显示一个空列表 + 「未写入配置」
        // 的下拉项；切回默认空间，至少保证用户看到的是一个真实存在的空间。
        state.workspace = DEFAULT_WORKSPACE;
        workspacePinned = true;
        rememberWorkspace(DEFAULT_WORKSPACE);
        state.editing = null;
        state.tasks = [];
        await load();
        toast("工作空间已删除。");
      } catch (error) {
        toast(errorText(error), "error");
      }
      draw();
    },
  });
}

// countOf 返回目录里某个空间的任务数（不在目录里时按 0 算）。
function countOf(workspace) {
  const entry = state.workspaces.find((item) => item.name === workspace);
  return entry ? entry.task_count : 0;
}

function draw() {
  if (!host) return;
  const children = [
    h("div.page-head", {},
      h("div", {}, h("h1", "任务路由"),
        h("p.sub", "把任务名当作模型名调用，自动使用该任务固定的模型与采样参数。")),
      h("div.spacer"),
      state.workspaces.length ? workspaceSwitcher() : null,
      state.revision ? badge(`版本 ${String(state.revision).slice(0, 12)}`, "muted") : null,
      buttonNode("新建任务", {
        small: true,
        variant: "secondary",
        disabled: state.saving,
        onClick: () => { state.editing = "__new__"; draw(); },
      }),
    ),
  ];
  if (state.error) children.push(notice(`无法读取或写入任务路由: ${state.error}`, "error"));
  // 没有模型不再挡住这个页面：任务可以先建成「尚未指定模型」的占位。只有当确实
  // 既没有模型也没有任务时，才把「先去配模型」当成唯一可做的事提示出来。
  if (!state.models.length && !state.tasks.length) {
    children.push(empty("尚未配置可用模型。", {
      hint: "请先在供应商页添加 Key 并绑定服务模型；也可以先建一个尚未指定模型的任务占位。",
      action: buttonNode("新建任务", { variant: "secondary", onClick: () => { state.editing = "__new__"; draw(); } }),
    }));
    render(host, children);
    return;
  }

  if (state.editing === "__new__") {
    children.push(card(cardHead("新建任务"), taskEditor(null)));
  }

  if (!state.tasks.length && state.editing !== "__new__") {
    // 空空间的提示要区分「默认空间本来就没有任务」与「这是一个还没落地的空间」：
    // 后者是用户刚输入的名字，得让他知道下一步该做什么。
    const isDefault = state.workspace === DEFAULT_WORKSPACE;
    children.push(empty(
      isDefault ? "尚未配置任务路由。" : `工作空间 ${state.workspace} 还没有任务。`,
      {
        // 命名空间由「在它里面建任务」隐式产生：没有任务时就还不存在于配置里，
        // 这一点必须说清楚，否则用户会去找一个并不存在的「保存空间」按钮。
        hint: isDefault
          ? "新建一个任务后，调用方传 model: \"TASK_XXXXXX\" 即可命中。"
          : "在这个空间里新建第一个任务后，它才会写进配置。调用方带 X-AMKR-Workspace: " +
            state.workspace + " 即可命中其中的任务。",
        action: buttonNode("新建任务", { variant: "secondary", onClick: () => { state.editing = "__new__"; draw(); } }),
      },
    ));
    render(host, children);
    return;
  }

  for (const task of state.tasks) {
    if (state.editing === task.name) {
      children.push(card(cardHead(`编辑 · ${task.display_name || task.name}`), taskEditor(task)));
      continue;
    }
    // 卡片标题用人取的显示名，任务名退到副标题/详情里：这个页面上人认的是「长文摘要」，
    // 而不是 TASK_000001。调用方仍然只能传任务名，因此它必须一直可见。
    children.push(card(
      cardHead(
        task.display_name || task.name,
        task.display_name ? badge(task.name, "muted") : null,
        task.model ? null : badge("尚未指定模型", "warn"),
        buttonNode("编辑", { small: true, variant: "text", disabled: state.saving, onClick: () => { state.editing = task.name; draw(); } }),
        buttonNode("删除", {
          small: true,
          variant: "text",
          disabled: state.saving,
          onClick: () => confirmDialog({
            title: "删除任务路由",
            message: `删除任务 ${task.name}？调用方再传这个名字会被当作未配置的模型。`,
            confirmLabel: "删除",
            danger: true,
            onConfirm: async () => {
              try {
                await api.deleteTask(state.revision, state.workspace, task.name);
                await load();
                state.editing = null;
                toast("任务路由已删除。");
              } catch (error) { toast(errorText(error), "error"); }
              draw();
            },
          }),
        }),
      ),
      kv([
        ["显示名称", task.display_name || "未设置"],
        ["首选模型", task.model || "尚未指定"],
        ["备选模型", task.fallback_model || "未配置"],
        ["固定参数", summary(task)],
      ]),
    ));
  }
  render(host, children);
}
