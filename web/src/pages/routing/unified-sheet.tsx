import { useMemo, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import { FAMILIES, enabledKeys } from "@/lib/endpoint-plans";
import type { ModelKindsIndex } from "@/lib/model-kinds";
import type { ModelSummaryResponse, UnifiedModel } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { ModelSelect, KeySelect } from "./model-select";
import { EFFORTS, familyModelAriaLabel, knownModel } from "./shared";

interface UnifiedSheetProps {
  unified: UnifiedModel | null;
  models: ModelSummaryResponse[];
  kinds: ModelKindsIndex | null;
  revision: string;
  onClose: () => void;
  onSaved: () => void;
  onRefresh?: () => Promise<unknown> | void;
  onAddModel?: () => void;
}

type Plan = { primary?: { model?: string; key?: string | null } | null; fallback?: { model?: string; key?: string | null } | null };

function buildFamilyDraft(unified: UnifiedModel | null, models: ModelSummaryResponse[]) {
  const initial: Record<string, { model: string; key: string }> = {};
  for (const family of FAMILIES) {
    const plan = unified?.[family.id] as Plan | undefined;
    const model = knownModel(models, plan?.primary?.model || "");
    initial[family.id] = { model, key: model ? plan?.primary?.key || "" : "" };
  }
  return initial;
}

// unifiedEditor 的 React 版：默认族是必经项，其余六族按端点分派；七个计划键每次写全，
// 未配置的族提交 null。推理强度属于模型本身，因此先写模型、拿到新 revision 再写计划。
export function UnifiedSheet({
  unified, models, kinds, revision, onClose, onSaved, onRefresh, onAddModel,
}: UnifiedSheetProps) {
  const [baseUnified, setBaseUnified] = useState<UnifiedModel | null>(unified);
  const [baseModels, setBaseModels] = useState<ModelSummaryResponse[]>(models);
  const [activeRevision, setActiveRevision] = useState(revision);

  const storedDefault = (baseUnified?.default || null) as Plan | null;
  const storedPrimary = knownModel(baseModels, storedDefault?.primary?.model || "");
  const storedFallback = knownModel(baseModels, storedDefault?.fallback?.model || "");

  const [primaryModel, setPrimaryModel] = useState(storedPrimary || baseModels[0]?.id || "");
  const [primaryKey, setPrimaryKey] = useState(storedPrimary ? storedDefault?.primary?.key || "" : "");
  const [routing, setRouting] = useState(storedPrimary && storedDefault?.primary?.key ? "key" : "auto");
  const [fallbackModel, setFallbackModel] = useState(storedFallback);
  const [fallbackKey, setFallbackKey] = useState(storedFallback ? storedDefault?.fallback?.key || "" : "");
  const [effort, setEffort] = useState(baseModels.find((model) => model.id === (storedPrimary || baseModels[0]?.id))?.reasoning_effort || "");
  const [families, setFamilies] = useState<Record<string, { model: string; key: string }>>(
    () => buildFamilyDraft(baseUnified, baseModels),
  );
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [saving, setSaving] = useState(false);

  const effectiveRevision = activeRevision || revision;
  const primaryKeys = enabledKeys(baseModels, primaryModel);

  const initial = useMemo(() => JSON.stringify({
    primary: storedPrimary || baseModels[0]?.id || "",
    primaryKey: storedPrimary ? storedDefault?.primary?.key || "" : "",
    fallback: storedFallback,
    fallbackKey: storedFallback ? storedDefault?.fallback?.key || "" : "",
    families: buildFamilyDraft(baseUnified, baseModels),
    effort: baseModels.find((model) => model.id === (storedPrimary || baseModels[0]?.id))?.reasoning_effort || "",
  }), [baseModels, baseUnified, storedDefault?.fallback?.key, storedDefault?.primary?.key, storedFallback, storedPrimary]);

  const current = JSON.stringify({
    primary: primaryModel,
    primaryKey: routing === "key" ? primaryKey : "",
    fallback: fallbackModel,
    fallbackKey: fallbackModel ? fallbackKey : "",
    families,
    effort,
  });
  const dirty = current !== initial;

  function changePrimary(next: string) {
    if (fallbackModel && next === fallbackModel) {
      // 选中的正好是当前回退 → 两者互换，避免出现主/回退同名这种必然被拒的配置。
      setFallbackModel(primaryModel);
      setFallbackKey(routing === "key" ? primaryKey : "");
      toast("主模型与回退模型已自动互换。");
    }
    setPrimaryModel(next);
    setEffort(baseModels.find((model) => model.id === next)?.reasoning_effort || "");
    const keys = enabledKeys(baseModels, next);
    if (!keys.includes(primaryKey)) setPrimaryKey(keys[0] || "");
    if (routing === "key" && !keys.length) setRouting("auto");
  }

  function changeRouting(next: string) {
    setRouting(next);
    if (next === "key" && !primaryKey) setPrimaryKey(primaryKeys[0] || "");
  }

  async function reloadLatest() {
    setReloading(true);
    try {
      const [unifiedRes, modelsRes] = await Promise.all([
        api.unified(),
        api.models(),
      ]);
      const freshUnified = ((unifiedRes as { unified_model?: UnifiedModel | null })?.unified_model ?? null);
      const freshModels = modelsRes.models || baseModels;
      const freshRevision = (unifiedRes as { config_revision?: string })?.config_revision || modelsRes.config_revision || effectiveRevision;

      const freshDefault = (freshUnified?.default || null) as Plan | null;
      const freshPrimary = knownModel(freshModels, freshDefault?.primary?.model || "");
      const freshFallback = knownModel(freshModels, freshDefault?.fallback?.model || "");
      const nextPrimary = freshPrimary || freshModels[0]?.id || "";

      setBaseUnified(freshUnified);
      setBaseModels(freshModels);
      setActiveRevision(freshRevision);
      setPrimaryModel(nextPrimary);
      setPrimaryKey(freshPrimary ? freshDefault?.primary?.key || "" : "");
      setRouting(freshPrimary && freshDefault?.primary?.key ? "key" : "auto");
      setFallbackModel(freshFallback);
      setFallbackKey(freshFallback ? freshDefault?.fallback?.key || "" : "");
      setEffort(freshModels.find((model) => model.id === nextPrimary)?.reasoning_effort || "");
      setFamilies(buildFamilyDraft(freshUnified, freshModels));
      setConflict(false);
      setError(null);
      await onRefresh?.();
      toast("已载入最新统一模型配置。");
    } catch (failure) {
      setError(`重新读取失败: ${errorText(failure)}`);
    } finally {
      setReloading(false);
    }
  }

  async function save() {
    if (!primaryModel) {
      setError("请先选择模型。");
      return;
    }
    if (routing === "key" && !primaryKey) {
      setError("当前模型没有可用的启用 Key。");
      return;
    }
    if (fallbackModel && fallbackModel === primaryModel) {
      setError("回退模型不能与主模型相同。");
      return;
    }
    for (const family of FAMILIES) {
      const picked = families[family.id];
      if (picked?.key && !picked.model) {
        setError(`${family.label}不能只指定 Key 而不指定模型。`);
        return;
      }
    }
    setSaving(true);
    setError(null);
    setConflict(false);
    let effortUpdated = false;
    try {
      let nextRevision = effectiveRevision;
      const storedEffort = baseModels.find((model) => model.id === primaryModel)?.reasoning_effort || "";
      if ((effort || "") !== (storedEffort || "")) {
        const updated = await api.updateModelEffort(nextRevision, primaryModel, effort || null) as ModelSummaryResponse & { config_revision?: string };
        effortUpdated = true;
        nextRevision = updated.config_revision || nextRevision;
        setActiveRevision(nextRevision);
      }
      const payload: Record<string, unknown> = {
        default: {
          primary: { model: primaryModel, key: routing === "key" ? primaryKey : null },
          fallback: fallbackModel ? { model: fallbackModel, key: fallbackKey || null } : null,
        },
      };
      for (const family of FAMILIES) {
        const picked = families[family.id];
        payload[family.id] = picked?.model
          ? { primary: { model: picked.model, key: picked.key || null } }
          : null;
      }
      await api.updateUnified(nextRevision, payload);
      toast.success("统一模型已更新。");
      onSaved();
      onClose();
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) {
        setConflict(true);
        void api.unified().then((res) => {
          const rev = (res as { config_revision?: string })?.config_revision;
          if (rev) setActiveRevision(rev);
        }).catch(() => undefined);
        void onRefresh?.();
      }
      if (effortUpdated) {
        void onRefresh?.();
        setError(`模型 ${primaryModel} 的推理强度已更新，但统一模型计划保存失败：${errorText(failure)}`);
      } else {
        setError(`统一模型操作失败: ${errorText(failure)}`);
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="编辑统一模型"
      subtitle="统一入口按入站端点族分派；default 是所有族的回落项"
      width="xl"
      dirty={dirty}
      onSave={save}
      saveLabel="保存"
      saving={saving}
    >
      <div className="space-y-5">
        {conflict ? (
          <Banner
            tone="warn"
            title="配置已在别处更新"
            action={(
              <Button
                size="sm"
                variant="secondary"
                loading={reloading}
                leadingIcon={<RefreshCw aria-hidden className="size-3.5" />}
                onClick={() => void reloadLatest()}
              >
                重新载入最新配置
              </Button>
            )}
          >
            你手上的版本已过期。建议先重新载入最新配置核对后再保存。
          </Banner>
        ) : null}

        {!baseModels.length ? (
          <Banner
            tone="warn"
            title="还没有可绑定的模型"
            action={onAddModel ? (
              <Button
                size="sm"
                variant="primary"
                leadingIcon={<Plus aria-hidden className="size-3.5" />}
                onClick={() => {
                  onClose();
                  onAddModel();
                }}
              >
                去添加模型
              </Button>
            ) : undefined}
          >
            请先添加至少一个模型路由，再为统一模型指定默认分派目标。
          </Banner>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="模型" required hint="文本（对话）族的真实模型，也是所有未配置族的回落项。">
            <ModelSelect
              models={baseModels}
              kinds={kinds}
              kind="text"
              value={primaryModel}
              aria-label="默认模型"
              disabled={!baseModels.length}
              onChange={changePrimary}
            />
          </Field>
          <Field label="推理强度" hint="写回默认模型本身；改了这里会先更新模型，再保存统一模型计划。">
            <Select aria-label="推理强度" value={effort} disabled={!primaryModel} onChange={(event) => setEffort(event.target.value)}>
              {EFFORTS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
            </Select>
          </Field>
          <Field label="路由方式">
            <Segmented
              aria-label="路由方式"
              value={routing}
              onValueChange={changeRouting}
              options={[
                { value: "auto", label: "自动路由" },
                { value: "key", label: "固定 Key", disabled: !primaryKeys.length },
              ]}
            />
          </Field>
          <Field
            label="固定 Key"
            hint={routing === "key" ? "固定 Key 必须属于当前模型已启用的 Key。" : "切为「固定 Key」后可锁定到单把 Key。"}
          >
            <KeySelect
              models={baseModels}
              modelId={primaryModel}
              value={routing === "key" ? primaryKey : ""}
              disabled={routing !== "key"}
              placeholder={routing === "key" ? "选择固定 Key" : "自动路由（按模型策略选择）"}
              aria-label="默认模型 Key"
              onChange={setPrimaryKey}
            />
          </Field>
          <Field label="回退模型" hint="主模型失败时改走它；留空表示不启用回退。">
            <ModelSelect
              models={baseModels.filter((model) => model.id !== primaryModel)}
              kinds={kinds}
              kind="text"
              value={fallbackModel}
              emptyLabel="不启用回退"
              disabled={!baseModels.length}
              aria-label="回退模型"
              onChange={(value) => {
                setFallbackModel(value);
                setFallbackKey("");
              }}
            />
          </Field>
          <Field label="回退 Key">
            <KeySelect
              models={baseModels}
              modelId={fallbackModel}
              value={fallbackKey}
              includeAuto
              aria-label="回退模型 Key"
              onChange={setFallbackKey}
            />
          </Field>
        </div>

        <div className="space-y-2">
          <div>
            <h3 className="text-dense font-medium text-ink">端点族计划</h3>
            <p className="mt-1 text-meta leading-5 text-ink-3">
              上面的默认模型服务对话端点；以下各族按入站端点分派，没有配置的族会回落到它。
              语音、视频与重排端点对 AMKR 不透明（只替换模型名后原样转发），因此所选模型必须绑定到确实提供该端点的上游 Key。
              选模型后换模型会清空该族已选 Key——Key 是模型级的。
            </p>
          </div>
          <div className="space-y-1.5">
            {FAMILIES.map((family) => {
              const picked = families[family.id] || { model: "", key: "" };
              return (
                <div key={family.id} className="grid items-center gap-2 rounded-control border border-rule px-2.5 py-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,0.8fr)]">
                  <div className="min-w-0">
                    <div className="text-dense text-ink">{family.label}</div>
                    <div className="truncate text-[11px] text-ink-3" title={family.paths}>{family.paths}</div>
                  </div>
                  <ModelSelect
                    models={baseModels}
                    kinds={kinds}
                    kind={family.kind}
                    value={picked.model}
                    emptyLabel="不配置映射"
                    aria-label={familyModelAriaLabel(family)}
                    onChange={(value) => setFamilies((currentFamilies) => ({
                      ...currentFamilies,
                      [family.id]: { model: value, key: "" },
                    }))}
                  />
                  <KeySelect
                    models={baseModels}
                    modelId={picked.model}
                    value={picked.key}
                    includeAuto
                    aria-label={family.keyLabel}
                    onChange={(value) => setFamilies((currentFamilies) => ({
                      ...currentFamilies,
                      [family.id]: { model: picked.model, key: value },
                    }))}
                  />
                </div>
              );
            })}
          </div>
        </div>

        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Sheet>
  );
}
