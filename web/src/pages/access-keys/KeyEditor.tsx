// 访问密钥编辑抽屉：新建与编辑共用。
//
// 两份清单的三态在提交时**逐字节**不同：
//   POST 不限制 = 不传字段；PUT 不限制 = null；全部禁止 = []；指定 = 排序后的数组。
// 这里不做任何「空数组等于不限制」的折叠——那会把运维写下的禁令静默读反。

import { useRef, useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { AccessKeyEntry, Provider } from "@/lib/types";
import { api } from "@/lib/api";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { Switch } from "@/components/ui/Switch";
import { useSecretDialog } from "@/components/ui/SecretDialog";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { EffectivePreview, MismatchBanner, ModelChecklist, ProviderChecklist, ScopeSection } from "./ScopeEditor";
import {
  effectiveScope,
  scopeMode,
  scopeValue,
  type ModelGroup,
  type ModelOption,
  type ModelProviderIndex,
  type ScopeMode,
} from "./scope";

function createScope(mode: ScopeMode, selected: string[]): string[] | undefined {
  const value = scopeValue(mode, selected);
  return value === null ? undefined : value;
}

export interface KeyEditorProps {
  accessKey: AccessKeyEntry | null;
  isNew: boolean;
  revision: string;
  providers: Provider[];
  index: ModelProviderIndex;
  modelOptions: ModelOption[];
  modelGroups: ModelGroup[];
  onClose: () => void;
  onSaved: () => void | Promise<void>;
  onRefresh: () => void | Promise<void>;
}

export function KeyEditor({
  accessKey, isNew, revision, providers, index, modelOptions, modelGroups,
  onClose, onSaved, onRefresh,
}: KeyEditorProps) {
  const showSecret = useSecretDialog();
  const confirm = useConfirm();
  const [name, setName] = useState(accessKey?.name || "");
  const [enabled, setEnabled] = useState(accessKey?.enabled !== false);
  const [key, setKey] = useState("");
  const [providerMode, setProviderMode] = useState<ScopeMode>(() => scopeMode(accessKey?.providers));
  const [selectedProviders, setSelectedProviders] = useState<string[]>(() => [...(accessKey?.providers || [])]);
  const [modelMode, setModelMode] = useState<ScopeMode>(() => scopeMode(accessKey?.models));
  const [selectedModels, setSelectedModels] = useState<string[]>(() => [...(accessKey?.models || [])]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<{ tone: "critical" | "warn"; text: string } | null>(null);
  const baseline = useRef(JSON.stringify({
    name: accessKey?.name || "",
    enabled: accessKey?.enabled !== false,
    key: "",
    providerMode: scopeMode(accessKey?.providers),
    selectedProviders: [...(accessKey?.providers || [])].sort(),
    modelMode: scopeMode(accessKey?.models),
    selectedModels: [...(accessKey?.models || [])].sort(),
  }));

  const dirty = JSON.stringify({
    name, enabled, key,
    providerMode,
    selectedProviders: [...selectedProviders].sort(),
    modelMode,
    selectedModels: [...selectedModels].sort(),
  }) !== baseline.current;

  const scope = effectiveScope({
    providerMode,
    providers: selectedProviders,
    modelMode,
    models: selectedModels,
    allModels: modelOptions.map((option) => option.value),
    modelToProviders: index.modelToProviders,
  });

  const providerSummary = providerMode === "unrestricted"
    ? "不限制：允许全部供应商。"
    : providerMode === "none"
      ? "全部禁止：这把密钥无法使用任何供应商，调用会直接失败。"
      : `已选 ${selectedProviders.length} 个供应商。`;
  const modelSummary = modelMode === "unrestricted"
    ? "不限制：允许全部模型与别名。"
    : modelMode === "none"
      ? "全部禁止：这把密钥无法调用任何模型。"
      : `已选 ${selectedModels.length} 个模型名（含别名）。`;

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
    const trimmed = name.trim();
    if (!trimmed) {
      setError({ tone: "critical", text: "请填写名称。" });
      return;
    }
    if (!revision) {
      setError({ tone: "critical", text: "尚未读取到配置版本，请稍后重试。" });
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (isNew) {
        const created = await api.createAccessKey(revision, trimmed, {
          ...(key.trim() ? { key: key.trim() } : {}),
          enabled,
          // 新建时「不限制」= 字段整体不传（POST 没有「清除」这一态）。
          providers: createScope(providerMode, selectedProviders),
          models: createScope(modelMode, selectedModels),
        });
        toast.success(`访问密钥 ${trimmed} 已创建。`);
        await onSaved();
        if (created.key) {
          await showSecret({
            title: "访问密钥已创建",
            warning: "这是唯一一次显示明文密钥。关闭后只能看到指纹，需要再取请轮换。",
            items: [
              { label: "访问密钥", value: created.key },
              { label: "名称", value: created.name || trimmed },
              { label: "ID", value: created.id },
            ],
          });
        }
      } else {
        await api.updateAccessKey(revision, accessKey!.id, {
          name: trimmed,
          enabled,
          // 编辑时三态必须显式提交：漏传会被服务端当成漏字段拒掉，null 才是清除限制。
          providers: scopeValue(providerMode, selectedProviders),
          models: scopeValue(modelMode, selectedModels),
        });
        toast.success("访问密钥已更新。");
        await onSaved();
      }
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
      title={isNew ? "新建访问密钥" : `编辑访问密钥 · ${accessKey?.name || ""}`}
      subtitle={isNew
        ? "密钥只写入服务端，之后列表只显示指纹。"
        : `指纹 ${accessKey?.key_fingerprint || "-"}；修改清单只影响后续调用。`}
      width="xl"
      dirty={dirty}
      onSave={save}
      saving={saving}
      footer={(
        <>
          <Button variant="secondary" disabled={saving} onClick={() => void cancel()}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void save()}>
            {saving ? "保存中…" : isNew ? "创建" : "保存"}
          </Button>
        </>
      )}
    >
      <div className="space-y-4">
        {error ? <Banner tone={error.tone}>{error.text}</Banner> : null}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="名称" required>
            <Input
              value={name}
              disabled={saving}
              placeholder="例如 试用账号 A"
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          {isNew ? (
            <Field label="密钥（留空自动生成）" hint="也可以填入应用侧已有的凭据。">
              <Input
                value={key}
                disabled={saving}
                autoComplete="off"
                placeholder="留空由服务端生成"
                onChange={(event) => setKey(event.target.value)}
              />
            </Field>
          ) : (
            <div className="flex items-end pb-1">
              <Switch checked={enabled} onCheckedChange={setEnabled} disabled={saving} label="启用这把密钥" />
            </div>
          )}
        </div>
        {isNew ? (
          <Switch checked={enabled} onCheckedChange={setEnabled} disabled={saving} label="创建后立即启用" />
        ) : null}

        <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
          <div className="space-y-3">
            <ScopeSection
              title="允许的供应商"
              subtitle="限定这把密钥能用到哪些上游供应商。"
              mode={providerMode}
              onModeChange={setProviderMode}
              summary={providerSummary}
            >
              <ProviderChecklist
                providers={providers}
                modelCounts={index.providerToModels}
                selected={selectedProviders}
                onChange={setSelectedProviders}
              />
            </ScopeSection>

            <ScopeSection
              title="允许的模型"
              subtitle="限定这把密钥能调用的模型与别名；别名要单独勾选。"
              mode={modelMode}
              onModeChange={setModelMode}
              summary={modelSummary}
            >
              <ModelChecklist
                groups={modelGroups}
                known={new Set(modelOptions.map((option) => option.value))}
                selected={selectedModels}
                onChange={setSelectedModels}
              />
            </ScopeSection>
          </div>

          <div className="space-y-3 lg:sticky lg:top-0">
            {scope.mismatched.length > 0 ? (
              <MismatchBanner
                count={scope.mismatched.length}
                items={scope.mismatched}
                missingProviders={scope.missingProviders}
                onGrant={() => {
                  setSelectedProviders((current) => [...new Set([...current, ...scope.missingProviders])]);
                }}
              />
            ) : null}
            <EffectivePreview scope={scope} totalModels={modelOptions.length} />
            <p className="text-meta leading-5 text-ink-3">
              开着「不限制」= 清除该项清单；关掉它再勾选 = 限定为所选内容（一个都不勾 = 一个都不许）。
              两份清单都在配置里现有的名字里勾选，服务端最终校验引用的存在性。
            </p>
          </div>
        </div>
      </div>
    </Sheet>
  );
}
