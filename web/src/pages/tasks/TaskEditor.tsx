// 任务编辑抽屉：新建与编辑共用一套表单。
//
// 草稿只在**打开时**初始化一次（组件随抽屉挂载/卸载），轮询或保存后的重新取数不会
// 覆盖正在编辑的内容——这是旧版整页重绘时最容易丢数据的地方。

import { useRef, useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { ModelSummaryResponse, Task } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Combobox } from "@/components/ui/Combobox";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { Switch } from "@/components/ui/Switch";
import { TokenInput } from "@/components/ui/TokenInput";
import { CallExample } from "./CallExample";
import {
  buildTaskPayload,
  draftFromTask,
  EFFORTS,
  emptyDraft,
  NUMERIC_PARAMS,
  validateDraft,
  type TaskDraft,
} from "./draft";
import { api } from "@/lib/api";

// 空值必须是一个**真实选项**：没有它，用户就没法把既有任务改回占位状态，新建设时也会
// 被迫先挑一个模型。Combobox 用空串当值时会和 placeholder 混在一起，因此用哨兵再映射。
const UNSET = "__unset__";
const NO_FALLBACK = "__none__";

export interface TaskEditorProps {
  task: Task | null;
  isNew: boolean;
  workspace: string;
  models: ModelSummaryResponse[];
  suggestedName: string;
  baseUrl?: string;
  revision: string;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}

export function TaskEditor({
  task, isNew, workspace, models, suggestedName, baseUrl, revision, onClose, onSaved, onRefresh,
}: TaskEditorProps) {
  const [draft, setDraft] = useState<TaskDraft>(() => {
    const next = task ? draftFromTask(task) : emptyDraft(suggestedName);
    // 只有新建任务才预选第一个模型；既有任务没有模型时保持「尚未指定」。
    if (!task && models[0]?.id) next.model = models[0].id;
    return next;
  });
  const baseline = useRef(JSON.stringify(draft));
  const [saving, setSaving] = useState(false);
  const confirm = useConfirm();
  const [error, setError] = useState<{ tone: "critical" | "warn"; text: string } | null>(null);
  const dirty = JSON.stringify(draft) !== baseline.current;

  function patch(update: Partial<TaskDraft>) {
    setDraft((current) => ({ ...current, ...update }));
  }

  function setNumber(key: string, update: Partial<{ fixed: boolean; value: string }>) {
    setDraft((current) => ({
      ...current,
      numbers: { ...current.numbers, [key]: { ...current.numbers[key], ...update } },
    }));
  }

  const catalogModels = models.map((model) => model.id);
  const modelOptions = [
    { value: UNSET, label: "（尚未指定）" },
    ...(draft.model && !catalogModels.includes(draft.model)
      ? [{ value: draft.model, label: `${draft.model}（不在模型目录中）` }]
      : []),
    ...catalogModels.map((id) => ({ value: id, label: id })),
  ];
  const fallbackOptions = [
    { value: NO_FALLBACK, label: "不启用备选" },
    ...(draft.fallback && !catalogModels.includes(draft.fallback)
      ? [{ value: draft.fallback, label: `${draft.fallback}（不在模型目录中）` }]
      : []),
    ...catalogModels.filter((id) => id !== draft.model).map((id) => ({ value: id, label: id })),
  ];

  // 底部「取消」也要走未保存确认；Sheet 只接管 Esc/遮罩/右上角关闭。
  async function cancel() {
    if (saving) return;
    if (dirty) {
      const discard = await confirm({
        title: "放弃未保存的更改？",
        message: "关闭后，这次修改不会保存。",
        confirmLabel: "放弃更改",
        cancelLabel: "继续编辑",
        danger: true,
      });
      if (!discard) return;
    }
    onClose();
  }

  async function save() {
    if (saving) return;
    const message = validateDraft(draft, isNew);
    if (message) {
      setError({ tone: "critical", text: message });
      return;
    }
    if (!revision) {
      setError({ tone: "critical", text: "尚未读取到配置版本，请稍后重试。" });
      return;
    }
    setError(null);
    setSaving(true);
    try {
      const payload = buildTaskPayload(draft);
      if (isNew) {
        await api.createTask(revision, workspace, { name: draft.name.trim(), ...payload });
      } else {
        await api.updateTask(revision, workspace, task!.name, payload);
      }
      toast.success("任务路由已保存。");
      await onSaved();
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        // 版本冲突说明手上的配置已过期：保留草稿，重新读一次，由用户核对后再点保存。
        setError({
          tone: "warn",
          text: "配置已在别处更新：已重新读取最新配置，请核对后再保存。",
        });
        await onRefresh();
      } else {
        setError({ tone: "critical", text: `保存失败：${errorText(caught)}` });
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title={isNew ? "新建任务" : `编辑 · ${task?.display_name || task?.name || ""}`}
      subtitle={isNew ? `工作空间：${workspace}` : `任务名 ${task?.name || ""} 不可修改`}
      width="xl"
      dirty={dirty}
      onSave={save}
      saving={saving}
      footer={(
        <>
          <Button variant="secondary" disabled={saving} onClick={() => void cancel()}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void save()}>
            {saving ? "保存中…" : "保存任务"}
          </Button>
        </>
      )}
    >
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_380px]">
        <div className="space-y-4">
          {error ? <Banner tone={error.tone}>{error.text}</Banner> : null}

          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="任务名"
              required={isNew}
              hint={isNew ? "调用方传的 model 名；默认建议当前空间下一个未占用编号，可改。" : undefined}
            >
              <Input
                value={draft.name}
                disabled={!isNew || saving}
                placeholder="TASK_000001"
                onChange={(event) => patch({ name: event.target.value })}
              />
            </Field>
            <Field label="显示名称（可选）" hint="只用于在这个页面上辨认任务，不影响调用。">
              <Input
                value={draft.displayName}
                disabled={saving}
                placeholder="例如：长文摘要"
                onChange={(event) => patch({ displayName: event.target.value })}
              />
            </Field>
            <Field label="首选模型">
              <Combobox
                options={modelOptions}
                value={draft.model || UNSET}
                onValueChange={(value) => {
                  const next = value === UNSET ? "" : String(value);
                  patch({
                    model: next,
                    // 首选换成了当前的备选时清掉备选；首选清空后备选失去意义，一并清掉。
                    fallback: !next || draft.fallback === next ? "" : draft.fallback,
                  });
                }}
                placeholder="（尚未指定）"
                disabled={saving}
                aria-label="首选模型"
              />
            </Field>
            <Field
              label="备选模型"
              hint={draft.model ? "首选模型重试失败后才会用到。" : "先选首选模型才能启用备选。"}
            >
              <Combobox
                options={fallbackOptions}
                value={draft.fallback || NO_FALLBACK}
                onValueChange={(value) => patch({ fallback: value === NO_FALLBACK ? "" : String(value) })}
                placeholder="不启用备选"
                // 未指定首选时备选无处可退（服务端也会拒绝），因此直接锁掉。
                disabled={saving || !draft.model}
                aria-label="备选模型"
              />
            </Field>
          </div>

          <div className="flex flex-col gap-1">
            <span className="text-meta font-medium text-ink-2">推理强度</span>
            <Segmented
              aria-label="推理强度"
              options={EFFORTS}
              value={draft.effort}
              onValueChange={(value) => patch({ effort: value })}
              className={saving ? "pointer-events-none opacity-50" : undefined}
            />
          </div>

          <div className="space-y-2 border-t border-rule pt-4">
            <h3 className="text-[15px] font-semibold text-ink">固定采样参数</h3>
            <p className="text-meta leading-5 text-ink-3">
              填了的参数由任务说了算：调用方再传同名参数（含 reasoning_effort）会被直接拒绝。
              留空的参数照常透传，不在这个列表里的参数也始终透传。
            </p>
            <div className="space-y-2">
              {NUMERIC_PARAMS.map((spec) => {
                const item = draft.numbers[spec.key] ?? { fixed: false, value: "" };
                return (
                  <div key={spec.key} className="grid grid-cols-[minmax(150px,1fr)_minmax(0,1.2fr)] items-center gap-3">
                    <Switch
                      checked={item.fixed}
                      onCheckedChange={(fixed) => setNumber(spec.key, { fixed, value: fixed ? item.value : "" })}
                      disabled={saving}
                      label={spec.label}
                    />
                    <Input
                      value={item.value}
                      disabled={saving || !item.fixed}
                      placeholder={item.fixed ? "填数值" : "留空表示不固定"}
                      inputMode="decimal"
                      aria-label={`${spec.label} 数值`}
                      onChange={(event) => setNumber(spec.key, { value: event.target.value })}
                    />
                  </div>
                );
              })}
              <div className="grid grid-cols-[minmax(150px,1fr)_minmax(0,1.2fr)] items-center gap-3">
                <Switch
                  checked={draft.stopFixed}
                  onCheckedChange={(fixed) => patch({ stopFixed: fixed, stop: fixed ? draft.stop : [] })}
                  disabled={saving}
                  label="stop"
                />
                <TokenInput
                  value={draft.stop}
                  onChange={(stop) => patch({ stop })}
                  disabled={saving || !draft.stopFixed}
                  placeholder={draft.stopFixed ? "输入停止词后回车" : "留空表示不固定"}
                  aria-label="stop 值"
                />
              </div>
            </div>
          </div>

          <p className="text-meta leading-5 text-ink-3">
            任务名不能与模型 ID 或别名撞名。首选模型可以先留空：任务会先作为占位存在，
            此时调用它会被明确拒绝并提示尚未指定模型，而不是落到别的模型上。
          </p>
        </div>

        <div className="lg:sticky lg:top-0">
          <CallExample draft={draft} workspace={workspace} baseUrl={baseUrl} />
        </div>
      </div>
    </Sheet>
  );
}
