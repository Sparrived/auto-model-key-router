import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, Plus } from "lucide-react";
import { api } from "@/lib/api";
import { errorText, shortRevision } from "@/lib/format";
import { normalizeModelKinds } from "@/lib/model-kinds";
import { PLAN_FAMILIES } from "@/lib/endpoint-plans";
import { queryKeys } from "@/lib/query-keys";
import type { RoutesResponse, UnifiedModel } from "@/lib/types";
import { setParams, useRoute } from "@/app/router";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { ListPane, ListPaneItem, SplitView } from "@/components/ui/SplitView";
import { AddModelDialog } from "./add-model-dialog";
import { MoveTargetDialog } from "./move-target-dialog";
import { RouteEditSheet } from "./route-edit-sheet";
import { RouteGraph } from "./route-graph";
import { UNIFIED_ID, modeLabel } from "./shared";
import { UnifiedDetail } from "./unified-detail";
import { UnifiedSheet } from "./unified-sheet";

export default function RoutingPage() {
  const route = useRoute();
  const client = useQueryClient();
  const routesQuery = useQuery({ queryKey: queryKeys.routes(), queryFn: () => api.routes() });
  const providersQuery = useQuery({ queryKey: queryKeys.providers(), queryFn: () => api.providers() });
  const modelsQuery = useQuery({ queryKey: queryKeys.models(), queryFn: () => api.models() });
  const unifiedQuery = useQuery({ queryKey: queryKeys.unified(), queryFn: () => api.unified() });

  const [search, setSearch] = useState("");
  const [addOpen, setAddOpen] = useState(false);
  const [editingRoute, setEditingRoute] = useState<string | null>(null);
  const [editingUnified, setEditingUnified] = useState(false);
  const [moving, setMoving] = useState<{ routeId: string; index: number } | null>(null);

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

  const selectedId = route.params.get("id") || UNIFIED_ID;
  const selectedRoute = routes.find((item) => item.id === selectedId) || null;
  const showUnified = selectedId === UNIFIED_ID || !selectedRoute;

  const configuredFamilies = PLAN_FAMILIES.filter((family) => {
    const plan = (unified?.[family.id] || null) as { primary?: { model?: string } | null } | null;
    return Boolean(plan?.primary?.model);
  }).length;

  const filteredRoutes = routes.filter((item) => {
    const query = search.trim().toLowerCase();
    if (!query) return true;
    return item.id.toLowerCase().includes(query)
      || (item.aliases || []).some((alias) => alias.toLowerCase().includes(query));
  });

  function select(id: string) {
    setParams((params) => {
      params.set("id", id);
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

  const editing = routes.find((item) => item.id === editingRoute) || null;
  const movingRoute = routes.find((item) => item.id === moving?.routeId) || null;

  return (
    <div className="flex h-[calc(100dvh-7.5rem)] min-h-[520px] flex-col gap-4">
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

      <div className="min-h-0 flex-1">
        <SplitView
          selected={!showUnified}
          onBack={() => select(UNIFIED_ID)}
          listWidth={300}
          list={
            <ListPane
              title="模型"
              search={{ value: search, onChange: setSearch, placeholder: "搜索名称或别名" }}
              actions={<IconButton aria-label="添加模型" size="sm" onClick={() => setAddOpen(true)}><Plus className="size-4" /></IconButton>}
              empty={search ? "没有匹配的模型。" : "还没有模型路由。"}
            >
              <ListPaneItem selected={showUnified} onClick={() => select(UNIFIED_ID)} leading={<UnifiedGlyph />}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate font-mono text-dense font-medium">{UNIFIED_ID}</span>
                  <span className="flex flex-wrap items-center gap-1.5 text-meta text-ink-3">
                    <Badge tone="neutral">统一入口</Badge>
                    <span>{configuredFamilies} / {PLAN_FAMILIES.length} 个端点族已配置</span>
                  </span>
                </span>
              </ListPaneItem>
              {filteredRoutes.map((item) => (
                <ListPaneItem
                  key={item.id}
                  selected={item.id === selectedRoute?.id}
                  onClick={() => select(item.id)}
                >
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate font-mono text-dense font-medium">{item.id}</span>
                    <span className="flex items-center gap-2 text-meta text-ink-3">
                      <span>{(item.targets || []).length} 个目标</span>
                      <span>{modeLabel(item.routing_mode)}</span>
                    </span>
                  </span>
                </ListPaneItem>
              ))}
            </ListPane>
          }
          detail={
            routesQuery.isLoading ? (
              <div className="flex h-full items-center justify-center gap-2 text-meta text-ink-3">
                <Loader2 aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
                正在读取模型路由…
              </div>
            ) : showUnified ? (
              <div className="h-full overflow-y-auto p-1">
                <UnifiedDetail
                  unified={unified}
                  models={models}
                  revision={revision}
                  onEdit={() => setEditingUnified(true)}
                  onChanged={refreshAll}
                />
              </div>
            ) : selectedRoute ? (
              <div className="h-full space-y-4 overflow-y-auto p-1">
                <Panel
                  padding={false}
                  title={selectedRoute.id}
                  description={
                    <span className="flex flex-wrap items-center gap-1.5">
                      <Badge tone="neutral">{(selectedRoute.targets || []).length} 个目标</Badge>
                      <Badge tone="neutral">{modeLabel(selectedRoute.routing_mode)}</Badge>
                    </span>
                  }
                  actions={
                    <Button size="sm" variant="secondary" onClick={() => setEditingRoute(selectedRoute.id)}>编辑</Button>
                  }
                >
                  <RouteGraph
                    route={selectedRoute}
                    models={models}
                    providers={providers}
                    onMoveTarget={(index) => setMoving({ routeId: selectedRoute.id, index })}
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
          onClose={() => setAddOpen(false)}
          onCreated={(id) => {
            select(id);
            setEditingRoute(id);
            // 新建后缓存里还没有这条路由：先刷新，编辑抽屉才能在 routes 里找到它。
            void refreshAll();
          }}
        />
      ) : null}

      {editing ? (
        <RouteEditSheet
          route={editing}
          providers={providers}
          models={models}
          kinds={kinds}
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

function UnifiedGlyph() {
  return (
    <span className="grid size-5 place-items-center rounded-chip border border-ink bg-select font-mono text-[11px] font-semibold text-ink">
      U
    </span>
  );
}
