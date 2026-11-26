import { useMemo, useRef, useState } from "react";
import { ArrowDown, ArrowUp, GripVertical, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import {
  PLAN_FAMILIES,
  endpointPlanPayload,
  readEndpointPlans,
} from "@/lib/endpoint-plans";
import type { ModelKindsIndex } from "@/lib/model-kinds";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import { freezeLookupKey, freezeTriggerText } from "@/lib/quota-freeze";
import type {
  ModelSummaryResponse,
  Provider,
  QuotaFreeze,
  RouteEntry,
  RouteTarget,
  RoutesResponse,
} from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { TokenInput } from "@/components/ui/TokenInput";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { KeySelect, ModelSelect, ProviderKeyCombobox, UpstreamModelInput } from "./model-select";
import {
  EFFORTS,
  MODES,
  allKeyOptions,
  familyModelAriaLabel,
  findProviderKey,
  findUpstreamCandidates,
  keyUpstreams,
  normalizeMode,
  routeNames,
  targetIdentity,
  targetText,
} from "./shared";

interface RouteEditSheetProps {
  route: RouteEntry;
  providers: Provider[];
  models: ModelSummaryResponse[];
  kinds: ModelKindsIndex | null;
  freezes?: Map<string, QuotaFreeze>;
  revision: string;
  loadRoutes: () => Promise<RoutesResponse>;
  onClose: () => void;
  // null = 保存时把目标清空，路由已被删除。
  onSaved: (newId: string | null) => void;
}

interface EditableTarget extends RouteTarget {
  _uid: number;
}

// 路由编辑抽屉：名称/别名/模式/推理强度/有序目标/端点计划一次提交。
//
// 两个容易丢数据的点单独说明：
//   - 目标顺序是配置的一部分（轮询与优先级都按它走），所以拖拽与上下移按钮/键盘并存；
//   - 端点计划整体替换，`endpointPlanPayload` 只写配置了模型的族——这与服务端语义一致。
export function RouteEditSheet({
  route, providers, models, kinds, freezes, revision, loadRoutes, onClose, onSaved,
}: RouteEditSheetProps) {
  const impactConfirm = useImpactConfirm();
  const nextUid = useRef(1);
  const keyOptions = useMemo(() => allKeyOptions(providers, freezes), [freezes, providers]);

  const storedEffort = models.find((model) => model.id === route.id)?.reasoning_effort || "";

  const [baseRoute, setBaseRoute] = useState<RouteEntry>(route);
  const [baseEffort, setBaseEffort] = useState(storedEffort);
  const [activeRevision, setActiveRevision] = useState(revision);

  const [name, setName] = useState(route.id);
  const [aliases, setAliases] = useState<string[]>(route.aliases || []);
  const [mode, setMode] = useState(normalizeMode(route.routing_mode));
  const [effort, setEffort] = useState(storedEffort);
  const [targets, setTargets] = useState<EditableTarget[]>(() =>
    (route.targets || []).map((target) => ({ ...target, _uid: nextUid.current++ })),
  );
  const [plans, setPlans] = useState(() => readEndpointPlans(route.endpoints, models));
  const [newKey, setNewKey] = useState(keyOptions[0]?.value || "");
  const [newUpstream, setNewUpstream] = useState("");
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [dragOver, setDragOver] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [saving, setSaving] = useState(false);

  const effectiveRevision = activeRevision || revision;
  const effectiveNewKey = keyOptions.some((option) => option.value === newKey)
    ? newKey
    : (keyOptions[0]?.value || "");

  const initial = useMemo(() => JSON.stringify({
    name: baseRoute.id,
    aliases: baseRoute.aliases || [],
    mode: normalizeMode(baseRoute.routing_mode),
    effort: baseEffort,
    targets: (baseRoute.targets || []).map((target) => ({
      provider: target.provider, key: target.key, upstream_model: target.upstream_model || "",
    })),
    plans: readEndpointPlans(baseRoute.endpoints, models),
  }), [baseEffort, baseRoute, models]);

  const current = JSON.stringify({
    name,
    aliases,
    mode,
    effort,
    targets: targets.map((target) => ({
      provider: target.provider, key: target.key, upstream_model: target.upstream_model || "",
    })),
    plans,
  });
  const dirty = current !== initial;

  const candidates = useMemo(
    () => findUpstreamCandidates(
      providers,
      targets,
      routeNames({ id: name.trim() || baseRoute.id, aliases }),
      freezes,
    ),
    [aliases, baseRoute.id, freezes, name, providers, targets],
  );

  const selectedKeyOption = keyOptions.find((option) => option.value === effectiveNewKey);
  const selectedFound = selectedKeyOption
    ? findProviderKey(providers, selectedKeyOption.providerId, selectedKeyOption.keyName)
    : null;
  const upstreamOptions = keyUpstreams(selectedFound?.key || {});

  function move(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || from >= targets.length || to >= targets.length) return;
    setTargets((list) => {
      const next = [...list];
      const [picked] = next.splice(from, 1);
      next.splice(to, 0, picked);
      return next;
    });
  }

  function updateTarget(uid: number, upstream: string) {
    setTargets((list) => list.map((target) => target._uid === uid ? { ...target, upstream_model: upstream } : target));
  }

  function addTarget() {
    if (!selectedKeyOption) {
      setError("请先选择一个 Key。");
      return;
    }
    const upstream = newUpstream.trim() || name.trim() || baseRoute.id;
    const candidate: RouteTarget = {
      provider: selectedKeyOption.providerId,
      key: selectedKeyOption.keyName,
      upstream_model: upstream,
    };
    if (targets.some((target) => targetIdentity(target, name.trim() || baseRoute.id) === targetIdentity(candidate))) {
      setError("这个目标已经在本路由里了。");
      return;
    }
    setError(null);
    setTargets((list) => [...list, { ...candidate, _uid: nextUid.current++ }]);
    setNewUpstream("");
  }

  async function reloadLatest() {
    setReloading(true);
    try {
      const [routesRes, modelsRes] = await Promise.all([
        loadRoutes(),
        api.models().catch(() => null),
      ]);
      const fresh = routesRes.routes.find((item) => item.id === baseRoute.id);
      if (!fresh) {
        toast("这条路由已在别处被删除。");
        onSaved(null);
        return;
      }
      const nextModels = modelsRes?.models || models;
      const freshEffort = nextModels.find((item) => item.id === fresh.id)?.reasoning_effort || "";
      setBaseRoute(fresh);
      setBaseEffort(freshEffort);
      setActiveRevision(routesRes.config_revision || effectiveRevision);
      setName(fresh.id);
      setAliases(fresh.aliases || []);
      setMode(normalizeMode(fresh.routing_mode));
      setEffort(freshEffort);
      setTargets((fresh.targets || []).map((target) => ({ ...target, _uid: nextUid.current++ })));
      setPlans(readEndpointPlans(fresh.endpoints, nextModels));
      setConflict(false);
      setError(null);
      toast("已载入最新配置。");
    } catch (failure) {
      setError(`重新读取失败: ${errorText(failure)}`);
    } finally {
      setReloading(false);
    }
  }

  async function removeRoute(message: string, confirmLabel: string) {
    const { confirmed } = await writeWithImpactConfirm({
      title: "删除模型路由",
      message,
      confirmLabel,
      alwaysConfirm: true,
      preview: async () => (await api.deleteRoute(effectiveRevision, baseRoute.id, { dryRun: true })) as unknown as Impact,
      commit: () => api.deleteRoute(effectiveRevision, baseRoute.id),
      confirm: impactConfirm,
    });
    if (!confirmed) return;
    toast(`路由 ${baseRoute.id} 已删除。`);
    onSaved(null);
  }

  async function save() {
    const nextId = name.trim();
    if (!nextId) {
      setError("对外名称不能为空。");
      return;
    }
    const cleaned = targets.map((target) => ({
      provider: target.provider,
      key: target.key,
      upstream_model: String(target.upstream_model || "").trim() || nextId,
    }));
    const seen = new Set<string>();
    for (const target of cleaned) {
      const id = targetIdentity(target);
      if (seen.has(id)) {
        setError(`目标 ${targetText(target)} 重复，请先移除重复项。`);
        return;
      }
      seen.add(id);
    }

    setSaving(true);
    setError(null);
    setConflict(false);
    let routeSaved = false;
    try {
      if (!cleaned.length) {
        // 目标删空再保存 = 删路由。这和「删除路由」按钮是同一个后果，也要同样把连带变动
        // 先摆出来；用户取消后停在编辑态，删掉的目标还能加回来。
        await removeRoute(`路由 ${baseRoute.id} 的目标已清空，保存会删掉这条路由。`, "删除路由");
        return;
      }
      const updatedRoute = await api.updateRoute(
        effectiveRevision,
        baseRoute.id,
        cleaned,
        aliases,
        normalizeMode(mode),
        nextId === baseRoute.id ? null : nextId,
        endpointPlanPayload(plans),
      ) as RouteEntry & { config_revision?: string };
      routeSaved = true;

      if ((effort || "") !== (baseEffort || "")) {
        let effortRevision = updatedRoute?.config_revision || "";
        if (!effortRevision) {
          const freshRoutes = await loadRoutes();
          effortRevision = freshRoutes.config_revision || effectiveRevision;
        }
        await api.updateModelEffort(effortRevision, nextId, effort || null);
      }

      toast.success("路由已保存。");
      onSaved(nextId);
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) {
        setConflict(true);
        void loadRoutes().then((res) => {
          if (res.config_revision) setActiveRevision(res.config_revision);
        }).catch(() => undefined);
      }
      if (routeSaved) {
        setError(`路由已保存，但推理强度更新失败：${errorText(failure)}`);
      } else {
        setError(`保存失败: ${errorText(failure)}`);
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
      title="编辑模型路由"
      subtitle={baseRoute.id}
      width="xl"
      dirty={dirty}
      onSave={save}
      saveLabel="保存路由"
      saving={saving}
      footerLeft={(
        <Button
          variant="danger-ghost"
          leadingIcon={<Trash2 aria-hidden className="size-4" />}
          onClick={() => void removeRoute(
            `删除模型路由 ${baseRoute.id}？绑定它的 Key 只会解除绑定；不再被任何其他路由使用的 Key 会被一并删除。`,
            "删除",
          )}
        >
          删除路由
        </Button>
      )}
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
            你手上的版本已过期。建议先重新载入最新配置，避免覆盖他人的修改；若确认以当前表单为准，可直接再次保存。
          </Banner>
        ) : null}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="对外名称（路由名）" required hint="改名会同时改写 unified_model、模型的端点计划、任务、工作空间与访问密钥清单里指向它的名字。">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="对外模型名" />
          </Field>
          <Field label="别名" hint="回车、逗号或失焦成芯片；别名与路由名等价，都会出现在 /v1/models。">
            <TokenInput value={aliases} onChange={setAliases} placeholder="逗号分隔，留空表示无别名" aria-label="别名" />
          </Field>
          <Field label="路由模式">
            <Segmented aria-label="路由模式" value={mode} onValueChange={setMode} options={MODES} />
          </Field>
          <Field label="默认推理强度" hint="写回模型本身；对直呼此模型和统一模型均生效。">
            <Select aria-label="默认推理强度" value={effort} onChange={(event) => setEffort(event.target.value)}>
              {EFFORTS.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
            </Select>
          </Field>
        </div>

        <section className="space-y-2">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="text-dense font-medium text-ink">
              {mode === "priority" ? "目标列表（按优先级）" : mode === "only_first" ? "目标列表（仅首条生效）" : "目标列表（按轮询顺序）"}
            </h3>
            <span className="text-meta text-ink-3">拖动左侧手柄、聚焦后按 ↑/↓ 方向键或点击按钮调整顺序</span>
          </div>
          {targets.length ? (
            <div className="space-y-1.5">
              {targets.map((target, index) => {
                const found = findProviderKey(providers, target.provider, target.key);
                const disabled = Boolean(found && found.key.enabled === false);
                const missing = Boolean(providers.length > 0 && !found);
                const freeze = freezes?.get(freezeLookupKey(target.provider, target.key));
                const targetUpstreams = keyUpstreams(found?.key || {});
                return (
                  <div
                    key={target._uid}
                    onDragOver={(event) => {
                      event.preventDefault();
                      setDragOver(index);
                    }}
                    onDragLeave={() => {
                      if (dragOver === index) setDragOver(null);
                    }}
                    onDrop={(event) => {
                      event.preventDefault();
                      const from = Number(event.dataTransfer.getData("text/plain"));
                      move(Number.isFinite(from) ? from : dragIndex ?? index, index);
                      setDragIndex(null);
                      setDragOver(null);
                    }}
                    className={[
                      "flex flex-wrap items-center gap-2 rounded-control border px-2 py-1.5 transition-colors",
                      dragOver === index && dragIndex !== index
                        ? "border-accent bg-select ring-1 ring-accent/40"
                        : "border-rule bg-panel",
                      dragIndex === index ? "opacity-50" : "",
                    ].join(" ")}
                  >
                    <span
                      draggable
                      role="button"
                      tabIndex={0}
                      aria-label={`拖动目标 ${index + 1}，按上下方向键调整顺序`}
                      className="cursor-grab touch-none rounded-chip p-0.5 text-ink-3 focus-visible:outline-2 focus-visible:outline-focus active:cursor-grabbing"
                      onKeyDown={(event) => {
                        if (event.key === "ArrowUp") {
                          event.preventDefault();
                          move(index, index - 1);
                        } else if (event.key === "ArrowDown") {
                          event.preventDefault();
                          move(index, index + 1);
                        }
                      }}
                      onDragStart={(event) => {
                        setDragIndex(index);
                        event.dataTransfer.effectAllowed = "move";
                        event.dataTransfer.setData("text/plain", String(index));
                      }}
                      onDragEnd={() => { setDragIndex(null); setDragOver(null); }}
                    >
                      <GripVertical aria-hidden className="size-4" />
                    </span>
                    <span className="grid size-6 shrink-0 place-items-center rounded-pill bg-ink text-[11px] tabular-nums text-panel">
                      {index + 1}
                    </span>
                    <span className="max-w-52 truncate font-mono text-meta text-ink-2" title={`${target.provider} / ${target.key}`}>
                      {target.provider} / {target.key}
                    </span>
                    {missing ? <Badge tone="warn">Key 不存在</Badge> : null}
                    {disabled ? <Badge tone="warn">已禁用</Badge> : null}
                    {freeze ? <Badge tone="critical">{freezeTriggerText(freeze)}</Badge> : null}
                    <UpstreamModelInput
                      value={target.upstream_model || ""}
                      candidates={targetUpstreams}
                      placeholder={name.trim() || baseRoute.id}
                      aria-label={`${target.provider} / ${target.key} 的上游模型名`}
                      className="w-52"
                      inputClassName="h-7"
                      onChange={(value) => updateTarget(target._uid, value)}
                    />
                    <span className="flex-1" />
                    <div className="flex items-center gap-1">
                      <IconButton aria-label={`上移目标 ${index + 1}`} size="sm" disabled={index === 0} onClick={() => move(index, index - 1)}>
                        <ArrowUp aria-hidden className="size-3.5" />
                      </IconButton>
                      <IconButton aria-label={`下移目标 ${index + 1}`} size="sm" disabled={index === targets.length - 1} onClick={() => move(index, index + 1)}>
                        <ArrowDown aria-hidden className="size-3.5" />
                      </IconButton>
                      <IconButton
                        aria-label={`移除目标 ${index + 1}`}
                        size="sm"
                        onClick={() => setTargets((list) => list.filter((item) => item._uid !== target._uid))}
                      >
                        <Trash2 aria-hidden className="size-3.5" />
                      </IconButton>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="rounded-control border border-dashed border-rule px-3 py-3 text-meta text-ink-3">
              此路由没有任何目标。保存后它会连同路由一起被删除——空路由不会出现在 /v1/models 里。
            </p>
          )}

          <div className="rounded-control bg-sunken p-2.5">
            <div className="grid items-center gap-2 sm:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)_auto]">
              <ProviderKeyCombobox
                providers={providers}
                freezes={freezes}
                value={effectiveNewKey}
                aria-label="添加目标：选择 Key"
                onChange={(nextValue) => {
                  setNewKey(nextValue);
                  setNewUpstream("");
                }}
              />
              <UpstreamModelInput
                value={newUpstream}
                candidates={upstreamOptions}
                placeholder={name.trim() || baseRoute.id}
                aria-label="添加目标：上游模型名"
                onChange={setNewUpstream}
                onEnter={addTarget}
              />
              <Button size="sm" variant="secondary" leadingIcon={<Plus aria-hidden className="size-3.5" />} onClick={addTarget}>
                添加目标
              </Button>
            </div>
            <p className="mt-1.5 text-meta text-ink-3">上游模型名留空 = 与路由名相同，按回车可直接添加。候选来自所选 Key 的探测结果。</p>
          </div>

          <div className="space-y-1.5">
            <p className="text-meta text-ink-3">探测到同名或相似上游模型的未绑定 Key</p>
            {candidates.length ? (
              <div className="flex flex-wrap gap-1.5">
                {candidates.map((candidate) => (
                  <Button
                    key={targetText(candidate)}
                    size="sm"
                    variant="secondary"
                    title={targetText(candidate)}
                    onClick={() => setTargets((list) => [...list, {
                      provider: candidate.provider,
                      key: candidate.key,
                      upstream_model: candidate.upstream_model,
                      _uid: nextUid.current++,
                    }])}
                  >
                    + {candidate.provider} / {candidate.key} / {candidate.upstream_model}
                    {!candidate.exact ? <span className="ml-1 text-[11px] text-ink-3">相似</span> : null}
                    {!candidate.enabled ? <span className="ml-1 text-[11px] text-warn-ink">已禁用</span> : null}
                    {candidate.frozen ? <span className="ml-1 text-[11px] text-critical-ink">冻结</span> : null}
                  </Button>
                ))}
              </div>
            ) : (
              <p className="text-meta text-ink-3">没有探测到匹配本路由名称的未绑定 Key。上游名完全不同时，可用上面的「添加目标」手选 Key 与上游名。</p>
            )}
          </div>
        </section>

        <section className="space-y-2">
          <div>
            <h3 className="text-dense font-medium text-ink">端点计划</h3>
            <p className="mt-1 text-meta leading-5 text-ink-3">
              给这个名称指定各端点族改发给哪个模型：直呼它时，配了计划的端点走那一族的模型，没配的端点仍用上面的目标。
              目标模型必须是模型列表里已有的名称。语音、视频与重排端点对 AMKR 不透明（只替换模型名后原样转发），
              因此所选模型必须绑定到确实提供该端点的上游 Key。
            </p>
          </div>
          <div className="space-y-1.5">
            {PLAN_FAMILIES.map((family) => {
              const picked = plans[family.id] || { model: "", key: "" };
              return (
                <div key={family.id} className="grid items-center gap-2 rounded-control border border-rule px-2.5 py-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,0.8fr)]">
                  <div className="min-w-0">
                    <div className="text-dense text-ink">{family.label}</div>
                    <div className="truncate text-[11px] text-ink-3" title={family.paths}>{family.paths}</div>
                  </div>
                  <ModelSelect
                    models={models}
                    kinds={kinds}
                    kind={family.kind}
                    value={picked.model}
                    emptyLabel="不配置映射"
                    aria-label={familyModelAriaLabel(family)}
                    onChange={(value) => setPlans((currentPlans) => ({
                      ...currentPlans,
                      [family.id]: { model: value, key: "" },
                    }))}
                  />
                  <KeySelect
                    models={models}
                    modelId={picked.model}
                    value={picked.key}
                    includeAuto
                    aria-label={family.keyLabel}
                    onChange={(value) => setPlans((currentPlans) => ({
                      ...currentPlans,
                      [family.id]: { model: picked.model, key: value },
                    }))}
                  />
                </div>
              );
            })}
          </div>
        </section>

        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Sheet>
  );
}
