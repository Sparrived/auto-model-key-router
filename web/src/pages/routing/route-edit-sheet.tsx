import { useId, useMemo, useState } from "react";
import { ArrowDown, ArrowUp, GripVertical, Plus, Trash2 } from "lucide-react";
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
import type {
  ModelSummaryResponse,
  Provider,
  RouteEntry,
  RouteTarget,
  RoutesResponse,
} from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { TokenInput } from "@/components/ui/TokenInput";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { ModelSelect, KeySelect } from "./model-select";
import { MODES, allKeyOptions, keyUpstreams, routeNames, targetText } from "./shared";

interface RouteEditSheetProps {
  route: RouteEntry;
  providers: Provider[];
  models: ModelSummaryResponse[];
  kinds: ModelKindsIndex | null;
  revision: string;
  loadRoutes: () => Promise<RoutesResponse>;
  onClose: () => void;
  // null = 保存时把目标清空，路由已被删除。
  onSaved: (newId: string | null) => void;
}

// 路由编辑抽屉：名称/别名/模式/有序目标/端点计划一次提交。
//
// 两个容易丢数据的点单独说明：
//   - 目标顺序是配置的一部分（轮询与优先级都按它走），所以拖拽与上下移按钮并存：
//     前者快，后者在不支持 HTML5 拖拽的环境与键盘操作下仍可用；
//   - 端点计划整体替换，`endpointPlanPayload` 只写配置了模型的族——这与服务端语义一致。
export function RouteEditSheet({ route, providers, models, kinds, revision, loadRoutes, onClose, onSaved }: RouteEditSheetProps) {
  const impactConfirm = useImpactConfirm();
  const listId = useId();
  const keyOptions = useMemo(() => allKeyOptions(providers), [providers]);
  const [name, setName] = useState(route.id);
  const [aliases, setAliases] = useState<string[]>(route.aliases || []);
  const [mode, setMode] = useState(route.routing_mode || "");
  const [targets, setTargets] = useState<RouteTarget[]>(() => (route.targets || []).map((target) => ({ ...target })));
  const [plans, setPlans] = useState(() => readEndpointPlans(route.endpoints, models));
  const [newKey, setNewKey] = useState(keyOptions[0]?.value || "");
  const [newUpstream, setNewUpstream] = useState("");
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [dragOver, setDragOver] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [saving, setSaving] = useState(false);

  const initial = useMemo(() => JSON.stringify({
    name: route.id,
    aliases: route.aliases || [],
    mode: route.routing_mode || "",
    targets: (route.targets || []).map((target) => ({
      provider: target.provider, key: target.key, upstream_model: target.upstream_model || "",
    })),
    plans: readEndpointPlans(route.endpoints, models),
  }), [models, route]);

  const current = JSON.stringify({
    name,
    aliases,
    mode,
    targets: targets.map((target) => ({
      provider: target.provider, key: target.key, upstream_model: target.upstream_model || "",
    })),
    plans,
  });
  const dirty = current !== initial;

  const proposedNames = new Set(routeNames({ id: name.trim() || route.id, aliases }));
  const bound = new Set(targets.map((target) => `${target.provider}|${target.key}|${target.upstream_model}`));
  const candidates = providers.flatMap((provider) =>
    (provider.keys || []).flatMap((key) =>
      keyUpstreams(key)
        .filter((upstream) => proposedNames.has(upstream))
        .filter((upstream) => !bound.has(`${provider.id}|${key.name}|${upstream}`))
        .map((upstream) => ({ provider: provider.id, key: key.name, upstream_model: upstream })),
    ),
  );

  const selectedKeyOption = keyOptions.find((option) => option.value === newKey);
  const selectedProvider = providers.find((provider) => provider.id === selectedKeyOption?.providerId);
  const selectedKey = selectedProvider?.keys.find((key) => key.name === selectedKeyOption?.keyName);
  const upstreamOptions = keyUpstreams(selectedKey || {});

  function move(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || from >= targets.length || to >= targets.length) return;
    setTargets((list) => {
      const next = [...list];
      const [picked] = next.splice(from, 1);
      next.splice(to, 0, picked);
      return next;
    });
  }

  function updateTarget(index: number, upstream: string) {
    setTargets((list) => list.map((target, position) => position === index ? { ...target, upstream_model: upstream } : target));
  }

  function addTarget() {
    if (!selectedKeyOption) {
      setError("请先选择一个 Key。");
      return;
    }
    const upstream = newUpstream.trim() || name.trim() || route.id;
    if (targets.some((target) =>
      target.provider === selectedKeyOption.providerId
      && target.key === selectedKeyOption.keyName
      && (target.upstream_model || "") === upstream)) {
      setError("这个目标已经在本路由里了。");
      return;
    }
    setError(null);
    setTargets((list) => [...list, {
      provider: selectedKeyOption.providerId,
      key: selectedKeyOption.keyName,
      upstream_model: upstream,
    }]);
    setNewUpstream("");
  }

  async function removeRoute(message: string, confirmLabel: string) {
    const { confirmed } = await writeWithImpactConfirm({
      title: "删除模型路由",
      message,
      confirmLabel,
      alwaysConfirm: true,
      preview: async () => (await api.deleteRoute(revision, route.id, { dryRun: true })) as unknown as Impact,
      commit: () => api.deleteRoute(revision, route.id),
      confirm: impactConfirm,
    });
    if (!confirmed) return;
    toast(`路由 ${route.id} 已删除。`);
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
    setSaving(true);
    setError(null);
    setConflict(false);
    try {
      if (!cleaned.length) {
        // 目标删空再保存 = 删路由。这和「删除路由」按钮是同一个后果，也要同样把连带变动
        // 先摆出来；用户取消后停在编辑态，删掉的目标还能加回来。
        await removeRoute(`路由 ${route.id} 的目标已清空，保存会删掉这条路由。`, "删除路由");
        return;
      }
      await api.updateRoute(
        revision,
        route.id,
        cleaned,
        aliases,
        // 服务端只接受 priority / round_robin / only_first；「默认策略」按运行时默认的轮询落盘。
        mode || "round_robin",
        nextId === route.id ? null : nextId,
        endpointPlanPayload(plans),
      );
      toast("路由已保存。");
      onSaved(nextId);
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) {
        setConflict(true);
        void loadRoutes();
      }
      setError(`保存失败: ${errorText(failure)}`);
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
      subtitle={route.id}
      width="xl"
      dirty={dirty}
      onSave={save}
      saveLabel="保存路由"
      saving={saving}
      footer={
        <div className="flex w-full items-center justify-between gap-3">
          <Button
            variant="danger-ghost"
            leadingIcon={<Trash2 aria-hidden className="size-4" />}
            onClick={() => void removeRoute(
              `删除模型路由 ${route.id}？绑定它的 Key 只会解除绑定；不再被任何其他路由使用的 Key 会被一并删除。`,
              "删除",
            )}
          >
            删除路由
          </Button>
          <div className="flex items-center gap-2">
            <Button variant="secondary" onClick={onClose}>取消</Button>
            <Button variant="primary" loading={saving} onClick={() => void save()}>保存路由</Button>
          </div>
        </div>
      }
    >
      <div className="space-y-5">
        {conflict ? <Banner tone="warn" title="配置已在别处更新">已重新读取最新配置，请核对后再保存。</Banner> : null}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="对外名称（路由名）" required hint="改名会同时改写 unified_model、模型的端点计划、任务、工作空间与访问密钥清单里指向它的名字。">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="对外模型名" />
          </Field>
          <Field label="别名" hint="回车或逗号成芯片；别名与路由名等价，都会出现在 /v1/models。">
            <TokenInput value={aliases} onChange={setAliases} placeholder="逗号分隔，留空表示无别名" aria-label="别名" />
          </Field>
          <Field label="路由模式">
            <Segmented aria-label="路由模式" value={mode} onValueChange={setMode} options={MODES} />
          </Field>
        </div>

        <section className="space-y-2">
          <div className="flex items-baseline justify-between">
            <h3 className="text-dense font-medium text-ink">轮询目标（按顺序）</h3>
            <span className="text-meta text-ink-3">拖动左侧手柄或使用上移 / 下移调整优先级</span>
          </div>
          {targets.length ? (
            <div className="space-y-1.5">
              {targets.map((target, index) => (
                <div
                  key={`${target.provider}|${target.key}|${index}`}
                  onDragOver={(event) => {
                    event.preventDefault();
                    setDragOver(index);
                  }}
                  onDrop={(event) => {
                    event.preventDefault();
                    const from = Number(event.dataTransfer.getData("text/plain"));
                    move(Number.isFinite(from) ? from : dragIndex ?? index, index);
                    setDragIndex(null);
                    setDragOver(null);
                  }}
                  className={[
                    "flex flex-wrap items-center gap-2 rounded-control border px-2 py-1.5",
                    dragOver === index ? "border-ink bg-select" : "border-rule bg-panel",
                  ].join(" ")}
                >
                  <span
                    draggable
                    role="button"
                    tabIndex={0}
                    aria-label={`拖动目标 ${index + 1}`}
                    className="cursor-grab touch-none text-ink-3 active:cursor-grabbing"
                    onDragStart={(event) => {
                      setDragIndex(index);
                      event.dataTransfer.effectAllowed = "move";
                      event.dataTransfer.setData("text/plain", String(index));
                    }}
                    onDragEnd={() => { setDragIndex(null); setDragOver(null); }}
                  >
                    <GripVertical aria-hidden className="size-4" />
                  </span>
                  <span className="grid size-6 shrink-0 place-items-center rounded-pill bg-ink text-[11px] tabular-nums text-panel">{index + 1}</span>
                  <span className="max-w-56 truncate font-mono text-meta text-ink-2" title={`${target.provider} / ${target.key}`}>
                    {target.provider} / {target.key}
                  </span>
                  <Input
                    value={target.upstream_model || ""}
                    onChange={(event) => updateTarget(index, event.target.value)}
                    placeholder={name.trim() || route.id}
                    aria-label={`${target.provider} / ${target.key} 的上游模型名`}
                    list={`${listId}-target-${index}`}
                    className="h-7 w-48"
                  />
                  <datalist id={`${listId}-target-${index}`}>
                    {keyUpstreams(providers.find((provider) => provider.id === target.provider)?.keys.find((key) => key.name === target.key) || {})
                      .map((upstream) => <option key={upstream} value={upstream} />)}
                  </datalist>
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
                      onClick={() => setTargets((list) => list.filter((_, position) => position !== index))}
                    >
                      <Trash2 aria-hidden className="size-3.5" />
                    </IconButton>
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="rounded-control border border-dashed border-rule px-3 py-3 text-meta text-ink-3">
              此路由没有任何目标。保存后它会连同路由一起被删除——空路由不会出现在 /v1/models 里。
            </p>
          )}

          <div className="rounded-control bg-sunken p-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <Select
                value={newKey}
                onChange={(event) => {
                  setNewKey(event.target.value);
                  setNewUpstream("");
                }}
                aria-label="添加目标：选择 Key"
                className="w-64"
              >
                {!keyOptions.length ? <option value="">没有可用的 Key</option> : null}
                {keyOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
              <Input
                value={newUpstream}
                onChange={(event) => setNewUpstream(event.target.value)}
                placeholder={name.trim() || route.id}
                aria-label="添加目标：上游模型名"
                list={`${listId}-add-target`}
                className="w-52"
              />
              <datalist id={`${listId}-add-target`}>
                {upstreamOptions.map((upstream) => <option key={upstream} value={upstream} />)}
              </datalist>
              <Button size="sm" variant="secondary" leadingIcon={<Plus aria-hidden className="size-3.5" />} onClick={addTarget}>
                添加目标
              </Button>
            </div>
            <p className="mt-1.5 text-meta text-ink-3">上游模型名留空 = 与路由名相同。候选来自所选 Key 的探测结果。</p>
          </div>

          <div className="space-y-1.5">
            <p className="text-meta text-ink-3">探测到本路由名称的 Key</p>
            {candidates.length ? (
              <div className="flex flex-wrap gap-1.5">
                {candidates.map((candidate) => (
                  <Button
                    key={targetText(candidate)}
                    size="sm"
                    variant="secondary"
                    title={targetText(candidate)}
                    onClick={() => setTargets((list) => [...list, { ...candidate }])}
                  >
                    + {candidate.provider} / {candidate.key} / {candidate.upstream_model}
                  </Button>
                ))}
              </div>
            ) : (
              <p className="text-meta text-ink-3">没有探测到本路由名称的未绑定 Key。上游名与路由名不同时，用上面的「添加目标」手选 Key 与上游名。</p>
            )}
          </div>
        </section>

        <section className="space-y-2">
          <div>
            <h3 className="text-dense font-medium text-ink">端点计划</h3>
            <p className="mt-1 text-meta leading-5 text-ink-3">
              给这个名称指定各端点族改发给哪个模型：直呼它时，配了计划的端点走那一族的模型，没配的端点仍用上面的轮询目标。
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
                    aria-label={`${family.label}模型`}
                    onChange={(value) => setPlans((current) => ({
                      ...current,
                      [family.id]: { model: value, key: "" },
                    }))}
                  />
                  <KeySelect
                    models={models}
                    modelId={picked.model}
                    value={picked.key}
                    includeAuto
                    aria-label={family.keyLabel}
                    onChange={(value) => setPlans((current) => ({
                      ...current,
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
