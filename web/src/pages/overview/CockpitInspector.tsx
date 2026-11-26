import { useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowRight, ArrowUp, ChevronDown, ChevronUp, Filter, Play, Terminal } from "lucide-react";
import { toast } from "sonner";
import { navigate } from "@/app/router";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { errorText, formatClockSeconds, formatCompact, formatCount, formatDuration } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type {
  KeyUsageResponse,
  Provider,
  QuotaFreeze,
  RequestItem,
  RouteEntry,
} from "@/lib/types";
import { modeLabel } from "@/pages/routing/shared";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Switch } from "@/components/ui/Switch";
import type { CockpitSelection } from "./CockpitTopology";

export interface CockpitInspectorProps {
  selection: CockpitSelection | null;
  routes: RouteEntry[];
  routesRevision?: string;
  providers: Provider[];
  providersRevision?: string;
  freezes: QuotaFreeze[];
  keyUsage?: KeyUsageResponse;
  streamItems: RequestItem[];
  onSelect: (selection: CockpitSelection | null) => void;
  onFilterModel: (modelId: string) => void;
  onFilterUpstreamKey: (providerId: string, keyName: string) => void;
  onOpenRequestDetail: (item: RequestItem) => void;
}

export function CockpitInspector({
  selection,
  routes,
  routesRevision,
  providers,
  providersRevision,
  freezes,
  keyUsage,
  streamItems,
  onSelect,
  onFilterModel,
  onFilterUpstreamKey,
  onOpenRequestDetail,
}: CockpitInspectorProps) {
  const queryClient = useQueryClient();
  const [busyAction, setBusyAction] = useState<string | null>(null);
  const [probeResult, setProbeResult] = useState<{ key: string; text: string; ok: boolean } | null>(null);
  const [testerOpen, setTesterOpen] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ text: string; ok: boolean } | null>(null);

  const selectedRoute =
    selection?.kind === "route"
      ? routes.find((r) => r.id === selection.id) ?? null
      : selection === null
        ? routes[0] ?? null
        : null;

  const selectedKeyInfo =
    selection?.kind === "key"
      ? (() => {
          const provider = providers.find((p) => p.id === selection.providerId) ?? null;
          const key = provider?.keys?.find((k) => k.name === selection.keyName) ?? null;
          const freeze = freezes.find(
            (f) => f.provider_id === selection.providerId && f.key_name === selection.keyName,
          );
          const usage = keyUsage?.upstream_keys?.find(
            (u) => u.provider_id === selection.providerId && u.key_name === selection.keyName,
          );
          const boundRoutes = routes.filter((r) =>
            (r.targets || []).some((t) => t.provider === selection.providerId && t.key === selection.keyName),
          );
          return { provider, key, freeze, usage, boundRoutes };
        })()
      : null;

  const selectedRequest =
    selection?.kind === "request" ? streamItems.find((item) => item.id === selection.id) ?? null : null;

  const matchedEntryRoute =
    selection?.kind === "entry"
      ? routes.find((r) => r.id === selection.id || (r.aliases || []).includes(selection.id)) ?? null
      : null;

  const quickTestModel =
    selection?.kind === "route"
      ? selection.id
      : selection?.kind === "request" && selectedRequest?.model_id
        ? selectedRequest.model_id
        : selection?.kind === "key" && selectedKeyInfo?.boundRoutes[0]
          ? selectedKeyInfo.boundRoutes[0].id
          : selection?.kind === "entry"
            ? (matchedEntryRoute?.id || selection.id)
            : routes[0]?.id || "";

  async function invalidateConfigAndOverview() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.routes() }),
      queryClient.invalidateQueries({ queryKey: queryKeys.providers() }),
      queryClient.invalidateQueries({ queryKey: queryKeys.models() }),
      queryClient.invalidateQueries({ queryKey: ["filtered-metrics"] }),
      queryClient.invalidateQueries({ queryKey: ["key-usage"] }),
    ]);
  }

  async function handleUpdateRouteMode(route: RouteEntry, nextMode: string) {
    if (!routesRevision) {
      toast.error("配置版本未就绪，请稍后刷新");
      return;
    }
    setBusyAction(`mode:${route.id}`);
    try {
      await api.updateRoute(
        routesRevision,
        route.id,
        route.targets,
        route.aliases || [],
        nextMode,
        null,
        route.endpoints,
      );
      toast.success(`已将 ${route.id} 策略切换为「${modeLabel(nextMode)}」`);
      await invalidateConfigAndOverview();
    } catch (error) {
      toast.error(`策略更新失败：${errorText(error)}`);
    } finally {
      setBusyAction(null);
    }
  }

  async function handleMoveTarget(route: RouteEntry, index: number, delta: -1 | 1) {
    if (!routesRevision) return;
    const nextIndex = index + delta;
    if (nextIndex < 0 || nextIndex >= route.targets.length) return;
    const nextTargets = [...route.targets];
    [nextTargets[index], nextTargets[nextIndex]] = [nextTargets[nextIndex], nextTargets[index]];
    setBusyAction(`move:${route.id}:${index}`);
    try {
      await api.updateRoute(
        routesRevision,
        route.id,
        nextTargets,
        route.aliases || [],
        route.routing_mode || "round_robin",
        null,
        route.endpoints,
      );
      toast.success(`已调整 ${route.id} 上游目标顺序`);
      await invalidateConfigAndOverview();
    } catch (error) {
      toast.error(`调整顺序失败：${errorText(error)}`);
    } finally {
      setBusyAction(null);
    }
  }

  async function handleToggleProviderKey(providerId: string, keyName: string, nextEnabled: boolean) {
    if (!providersRevision) {
      toast.error("配置版本未就绪，请稍后刷新");
      return;
    }
    setBusyAction(`toggle:${providerId}:${keyName}`);
    try {
      await api.updateProviderKey(providersRevision, providerId, keyName, { enabled: nextEnabled });
      toast.success(`${providerId} / ${keyName} 已${nextEnabled ? "启用" : "停用"}`);
      await invalidateConfigAndOverview();
    } catch (error) {
      toast.error(`切换 Key 状态失败：${errorText(error)}`);
    } finally {
      setBusyAction(null);
    }
  }

  async function handleProbeKey(providerId: string, keyName: string) {
    if (!providersRevision) {
      toast.error("配置版本未就绪，请稍后刷新");
      return;
    }
    const composite = `${providerId} / ${keyName}`;
    setBusyAction(`probe:${composite}`);
    try {
      const start = performance.now();
      await api.probeKey(providersRevision, providerId, keyName);
      const elapsed = Math.round(performance.now() - start);
      setProbeResult({ key: composite, ok: true, text: `探测成功 · 耗时 ${elapsed}ms` });
      toast.success(`探测 ${composite} 成功`);
      await invalidateConfigAndOverview();
    } catch (error) {
      const msg = errorText(error);
      setProbeResult({ key: composite, ok: false, text: `探测失败：${msg}` });
      toast.error(`探测 ${composite} 失败：${msg}`);
    } finally {
      setBusyAction(null);
    }
  }

  async function handleQuickTest(modelId: string, text: string) {
    if (!modelId) {
      toast.error("暂无可用的模型进行调试");
      return;
    }
    setTesting(true);
    setTestResult(null);
    try {
      const result = await api.chatCompletions({
        body: { model: modelId, messages: [{ role: "user", content: text }], stream: false },
      });
      const snippet = (result.message.content || "").trim().slice(0, 72);
      setTestResult({
        ok: true,
        text: `${modelId} · ${formatDuration(result.duration)}${snippet ? ` · ${snippet}` : ""}`,
      });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["filtered-metrics"] }),
        queryClient.invalidateQueries({ queryKey: ["key-usage"] }),
      ]);
    } catch (error) {
      setTestResult({ ok: false, text: errorText(error) });
    } finally {
      setTesting(false);
    }
  }

  function handleFormSubmit(event: FormEvent) {
    event.preventDefault();
    const content = prompt.trim() || "ping";
    void handleQuickTest(quickTestModel, content);
    setPrompt("");
  }

  // Determine Header metadata
  let eyebrow = "ROUTE INSPECTOR";
  let title = selectedRoute?.id || "配置与实时检查器";
  let badgeLabel = selectedRoute ? modeLabel(selectedRoute.routing_mode) : "READY";
  let badgeTone: "neutral" | "good" | "warn" | "critical" | "accent" = selectedRoute ? "accent" : "neutral";

  if (selection?.kind === "key") {
    eyebrow = "KEY INSPECTOR";
    title = `${selection.providerId} / ${selection.keyName}`;
    const enabled = selectedKeyInfo?.key?.enabled !== false;
    badgeLabel = !enabled ? "已停用" : selectedKeyInfo?.freeze ? "额度冻结" : "运行正常";
    badgeTone = !enabled || selectedKeyInfo?.freeze ? "critical" : "good";
  } else if (selection?.kind === "entry") {
    eyebrow = "ENTRY INSPECTOR";
    title = selection.id;
    badgeLabel = "调用名";
    badgeTone = "neutral";
  } else if (selection?.kind === "request" && selectedRequest) {
    eyebrow = "REQUEST DETAIL";
    title = `#${selectedRequest.id} · ${selectedRequest.model_id || "—"}`;
    badgeLabel = `${selectedRequest.status_code ?? "—"}`;
    badgeTone = !selectedRequest.success ? "critical" : selectedRequest.retried ? "warn" : "good";
  }

  const presetRoutes = routes.slice(0, 3);

  const routeTelemetry = selectedRoute
    ? (() => {
        const reqs = (keyUsage?.model_keys ?? [])
          .filter((row) => row.model_id === selectedRoute.id)
          .reduce((sum, row) => sum + Number(row.stats?.requests || 0), 0);
        const recent = streamItems.filter((item) => item.model_id === selectedRoute.id).slice(0, 4);
        const withDur = recent.filter((item) => item.duration_ms != null && Number(item.duration_ms) > 0);
        const avgDur = withDur.length
          ? withDur.reduce((sum, item) => sum + Number(item.duration_ms || 0), 0) / withDur.length
          : null;
        return { reqs, avgDur, recent };
      })()
    : null;

  const keyRecentHits =
    selection?.kind === "key"
      ? streamItems
          .filter(
            (item) =>
              item.provider_id === selection.providerId && item.key_name === selection.keyName,
          )
          .slice(0, 4)
      : [];

  return (
    <Panel
      fill
      padding={false}
      title={
        <span>
          <span className="block font-mono text-[10px] font-normal uppercase tracking-wider text-ink-3">{eyebrow}</span>
          <span className="mt-0.5 block truncate font-mono text-[13px] font-semibold text-ink">{title}</span>
        </span>
      }
      actions={<Badge tone={badgeTone}>{badgeLabel}</Badge>}
      className="h-full min-h-0 flex-1"
    >
      <div className="min-h-0 flex-1 overflow-y-auto divide-y divide-rule">
        {selection?.kind === "request" && selectedRequest ? (
          <div className="space-y-3 p-3.5">
            <div className="grid grid-cols-2 divide-x divide-rule rounded-control border border-rule bg-sunken">
              <div className="p-2">
                <div className="text-[10px] text-ink-3">首字延迟 TTFT</div>
                <div className="mt-0.5 font-mono text-[12px] font-semibold text-ink">
                  {selectedRequest.first_token_ms != null ? formatDuration(Number(selectedRequest.first_token_ms)) : "—"}
                </div>
              </div>
              <div className="p-2">
                <div className="text-[10px] text-ink-3">总耗时</div>
                <div className="mt-0.5 font-mono text-[12px] font-semibold text-ink">
                  {selectedRequest.duration_ms != null ? formatDuration(Number(selectedRequest.duration_ms)) : "—"}
                </div>
              </div>
            </div>

            <div className="divide-y divide-rule text-[11px]">
              <div className="flex items-center justify-between py-1.5">
                <span className="text-ink-3">调用模型</span>
                <button
                  type="button"
                  onClick={() => selectedRequest.model_id && onSelect({ kind: "route", id: selectedRequest.model_id })}
                  className="font-mono text-accent hover:underline"
                >
                  {selectedRequest.model_id || "—"} →
                </button>
              </div>
              <div className="flex items-center justify-between py-1.5">
                <span className="text-ink-3">命中上游 Key</span>
                {selectedRequest.provider_id && selectedRequest.key_name ? (
                  <button
                    type="button"
                    onClick={() =>
                      onSelect({
                        kind: "key",
                        providerId: String(selectedRequest.provider_id),
                        keyName: String(selectedRequest.key_name),
                      })
                    }
                    className="font-mono text-good-ink hover:underline"
                  >
                    {selectedRequest.provider_id} / {selectedRequest.key_name} →
                  </button>
                ) : (
                  <span className="font-mono text-ink-2">—</span>
                )}
              </div>
              <div className="flex items-center justify-between py-1.5">
                <span className="text-ink-3">Token 消耗</span>
                <span className="font-mono text-ink-2">
                  {formatCompact(selectedRequest.prompt_tokens)} in / {formatCompact(selectedRequest.completion_tokens)} out
                </span>
              </div>
            </div>

            <Button size="sm" variant="primary" className="w-full" onClick={() => onOpenRequestDetail(selectedRequest)}>
              查看完整请求详情与 JSON
            </Button>
          </div>
        ) : selection?.kind === "key" && selectedKeyInfo ? (
          <div className="space-y-3 p-3.5">
            <div className="grid grid-cols-2 divide-x divide-rule rounded-control border border-rule bg-sunken">
              <div className="p-2">
                <div className="text-[10px] text-ink-3">窗口请求数</div>
                <div className="mt-0.5 font-mono text-[13px] font-semibold text-ink">
                  {formatCount(selectedKeyInfo.usage?.stats?.requests ?? 0)}
                </div>
              </div>
              <div className="p-2">
                <div className="text-[10px] text-ink-3">Token 用量</div>
                <div className="mt-0.5 font-mono text-[13px] font-semibold text-ink">
                  {formatCompact(selectedKeyInfo.usage?.stats?.total_tokens ?? 0)}
                </div>
              </div>
            </div>

            <div className="divide-y divide-rule text-[11px]">
              <div className="flex items-center justify-between gap-2 py-1.5">
                <span className="text-ink-3">Base URL</span>
                <span className="truncate font-mono text-[10px] text-ink-2">{selectedKeyInfo.provider?.base_url || "—"}</span>
              </div>
              {selectedKeyInfo.freeze ? (
                <div className="py-1.5 text-critical-ink">
                  额度冻结：{selectedKeyInfo.freeze.reason || "窗口配额耗尽"}
                </div>
              ) : null}
              {selectedKeyInfo.key ? (
                <div className="flex items-center justify-between py-2">
                  <span className="text-ink-2">允许接收新请求</span>
                  <Switch
                    aria-label={`启用 ${selection.providerId} / ${selection.keyName}`}
                    checked={selectedKeyInfo.key.enabled !== false}
                    disabled={Boolean(busyAction)}
                    onCheckedChange={(checked) => void handleToggleProviderKey(selection.providerId, selection.keyName, checked)}
                  />
                </div>
              ) : null}
            </div>

            <div className="flex gap-1.5">
              {selectedKeyInfo.key ? (
                <Button
                  size="sm"
                  variant="primary"
                  className="flex-1"
                  loading={busyAction === `probe:${selection.providerId} / ${selection.keyName}`}
                  onClick={() => void handleProbeKey(selection.providerId, selection.keyName)}
                >
                  探测连通性
                </Button>
              ) : null}
              <Button
                size="sm"
                variant="secondary"
                leadingIcon={<Filter className="size-3" aria-hidden />}
                onClick={() => onFilterUpstreamKey(selection.providerId, selection.keyName)}
              >
                按此 Key 筛选
              </Button>
            </div>

            {probeResult?.key === `${selection.providerId} / ${selection.keyName}` ? (
              <div
                className={cn(
                  "rounded-control border px-2.5 py-1.5 font-mono text-[11px]",
                  probeResult.ok ? "border-good/40 text-good-ink" : "border-critical/45 text-critical-ink",
                )}
              >
                {probeResult.text}
              </div>
            ) : null}

            {selectedKeyInfo.boundRoutes.length ? (
              <div>
                <div className="mb-1.5 text-[11px] font-medium text-ink-2">关联路由组 ({selectedKeyInfo.boundRoutes.length})</div>
                <div className="divide-y divide-rule rounded-control border border-rule">
                  {selectedKeyInfo.boundRoutes.map((r) => (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() => onSelect({ kind: "route", id: r.id })}
                      className="flex w-full items-center justify-between bg-raised px-2.5 py-2 text-left hover:bg-hover"
                    >
                      <span className="truncate font-mono text-[11px] font-semibold text-ink">{r.id}</span>
                      <Badge tone="neutral">{modeLabel(r.routing_mode)}</Badge>
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            {keyRecentHits.length ? (
              <div>
                <div className="mb-1.5 flex items-center justify-between">
                  <span className="text-[11px] font-medium text-ink-2">最近命中记录</span>
                  <span className="font-mono text-[10px] text-ink-3">RECENT</span>
                </div>
                <div className="divide-y divide-rule overflow-hidden rounded-control border border-rule">
                  {keyRecentHits.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => onOpenRequestDetail(item)}
                      className="flex w-full items-center justify-between gap-2 bg-raised px-2.5 py-1.5 text-left hover:bg-hover"
                    >
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span
                          aria-hidden
                          className="size-1.5 shrink-0 rounded-pill"
                          style={{
                            backgroundColor: !item.success ? "var(--critical)" : item.retried ? "var(--warn)" : "var(--good)",
                          }}
                        />
                        <span className="font-mono text-[10px] text-ink-3">{formatClockSeconds(item.created_at)}</span>
                        <span className="truncate font-mono text-[11px] text-ink">{item.model_id || "—"}</span>
                      </span>
                      <span className="shrink-0 font-mono text-[10px] tabular-nums text-ink-3">
                        {item.duration_ms != null ? formatDuration(Number(item.duration_ms)) : "—"}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            <div className="grid grid-cols-2 gap-1.5">
              <Button
                size="sm"
                variant="secondary"
                className="w-full"
                onClick={() => navigate("providers", { id: selection.providerId })}
              >
                管理供应商
              </Button>
              <Button
                size="sm"
                variant="secondary"
                className="w-full"
                onClick={() =>
                  navigate("status", {
                    provider: selection.providerId,
                    uk: `${selection.providerId}/${selection.keyName}`,
                  })
                }
              >
                状态监控
              </Button>
            </div>
          </div>
        ) : selection?.kind === "entry" ? (
          <div className="space-y-3 p-3.5">
            <p className="text-[11px] leading-5 text-ink-3">
              这是调用方在请求体中传入的原始模型名称（<span className="font-mono text-ink-2">requested_model_id</span>）。点击模型路由组或上游 Key 节点可原地调整调度优先级。
            </p>
            {matchedEntryRoute ? (
              <Button size="sm" variant="secondary" onClick={() => onSelect({ kind: "route", id: matchedEntryRoute.id })}>
                检查对应路由 {matchedEntryRoute.id} →
              </Button>
            ) : routes[0] ? (
              <Button size="sm" variant="secondary" onClick={() => onSelect({ kind: "route", id: routes[0].id })}>
                检查默认路由 {routes[0].id} →
              </Button>
            ) : null}
          </div>
        ) : selectedRoute ? (
          <div className="space-y-3 p-3.5">
            {routeTelemetry ? (
              <div className="grid grid-cols-2 divide-x divide-rule rounded-control border border-rule bg-sunken">
                <div className="p-2">
                  <div className="text-[10px] text-ink-3">窗口请求数</div>
                  <div className="mt-0.5 font-mono text-[12px] font-semibold tabular-nums text-ink">
                    {formatCount(routeTelemetry.reqs)}
                  </div>
                </div>
                <div className="p-2">
                  <div className="text-[10px] text-ink-3">最近平均耗时</div>
                  <div className="mt-0.5 font-mono text-[12px] font-semibold tabular-nums text-ink">
                    {routeTelemetry.avgDur !== null ? formatDuration(routeTelemetry.avgDur) : "—"}
                  </div>
                </div>
              </div>
            ) : null}

            {selectedRoute.aliases?.length ? (
              <div className="flex flex-wrap items-center gap-1 border-b border-rule pb-2 text-[10px]">
                <span className="text-ink-3">别名</span>
                {selectedRoute.aliases.map((alias) => (
                  <Badge key={alias} tone="neutral">{alias}</Badge>
                ))}
              </div>
            ) : null}

            <div className="space-y-1.5">
              <div className="flex items-center justify-between">
                <span className="text-[11px] font-medium text-ink-2">路由分发策略</span>
                <button
                  type="button"
                  onClick={() => onFilterModel(selectedRoute.id)}
                  className="inline-flex items-center gap-1 font-mono text-[10px] text-accent hover:underline"
                >
                  <Filter className="size-2.5" aria-hidden />
                  按此筛选
                </button>
              </div>
              <Segmented
                aria-label={`${selectedRoute.id} 路由策略`}
                value={selectedRoute.routing_mode || "round_robin"}
                onValueChange={(val) => void handleUpdateRouteMode(selectedRoute, val)}
                className="grid w-full grid-cols-3"
                options={[
                  { value: "round_robin", label: "轮询" },
                  { value: "priority", label: "优先级" },
                  { value: "only_first", label: "首 Key" },
                ]}
              />
            </div>

            <div>
              <div className="mb-1.5 flex items-center justify-between">
                <span className="text-[11px] font-medium text-ink-2">上游目标优先级</span>
                <span className="font-mono text-[10px] text-ink-3">{selectedRoute.targets.length} TARGETS</span>
              </div>
              <div className="divide-y divide-rule overflow-hidden rounded-control border border-rule">
                {selectedRoute.targets.map((t, index) => {
                  const providerObj = providers.find((p) => p.id === t.provider);
                  const keyObj = providerObj?.keys?.find((k) => k.name === t.key);
                  const enabled = keyObj ? keyObj.enabled !== false : true;
                  return (
                    <div key={`${t.provider}|${t.key}|${index}`} className={cn("bg-raised p-2.5", !enabled && "opacity-60")}>
                      <div className="flex items-center justify-between gap-2">
                        <div className="flex min-w-0 items-center gap-2">
                          <span
                            className={cn(
                              "flex size-4 shrink-0 items-center justify-center rounded-chip font-mono text-[10px] font-semibold",
                              index === 0 && enabled
                                ? "bg-accent text-on-accent"
                                : "border border-rule-strong text-ink-3",
                            )}
                          >
                            {index + 1}
                          </span>
                          <button
                            type="button"
                            onClick={() => onSelect({ kind: "key", providerId: t.provider, keyName: t.key })}
                            className="truncate font-mono text-[11px] font-semibold text-ink hover:text-accent"
                          >
                            {t.provider} / {t.key}
                          </button>
                        </div>
                        <div className="flex shrink-0 items-center gap-1">
                          <button
                            type="button"
                            aria-label={`上移目标 ${index + 1}`}
                            disabled={index === 0 || Boolean(busyAction)}
                            onClick={() => void handleMoveTarget(selectedRoute, index, -1)}
                            className="grid size-5 place-items-center rounded-control border border-rule-strong bg-panel text-ink-2 hover:bg-hover hover:text-ink disabled:opacity-40"
                          >
                            <ArrowUp className="size-3" aria-hidden />
                          </button>
                          <button
                            type="button"
                            aria-label={`下移目标 ${index + 1}`}
                            disabled={index === selectedRoute.targets.length - 1 || Boolean(busyAction)}
                            onClick={() => void handleMoveTarget(selectedRoute, index, 1)}
                            className="grid size-5 place-items-center rounded-control border border-rule-strong bg-panel text-ink-2 hover:bg-hover hover:text-ink disabled:opacity-40"
                          >
                            <ArrowDown className="size-3" aria-hidden />
                          </button>
                          {keyObj ? (
                            <Switch
                              aria-label={`开关 ${t.provider} / ${t.key}`}
                              checked={enabled}
                              disabled={Boolean(busyAction)}
                              onCheckedChange={(checked) => void handleToggleProviderKey(t.provider, t.key, checked)}
                              className="ml-1"
                            />
                          ) : null}
                        </div>
                      </div>
                      <div className="mt-1 truncate pl-6 font-mono text-[10px] text-ink-3">
                        上游模型 {t.upstream_model || selectedRoute.id}
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>

            {routeTelemetry?.recent.length ? (
              <div>
                <div className="mb-1.5 flex items-center justify-between">
                  <span className="text-[11px] font-medium text-ink-2">最近命中记录</span>
                  <span className="font-mono text-[10px] text-ink-3">RECENT</span>
                </div>
                <div className="divide-y divide-rule overflow-hidden rounded-control border border-rule">
                  {routeTelemetry.recent.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => onOpenRequestDetail(item)}
                      className="flex w-full items-center justify-between gap-2 bg-raised px-2.5 py-1.5 text-left hover:bg-hover"
                    >
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span
                          aria-hidden
                          className="size-1.5 shrink-0 rounded-pill"
                          style={{
                            backgroundColor: !item.success ? "var(--critical)" : item.retried ? "var(--warn)" : "var(--good)",
                          }}
                        />
                        <span className="font-mono text-[10px] text-ink-3">{formatClockSeconds(item.created_at)}</span>
                        <span className="truncate font-mono text-[11px] text-ink">
                          {item.provider_id && item.key_name ? `${item.provider_id} / ${item.key_name}` : item.provider_id || "—"}
                        </span>
                      </span>
                      <span className="shrink-0 font-mono text-[10px] tabular-nums text-ink-3">
                        {item.duration_ms != null ? formatDuration(Number(item.duration_ms)) : "—"}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            <div className="grid grid-cols-2 gap-1.5">
              <Button
                size="sm"
                variant="secondary"
                className="w-full"
                trailingIcon={<ArrowRight className="size-3.5" aria-hidden />}
                onClick={() => navigate("routing", { id: selectedRoute.id })}
              >
                完整路由配置
              </Button>
              <Button
                size="sm"
                variant="primary"
                className="w-full"
                leadingIcon={<Play className="size-3" aria-hidden />}
                onClick={() => navigate("playground", { model: selectedRoute.id })}
              >
                试验场试跑
              </Button>
            </div>
          </div>
        ) : (
          <div className="p-4 text-center text-meta text-ink-3">
            点击拓扑中的入口、模型路由组或上游 Key，可在此实时查看并调整配置。
          </div>
        )}
      </div>

      {/* Collapsible Quick Debugger Drawer */}
      <div className="shrink-0 border-t border-rule bg-raised">
        <button
          type="button"
          onClick={() => setTesterOpen((v) => !v)}
          className="flex w-full items-center justify-between px-3 py-2 text-left transition-colors hover:bg-hover"
        >
          <span className="flex items-center gap-1.5">
            <Terminal className="size-3.5 text-accent" aria-hidden />
            <span className="text-[11px] font-semibold text-ink">快捷调试终端</span>
            <Badge tone="neutral">/v1/chat</Badge>
          </span>
          <span className="inline-flex items-center gap-1 font-mono text-[10px] text-ink-3">
            {testerOpen ? "收起" : "展开"}
            {testerOpen ? <ChevronDown className="size-3" aria-hidden /> : <ChevronUp className="size-3" aria-hidden />}
          </span>
        </button>

        {testerOpen ? (
          <div className="space-y-2 border-t border-rule px-3 py-2.5">
            {presetRoutes.length ? (
              <div className="flex flex-wrap gap-1">
                {presetRoutes.map((r) => (
                  <button
                    key={r.id}
                    type="button"
                    disabled={testing}
                    onClick={() => void handleQuickTest(r.id, "hello")}
                    className="inline-flex h-6 items-center gap-1 rounded-control border border-rule-strong bg-panel px-2 font-mono text-[10px] text-ink-2 hover:bg-hover hover:text-ink disabled:opacity-50"
                  >
                    <Play className="size-2.5 text-accent" aria-hidden />
                    试发 {r.id}
                  </button>
                ))}
              </div>
            ) : null}

            {testResult ? (
              <div
                className={cn(
                  "rounded-control border bg-paper px-2 py-1.5 font-mono text-[11px]",
                  testResult.ok ? "border-good/40 text-good-ink" : "border-critical/45 text-critical-ink",
                )}
              >
                {testResult.text}
              </div>
            ) : null}

            <form onSubmit={handleFormSubmit} className="flex items-center gap-1.5">
              <input
                type="text"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder={quickTestModel ? `向 ${quickTestModel} 发送测试 Prompt…` : "输入测试 Prompt…"}
                aria-label="快捷调试输入"
                className="h-7 min-w-0 flex-1 rounded-control border border-rule-strong bg-paper px-2 text-[11px] text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none"
              />
              <Button size="sm" type="submit" variant="primary" loading={testing}>
                发送
              </Button>
            </form>
          </div>
        ) : null}
      </div>
    </Panel>
  );
}
