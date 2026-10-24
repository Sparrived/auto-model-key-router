import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, Plus, Search } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { errorText } from "@/lib/format";
import {
  ENDPOINT_FAMILY,
  endpointLabel,
  endpointPath,
  endpointsOf,
  groupByKind,
  normalizeModelKinds,
  type ModelKindsIndex,
} from "@/lib/model-kinds";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import type { Provider, ProviderKey } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";

const MODEL_KIND_ALL = "all";

export interface BindingState {
  loading: boolean;
  loadError: string | null;
  kinds: ModelKindsIndex | null;
  all: string[];
  selected: Set<string>;
  initial: Set<string>;
  discovered: Set<string>;
  search: string;
  setSearch: (value: string) => void;
  kindFilter: string;
  setKindFilter: (value: string) => void;
  toggle: (model: string) => void;
  addCustom: (model: string) => void;
  selectVisible: (names: string[]) => void;
  clear: () => void;
  added: number;
  removed: number;
  saving: boolean;
  saveError: string | null;
  conflict: boolean;
  save: () => Promise<boolean>;
}

interface UseKeyModelBindingArgs {
  providerId: string;
  keyName: string;
  revision: string;
  discovered: string[];
  enabled?: boolean;
  onSaved?: () => void;
}

// useKeyModelBinding 管理「Key 服务模型」抽屉的草稿与保存。
//
// 草稿在**读取完成时**初始化一次；之后重新取数（轮询、409 刷新）只更新可用模型清单，
// 不会覆盖用户正在勾选的内容。保存走「先预演、再确认、最后真写」。
export function useKeyModelBinding({
  providerId, keyName, revision, discovered, enabled = true, onSaved,
}: UseKeyModelBindingArgs): BindingState {
  const client = useQueryClient();
  const impactConfirm = useImpactConfirm();
  const modelsQuery = useQuery({
    queryKey: ["key-models", providerId, keyName],
    queryFn: () => api.keyModels(providerId, keyName),
    enabled: enabled && Boolean(providerId && keyName),
    retry: false,
  });

  const [initial, setInitial] = useState<Set<string>>(new Set());
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [ready, setReady] = useState(false);
  const [search, setSearch] = useState("");
  const [kindFilter, setKindFilter] = useState(MODEL_KIND_ALL);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);

  // 换 Key（添加 Key 向导从第 ② 步进入第 ③ 步）时重新初始化。
  useEffect(() => {
    setInitial(new Set());
    setSelected(new Set());
    setReady(false);
    setSearch("");
    setKindFilter(MODEL_KIND_ALL);
    setSaveError(null);
    setConflict(false);
  }, [providerId, keyName]);

  useEffect(() => {
    if (!enabled || ready || !modelsQuery.data) return;
    const bound = [...new Set((modelsQuery.data.models || []).map((name) => String(name).trim()).filter(Boolean))].sort();
    setInitial(new Set(bound));
    setSelected(new Set(bound));
    setReady(true);
  }, [enabled, modelsQuery.data, ready]);

  const all = useMemo(() => {
    const names = new Set<string>([...initial, ...discovered, ...selected]);
    return [...names].sort();
  }, [discovered, initial, selected]);

  const kindNames = useMemo(() => [...new Set([...discovered, ...initial, ...selected])].sort(), [discovered, initial, selected]);
  const kindsQuery = useQuery({
    queryKey: queryKeys.modelKinds(kindNames),
    queryFn: () => api.modelKinds(kindNames),
    enabled: enabled && kindNames.length > 0,
    retry: false,
  });
  const kinds = useMemo(
    () => (kindsQuery.data ? normalizeModelKinds(kindsQuery.data) : null),
    [kindsQuery.data],
  );

  function toggle(model: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(model)) next.delete(model);
      else next.add(model);
      return next;
    });
  }

  function addCustom(model: string) {
    const value = model.trim();
    if (!value) return;
    setSelected((current) => new Set(current).add(value));
  }

  function selectVisible(names: string[]) {
    setSelected((current) => new Set([...current, ...names]));
  }

  function clear() {
    setSelected(new Set());
  }

  const added = [...selected].filter((model) => !initial.has(model)).length;
  const removed = [...initial].filter((model) => !selected.has(model)).length;

  async function save(): Promise<boolean> {
    // 快照一份选中集合：确认框弹出期间用户仍可能改动卡片，而真写必须与预演送的是同一份清单。
    const models = [...selected];
    setSaving(true);
    setSaveError(null);
    setConflict(false);
    try {
      const { confirmed } = await writeWithImpactConfirm({
        title: "保存 Key 服务模型",
        message: `保存 Key ${keyName} 的 ${models.length} 个服务模型？`,
        confirmLabel: "保存模型",
        preview: async () => (await api.setKeyModels(revision, providerId, keyName, models, { dryRun: true })) as unknown as Impact,
        commit: () => api.setKeyModels(revision, providerId, keyName, models),
        confirm: impactConfirm,
      });
      if (!confirmed) return false;
      toast(`Key ${keyName} 已保存 ${models.length} 个模型。`);
      await client.invalidateQueries({ queryKey: queryKeys.providers() });
      await client.invalidateQueries({ queryKey: queryKeys.routes() });
      onSaved?.();
      return true;
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) {
        setConflict(true);
        await client.invalidateQueries({ queryKey: queryKeys.providers() });
      }
      setSaveError(`保存失败: ${errorText(failure)}`);
      return false;
    } finally {
      setSaving(false);
    }
  }

  return {
    loading: enabled && !ready && !modelsQuery.isError,
    loadError: modelsQuery.error ? errorText(modelsQuery.error) : null,
    kinds,
    all,
    selected,
    initial,
    discovered: new Set(discovered),
    search,
    setSearch,
    kindFilter,
    setKindFilter,
    toggle,
    addCustom,
    selectVisible,
    clear,
    added,
    removed,
    saving,
    saveError,
    conflict,
    save,
  };
}

function endpointHint(binding: BindingState, model: string): string {
  const kinds = binding.kinds;
  // 目录不可用时不画端点提示：此时类型只由名字规则给出，而这条提示会直接引导用户去改
  // 上游路径——标错分组的代价是分错组，标错端点的代价是改错配置。这一处刻意比类型分组更保守。
  if (!kinds?.available) return "";
  const ids = endpointsOf(kinds, model);
  if (!ids.length) return "";
  return `端点 ${ids.map((id) => {
    const path = endpointPath(id);
    return path ? `${endpointLabel(id)} · ${path}` : endpointLabel(id);
  }).join("、")}`;
}

function groupHint(binding: BindingState, kind: string, names: string[]): string {
  if (!binding.kinds?.available) return "";
  const parts: string[] = [];
  const endpoints = [...new Set(names.flatMap((name) => endpointsOf(binding.kinds, name)))];
  if (endpoints.length) {
    parts.push(`端点 ${endpoints.map((id) => endpointLabel(id)).join("、")}`);
  }
  const modes = ENDPOINT_FAMILY[kind] || [];
  if (modes.length) parts.push(`上游模式 ${modes.join("/")}`);
  return parts.join(" · ");
}

export function KeyModelBindingBody({ binding }: { binding: BindingState }) {
  const [custom, setCustom] = useState("");
  const kindGroups = useMemo(
    () => (binding.kinds ? groupByKind(binding.all, binding.kinds) : null),
    [binding.all, binding.kinds],
  );
  const groups = kindGroups && kindGroups.length > 1 ? kindGroups : null;
  const query = binding.search.trim().toLowerCase();
  const visibleNames = binding.all.filter((name) => {
    if (query && !name.toLowerCase().includes(query)) return false;
    if (!groups || binding.kindFilter === MODEL_KIND_ALL) return true;
    return kindGroups?.find((group) => group.names.includes(name))?.kind === binding.kindFilter;
  });
  const visibleSet = new Set(visibleNames);

  if (binding.loading) {
    return (
      <div className="flex items-center gap-2 py-10 text-meta text-ink-3">
        <Loader2 aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
        正在读取 Key 绑定…
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <p className="text-meta leading-5 text-ink-3">
        只显示该 Key 对外提供的模型；黄色卡片表示已启用但当前探测未发现。
        取消勾选一个模型会解除本 Key 与它的绑定；若它因此失去全部绑定，模型会被删除，
        引用它的访问密钥、工作空间与任务会一并变动（保存前会先列出来）。
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={binding.search}
          onChange={(event) => binding.setSearch(event.target.value)}
          placeholder="搜索模型"
          aria-label="搜索模型"
          leadingIcon={<Search aria-hidden />}
          className="w-56"
          size="sm"
        />
        <span className="text-meta text-ink-3">{binding.selected.size} 个已选</span>
        <span className="flex-1" />
        <Button size="sm" variant="secondary" onClick={() => binding.selectVisible(visibleNames)}>
          全选当前可见
        </Button>
        <Button size="sm" variant="ghost" onClick={binding.clear}>清空</Button>
      </div>

      {groups ? (
        <Segmented
          aria-label="按模型类型筛选"
          value={binding.kindFilter}
          onValueChange={binding.setKindFilter}
          options={[
            { value: MODEL_KIND_ALL, label: `全部 · ${binding.all.length}` },
            ...groups.map((group) => ({ value: group.kind, label: `${group.label} · ${group.names.length}` })),
          ]}
        />
      ) : null}

      {groups ? (
        groups
          .filter((group) => binding.kindFilter === MODEL_KIND_ALL || group.kind === binding.kindFilter)
          .map((group) => {
            const names = group.names.filter((name) => visibleSet.has(name));
            if (!names.length) return null;
            const hint = groupHint(binding, group.kind, group.names);
            return (
              <div key={group.kind} className="space-y-2">
                <div className="flex flex-wrap items-baseline gap-2">
                  <span className="text-meta font-medium text-ink-2">{group.label} · {group.names.length}</span>
                  {hint ? <span className="text-meta text-ink-3">{hint}</span> : null}
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {names.map((name) => (
                    <ModelChip key={name} binding={binding} name={name} />
                  ))}
                </div>
              </div>
            );
          })
      ) : (
        <div className="flex flex-wrap gap-1.5">
          {visibleNames.map((name) => <ModelChip key={name} binding={binding} name={name} />)}
        </div>
      )}

      {!visibleNames.length ? <p className="text-meta text-ink-3">没有匹配的模型。</p> : null}

      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={custom}
          onChange={(event) => setCustom(event.target.value)}
          placeholder="自定义模型名称"
          aria-label="自定义模型名称"
          size="sm"
          className="w-56"
          onKeyDown={(event) => {
            if (event.key !== "Enter") return;
            event.preventDefault();
            binding.addCustom(custom);
            setCustom("");
          }}
        />
        <Button
          size="sm"
          variant="secondary"
          leadingIcon={<Plus aria-hidden className="size-3.5" />}
          onClick={() => {
            binding.addCustom(custom);
            setCustom("");
          }}
        >
          添加自定义模型
        </Button>
      </div>

      {binding.kinds ? (
        <p className="text-meta leading-5 text-ink-3">
          {binding.kinds.available ? "类型来自 models.dev 目录与名字规则" : "价格目录尚未就绪，类型依据仅名字规则"}；
          「未分类」表示没有任何证据（自建网关、中转站改名等），不影响能否调用；
          {binding.kinds.available
            ? "端点提示（分组标题旁与卡片悬停）出自同一份读数，取不到时不显示。"
            : "端点提示只在目录可用时显示：名字规则给出的端点可能不准，宁可不显示。"}
        </p>
      ) : null}
      {binding.loadError ? <Banner tone="warn">读取 Key 绑定失败: {binding.loadError}</Banner> : null}
      {binding.conflict ? (
        <Banner tone="warn" title="配置已在别处更新">已重新读取最新配置，请核对后再保存。</Banner>
      ) : null}
      {binding.saveError ? <Banner tone="critical">{binding.saveError}</Banner> : null}
    </div>
  );
}

function ModelChip({ binding, name }: { binding: BindingState; name: string }) {
  const selected = binding.selected.has(name);
  const missing = selected && !binding.discovered.has(name) && !binding.initial.has(name);
  const hint = endpointHint(binding, name);
  const title = [missing ? "黄色卡片表示已启用但当前探测未发现" : "", hint].filter(Boolean).join("\n") || undefined;
  return (
    <button
      type="button"
      aria-pressed={selected}
      aria-label={`${selected ? "关闭" : "打开"}模型 ${name}`}
      title={title}
      onClick={() => binding.toggle(name)}
      className={[
        "inline-flex h-6 max-w-full items-center rounded-chip border px-2 font-mono text-meta transition-colors",
        selected
          ? missing
            ? "border-warn bg-warn-bg text-warn-ink"
            : "border-ink bg-select text-ink"
          : "border-rule bg-panel text-ink-2 hover:bg-hover hover:text-ink",
      ].join(" ")}
    >
      <span className="truncate">{name}</span>
    </button>
  );
}

interface ModelBindingSheetProps {
  provider: Provider;
  providerKey: ProviderKey;
  revision: string;
  onClose: () => void;
  onSaved: () => void;
}

export function ModelBindingSheet({ provider, providerKey, revision, onClose, onSaved }: ModelBindingSheetProps) {
  const binding = useKeyModelBinding({
    providerId: provider.id,
    keyName: providerKey.name,
    revision,
    discovered: providerKey.capabilities?.models || [],
    onSaved,
  });
  const changed = binding.added > 0 || binding.removed > 0;

  return (
    <Sheet
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={`Key ${providerKey.name} 的服务模型`}
      subtitle={`供应商 ${provider.id} · 保存前会预演连带影响`}
      width="xl"
      dirty={changed}
      footer={
        <div className="flex w-full items-center justify-between gap-3">
          <span className="text-meta text-ink-2">新增 {binding.added} · 移除 {binding.removed}</span>
          <div className="flex items-center gap-2">
            <Button variant="secondary" onClick={onClose}>取消</Button>
            <Button
              variant="primary"
              loading={binding.saving}
              disabled={!changed}
              onClick={() => {
                void binding.save().then((ok) => {
                  if (ok) {
                    onSaved();
                    onClose();
                  }
                });
              }}
            >
              保存模型
            </Button>
          </div>
        </div>
      }
    >
      <KeyModelBindingBody binding={binding} />
    </Sheet>
  );
}
