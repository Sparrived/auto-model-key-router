// 模型改动的连带影响：把服务端预演（`dry_run=1`）的结果渲染成二次确认，并负责
// 「先预演、再确认、最后真写」这条流程本身。
//
// 为什么要有这个模块：删一个模型从来不只是删那个模型——引用它的访问密钥会少一项授权、
// 工作空间会少一个能直呼的模型、任务会被删掉、unified_model 会被改写。原先这些变动要么
// 让整次保存直接被校验驳回（用户想删的恰恰是那个模型，却因为一份清单里还写着它而删不动），
// 要么在落盘后静默发生。现在的口径是：**自动改，但先把要改的地方说清楚**。
//
// 影响清单由服务端算（它比页面更清楚哪些引用真的失效了），这里只负责呈现与流程。

export interface RemovedKey {
  provider: string;
  key: string;
}

export interface ImpactAccessKey {
  id: string;
  name?: string;
  models?: string[];
  models_cleared?: boolean;
  providers?: string[];
  providers_cleared?: boolean;
}

export interface ImpactWorkspace {
  name: string;
  models?: string[];
  models_cleared?: boolean;
}

export interface Impact {
  removed_models?: string[];
  removed_keys?: RemovedKey[];
  access_keys?: ImpactAccessKey[];
  workspaces?: ImpactWorkspace[];
  removed_tasks?: string[];
  unified_model?: unknown;
  endpoint_plans?: unknown;
  quota_freeze_whitelist?: string[];
}

// hasImpact 报告预演结果里是否有需要用户点头的连带变动。
export function hasImpact(impact: Impact | null | undefined): boolean {
  if (!impact) return false;
  return Boolean(
    impact.removed_models?.length ||
    // 供应商 Key 被清理：删路由只是解绑，但一把再没有任何路由使用的 Key 会跟着消失。
    // 这个消失最不显眼（页面删的是模型路由，用户不会想到某把 Key 没了），必须单独问一句。
    impact.removed_keys?.length ||
    impact.access_keys?.length ||
    impact.workspaces?.length ||
    impact.removed_tasks?.length ||
    impact.unified_model ||
    // 模型的端点计划（endpoints）也会指向别的模型：删掉一个模型名，别处把它当作某一族的
    // 分派目标时，那条计划会被改写。这件事必须说出来——它是"删一个名字"最不显眼的连带后果。
    impact.endpoint_plans ||
    // 提前冻结白名单也是模型名的硬引用：删模型/改名时必须同步清理或跟随。
    impact.quota_freeze_whitelist?.length,
  );
}

export interface ImpactConfirmArgs {
  title: string;
  message: string;
  impact: Impact | null;
  impactIntro?: string;
  confirmLabel: string;
  impacted: boolean;
}

export interface WriteWithImpactConfirmOptions<T> {
  title: string;
  message: string;
  impactIntro?: string;
  confirmLabel?: string;
  preview: () => Promise<Impact | null | undefined>;
  commit: () => Promise<T>;
  alwaysConfirm?: boolean;
  // 确认 UI 由后续任务的 ImpactDialog 注入：这里只关心"用户是否同意"。
  confirm: (args: ImpactConfirmArgs) => Promise<boolean>;
}

export interface WriteWithImpactConfirmResult<T> {
  confirmed: boolean;
  result: T | undefined;
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
export async function writeWithImpactConfirm<T>({
  title, message, impactIntro, confirmLabel, preview, commit, alwaysConfirm = false, confirm,
}: WriteWithImpactConfirmOptions<T>): Promise<WriteWithImpactConfirmResult<T>> {
  const impact = await preview();
  const impacted = hasImpact(impact);
  if (!alwaysConfirm && !impacted) return { confirmed: true, result: await commit() };
  const confirmed = await confirm({
    title,
    message,
    impact: impacted ? (impact ?? null) : null,
    impactIntro,
    confirmLabel: confirmLabel || "确认改动",
    impacted,
  });
  if (!confirmed) return { confirmed: false, result: undefined };
  return { confirmed: true, result: await commit() };
}

export interface ImpactSection {
  title: string;
  lines: string[];
}

// impactSections 把影响清单折成对话框可直接渲染的分组；文案与 legacy 完全一致。
export function impactSections(impact: Impact | null | undefined): ImpactSection[] {
  if (!impact) return [];
  const sections: ImpactSection[] = [];
  if (impact.removed_models?.length) {
    sections.push(section("模型", impact.removed_models.map(
      (name) => `${name} 失去全部 Key 绑定，将被删除`)));
  }
  if (impact.removed_keys?.length) {
    // 服务端已按 provider、key 升序排好，这里照序渲染：顺序即清单本身的顺序，
    // 再排一次只会让「确认框看到的」与「服务端算出的」多一个可能不一致的地方。
    sections.push(section("供应商 Key", impact.removed_keys.map(
      (removed) => `${removed.provider} / ${removed.key} 不再被任何路由绑定，将被删除`)));
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
  if (impact.endpoint_plans) {
    sections.push(section("端点计划", [
      "别的模型名把它当作某一端点族的分派目标，那几条端点计划会被清掉（未配置的族回落到该模型自己的目标）",
    ]));
  }
  if (impact.quota_freeze_whitelist?.length) {
    sections.push(section("提前冻结白名单", [
      `提前冻结白名单将同步更新：${impact.quota_freeze_whitelist.join("、")}`,
    ]));
  }
  return sections;
}

function section(title: string, lines: string[]): ImpactSection {
  return { title, lines };
}

// accessKeyLines 逐条说明一把访问密钥会失去什么。
//
// 空清单要单独说一句：清单的三态里 `[]` 是**一个都不许**，与「不限制」正好相反，
// 只说"移除了最后一项"会让用户以为这把密钥回到了不限制。
function accessKeyLines(key: ImpactAccessKey): string[] {
  const label = key.name && key.name !== key.id ? `${key.name}（${key.id}）` : key.id;
  const lines: string[] = [];
  if (key.models?.length) lines.push(`${label}：模型清单移除 ${key.models.join("、")}`);
  if (key.models_cleared) lines.push(`${label}：模型清单被摘空，这把密钥将调不动任何模型`);
  if (key.providers?.length) lines.push(`${label}：供应商清单移除 ${key.providers.join("、")}`);
  if (key.providers_cleared) lines.push(`${label}：供应商清单被摘空，这把密钥将调不动任何模型`);
  return lines;
}

function workspaceLines(workspace: ImpactWorkspace): string[] {
  const lines: string[] = [];
  if (workspace.models?.length) {
    lines.push(`${workspace.name}：模型清单移除 ${workspace.models.join("、")}`);
  }
  if (workspace.models_cleared) {
    lines.push(`${workspace.name}：模型清单被摘空，该空间不能再直呼任何模型（任务名照常可用）`);
  }
  return lines;
}
