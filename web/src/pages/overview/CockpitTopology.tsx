import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import type {
  KeyUsageResponse,
  MetricStats,
  Provider,
  QuotaFreeze,
  RouteEntry,
  StatusMonitorResponse,
} from "@/lib/types";
import { aggregateCells, healthBand, type StatusCell } from "@/lib/status-math";
import { formatCompact, formatDuration } from "@/lib/format";
import { cn } from "@/lib/cn";
import { modeLabel } from "@/pages/routing/shared";

export type CockpitSelection =
  | { kind: "entry"; id: string }
  | { kind: "route"; id: string }
  | { kind: "key"; providerId: string; keyName: string }
  | { kind: "request"; id: number };

interface EntryNode {
  id: string;
  requests: number;
  routes: string[];
}

interface RouteNode {
  id: string;
  routingMode: string;
  primaryKey: string | null;
  requests: number;
  targetKeys: Array<{ providerId: string; keyName: string }>;
}

interface KeyNode {
  compositeId: string;
  providerId: string;
  keyName: string;
  enabled: boolean;
  frozen: boolean;
  freezeReason?: string;
  band: "good" | "warn" | "bad" | "unknown" | "no-traffic";
  requests: number;
  avgTtftMs: number | null;
}

interface FiberCurve {
  id: string;
  d: string;
  stroke: string;
  width: number;
  opacity: number;
  animate: boolean;
}

export interface CockpitTopologyProps {
  modelRequestedModels?: Record<string, Record<string, MetricStats>>;
  keyUsage?: KeyUsageResponse;
  routes: RouteEntry[];
  providers: Provider[];
  statusMonitor?: StatusMonitorResponse;
  freezes: QuotaFreeze[];
  selection: CockpitSelection | null;
  onSelect: (selection: CockpitSelection | null) => void;
}

function statsAvgTtft(stats: { requests?: number; total_first_token_ms?: number; avg_first_token_ms?: unknown } | undefined): number | null {
  const requests = Number(stats?.requests || 0);
  if (!(requests > 0)) return null;
  const total = Number(stats?.total_first_token_ms || 0);
  if (total > 0) return total / requests;
  return null;
}

export function CockpitTopology({
  modelRequestedModels,
  keyUsage,
  routes,
  providers,
  statusMonitor,
  freezes,
  selection,
  onSelect,
}: CockpitTopologyProps) {
  const stageRef = useRef<HTMLDivElement>(null);
  const entryRefs = useRef(new Map<string, HTMLButtonElement>());
  const routeRefs = useRef(new Map<string, HTMLButtonElement>());
  const keyRefs = useRef(new Map<string, HTMLButtonElement>());
  const [fibers, setFibers] = useState<FiberCurve[]>([]);

  const graph = useMemo(() => {
    // 01 / 接入调用名（来自实时请求流量的 requested_model 维度，无流量时展示路由入口）
    const entryMap = new Map<string, { requests: number; routes: Set<string> }>();
    const routeReqMap = new Map<string, number>();

    for (const [routeId, requestedMap] of Object.entries(modelRequestedModels || {})) {
      let routeReqs = 0;
      for (const [entryName, stats] of Object.entries(requestedMap || {})) {
        const count = Number(stats?.requests || 0);
        if (count <= 0) continue;
        routeReqs += count;
        const prev = entryMap.get(entryName) ?? { requests: 0, routes: new Set<string>() };
        prev.requests += count;
        prev.routes.add(routeId);
        entryMap.set(entryName, prev);
      }
      if (routeReqs > 0) routeReqMap.set(routeId, routeReqs);
    }

    const entries: EntryNode[] = [...entryMap.entries()]
      .sort((a, b) => b[1].requests - a[1].requests || a[0].localeCompare(b[0]))
      .slice(0, 6)
      .map(([id, info]) => ({ id, requests: info.requests, routes: [...info.routes] }));

    // 02 / 模型路由（合并配置路由与流量观察到的路由）
    const keyUsageByModelKey = new Map<string, number>();
    const observedKeysByRoute = new Map<string, Set<string>>();
    for (const row of keyUsage?.model_keys ?? []) {
      const composite = `${row.provider_id} / ${row.key_name}`;
      keyUsageByModelKey.set(`${row.model_id}\u0000${composite}`, Number(row.stats?.requests || 0));
      const set = observedKeysByRoute.get(row.model_id) ?? new Set<string>();
      set.add(composite);
      observedKeysByRoute.set(row.model_id, set);
      if (!routeReqMap.has(row.model_id)) {
        routeReqMap.set(row.model_id, Number(row.stats?.requests || 0));
      }
    }

    const routeMap = new Map<string, RouteNode>();
    for (const r of routes) {
      const targetKeys = (r.targets || []).map((t) => ({ providerId: t.provider, keyName: t.key }));
      const primary = targetKeys[0] ? `${targetKeys[0].providerId} / ${targetKeys[0].keyName}` : null;
      routeMap.set(r.id, {
        id: r.id,
        routingMode: r.routing_mode || "",
        primaryKey: primary,
        requests: routeReqMap.get(r.id) || 0,
        targetKeys,
      });
    }
    for (const [modelId, reqs] of routeReqMap.entries()) {
      if (!routeMap.has(modelId)) {
        const obs = [...(observedKeysByRoute.get(modelId) ?? [])].map((composite) => {
          const [providerId, ...rest] = composite.split(" / ");
          return { providerId, keyName: rest.join(" / ") };
        });
        routeMap.set(modelId, {
          id: modelId,
          routingMode: "",
          primaryKey: obs[0] ? `${obs[0].providerId} / ${obs[0].keyName}` : null,
          requests: reqs,
          targetKeys: obs,
        });
      }
    }

    const routeNodes = [...routeMap.values()]
      .sort((a, b) => b.requests - a.requests || a.id.localeCompare(b.id))
      .slice(0, 7);

    // Fallback entry if no window traffic yet so col 1 aligns cleanly
    if (entries.length === 0 && routeNodes.length > 0) {
      entries.push({
        id: "unified-model / 直呼入口",
        requests: 0,
        routes: routeNodes.slice(0, 3).map((r) => r.id),
      });
    }

    // 03 / 上游密钥池（合并配置 Keys + Status 矩阵 + 冻结状态）
    const freezeSet = new Map<string, QuotaFreeze>();
    for (const f of freezes) {
      freezeSet.set(`${f.provider_id} / ${f.key_name}`, f);
    }

    const statusByKey = new Map<string, { band: "good" | "warn" | "bad" | "unknown" | "no-traffic"; requests: number; ttft: number | null }>();
    for (const row of statusMonitor?.matrix.rows ?? []) {
      if (!row.provider_id || !row.key_name) continue;
      const agg = aggregateCells(row.cells as StatusCell[]);
      statusByKey.set(`${row.provider_id} / ${row.key_name}`, {
        band: healthBand(agg, "overall"),
        requests: agg.requests,
        ttft: statsAvgTtft(agg),
      });
    }

    const usageByKey = new Map<string, { requests: number; ttft: number | null }>();
    for (const u of keyUsage?.upstream_keys ?? []) {
      usageByKey.set(`${u.provider_id} / ${u.key_name}`, {
        requests: Number(u.stats?.requests || 0),
        ttft: statsAvgTtft(u.stats),
      });
    }

    const keyMap = new Map<string, KeyNode>();
    for (const provider of providers) {
      for (const k of provider.keys || []) {
        const compositeId = `${provider.id} / ${k.name}`;
        const st = statusByKey.get(compositeId);
        const us = usageByKey.get(compositeId);
        const fr = freezeSet.get(compositeId);
        keyMap.set(compositeId, {
          compositeId,
          providerId: provider.id,
          keyName: k.name,
          enabled: k.enabled !== false,
          frozen: Boolean(fr),
          freezeReason: fr?.reason || (fr ? "额度冻结中" : undefined),
          band: st?.band ?? "unknown",
          requests: us?.requests ?? st?.requests ?? 0,
          avgTtftMs: us?.ttft ?? st?.ttft ?? null,
        });
      }
    }

    // Also include keys only seen in traffic / routes / status matrix
    const ensureKey = (providerId: string, keyName: string) => {
      const compositeId = `${providerId} / ${keyName}`;
      if (keyMap.has(compositeId)) return;
      const st = statusByKey.get(compositeId);
      const us = usageByKey.get(compositeId);
      const fr = freezeSet.get(compositeId);
      keyMap.set(compositeId, {
        compositeId,
        providerId,
        keyName,
        enabled: true,
        frozen: Boolean(fr),
        freezeReason: fr?.reason,
        band: st?.band ?? "unknown",
        requests: us?.requests ?? st?.requests ?? 0,
        avgTtftMs: us?.ttft ?? st?.ttft ?? null,
      });
    };

    for (const r of routeNodes) {
      for (const t of r.targetKeys) ensureKey(t.providerId, t.keyName);
    }
    for (const row of keyUsage?.model_keys ?? []) ensureKey(row.provider_id, row.key_name);

    const keyNodes = [...keyMap.values()]
      .sort((a, b) => b.requests - a.requests || Number(b.enabled) - Number(a.enabled) || a.compositeId.localeCompare(b.compositeId))
      .slice(0, 8);

    const visibleRouteSet = new Set(routeNodes.map((r) => r.id));
    const visibleKeySet = new Set(keyNodes.map((k) => k.compositeId));

    // Edges between Col 1 -> Col 2 and Col 2 -> Col 3
    const e2r: Array<{ entryId: string; routeId: string; requests: number }> = [];
    for (const e of entries) {
      for (const rid of e.routes) {
        if (visibleRouteSet.has(rid)) {
          e2r.push({ entryId: e.id, routeId: rid, requests: e.requests });
        }
      }
    }

    const r2k: Array<{ routeId: string; keyComposite: string; isPrimary: boolean; requests: number }> = [];
    for (const r of routeNodes) {
      const seen = new Set<string>();
      r.targetKeys.forEach((t, idx) => {
        const comp = `${t.providerId} / ${t.keyName}`;
        if (!visibleKeySet.has(comp) || seen.has(comp)) return;
        seen.add(comp);
        r2k.push({
          routeId: r.id,
          keyComposite: comp,
          isPrimary: idx === 0,
          requests: keyUsageByModelKey.get(`${r.id}\u0000${comp}`) ?? 0,
        });
      });
      for (const comp of observedKeysByRoute.get(r.id) ?? []) {
        if (!visibleKeySet.has(comp) || seen.has(comp)) continue;
        seen.add(comp);
        r2k.push({
          routeId: r.id,
          keyComposite: comp,
          isPrimary: false,
          requests: keyUsageByModelKey.get(`${r.id}\u0000${comp}`) ?? 0,
        });
      }
    }

    return { entries, routeNodes, keyNodes, e2r, r2k };
  }, [modelRequestedModels, keyUsage, routes, providers, statusMonitor, freezes]);

  // Compute highlighted node sets based on current selection
  const highlight = useMemo(() => {
    if (!selection) return null;
    const entries = new Set<string>();
    const routesSet = new Set<string>();
    const keys = new Set<string>();

    if (selection.kind === "entry") {
      entries.add(selection.id);
      for (const edge of graph.e2r) {
        if (edge.entryId === selection.id) routesSet.add(edge.routeId);
      }
      for (const edge of graph.r2k) {
        if (routesSet.has(edge.routeId)) keys.add(edge.keyComposite);
      }
    } else if (selection.kind === "route") {
      routesSet.add(selection.id);
      for (const edge of graph.e2r) {
        if (edge.routeId === selection.id) entries.add(edge.entryId);
      }
      for (const edge of graph.r2k) {
        if (edge.routeId === selection.id) keys.add(edge.keyComposite);
      }
    } else if (selection.kind === "key") {
      const comp = `${selection.providerId} / ${selection.keyName}`;
      keys.add(comp);
      for (const edge of graph.r2k) {
        if (edge.keyComposite === comp) routesSet.add(edge.routeId);
      }
      for (const edge of graph.e2r) {
        if (routesSet.has(edge.routeId)) entries.add(edge.entryId);
      }
    } else {
      // request selection highlights all matching route/key passed from parent via route/key clicks
      return null;
    }

    return { entries, routes: routesSet, keys };
  }, [selection, graph]);

  const computeFibers = useCallback(() => {
    const stage = stageRef.current;
    if (!stage) return;
    const rect = stage.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;

    const anchor = (el: HTMLElement | undefined, side: "left" | "right") => {
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return {
        x: (side === "right" ? r.right : r.left) - rect.left,
        y: r.top + r.height / 2 - rect.top,
      };
    };

    const nextFibers: FiberCurve[] = [];

    for (const edge of graph.e2r) {
      const p1 = anchor(entryRefs.current.get(edge.entryId), "right");
      const p2 = anchor(routeRefs.current.get(edge.routeId), "left");
      if (!p1 || !p2) continue;
      const dx = Math.abs(p2.x - p1.x) * 0.45;
      const active = !highlight || (highlight.entries.has(edge.entryId) && highlight.routes.has(edge.routeId));
      nextFibers.push({
        id: `e2r:${edge.entryId}->${edge.routeId}`,
        d: `M ${p1.x} ${p1.y} C ${p1.x + dx} ${p1.y}, ${p2.x - dx} ${p2.y}, ${p2.x} ${p2.y}`,
        stroke: active ? "var(--accent)" : "var(--rule-strong)",
        width: active ? 1.6 : 1,
        opacity: active ? 0.85 : 0.25,
        animate: active && edge.requests > 0,
      });
    }

    const keyNodeMap = new Map(graph.keyNodes.map((k) => [k.compositeId, k]));
    for (const edge of graph.r2k) {
      const p1 = anchor(routeRefs.current.get(edge.routeId), "right");
      const p2 = anchor(keyRefs.current.get(edge.keyComposite), "left");
      if (!p1 || !p2) continue;
      const dx = Math.abs(p2.x - p1.x) * 0.45;
      const keyNode = keyNodeMap.get(edge.keyComposite);
      const disabledOrFrozen = !keyNode?.enabled || keyNode?.frozen || keyNode?.band === "bad";
      const active =
        !highlight || (highlight.routes.has(edge.routeId) && highlight.keys.has(edge.keyComposite));

      let stroke = "var(--rule-strong)";
      if (keyNode?.frozen || keyNode?.band === "bad") stroke = "var(--critical)";
      else if (!keyNode?.enabled) stroke = "var(--rule-strong)";
      else if (active && (edge.isPrimary || highlight)) stroke = "var(--good)";
      else if (active) stroke = "var(--series-1)";

      nextFibers.push({
        id: `r2k:${edge.routeId}->${edge.keyComposite}`,
        d: `M ${p1.x} ${p1.y} C ${p1.x + dx} ${p1.y}, ${p2.x - dx} ${p2.y}, ${p2.x} ${p2.y}`,
        stroke,
        width: active && edge.isPrimary ? 1.7 : 1.1,
        opacity: disabledOrFrozen ? 0.35 : active ? 0.85 : 0.25,
        animate: Boolean(active && !disabledOrFrozen && (edge.requests > 0 || edge.isPrimary)),
      });
    }

    setFibers(nextFibers);
  }, [graph, highlight]);

  useLayoutEffect(() => {
    computeFibers();
    const stage = stageRef.current;
    if (!stage || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => computeFibers());
    ro.observe(stage);
    return () => ro.disconnect();
  }, [computeFibers]);

  if (!graph.routeNodes.length && !graph.keyNodes.length) {
    return (
      <div className="amkr-dot-matrix flex min-h-[280px] flex-1 items-center justify-center rounded-control border border-rule p-6 text-center text-meta text-ink-3">
        当前窗口内暂无路由或上游密钥拓扑数据。
      </div>
    );
  }

  return (
    <div
      ref={stageRef}
      className="amkr-dot-matrix relative flex min-h-[300px] flex-1 flex-col gap-4 overflow-x-auto overflow-y-auto p-3.5 md:flex-row md:items-center md:justify-between md:gap-5"
    >
      {/* 连线层（仅桌面横排时渲染矢量连线） */}
      <svg
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 hidden size-full md:block"
        style={{ zIndex: 1 }}
      >
        <g>
          {fibers.map((fiber) => (
            <path
              key={fiber.id}
              d={fiber.d}
              fill="none"
              stroke={fiber.stroke}
              strokeWidth={fiber.width}
              strokeLinecap="round"
              opacity={fiber.opacity}
              className={fiber.animate ? "amkr-fiber-ambient" : undefined}
            />
          ))}
        </g>
        <g>
          {fibers
            .filter((fiber) => fiber.animate)
            .slice(0, 16)
            .map((fiber) => (
              <circle key={`dot:${fiber.id}`} r="2" fill={fiber.stroke}>
                <animateMotion dur="2.1s" repeatCount="indefinite" path={fiber.d} />
              </circle>
            ))}
        </g>
      </svg>

      {/* Column 1: Entry Calls */}
      <div className="relative z-[2] flex w-full shrink-0 flex-col gap-2 md:w-[178px]">
        <div className="flex items-center justify-between px-0.5 font-mono text-[10px] uppercase tracking-wider text-ink-3">
          <span>01 / 调用入口</span>
          <span>ENTRY</span>
        </div>
        {graph.entries.map((entry) => {
          const isSelected = selection?.kind === "entry" && selection.id === entry.id;
          const isDimmed = Boolean(highlight && !highlight.entries.has(entry.id));
          return (
            <button
              key={entry.id}
              ref={(node) => {
                if (node) entryRefs.current.set(entry.id, node);
                else entryRefs.current.delete(entry.id);
              }}
              type="button"
              onClick={() => onSelect({ kind: "entry", id: entry.id })}
              className={cn(
                "group relative rounded-[5px] border bg-raised px-2.5 py-2 text-left shadow-[var(--shadow-node)] transition-all",
                isSelected ? "border-accent ring-1 ring-accent" : "border-rule hover:border-rule-strong hover:bg-hover",
                isDimmed && "opacity-35",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "pointer-events-none absolute -right-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] bg-panel md:block",
                  isSelected ? "border-accent bg-accent" : "border-rule-strong group-hover:border-accent group-hover:bg-accent",
                )}
              />
              <div className="truncate font-mono text-[11px] font-semibold text-ink">{entry.id}</div>
              <div className="mt-1 flex items-center justify-between font-mono text-[10px] text-ink-3">
                <span>{entry.routes.length} 条路由</span>
                <span>{entry.requests > 0 ? `${formatCompact(entry.requests)} 次` : "待机"}</span>
              </div>
            </button>
          );
        })}
      </div>

      {/* Column 2: Model Routes */}
      <div className="relative z-[2] flex w-full shrink-0 flex-col gap-2 md:w-[214px]">
        <div className="flex items-center justify-between px-0.5 font-mono text-[10px] uppercase tracking-wider text-ink-3">
          <span>02 / 模型路由组</span>
          <span>ROUTES</span>
        </div>
        {graph.routeNodes.map((routeNode) => {
          const isSelected = selection?.kind === "route" && selection.id === routeNode.id;
          const isDimmed = Boolean(highlight && !highlight.routes.has(routeNode.id));
          return (
            <button
              key={routeNode.id}
              ref={(node) => {
                if (node) routeRefs.current.set(routeNode.id, node);
                else routeRefs.current.delete(routeNode.id);
              }}
              type="button"
              onClick={() => onSelect({ kind: "route", id: routeNode.id })}
              style={{ borderLeftWidth: "2px", borderLeftColor: isSelected ? "var(--accent)" : "var(--rule-strong)" }}
              className={cn(
                "group relative rounded-[5px] border bg-raised px-2.5 py-2 text-left shadow-[var(--shadow-node)] transition-all",
                isSelected ? "border-accent ring-1 ring-accent" : "border-rule hover:border-rule-strong hover:bg-hover",
                isDimmed && "opacity-35",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "pointer-events-none absolute -left-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] bg-panel md:block",
                  isSelected ? "border-accent bg-accent" : "border-rule-strong group-hover:border-accent group-hover:bg-accent",
                )}
              />
              <span
                aria-hidden
                className={cn(
                  "pointer-events-none absolute -right-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] bg-panel md:block",
                  isSelected ? "border-accent bg-accent" : "border-rule-strong group-hover:border-accent group-hover:bg-accent",
                )}
              />
              <div className="flex items-center justify-between gap-1.5">
                <span className="truncate font-mono text-[12px] font-semibold text-ink">{routeNode.id}</span>
                <span className="shrink-0 rounded-chip border border-rule-strong px-1.5 py-0 font-mono text-[10px] leading-4 text-ink-2">
                  {modeLabel(routeNode.routingMode)}
                </span>
              </div>
              <div className="mt-1 flex items-center justify-between gap-2 font-mono text-[10px] text-ink-3">
                <span className="truncate">{routeNode.primaryKey ? `主用 ${routeNode.primaryKey}` : "自动路由"}</span>
                <span className="shrink-0 text-ink-2">{formatCompact(routeNode.requests)} 次</span>
              </div>
            </button>
          );
        })}
      </div>

      {/* Column 3: Upstream Key Pool */}
      <div className="relative z-[2] flex w-full shrink-0 flex-col gap-1.5 md:w-[216px]">
        <div className="flex items-center justify-between px-0.5 font-mono text-[10px] uppercase tracking-wider text-ink-3">
          <span>03 / 上游密钥池</span>
          <span>KEYS</span>
        </div>
        {graph.keyNodes.map((keyNode) => {
          const isSelected =
            selection?.kind === "key" &&
            selection.providerId === keyNode.providerId &&
            selection.keyName === keyNode.keyName;
          const isDimmed = Boolean(highlight && !highlight.keys.has(keyNode.compositeId));
          const isCrit = keyNode.frozen || !keyNode.enabled || keyNode.band === "bad";
          const isWarn = !isCrit && keyNode.band === "warn";
          const dotColor = !keyNode.enabled
            ? "var(--neutral)"
            : isCrit
              ? "var(--critical)"
              : isWarn
                ? "var(--warn)"
                : "var(--good)";

          return (
            <button
              key={keyNode.compositeId}
              ref={(node) => {
                if (node) keyRefs.current.set(keyNode.compositeId, node);
                else keyRefs.current.delete(keyNode.compositeId);
              }}
              type="button"
              onClick={() => onSelect({ kind: "key", providerId: keyNode.providerId, keyName: keyNode.keyName })}
              className={cn(
                "group relative rounded-[5px] border bg-raised px-2.5 py-1.5 text-left shadow-[var(--shadow-node)] transition-all",
                isSelected ? "border-accent ring-1 ring-accent" : isCrit && keyNode.enabled ? "border-critical/45 hover:border-critical" : "border-rule hover:border-rule-strong hover:bg-hover",
                !keyNode.enabled && "opacity-65",
                isDimmed && "opacity-35",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "pointer-events-none absolute -left-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] bg-panel md:block",
                  isSelected ? "border-accent bg-accent" : "border-rule-strong group-hover:border-accent group-hover:bg-accent",
                )}
              />
              <div className="flex items-center justify-between gap-1.5">
                <div className="flex min-w-0 items-center gap-1.5">
                  <span aria-hidden className="size-1.5 shrink-0 rounded-pill" style={{ backgroundColor: dotColor }} />
                  <span className="truncate font-mono text-[11px] font-semibold text-ink">{keyNode.compositeId}</span>
                </div>
                <span
                  className={cn(
                    "shrink-0 rounded-chip border px-1.5 font-mono text-[10px] leading-4",
                    isCrit ? "border-critical/45 text-critical-ink" : isWarn ? "border-warn/45 text-warn-ink" : "border-rule-strong text-ink-3",
                  )}
                >
                  {!keyNode.enabled
                    ? "已停用"
                    : keyNode.frozen
                      ? "冻结"
                      : keyNode.avgTtftMs !== null
                        ? formatDuration(keyNode.avgTtftMs)
                        : "就绪"}
                </span>
              </div>
              <div className="mt-0.5 flex items-center justify-between pl-3 font-mono text-[10px] text-ink-3">
                <span className="truncate">{keyNode.freezeReason || keyNode.providerId}</span>
                <span className="shrink-0">{formatCompact(keyNode.requests)} 次</span>
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}
