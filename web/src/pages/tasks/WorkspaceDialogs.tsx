// 工作空间自己的三个对话框：模型授权、改名、新建。
//
// 默认空间（default）是「不带 X-AMKR-Workspace 头」的调用方命中的那个，改名/删除会让
// 所有人落空；它也没有作用域凭据，模型清单配了没有对象生效。三个动作因此都在调用前
// 由页面禁用，而不是靠服务端 400 兜底。

import { useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { ModelSummaryResponse, WorkspaceCreateResponse, WorkspaceSummary } from "@/lib/types";
import { api } from "@/lib/api";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";

export type WorkspaceModelMode = "unrestricted" | "list" | "none";

export function workspaceModelMode(models: string[] | null | undefined): WorkspaceModelMode {
  if (models === undefined || models === null) return "unrestricted";
  return models.length === 0 ? "none" : "list";
}

export interface WorkspaceModelsSheetProps {
  workspace: string;
  entry: WorkspaceSummary | undefined;
  models: ModelSummaryResponse[];
  revision: string;
  onClose: () => void;
  onSaved: () => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}

export function WorkspaceModelsSheet({
  workspace, entry, models, revision, onClose, onSaved, onRefresh,
}: WorkspaceModelsSheetProps) {
  const [mode, setMode] = useState<WorkspaceModelMode>(() => workspaceModelMode(entry?.models));
  const [selected, setSelected] = useState<Set<string>>(
    () => new Set(Array.isArray(entry?.models) ? entry.models : []),
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<{ tone: "critical" | "warn"; text: string } | null>(null);
  const [keyword, setKeyword] = useState("");

  const catalogIds = models.map((model) => model.id);
  // 已选里可能出现配置里已经删掉的名字：悄悄丢掉等于在运维没看见的情况下削减权限。
  const candidates = [...new Set([...catalogIds, ...selected])].sort((a, b) => a.localeCompare(b, "zh-CN"));
  const visible = candidates.filter((name) => !keyword.trim() || name.toLowerCase().includes(keyword.trim().toLowerCase()));

  function toggle(name: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(name);
      else next.delete(name);
      return next;
    });
  }

  async function save() {
    if (saving) return;
    const value = mode === "unrestricted" ? null : mode === "none" ? [] : [...selected].sort();
    setSaving(true);
    setError(null);
    try {
      await api.setWorkspaceModels(revision, workspace, value);
      toast.success(mode === "unrestricted"
        ? `已取消 ${workspace} 的模型限制。`
        : `已更新 ${workspace} 的模型授权（${value?.length ?? 0} 个）。`);
      await onSaved();
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setError({ tone: "warn", text: "配置已在别处更新：已重新读取最新配置，请核对后再保存。" });
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
      title={`模型授权 · ${workspace}`}
      subtitle="设定这个空间允许直呼哪些模型。任务名不受限制——任务自己固定的模型就是该空间被授权用的。"
      width="md"
      saving={saving}
      onSave={save}
      footer={(
        <>
          <Button variant="secondary" disabled={saving} onClick={onClose}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void save()}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </>
      )}
    >
      <div className="space-y-4">
        {error ? <Banner tone={error.tone}>{error.text}</Banner> : null}
        <Segmented
          aria-label="模型授权范围"
          value={mode}
          onValueChange={(value) => setMode(value as WorkspaceModelMode)}
          options={[
            { value: "unrestricted", label: "不限制" },
            { value: "list", label: "指定清单" },
            { value: "none", label: "全部禁止" },
          ]}
        />
        <p className="text-meta leading-5 text-ink-3">
          {mode === "unrestricted"
            ? "允许这个空间的推理 key 直呼全部模型。"
            : mode === "none"
              ? "一个都不许直呼：这个空间只能通过任务名调用。"
              : "只允许勾选的模型；一个都不勾 = 这个空间只能通过任务名调用。"}
        </p>
        {mode === "list" ? (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder="搜索模型"
                aria-label="搜索模型"
                size="sm"
                className="max-w-56"
              />
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setSelected(new Set(candidates))}
              >
                全选
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>清空</Button>
              <span className="text-meta text-ink-3">已选 {selected.size} 项</span>
            </div>
            <div className="max-h-64 space-y-1 overflow-y-auto rounded-control border border-rule p-2">
              {visible.length === 0 ? (
                <p className="px-1 py-4 text-center text-meta text-ink-3">没有匹配的模型。</p>
              ) : (
                visible.map((name) => {
                  const unknown = !catalogIds.includes(name);
                  return (
                    <div key={name} className="flex items-center justify-between gap-2 rounded-chip px-1 py-0.5 hover:bg-hover">
                      <Checkbox
                        checked={selected.has(name)}
                        onCheckedChange={(checked) => toggle(name, checked)}
                        label={<span className="font-mono">{name}</span>}
                        description={unknown ? "配置里已经没有这个模型，保存后会被服务端拒绝" : undefined}
                      />
                      {unknown ? <span className="text-meta text-warn-ink">已不在配置中</span> : null}
                    </div>
                  );
                })
              )}
            </div>
          </div>
        ) : null}
      </div>
    </Sheet>
  );
}

export interface RenameWorkspaceDialogProps {
  workspace: string;
  taskCount: number;
  revision: string;
  onClose: () => void;
  onRenamed: (name: string) => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}

export function RenameWorkspaceDialog({
  workspace, taskCount, revision, onClose, onRenamed, onRefresh,
}: RenameWorkspaceDialogProps) {
  const [name, setName] = useState(workspace);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    if (saving) return;
    const trimmed = name.trim();
    if (!trimmed) {
      setError("工作空间名不能为空。");
      return;
    }
    if (trimmed === workspace) {
      onClose();
      return;
    }
    if (trimmed === "default") {
      setError("「default」是默认工作空间，不能重名。");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.renameWorkspace(revision, workspace, trimmed);
      toast.success(`工作空间已改名为 ${trimmed}。`);
      await onRenamed(trimmed);
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setError("配置已在别处更新：已重新读取最新配置，请核对后再保存。");
        await onRefresh();
      } else {
        setError(errorText(caught));
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title="重命名工作空间"
      description={`其中的 ${taskCount} 个任务与两把 key 会一起搬到新名字下。`}
      footer={(
        <>
          <Button variant="secondary" disabled={saving} onClick={onClose}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void submit()}>保存名称</Button>
        </>
      )}
    >
      <div className="space-y-3">
        {error ? <Banner tone="critical">{error}</Banner> : null}
        <Field label="新的工作空间名" required>
          <Input
            value={name}
            autoFocus
            disabled={saving}
            aria-label="新的工作空间名"
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                void submit();
              }
            }}
          />
        </Field>
      </div>
    </Dialog>
  );
}

export interface NewWorkspaceDialogProps {
  revision: string;
  onClose: () => void;
  onCreated: (created: WorkspaceCreateResponse) => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}

export function NewWorkspaceDialog({ revision, onClose, onCreated, onRefresh }: NewWorkspaceDialogProps) {
  const [name, setName] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit() {
    if (saving) return;
    const trimmed = name.trim();
    if (!trimmed) {
      setError("工作空间名不能为空。");
      return;
    }
    if (trimmed === "default") {
      setError("「default」是默认工作空间，不能重名。");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const created = await api.createWorkspace(revision, trimmed);
      await onCreated(created);
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setError("配置已在别处更新：已重新读取最新配置，请核对后再保存。");
        await onRefresh();
      } else {
        setError(errorText(caught));
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      title="新建工作空间"
      description="空间创建后会立刻写入配置，并生成两把只属于它的 key：面板 key 交给要嵌入工作空间面板的应用，推理 key 交给要调用 /v1 的项目。"
      footer={(
        <>
          <Button variant="secondary" disabled={saving} onClick={onClose}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void submit()}>创建</Button>
        </>
      )}
    >
      <div className="space-y-3">
        {error ? <Banner tone="critical">{error}</Banner> : null}
        <Field label="工作空间名" required hint="创建后会立刻写进配置，不能是 default。">
          <Input
            value={name}
            autoFocus
            disabled={saving}
            placeholder="例如 teamA"
            aria-label="工作空间名"
            onChange={(event) => setName(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                void submit();
              }
            }}
          />
        </Field>
      </div>
    </Dialog>
  );
}

// panelEmbedURL 拼出面板页的地址，带上 key 的 fragment。
//
// key 放 fragment（#）而不是查询串：fragment 不会进 Referer、不进服务端访问日志，
// 而查询串两处都会留下明文凭据（见 webui/panel-api.js）。
export function panelEmbedURL(key: string): string {
  const path = String(location.pathname || "");
  let base = "/ui";
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) base = `${path.slice(0, index)}/ui`;
  else if (path.endsWith("/ui")) base = path;
  return `${location.origin}${base}/panel.html#k=${encodeURIComponent(key)}`;
}
