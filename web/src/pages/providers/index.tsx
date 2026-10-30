import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, MoreHorizontal, Pencil, Plus, Radar, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText, formatDateTime, shortRevision } from "@/lib/format";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import {
  freezeLookupKey,
  indexQuotaFreezes,
  invalidateFreezeViews,
  useDeadlineRefresh,
  useNow,
} from "@/lib/quota-freeze";
import { queryKeys } from "@/lib/query-keys";
import { ProviderIcon } from "@/lib/brand-icons";
import type { Provider, ProviderKey, ProvidersResponse, QuotaFreeze } from "@/lib/types";
import { navigate, setParams, useRoute } from "@/app/router";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/DropdownMenu";
import { Empty } from "@/components/ui/Empty";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { ListPane, ListPaneItem, SplitView } from "@/components/ui/SplitView";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { AddKeySheet } from "./add-key-sheet";
import { AddProviderDialog, ProviderEditSheet } from "./provider-sheet";
import { KeysTable } from "./keys-table";
import { ModelBindingSheet } from "./model-binding";
import { ProbePanel } from "./probe-panel";
import { ROUTE_MODES } from "./route-modes";

// boundModelsByKey 把路由表反过来索引成 `供应商|Key` → 它服务的模型 ID（升序）。
//
// Key 行下方要显示的是「这个 Key 正在服务哪些模型」，而那是模型 targets 里的绑定关系，
// 只读 providers 看不到。
function boundModelsByKey(routes: Array<{ id: string; targets?: Array<{ provider: string; key: string }> }>): Map<string, string[]> {
  const index = new Map<string, Set<string>>();
  for (const route of routes) {
    for (const target of route.targets || []) {
      const id = `${target.provider}|${target.key}`;
      if (!index.has(id)) index.set(id, new Set());
      index.get(id)?.add(route.id);
    }
  }
  const result = new Map<string, string[]>();
  for (const [id, models] of index) result.set(id, [...models].sort());
  return result;
}

export default function ProvidersPage() {
  const route = useRoute();
  const client = useQueryClient();
  const impactConfirm = useImpactConfirm();
  const providersQuery = useQuery({ queryKey: queryKeys.providers(), queryFn: () => api.providers() });
  const routesQuery = useQuery({ queryKey: queryKeys.routes(), queryFn: () => api.routes() });
  // 额度冻结：本地状态快照，与账号资源页共用同一个查询键。老后端没有这个接口（404）
  // 时只是不显示冻结信息，页面其余部分照常工作，所以关掉重试。
  const freezesQuery = useQuery({
    queryKey: queryKeys.quotaFreezes(),
    queryFn: api.quotaFreezes,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const freezes = useMemo(() => freezesQuery.data?.freezes ?? [], [freezesQuery.data]);
  const freezeIndex = useMemo(() => indexQuotaFreezes(freezes), [freezes]);
  // 页面不轮询：倒计时本地重算，只有最近一条 expires_at 到点才拉一次冻结列表，
  // 让 Key 行徽标与顶部提示自己消失。
  const now = useNow();
  const freezeDeadlines = useMemo(
    () => freezes.map((freeze) => freeze.expires_at),
    [freezes],
  );
  useDeadlineRefresh(freezeDeadlines, () => {
    void freezesQuery.refetch();
  });
  // 同一时刻只放行一次解冻，避免连点发出多个 DELETE（第二次会 404）。
  const releasingRef = useRef<string | null>(null);

  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [addingKeyFor, setAddingKeyFor] = useState<string | null>(null);
  const [bindingTarget, setBindingTarget] = useState<{ provider: string; key: string } | null>(null);
  const [probeOpen, setProbeOpen] = useState(false);

  const providers = providersQuery.data?.providers || [];
  const revision = providersQuery.data?.config_revision || "";
  const selectedId = route.params.get("id") || "";
  const selected = providers.find((provider) => provider.id === selectedId) || providers[0] || null;
  const boundModels = useMemo(() => boundModelsByKey(routesQuery.data?.routes || []), [routesQuery.data]);
  const customizedRouteCount = selected
    ? ROUTE_MODES.filter((mode) => selected.routes?.[mode.id]).length
    : 0;

  const filtered = providers.filter((provider) => {
    const query = search.trim().toLowerCase();
    if (!query) return true;
    return provider.id.toLowerCase().includes(query) || provider.base_url.toLowerCase().includes(query);
  });

  async function refreshProviders(): Promise<ProvidersResponse> {
    const data = await client.fetchQuery({
      queryKey: queryKeys.providers(),
      queryFn: () => api.providers(),
      staleTime: 0,
    });
    await client.invalidateQueries({ queryKey: queryKeys.routes() });
    return data;
  }

  // 手工解冻：只解除本次冻结（服务端附抑制窗口），不写配置；成功后刷新供应商与冻结列表。
  async function releaseFreeze(freeze: QuotaFreeze) {
    const id = freezeLookupKey(freeze.provider_id, freeze.key_name);
    if (releasingRef.current) return;
    releasingRef.current = id;
    try {
      const result = await api.releaseQuotaFreeze(freeze.provider_id, freeze.key_name);
      const suppressed = result.suppressed_until ? `，抑制至 ${formatDateTime(result.suppressed_until)}` : "";
      toast.success(`已解除 ${freeze.provider_id} · ${freeze.key_name} 的额度冻结${suppressed}。`);
      await invalidateFreezeViews(client);
      await refreshProviders();
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 404) {
        // 冻结已被别的路径解除（到期自动解冻/账号资源页手工解冻）：刷新追上真实状态。
        toast("该 Key 当前没有冻结，已刷新列表。");
        await invalidateFreezeViews(client);
        await refreshProviders().catch(() => undefined);
      } else {
        toast.error(`解除额度冻结失败：${errorText(failure)}`);
      }
    } finally {
      releasingRef.current = null;
    }
  }

  function selectProvider(id: string) {
    setParams((params) => {
      params.set("id", id);
      return params;
    });
    setProbeOpen(false);
  }

  async function removeProvider(provider: Provider) {
    try {
      const { confirmed } = await writeWithImpactConfirm({
        title: "删除供应商",
        message: `删除供应商 ${provider.id} 及其全部 Key？`,
        confirmLabel: "删除",
        alwaysConfirm: true,
        preview: async () => (await api.deleteProvider(revision, provider.id, { dryRun: true })) as unknown as Impact,
        commit: () => api.deleteProvider(revision, provider.id),
        confirm: impactConfirm,
      });
      if (!confirmed) return;
      await refreshProviders();
      if (selected?.id === provider.id) {
        setParams((params) => {
          params.delete("id");
          return params;
        });
      }
      toast("供应商已删除。");
    } catch (failure) {
      toast.error(errorText(failure));
    }
  }

  const editing = providers.find((provider) => provider.id === editingProvider) || null;
  const addingKeyProvider = providers.find((provider) => provider.id === addingKeyFor) || null;
  const bindingProvider = providers.find((provider) => provider.id === bindingTarget?.provider) || null;
  const bindingKey = bindingProvider?.keys.find((key) => key.name === bindingTarget?.key) || null;

  return (
    <div className="flex h-[calc(100dvh-7.5rem)] min-h-[520px] flex-col gap-4">
      <PageHeader
        title="供应商"
        description="管理上游供应商、Key 与它们的模型能力。"
        actions={
          <>
            {revision ? <Badge tone="neutral">版本 {shortRevision(revision)}</Badge> : null}
            <Button variant="primary" leadingIcon={<Plus aria-hidden className="size-4" />} onClick={() => setCreateOpen(true)}>
              添加供应商
            </Button>
          </>
        }
      />

      {providersQuery.isError ? (
        <Banner tone="critical">无法读取供应商配置: {errorText(providersQuery.error)}</Banner>
      ) : null}

      {freezes.length ? (
        <Banner
          tone="warn"
          title={`${freezes.length} 把上游 Key 因额度耗尽或低于底限被自动冻结（不影响启用开关）`}
          action={(
            <Button size="sm" variant="secondary" onClick={() => navigate("accounts")}>
              前往账号资源
            </Button>
          )}
        >
          冻结只影响 AMKR 的选路，不会改动配置里的启用状态；内置计时器到点会自动解除，不需要手动刷新。
        </Banner>
      ) : null}

      <div className="min-h-0 flex-1">
        <SplitView
          selected={Boolean(selectedId && selected)}
          onBack={() => setParams((params) => { params.delete("id"); return params; })}
          listWidth={280}
          list={
            <ListPane
              title="供应商"
              search={{ value: search, onChange: setSearch, placeholder: "搜索供应商" }}
              actions={<IconButton aria-label="添加供应商" size="sm" onClick={() => setCreateOpen(true)}><Plus className="size-4" /></IconButton>}
              empty={search ? "没有匹配的供应商。" : "尚未配置供应商。"}
            >
              {filtered.map((provider) => {
                const keyCount = provider.keys.length;
                const enabledCount = provider.keys.filter((key) => key.enabled).length;
                return (
                  <ListPaneItem
                    key={provider.id}
                    selected={provider.id === selected?.id}
                    onClick={() => selectProvider(provider.id)}
                    leading={<ProviderIcon id={provider.id} baseUrl={provider.base_url} size={18} />}
                  >
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate text-dense font-medium">{provider.id}</span>
                      <span className="flex items-center gap-2 text-meta text-ink-3">
                        <span>{keyCount ? `${keyCount} Key` : "无 Key"}</span>
                        {keyCount ? <span>{enabledCount} 启用</span> : null}
                      </span>
                    </span>
                  </ListPaneItem>
                );
              })}
            </ListPane>
          }
          detail={providersQuery.isLoading ? (
            <div className="flex h-full items-center justify-center gap-2 text-meta text-ink-3">
              <Loader2 aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
              正在读取供应商配置…
            </div>
          ) : selected ? (
            <div className="h-full space-y-4 overflow-y-auto p-1">
              <Panel padding={false}>
                <div className="flex flex-wrap items-start gap-3 p-4">
                  <ProviderIcon id={selected.id} baseUrl={selected.base_url} size={32} />
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="font-mono text-title font-semibold text-ink">{selected.id}</h2>
                      <Badge tone={Number(selected.max_concurrency) > 0 ? "good" : "neutral"}>
                        {Number(selected.max_concurrency) > 0 ? `并发 ${selected.max_concurrency}` : "并发不限"}
                      </Badge>
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-2 text-meta text-ink-3">
                      <CopyChip text={selected.base_url} label="供应商地址" />
                    </div>
                  </div>
                  <div className="flex items-center gap-1">
                    <Button variant="secondary" size="sm" leadingIcon={<Pencil aria-hidden className="size-3.5" />} onClick={() => setEditingProvider(selected.id)}>
                      编辑
                    </Button>
                    <DropdownMenu
                      trigger={<IconButton aria-label="更多供应商操作" size="sm"><MoreHorizontal aria-hidden className="size-4" /></IconButton>}
                    >
                      <DropdownMenuItem icon={<Trash2 />} danger onSelect={() => void removeProvider(selected)}>
                        删除供应商
                      </DropdownMenuItem>
                    </DropdownMenu>
                  </div>
                </div>
                <details className="border-t border-rule px-4 py-2.5">
                  <summary className="flex w-fit cursor-pointer items-center gap-2 text-meta text-ink-2">
                    <span>上游路径</span>
                    <Badge tone="neutral">已自定义 {customizedRouteCount}/{ROUTE_MODES.length}</Badge>
                  </summary>
                  <div className="mt-2 grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
                    {ROUTE_MODES.map((mode) => (
                      <div key={mode.id} className="flex items-baseline justify-between gap-3 text-meta">
                        <span className="text-ink-3">{mode.label}</span>
                        <span className={selected.routes?.[mode.id] ? "font-mono text-ink" : "text-ink-3"}>
                          {selected.routes?.[mode.id] || "默认"}
                        </span>
                      </div>
                    ))}
                  </div>
                  <p className="mt-2 text-meta text-ink-3">在「编辑」里可以逐模式改写路径；留空即使用标准路径。</p>
                </details>
              </Panel>

              <Panel
                padding={false}
                title={`Key（${selected.keys.length}）`}
                actions={
                  <>
                    <Button
                      size="sm"
                      variant="secondary"
                      leadingIcon={<Radar aria-hidden className="size-3.5" />}
                      aria-expanded={probeOpen}
                      onClick={() => setProbeOpen((open) => !open)}
                    >
                      批量探测
                    </Button>
                    <Button size="sm" variant="primary" leadingIcon={<Plus aria-hidden className="size-3.5" />} onClick={() => setAddingKeyFor(selected.id)}>
                      添加 Key
                    </Button>
                  </>
                }
              >
                <KeysTable
                  provider={selected}
                  revision={revision}
                  boundModels={boundModels}
                  freezes={freezeIndex}
                  now={now}
                  onReleaseFreeze={(freeze) => void releaseFreeze(freeze)}
                  onRefresh={refreshProviders}
                  onOpenModels={(key: ProviderKey) => setBindingTarget({ provider: selected.id, key: key.name })}
                />
              </Panel>

              {probeOpen ? <ProbePanel provider={selected} /> : null}

              <details className="rounded-panel border border-rule bg-panel px-4 py-3">
                <summary className="cursor-pointer text-meta text-ink-2">这个供应商会被哪些模型使用</summary>
                <div className="mt-2 space-y-1 text-meta text-ink-3">
                  {[...boundModels.entries()].filter(([id]) => id.startsWith(`${selected.id}|`)).length ? (
                    [...boundModels.entries()].filter(([id]) => id.startsWith(`${selected.id}|`)).map(([id, models]) => (
                      <div key={id} className="flex flex-wrap gap-2">
                        <span className="font-mono text-ink-2">{id.slice(selected.id.length + 1)}</span>
                        <span>{models.join("、")}</span>
                      </div>
                    ))
                  ) : (
                    <p>还没有模型绑定这个供应商的 Key。到模型路由页添加目标后才会出现。</p>
                  )}
                </div>
              </details>
            </div>
          ) : (
            <div className="flex h-full items-center justify-center">
              <Empty
                title="还没有供应商"
                description="添加第一个上游供应商，然后为它添加 Key 并绑定服务模型。"
                action={<Button variant="primary" onClick={() => setCreateOpen(true)}>添加供应商</Button>}
              />
            </div>
          )}
        />
      </div>

      <AddProviderDialog
        open={createOpen}
        revision={revision}
        onClose={() => setCreateOpen(false)}
        onCreated={(id) => selectProvider(id)}
        onRefresh={refreshProviders}
      />
      {editing ? (
        <ProviderEditSheet
          provider={editing}
          revision={revision}
          onClose={() => setEditingProvider(null)}
          onSaved={(id) => {
            if (id !== editing.id) {
              setParams((params) => {
                params.set("id", id);
                return params;
              });
            }
          }}
          onRefresh={refreshProviders}
        />
      ) : null}
      {addingKeyProvider ? (
        <AddKeySheet
          provider={addingKeyProvider}
          revision={revision}
          onClose={() => setAddingKeyFor(null)}
          onSaved={() => { void refreshProviders(); }}
          loadProviders={refreshProviders}
        />
      ) : null}
      {bindingProvider && bindingKey ? (
        <ModelBindingSheet
          provider={bindingProvider}
          providerKey={bindingKey}
          revision={revision}
          onClose={() => setBindingTarget(null)}
          onSaved={() => { void refreshProviders(); }}
        />
      ) : null}
    </div>
  );
}
