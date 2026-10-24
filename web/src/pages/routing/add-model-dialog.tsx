import { useId, useState } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { Provider } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { TokenInput } from "@/components/ui/TokenInput";
import { MODES, allKeyOptions, keyUpstreams } from "./shared";

interface AddModelDialogProps {
  revision: string;
  providers: Provider[];
  onClose: () => void;
  onCreated: (id: string) => void;
}

// 服务端的写路径不变式是「没有 target 的模型不存在」，因此新建时必须同时绑定第一条目标。
// 建好之后直接进编辑抽屉——新名称的端点计划通常正是接下来要配的东西。
export function AddModelDialog({ revision, providers, onClose, onCreated }: AddModelDialogProps) {
  const listId = useId();
  const keyOptions = allKeyOptions(providers);
  const [id, setId] = useState("");
  const [aliases, setAliases] = useState<string[]>([]);
  const [mode, setMode] = useState("");
  const [keyValue, setKeyValue] = useState(keyOptions[0]?.value || "");
  const [upstream, setUpstream] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const option = keyOptions.find((item) => item.value === keyValue);
  const provider = providers.find((item) => item.id === option?.providerId);
  const key = provider?.keys.find((item) => item.name === option?.keyName);
  const upstreams = keyUpstreams(key || {});

  async function submit() {
    const nextId = id.trim();
    if (!nextId) {
      setError("请填写模型名称。");
      return;
    }
    if (!option) {
      setError("还没有任何供应商 Key。请先在供应商页添加 Key，再回来新建模型。");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.createRoute(
        revision,
        nextId,
        [{ provider: option.providerId, key: option.keyName, upstream_model: upstream.trim() || nextId }],
        aliases,
        mode,
      );
      toast(`模型 ${nextId} 已创建。`);
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
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="添加模型名称"
      description="新建一个对外模型名；第一条目标必填，因为空模型在配置里不存在。"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>取消</Button>
          <Button variant="primary" leadingIcon={<Plus aria-hidden className="size-4" />} loading={saving} onClick={() => void submit()}>
            创建模型
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="模型名称" required>
            <Input value={id} onChange={(event) => setId(event.target.value)} placeholder="对外模型名，例如 my-model" />
          </Field>
          <Field label="别名" hint="回车或逗号成芯片；别名与模型名等价。">
            <TokenInput value={aliases} onChange={setAliases} placeholder="逗号分隔，留空表示无别名" aria-label="别名" />
          </Field>
          <Field label="路由模式">
            <Segmented aria-label="路由模式" value={mode} onValueChange={setMode} options={MODES} />
          </Field>
          <Field label="供应商 / Key" required>
            <Select value={keyValue} onChange={(event) => { setKeyValue(event.target.value); setUpstream(""); }} aria-label="供应商与 Key">
              {!keyOptions.length ? <option value="">没有可用的 Key</option> : null}
              {keyOptions.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
            </Select>
          </Field>
        </div>
        <Field label="上游模型名" hint="留空 = 与模型名相同。候选来自该 Key 的探测结果。">
          <Input
            value={upstream}
            onChange={(event) => setUpstream(event.target.value)}
            placeholder="上游模型名（留空 = 与模型名相同）"
            list={listId}
          />
        </Field>
        <datalist id={listId}>
          {upstreams.map((name) => <option key={name} value={name} />)}
        </datalist>
        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Dialog>
  );
}
