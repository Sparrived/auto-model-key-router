import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, Play, Plus } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText, shortRevision } from "@/lib/format";
import { normalizeModelKinds } from "@/lib/model-kinds";
import { PLAN_FAMILIES } from "@/lib/endpoint-plans";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import { indexQuotaFreezes, useDeadlineRefresh, useNow } from "@/lib/quota-freeze";
import { queryKeys } from "@/lib/query-keys";
import type { RouteEntry, RoutesResponse, UnifiedModel } from "@/lib/types";
import { navigate, setParams, useRoute } from "@/app/router";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { ListPane, ListPaneItem, SplitView } from "@/components/ui/SplitView";
import { AddModelDialog } from "./add-model-dialog";
import { MoveTargetDialog } from "./move-target-dialog";
import { RouteEditSheet } from "./route-edit-sheet";
import { RouteGraph } from "./route-graph";
import {
  UNIFIED_ID,
  matchesRouteQuery,
  matchesUnifiedQuery,
  modeLabel,
  normalizeMode,
  routeHasEndpointPlans,
  routeTargetIssues,
  targetText,
} from "./shared";
import { UnifiedDetail } from "./unified-detail";
import { UnifiedSheet } from "./unified-sheet";

type RouteListFilter = "all" | "issues" | "endpoints";

export default function RoutingPage() {
  const route = useRoute();
  const client = useQueryClient();
  const confirm = useConfirm();
  const impactConfirm = useImpactConfirm();

  const routesQuery = useQuery({ queryKey: queryKeys.routes(), queryFn: () => api.routes() });
  const providersQuery = useQuery({ queryKey: queryKeys.providers(), queryFn: () => api.providers() });
  const modelsQuery = useQuery({ queryKey: queryKeys.models(), queryFn: () => api.models() });
  const unifiedQuery = useQuery({ queryKey: queryKeys.unified(), queryFn: () => api.unified() });
  // 额度冻结快照：与供应商页共用查询键，老后端 404 时静默忽略。
  const freezesQuery = useQuery({
    queryKey: queryKeys.quotaFreezes(),
    queryFn: api.quotaFreezes,
    retry: false,
    refetchOnWindowFocus: false,
  });

  const freezes = useMemo(() => freezesQuery.data?.freezes ?? [], [freezesQuery.data]);
  const freezeIndex = useMemo(() => indexQuotaFreezes(freezes), [freezes]);
  const now = useNow();
  const freezeDeadlines = useMemo(() => freezes.map((freeze) => freeze.expires_at), [freezes]);
  useDeadlineRefresh(freezeDeadlines, () => {
    void freezesQuery.refetch();
  });

  const [search, setSearch] = useState("");
  const [listFilter, setListFilter] = useState<RouteListFilter>("all");
  const [addOpen, setAddOpen] = useState(false);
  const [editingRoute, setEditingRoute] = useState<string | null>(null);
  const [editingUnified, setEditingUnified] = useState(false);
  const [moving, setMoving] = useState<{ routeId: string; index: number } | null>(null);
  const [quickBusy, setQuickBusy] = useState(false);

  const routes = routesQuery.data?.routes || [];
  const providers = providersQuery.data?.providers || [];
  const models = modelsQuery.data?.models || [];
  const unified = (unifiedQuery.data?.unified_model || null) as UnifiedModel | null;
  const revision = routesQuery.data?.config_revision || "";

  const modelIds = useMemo(() => models.map((model) => model.id), [models]);
  const kindsQuery = useQuery({
    queryKey: queryKeys.modelKinds(modelIds),
    queryFn: () => api.modelKinds(modelIds),
    enabled: modelIds.length > 0,
    retry: false,
  });
  // 模型类型只影响下拉排序：这次请求失败时按平铺处理，绝不能让整页读不出来。
  const kinds = useMemo(
    () => (kindsQuery.data ? normalizeModelKinds(kindsQuery.data) : null),
    [kindsQuery.data],
  );

  // URL 里带明确 id 才在移动端切到详情栏（保证手机端既能返回列表，也能点进 unified-model）；
  // 桌面端 SplitView 始终双栏，未带 id 时默认展示 unified-model。
  const rawSelectedId = route.params.get("id");
  const selectedId = rawSelectedId || UNIFIED_ID;
  const routeNew = route.params.get("new") === "1";
  const selectedRoute = routes.find((item) => item.id === selectedId) || null;
  const showUnified = selectedId === UNIFIED_ID || !selectedRoute;
  const selectedModel = selectedRoute ? models.find((item) => item.id === selectedRoute.id) || null : null;

  useEffect(() => {
    if (!routeNew) return;
    setAddOpen(true);
    setParams((params) => {
      params.delete("new");
      return params;
    }, { replace: true });
  }, [routeNew]);

  const configuredFamilies = PLAN_FAMILIES.filter((family) => {
    const plan = (unified?.[family.id] || null) as { primary?: { model?: string } | null } | null;
    return Boolean(plan?.primary?.model);
  }).length;

  const issuesByRoute = useMemo(() => {
    const map = new Map<string, ReturnType<typeof routeTargetIssues>>();
    for (const item of routes) {
      map.set(item.id, routeTargetIssues(item, providers, freezeIndex));
    }
    return map;
  }, [freezeIndex, providers, routes]);

  const issueRouteCount = useMemo(
    () => routes.filter((item) => (issuesByRoute.get(item.id)?.total || 0) > 0).length,
    [issuesByRoute, routes],
  );
  const endpointRouteCount = useMemo(
    () => routes.filter((item) => routeHasEndpointPlans(item)).length,
    [routes],
  );

  const hasFilter = Boolean(search.trim()) || listFilter !== "all";
  const showUnifiedInList = listFilter === "all" && matchesUnifiedQuery(unified, search);

  const filteredRoutes = useMemo(() => routes.filter((item) => {
    if (listFilter === "issues" && !(issuesByRoute.get(item.id)?.total || 0)) return false;
    if (listFilter === "endpoints" && !routeHasEndpointPlans(item)) return false;
    return matchesRouteQuery(item, search);
  }), [issuesByRoute, listFilter, routes, search]);

  function select(id: string) {
    setParams((params) => {
      params.set("id", id);
      return params;
    });
  }

  function clearSelection() {
    setParams((params) => {
      params.delete("id");
      return params;
    });
  }

  async function loadRoutes(): Promise<RoutesResponse> {
    return client.fetchQuery({
      queryKey: queryKeys.routes(),
      queryFn: () => api.routes(),
      staleTime: 0,
    });
  }

  async function refreshAll() {
    await Promise.all([
      client.invalidateQueries({ queryKey: queryKeys.routes() }),
      client.invalidateQueries({ queryKey: queryKeys.providers() }),
      client.invalidateQueries({ queryKey: queryKeys.models() }),
      client.invalidateQueries({ queryKey: queryKeys.unified() }),
    ]);
  }

  async function quickChangeMode(targetRoute: RouteEntry, nextMode: string) {
    setQuickBusy(true);
    try {
      await api.updateRoute(
        revision,
        targetRoute.id,
        targetRoute.targets,
        targetRoute.aliases || [],
        normalizeMode(nextMode),
      );
      toast.success(`路由模式已切为「${modeLabel(nextMode)}」。`);
      await refreshAll();
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) void refreshAll();
      toast.error(`切换路由模式失败: ${errorText(failure)}`);
    } finally {
      setQuickBusy(false);
    }
  }

  async function quickReorderTarget(targetRoute: RouteEntry, from: number, to: number) {
    const list = [...(targetRoute.targets || [])];
    if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return;
    const [picked] = list.splice(from, 1);
    list.splice(to, 0, picked);
    setQuickBusy(true);
    try {
      await api.updateRoute(
        revision,
        targetRoute.id,
        list,
        targetRoute.aliases || [],
        normalizeMode(targetRoute.routing_mode),
      );
      toast.success("目标顺序已更新。");
      await refreshAll();
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) void refreshAll();
      toast.error(`调整顺序失败: ${errorText(failure)}`);
    } finally {
      setQuickBusy(false);
    }
  }

  async function quickRemoveTarget(targetRoute: RouteEntry, index: number) {
    const target = targetRoute.targets[index];
    if (!target) return;
    setQuickBusy(true);
    try {
      if (targetRoute.targets.length === 1) {
        const { confirmed } = await writeWithImpactConfirm({
          title: "删除模型路由",
          message: `这是路由 ${targetRoute.id} 的最后一条目标，移除后会删掉这条路由。`,
          confirmLabel: "删除路由",
          alwaysConfirm: true,
          preview: async () => (await api.deleteRoute(revision, targetRoute.id, { dryRun: true })) as unknown as Impact,
          commit: () => api.deleteRoute(revision, targetRoute.id),
          confirm: impactConfirm,
        });
        if (!confirmed) return;
        toast(`路由 ${targetRoute.id} 已删除。`);
        select(UNIFIED_ID);
        await refreshAll();
        return;
      }

      const ok = await confirm({
        title: "移除路由目标",
        message: `从路由 ${targetRoute.id} 移除 ${targetText({ ...target, upstream_model: target.upstream_model || targetRoute.id })}？`,
        confirmLabel: "移除",
        danger: true,
      });
      if (!ok) return;
      const remaining = targetRoute.targets.filter((_, position) => position !== index);
      await api.updateRoute(
        revision,
        targetRoute.id,
        remaining,
        targetRoute.aliases || [],
        normalizeMode(targetRoute.routing_mode),
      );
      toast.success("目标已移除。");
      await refreshAll();
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) void refreshAll();
      toast.error(`移除目标失败: ${errorText(failure)}`);
    } finally {
      setQuickBusy(false);
    }
  }

  const editing = routes.find((item) => item.id === editingRoute) || null;
  const movingRoute = routes.find((item) => item.id === moving?.routeId) || null;
  const selectedIssues = selectedRoute ? issuesByRoute.get(selectedRoute.id) : null;

  return (
    <div className="flex h-[calc(100dvh-9.5rem)] min-h-[420px] flex-col gap-3 md:h-[calc(100dvh-5.75rem)] md:min-h-[520px]">
      <PageHeader
        title="模型路由"
        description="决定一个对外模型名按什么顺序、落到哪个供应商的哪把 Key。"
        actions={
          <>
            {revision ? <Badge tone="neutral">版本 {shortRevision(revision)}</Badge> : null}
            <Button variant="primary" leadingIcon={<Plus aria-hidden className="size-4" />} onClick={() => setAddOpen(true)}>
              添加模型
            </Button>
          </>
        }
      />

      {routesQuery.isError ? <Banner tone="critical">无法读取模型路由: {errorText(routesQuery.error)}</Banner> : null}
      {providersQuery.isError ? <Banner tone="warn">供应商读取失败，目标与 Key 下拉可能不完整。</Banner> : null}
      {unifiedQuery.isError ? <Banner tone="warn">统一模型读取失败: {errorText(unifiedQuery.error)}</Banner> : null}

      <div className="min-h-0 flex-1 overflow-hidden rounded-panel border border-rule bg-panel shadow-[var(--shadow-deck)]">
        <SplitView
          selected={Boolean(rawSelectedId)}
          onBack={clearSelection}
          listWidth={300}
          list={
            <ListPane
              title="模型"
              search={{ value: search, onChange: setSearch, placeholder: "搜索模型、别名、供应商或 Key" }}
              actions={<span className="text-meta tabular-nums text-ink-3">{routes.length} 条</span>}
            >
              {issueRouteCount > 0 || endpointRouteCount > 0 || listFilter !== "all" ? (
                <div className="mb-1.5 flex flex-wrap gap-1 px-1.5" role="group" aria-label="模型路由筛选">
                  <FilterPill active={listFilter === "all"} onClick={() => setListFilter("all")}>
                    全部
                  </FilterPill>
                  {issueRouteCount > 0 || listFilter === "issues" ? (
                    <FilterPill active={listFilter === "issues"} onClick={() => setListFilter("issues")}>
                      有异常 ({issueRouteCount})
                    </FilterPill>
                  ) : null}
                  {endpointRouteCount > 0 || listFilter === "endpoints" ? (
                    <FilterPill active={listFilter === "endpoints"} onClick={() => setListFilter("endpoints")}>
                      端点计划 ({endpointRouteCount})
                    </FilterPill>
                  ) : null}
                </div>
              ) : null}

              {showUnifiedInList ? (
                <ListPaneItem selected={showUnified} onClick={() => select(UNIFIED_ID)} leading={<UnifiedGlyph />}>
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-mono text-dense font-medium">{UNIFIED_ID}</span>
                    <span className="flex flex-wrap items-center gap-1.5 text-meta text-ink-3">
                      <Badge tone={unified ? "good" : "neutral"}>{unified ? "统一入口" : "未启用"}</Badge>
                      {unified ? <span>{configuredFamilies} / {PLAN_FAMILIES.length} 族已配</span> : null}
                    </span>
                  </span>
                </ListPaneItem>
              ) : null}

              {filteredRoutes.map((item) => {
                const issues = issuesByRoute.get(item.id);
                const hasEndpoints = routeHasEndpointPlans(item);
                return (
                  <ListPaneItem
                    key={item.id}
                    selected={item.id === selectedRoute?.id}
                    onClick={() => select(item.id)}
                  >
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate font-mono text-dense font-medium">{item.id}</span>
                      <span className="flex flex-wrap items-center gap-1.5 text-meta text-ink-3">
                        <span>{(item.targets || []).length} 个目标</span>
                        <span>{modeLabel(item.routing_mode)}</span>
                        {issues?.frozenCount ? <Badge tone="critical">冻结 {issues.frozenCount}</Badge> : null}
                        {issues?.disabledCount ? <Badge tone="warn">禁用 {issues.disabledCount}</Badge> : null}
                        {issues?.missingCount ? <Badge tone="warn">缺失 {issues.missingCount}</Badge> : null}
                        {hasEndpoints ? <Badge tone="neutral">端点</Badge> : null}
                      </span>
                    </span>
                  </ListPaneItem>
                );
              })}

              {filteredRoutes.length === 0 && hasFilter ? (
                <div className="flex flex-col items-center gap-2 px-3 py-8 text-center text-meta text-ink-3">
                  <span>没有匹配的模型路由。</span>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => {
                      setSearch("");
                      setListFilter("all");
                    }}
                  >
                    重置筛选
                  </Button>
                </div>
              ) : null}

              {routes.length === 0 && !hasFilter && !routesQuery.isLoading ? (
                <p className="px-3 py-6 text-center text-meta text-ink-3">
                  还没有模型路由，点击右上角「添加模型」开始配置。
                </p>
              ) : null}
            </ListPane>
          }
          detail={
            routesQuery.isLoading ? (
              <div className="flex h-full items-center justify-center gap-2 text-meta text-ink-3">
                <Loader2 aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
                正在读取模型路由…
              </div>
            ) : showUnified ? (
              <div className="h-full overflow-y-auto bg-paper/45 p-3.5">
                <UnifiedDetail
                  unified={unified}
                  models={models}
                  revision={revision}
                  onEdit={() => setEditingUnified(true)}
                  onChanged={refreshAll}
                  onSelectRoute={select}
                />
              </div>
            ) : selectedRoute ? (
              <div className="h-full space-y-3 overflow-y-auto bg-paper/45 p-3.5">
                {selectedIssues && selectedIssues.total > 0 ? (
                  <Banner tone={selectedIssues.frozenCount > 0 ? "critical" : "warn"}>
                    此路由存在不可用目标：
                    {[
                      selectedIssues.frozenCount ? `${selectedIssues.frozenCount} 个已额度冻结` : "",
                      selectedIssues.disabledCount ? `${selectedIssues.disabledCount} 个已禁用` : "",
                      selectedIssues.missingCount ? `${selectedIssues.missingCount} 个 Key 不存在` : "",
                    ].filter(Boolean).join("，")}。
                    运行时会自动跳过不可用目标。
                  </Banner>
                ) : null}
                <Panel
                  padding={false}
                  title={selectedRoute.id}
                  description={
                    <span className="flex flex-wrap items-center gap-1.5">
                      <Badge tone="neutral">{(selectedRoute.targets || []).length} 个目标</Badge>
                      <Badge tone="neutral">{modeLabel(selectedRoute.routing_mode)}</Badge>
                      {selectedModel?.reasoning_effort ? (
                        <Badge tone="neutral">推理强度 {selectedModel.reasoning_effort}</Badge>
                      ) : null}
                    </span>
                  }
                  actions={
                    <div className="flex items-center gap-1.5">
                      <Button
                        size="sm"
                        variant="ghost"
                        leadingIcon={<Play className="size-3.5" aria-hidden />}
                        onClick={() => navigate("playground", { model: selectedRoute.id })}
                      >
                        试验场试跑
                      </Button>
                      <Button size="sm" variant="secondary" onClick={() => setEditingRoute(selectedRoute.id)}>编辑</Button>
                    </div>
                  }
                >
                  <RouteGraph
                    route={selectedRoute}
                    models={models}
                    providers={providers}
                    freezes={freezeIndex}
                    now={now}
                    busy={quickBusy}
                    onChangeMode={(nextMode) => void quickChangeMode(selectedRoute, nextMode)}
                    onReorderTarget={(from, to) => void quickReorderTarget(selectedRoute, from, to)}
                    onRemoveTarget={(index) => void quickRemoveTarget(selectedRoute, index)}
                    onMoveTarget={(index) => setMoving({ routeId: selectedRoute.id, index })}
                    onOpenProvider={(providerId) => navigate("providers", { id: providerId })}
                    onSelectRoute={select}
                  />
                </Panel>
              </div>
            ) : null
          }
        />
      </div>

      {addOpen ? (
        <AddModelDialog
          revision={revision}
          providers={providers}
          freezes={freezeIndex}
          onClose={() => setAddOpen(false)}
          onCreated={(id) => {
            void refreshAll().then(() => select(id));
          }}
        />
      ) : null}

      {editing ? (
        <RouteEditSheet
          route={editing}
          providers={providers}
          models={models}
          kinds={kinds}
          freezes={freezeIndex}
          revision={revision}
          loadRoutes={loadRoutes}
          onClose={() => setEditingRoute(null)}
          onSaved={(newId) => {
            setEditingRoute(null);
            if (newId) select(newId);
            else select(UNIFIED_ID);
            void refreshAll();
          }}
        />
      ) : null}

      {editingUnified ? (
        <UnifiedSheet
          unified={unified}
          models={models}
          kinds={kinds}
          revision={revision}
          onClose={() => setEditingUnified(false)}
          onSaved={() => { void refreshAll(); }}
          onRefresh={refreshAll}
          onAddModel={() => setAddOpen(true)}
        />
      ) : null}

      {moving && movingRoute ? (
        <MoveTargetDialog
          route={movingRoute}
          targetIndex={moving.index}
          routes={routes}
          revision={revision}
          loadRoutes={loadRoutes}
          onClose={() => setMoving(null)}
          onMoved={(destinationId, sourceDeleted) => {
            setMoving(null);
            select(sourceDeleted ? destinationId : movingRoute.id);
            void refreshAll();
          }}
        />
      ) : null}
    </div>
  );
}

function FilterPill({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={[
        "rounded-chip border px-2 py-0.5 text-[11px] transition-colors",
        active
          ? "border-accent bg-accent/10 font-medium text-ink"
          : "border-rule bg-sunken text-ink-3 hover:border-rule-strong hover:text-ink",
      ].join(" ")}
    >
      {children}
    </button>
  );
}

function UnifiedGlyph() {
  return (
    <span className="grid size-5 place-items-center rounded-chip border border-ink bg-select font-mono text-[11px] font-semibold text-ink">
      U
    </span>
  );
}
