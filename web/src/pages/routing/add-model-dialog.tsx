import { useMemo, useRef, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { Provider, QuotaFreeze, RouteTarget } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { TokenInput } from "@/components/ui/TokenInput";
import { ProviderKeyCombobox, UpstreamModelInput } from "./model-select";
import { MODES, allKeyOptions, keyUpstreams, targetIdentity } from "./shared";

interface AddModelDialogProps {
  revision: string;
  providers: Provider[];
  freezes?: Map<string, QuotaFreeze>;
  onClose: () => void;
  onCreated: (id: string) => void;
}

interface DraftTargetRow {
  uid: number;
  keyValue: string;
  upstream: string;
}

// 服务端的写路径不变式是「没有 target 的模型不存在」，因此新建时必须至少绑定一条目标；
// 同时允许在弹窗里直接追加多条目标，免去创建后再被迫弹编辑抽屉的打断。
export function AddModelDialog({ revision, providers, freezes, onClose, onCreated }: AddModelDialogProps) {
  const keyOptions = useMemo(() => allKeyOptions(providers, freezes), [freezes, providers]);
  const defaultKeyValue = keyOptions[0]?.value || "";
  const nextUid = useRef(2);
  const [id, setId] = useState("");
  const [aliases, setAliases] = useState<string[]>([]);
  const [mode, setMode] = useState("round_robin");
  const [rows, setRows] = useState<DraftTargetRow[]>([{ uid: 1, keyValue: "", upstream: "" }]);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  function resolveKeyValue(raw: string): string {
    if (raw && keyOptions.some((item) => item.value === raw)) return raw;
    return defaultKeyValue;
  }

  function upstreamsFor(keyValue: string): string[] {
    const option = keyOptions.find((item) => item.value === keyValue);
    if (!option) return [];
    const provider = providers.find((item) => item.id === option.providerId);
    const key = provider?.keys.find((item) => item.name === option.keyName);
    return keyUpstreams(key || {});
  }

  function addRow() {
    const used = new Set(rows.map((row) => resolveKeyValue(row.keyValue)));
    const unused = keyOptions.find((item) => !used.has(item.value))?.value || defaultKeyValue;
    setRows((list) => [...list, { uid: nextUid.current++, keyValue: unused, upstream: "" }]);
  }

  function updateRow(uid: number, patch: Partial<DraftTargetRow>) {
    setRows((list) => list.map((row) => row.uid === uid ? { ...row, ...patch } : row));
  }

  function removeRow(uid: number) {
    setRows((list) => list.length > 1 ? list.filter((row) => row.uid !== uid) : list);
  }

  async function submit() {
    const nextId = id.trim();
    if (!nextId) {
      setError("请填写模型名称。");
      return;
    }
    if (!keyOptions.length) {
      setError("还没有任何供应商 Key。请先在供应商页添加 Key，再回来新建模型。");
      return;
    }

    const targets: RouteTarget[] = [];
    const seen = new Set<string>();
    for (const row of rows) {
      const effectiveKey = resolveKeyValue(row.keyValue);
      const option = keyOptions.find((item) => item.value === effectiveKey);
      if (!option) {
        setError("请为每条目标选择一个有效的供应商 Key。");
        return;
      }
      const target: RouteTarget = {
        provider: option.providerId,
        key: option.keyName,
        upstream_model: row.upstream.trim() || nextId,
      };
      const identity = targetIdentity(target);
      if (seen.has(identity)) {
        setError(`目标 ${target.provider} / ${target.key} / ${target.upstream_model} 重复，请去重后再创建。`);
        return;
      }
      seen.add(identity);
      targets.push(target);
    }

    setSaving(true);
    setError(null);
    try {
      await api.createRoute(revision, nextId, targets, aliases, mode);
      toast.success(`模型 ${nextId} 已创建。`);
      onCreated(nextId);
      onClose();
    } catch (failure) {
      // 409（同名已存在）必须留在对话框里并复位按钮：关掉对话框会让用户以为创建成功。
      setError(`创建失败: ${errorText(failure)}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open
      size="lg"
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="添加模型名称"
      description="新建一个对外模型名；至少绑定一条目标，因为空模型在配置里不存在。"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>取消</Button>
          <Button variant="primary" leadingIcon={<Plus aria-hidden className="size-4" />} loading={saving} onClick={() => void submit()}>
            创建模型
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="模型名称" required>
            <Input value={id} onChange={(event) => setId(event.target.value)} placeholder="对外模型名，例如 my-model" />
          </Field>
          <Field label="别名" hint="回车、逗号或失焦成芯片；别名与模型名等价。">
            <TokenInput value={aliases} onChange={setAliases} placeholder="逗号分隔，留空表示无别名" aria-label="别名" />
          </Field>
        </div>

        <Field label="路由模式">
          <Segmented aria-label="路由模式" value={mode} onValueChange={setMode} options={MODES} />
        </Field>

        <div className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <div>
              <div className="text-dense font-medium text-ink">
                目标绑定 <span className="text-critical">*</span>
              </div>
              <p className="text-meta text-ink-3">上游模型名留空 = 与模型名相同；展开右侧按钮可搜索已探测的上游模型。</p>
            </div>
            <Button
              size="sm"
              variant="secondary"
              disabled={!keyOptions.length}
              leadingIcon={<Plus aria-hidden className="size-3.5" />}
              onClick={addRow}
            >
              再加一条目标
            </Button>
          </div>

          <div className="space-y-2">
            {rows.map((row, index) => {
              const effectiveKey = resolveKeyValue(row.keyValue);
              const upstreams = upstreamsFor(effectiveKey);
              return (
                <div key={row.uid} className="grid items-center gap-2 rounded-control border border-rule bg-sunken/70 p-2 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_auto]">
                  <ProviderKeyCombobox
                    providers={providers}
                    freezes={freezes}
                    value={effectiveKey}
                    aria-label={rows.length === 1 ? "供应商与 Key" : `目标 ${index + 1} 供应商与 Key`}
                    onChange={(nextValue) => updateRow(row.uid, { keyValue: nextValue, upstream: "" })}
                  />
                  <UpstreamModelInput
                    value={row.upstream}
                    candidates={upstreams}
                    placeholder={id.trim() || "上游模型名（留空 = 与模型名相同）"}
                    aria-label={rows.length === 1 ? "上游模型名" : `目标 ${index + 1} 上游模型名`}
                    onChange={(nextUpstream) => updateRow(row.uid, { upstream: nextUpstream })}
                  />
                  {rows.length > 1 ? (
                    <IconButton aria-label={`删除第 ${index + 1} 条目标`} size="sm" onClick={() => removeRow(row.uid)}>
                      <Trash2 aria-hidden className="size-3.5" />
                    </IconButton>
                  ) : null}
                </div>
              );
            })}
          </div>
        </div>

        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Dialog>
  );
}
