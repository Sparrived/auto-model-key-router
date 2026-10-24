// 任务编辑草稿与请求体：把「表单里长什么样」与「接口要什么形状」分开。
//
// 这里单独成模块，是因为三态/空值的语义很容易在 JSX 里写丢：清空的字段必须发 null
// （键存在 + null = 清空），而留空的采样参数必须**不出现**在 params 里（省略 = 不固定，
// 调用方可以自己传）。这两条一旦写反都不会报错，只会静默改变路由行为。

import type { Task, TaskParams, TaskPayload } from "@/lib/types";

export interface NumericParamSpec {
  key: keyof TaskParams;
  label: string;
  integer?: boolean;
}

// 数值型固定参数：top_k / seed / max_tokens 必须是整数。
export const NUMERIC_PARAMS: NumericParamSpec[] = [
  { key: "temperature", label: "temperature" },
  { key: "top_p", label: "top_p" },
  { key: "top_k", label: "top_k", integer: true },
  { key: "frequency_penalty", label: "frequency_penalty" },
  { key: "presence_penalty", label: "presence_penalty" },
  { key: "seed", label: "seed", integer: true },
  { key: "max_tokens", label: "max_tokens", integer: true },
];

export const EFFORTS = [
  { value: "", label: "不固定" },
  { value: "none", label: "none" },
  { value: "minimal", label: "minimal" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

export const PARAM_LABELS: Record<string, string> = Object.fromEntries([
  ...NUMERIC_PARAMS.map((param) => [param.key, param.label]),
  ["stop", "stop"],
  ["reasoning_effort", "reasoning_effort"],
]);

export interface NumericDraft {
  fixed: boolean;
  value: string;
}

export interface TaskDraft {
  name: string;
  displayName: string;
  model: string;
  fallback: string;
  effort: string;
  numbers: Record<string, NumericDraft>;
  stopFixed: boolean;
  stop: string[];
}

export function emptyDraft(name = ""): TaskDraft {
  return {
    name,
    displayName: "",
    model: "",
    fallback: "",
    effort: "",
    numbers: Object.fromEntries(NUMERIC_PARAMS.map((param) => [param.key, { fixed: false, value: "" }])),
    stopFixed: false,
    stop: [],
  };
}

// 既有任务没有模型时**不能**回落到第一个模型：那会在用户没碰下拉的情况下把占位任务
// 悄悄绑上某个模型。只有新建任务才预选第一个模型（由调用方传入默认草稿）。
export function draftFromTask(task: Task): TaskDraft {
  const params = task.params || {};
  const numbers: Record<string, NumericDraft> = {};
  for (const spec of NUMERIC_PARAMS) {
    const value = params[spec.key];
    const fixed = value !== undefined && value !== null;
    numbers[spec.key] = { fixed, value: fixed ? String(value) : "" };
  }
  return {
    name: task.name,
    displayName: task.display_name || "",
    model: task.model || "",
    fallback: task.fallback_model || "",
    effort: params.reasoning_effort || "",
    numbers,
    stopFixed: Array.isArray(params.stop) && params.stop.length > 0,
    stop: Array.isArray(params.stop) ? [...params.stop] : [],
  };
}

export function nextTaskName(tasks: Task[]): string {
  let max = 0;
  for (const task of tasks) {
    const match = /^TASK_(\d{6})$/.exec(task.name);
    if (match) max = Math.max(max, Number(match[1]));
  }
  return `TASK_${String(max + 1).padStart(6, "0")}`;
}

// validateDraft 返回第一条能说清的错误，null 表示可以提交。
// 任务名在编辑时不可改，因此只对新建校验非空。
export function validateDraft(draft: TaskDraft, isNew: boolean): string | null {
  if (isNew && !draft.name.trim()) return "请填写任务名。";
  if (draft.fallback && draft.fallback === draft.model) return "备选模型不能与首选模型相同。";
  if (draft.fallback && !draft.model) return "未指定首选模型时不能设置备选模型。";
  for (const spec of NUMERIC_PARAMS) {
    const item = draft.numbers[spec.key];
    if (!item?.fixed) continue;
    const raw = item.value.trim();
    if (!raw) return `${spec.label} 已固定，请填写数值。`;
    const value = Number(raw);
    if (!Number.isFinite(value)) return `${spec.label} 必须是数字`;
    if (spec.integer && !Number.isInteger(value)) return `${spec.label} 必须是整数`;
  }
  if (draft.stopFixed && draft.stop.length === 0) return "stop 已固定，请至少填一个停止词。";
  return null;
}

// collectParams 只写用户真正固定了的参数；留空 = 不写进配置，调用方可以自己传。
export function collectParams(draft: TaskDraft): TaskParams {
  const params: TaskParams = {};
  for (const spec of NUMERIC_PARAMS) {
    const item = draft.numbers[spec.key];
    if (!item?.fixed) continue;
    const raw = item.value.trim();
    if (!raw) continue;
    // TaskParams 的键各自带类型；按联合键写入时 TS 会收成 never，这里按键名写值即可。
    (params as Record<string, number>)[spec.key] = Number(raw);
  }
  if (draft.stopFixed && draft.stop.length > 0) params.stop = [...draft.stop];
  if (draft.effort) params.reasoning_effort = draft.effort;
  return params;
}

// buildTaskPayload 是 PUT/POST 共用的形状：清空的字段发 null（服务端把「键存在 + null」
// 当作清空，省略则是不改）；任务名不在其中——它不是可更新的字段。
export function buildTaskPayload(draft: TaskDraft): TaskPayload {
  return {
    model: draft.model || null,
    display_name: draft.displayName.trim() || null,
    fallback_model: draft.fallback || null,
    params: collectParams(draft),
  };
}

export function summarizeParams(params: TaskParams | undefined): Array<{ label: string; value: string }> {
  if (!params) return [];
  const result: Array<{ label: string; value: string }> = [];
  for (const spec of NUMERIC_PARAMS) {
    const value = params[spec.key];
    if (value !== undefined && value !== null) result.push({ label: spec.label, value: String(value) });
  }
  if (Array.isArray(params.stop) && params.stop.length > 0) {
    result.push({ label: "stop", value: params.stop.join(", ") });
  }
  if (params.reasoning_effort) result.push({ label: "reasoning_effort", value: params.reasoning_effort });
  return result;
}
