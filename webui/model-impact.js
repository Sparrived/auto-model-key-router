// 模型改动的连带影响：把服务端预演（`dry_run=1`）的结果渲染成二次确认，并负责
// 「先预演、再确认、最后真写」这条流程本身。
//
// 为什么要有这个模块：删一个模型从来不只是删那个模型——引用它的访问密钥会少一项授权、
// 工作空间会少一个能直呼的模型、任务会被删掉、unified_model 会被改写。原先这些变动要么
// 让整次保存直接被校验驳回（用户想删的恰恰是那个模型，却因为一份清单里还写着它而删不动），
// 要么在落盘后静默发生。现在的口径是：**自动改，但先把要改的地方说清楚**。
//
// 影响清单由服务端算（它比页面更清楚哪些引用真的失效了），这里只负责呈现与流程。

import { h } from "./dom.js";
import { dialog } from "./ui.js";

// 默认的过渡句：清单由服务端预演得出，与最终落盘结果同源。
const DEFAULT_IMPACT_INTRO = "以下内容会一并变动：";

// hasImpact 报告预演结果里是否有需要用户点头的连带变动。
export function hasImpact(impact) {
  if (!impact) return false;
  return Boolean(
    impact.removed_models?.length ||
    impact.access_keys?.length ||
    impact.workspaces?.length ||
    impact.removed_tasks?.length ||
    impact.unified_model,
  );
}

// writeWithImpactConfirm 执行一次可能连带改动的写操作。
//
// preview 与 commit 必须是**同一件事**（同一组参数、同一个调用，只差一个 dry_run），
// 否则确认框里说的是 A、落盘的是 B。
//
// alwaysConfirm 用于删除这类**本身就危险**的操作：即使没有连带变动也要问一句，不能因为
// 影响清单为空就静默删掉。
//
// 返回 { confirmed, result }：confirmed 为 false 表示用户在确认框里取消了——调用方此时
// 不该刷新或提示"已保存"。真写失败照常抛出，与直接调接口一致。
export async function writeWithImpactConfirm({
  title, message, impactIntro, confirmLabel, preview, commit, alwaysConfirm = false,
}) {
  const impact = await preview();
  const impacted = hasImpact(impact);
  if (!alwaysConfirm && !impacted) return { confirmed: true, result: await commit() };
  const confirmed = await askConfirm({ title, message, impact, impactIntro, confirmLabel, impacted });
  if (!confirmed) return { confirmed: false, result: undefined };
  return { confirmed: true, result: await commit() };
}

// askConfirm 弹出确认框，返回用户是否点了确认。
//
// Esc 与关闭按钮走 onClose（算取消），所以"关掉"与"取消"是同一个结果——一个不表态的
// 关闭不该被当成同意。
function askConfirm({ title, message, impact, impactIntro, confirmLabel, impacted }) {
  return new Promise((resolve) => {
    let ref = null;
    const decide = (confirmed) => {
      resolve(confirmed);
      if (ref) ref.close();
    };
    ref = dialog({
      title,
      body: confirmBody(message, impacted ? impact : null, impactIntro),
      actions: [
        { label: "取消", variant: "text", onClick: () => decide(false) },
        { label: confirmLabel || "确认改动", variant: "danger", onClick: () => decide(true) },
      ],
      // promise 一旦定下来，后续的 resolve 都会被忽略，因此 decide 之后的 onClose
      // 不会把结论翻回去。
      onClose: () => resolve(false),
    });
  });
}

// confirmBody 渲染确认框正文：这次操作是什么 + （有连带变动时）分组的影响清单。
function confirmBody(message, impact, impactIntro) {
  if (!impact) return h("div.stack.tight", {}, h("p", message));
  return h("div.stack.tight", {},
    h("p", message),
    h("p.muted", impactIntro || DEFAULT_IMPACT_INTRO),
    ...impactSections(impact),
  );
}

function impactSections(impact) {
  const sections = [];
  if (impact.removed_models?.length) {
    sections.push(section("模型", impact.removed_models.map(
      (name) => `${name} 失去全部 Key 绑定，将被删除`)));
  }
  if (impact.access_keys?.length) {
    sections.push(section("访问密钥", impact.access_keys.flatMap(accessKeyLines)));
  }
  if (impact.workspaces?.length) {
    sections.push(section("工作空间", impact.workspaces.flatMap(workspaceLines)));
  }
  if (impact.removed_tasks?.length) {
    sections.push(section("任务", [
      `${impact.removed_tasks.join("、")} 引用了被删的模型，将被一并删除`,
    ]));
  }
  if (impact.unified_model) {
    sections.push(section("统一模型", [
      "unified_model 指向被删的模型，会被改写或移除",
    ]));
  }
  return sections;
}

function section(title, lines) {
  return h("div", {},
    h("h4", title),
    h("ul", {}, lines.map((line) => h("li", line))),
  );
}

// accessKeyLines 逐条说明一把访问密钥会失去什么。
//
// 空清单要单独说一句：清单的三态里 `[]` 是**一个都不许**，与「不限制」正好相反，
// 只说"移除了最后一项"会让用户以为这把密钥回到了不限制。
function accessKeyLines(key) {
  const label = key.name && key.name !== key.id ? `${key.name}（${key.id}）` : key.id;
  const lines = [];
  if (key.models?.length) lines.push(`${label}：模型清单移除 ${key.models.join("、")}`);
  if (key.models_cleared) lines.push(`${label}：模型清单被摘空，这把密钥将调不动任何模型`);
  if (key.providers?.length) lines.push(`${label}：供应商清单移除 ${key.providers.join("、")}`);
  if (key.providers_cleared) lines.push(`${label}：供应商清单被摘空，这把密钥将调不动任何模型`);
  return lines;
}

function workspaceLines(workspace) {
  const lines = [];
  if (workspace.models?.length) {
    lines.push(`${workspace.name}：模型清单移除 ${workspace.models.join("、")}`);
  }
  if (workspace.models_cleared) {
    lines.push(`${workspace.name}：模型清单被摘空，该空间不能再直呼任何模型（任务名照常可用）`);
  }
  return lines;
}
