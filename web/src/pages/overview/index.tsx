// 概览：现在怎么样？
//
// 这一页只回答“此刻发生了什么”，因此所有面板共用同一组 URL 筛选与时间窗：
// 仪表带、流向、流量/结果、请求流都从 filtered-metrics 或同参数的 /ui/* 读数取数。
// 唯一刻意不跟随筛选的是 active_requests（全局在途数），在筛选态会明确加注。

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, keepPreviousData } from "@tanstack/react-query";
import { ChevronRight, CircleAlert, RefreshCw } from "lucide-react";
import { navigate } from "@/app/router";
import { useMetricFilters, useSelectFilter } from "@/lib/hooks/useMetricFilters";
import {
  rangeLabel,
  rangeToQuery,
  toApiFilters,
  writeFilters,
  type MetricFilterState,
} from "@/lib/filters";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  BreakdownGroup,
  FilteredMetricsResponse,
  MetricStats,
  RequestItem,
} from "@/lib/types";
import { aggregateCells, errorReasons, healthBand, healthScore, rowLabel, type StatusCell } from "@/lib/status-math";
import { formatCompactNumber, formatDurationValue, formatPercentValue, pickBucketSeconds, type FlowLink } from "@/lib/chart-math";
import { formatClockSeconds, formatCompact, formatCount, formatDateTime, formatDuration } from "@/lib/format";
import { aggregateCost, formatCost, formatPrice, loadPricing, lookupPrice, requestCost, type PriceIndex } from "@/lib/pricing";
import { cn } from "@/lib/cn";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { Empty } from "@/components/ui/Empty";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { StatusDot } from "@/components/ui/StatusDot";
import { FilterBar } from "@/components/filters/FilterBar";
import { FlowMap, Sparkline, StackedColumns, TimeSeriesChart, type ChartSeries, type TimeBucket } from "@/components/charts";

const CALLER_LABELS: Record<string, string> = {
  local: "本机",
  workspace: "工作空间凭据",
  access_key: "访问密钥",
};

const API_FORMAT_LABELS: Record<string, string> = {
  "chat/completions": "Chat",
  messages: "Messages",
  responses: "Responses",
  embeddings: "Embeddings",
  "images/generations": "Images",
  "images/edits": "Images 编辑",
  "images/variations": "Images 变体",
  "audio/speech": "语音合成",
  "audio/transcriptions": "语音转写",
  "audio/translations": "语音翻译",
  videos: "视频生成",
  rerank: "重排",
};

type StreamFilter = "all" | "success" | "failure" | "retry";
type FlowMetric = "requests" | "tokens";

function hasFilters(state: MetricFilterState): boolean {
  return Boolean(state.provider.length || state.model.length || state.uk.length || state.ak.length || state.ws.length || state.caller.length || state.outcome);
}

function statsAvgTtft(stats: MetricStats | null | undefined): number | null {
  const requests = Number(stats?.requests || 0);
  if (!(requests > 0)) return null;
  const total = Number(stats?.total_first_token_ms || 0);
  if (total > 0) return total / requests;
  const avg = Number(stats?.avg_first_token_ms || 0);
  return avg > 0 ? avg : null;
}

function statsSuccessRate(stats: MetricStats | null | undefined): number | null {
  const requests = Number(stats?.requests || 0);
  return requests > 0 ? Number(stats?.successes || 0) / requests : null;
}

function durationText(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  return formatDuration(Number(value));
}

function costText(index: PriceIndex | null, value: number | null): string {
  if (!index || value === null || !Number.isFinite(value) || value <= 0) return "—";
  return formatCost(value);
}

function breakdownKeyName(key: BreakdownGroup["key"]): string {
  if (typeof key === "string") return key;
  return key.upstream_model_id || key.model_id || key.requested_model_id || key.key_name || "—";
}

function failureCodesFromRequests(items: RequestItem[]): Record<string, number> {
  const output: Record<string, number> = {};
  for (const item of items) {
    const code = item.status_code === null || item.status_code === undefined ? "none" : String(item.status_code);
    output[code] = (output[code] || 0) + Number(item.failures || 1);
  }
  return output;
}

function failureCodesOfCells(cells: StatusCell[]): Record<string, number> {
  const output: Record<string, number> = {};
  for (const cell of cells) {
    const codes = cell && typeof cell === "object" ? (cell as { status_codes?: Record<string, number> }).status_codes : null;
    if (!codes) continue;
    for (const [code, raw] of Object.entries(codes)) {
      // 状态码表混有成功码；这里只挑出非 2xx 的尝试，避免把成功请求算进失败原因。
      if (code === "none" || Number(code) >= 300) output[code] = (output[code] || 0) + Number(raw || 0);
    }
  }
  return output;
}

function requestRouteText(item: RequestItem): string {
  return [
    item.requested_model_id && item.requested_model_id !== item.model_id
      ? `${item.requested_model_id} → ${item.model_id}`
      : null,
    item.provider_id || null,
    item.key_name || null,
    item.upstream_model_id && item.upstream_model_id !== item.model_id ? item.upstream_model_id : null,
  ].filter(Boolean).join(" · ");
}

function requestRouteTitle(item: RequestItem): string {
  return [
    `请求模型 ${item.requested_model_id || "—"}`,
    `实际模型 ${item.model_id || "—"}`,
    `上游模型 ${item.upstream_model_id || "—"}`,
    `供应商 ${item.provider_id || "—"}`,
    `上游 Key ${item.key_name || "—"}`,
  ].join("\n");
}

function sourceText(item: RequestItem): string {
  return [callerLabel(item), item.workspace ? String(item.workspace) : null].filter(Boolean).join(" · ") || "—";
}

function requestCostTitle(item: RequestItem, index: PriceIndex | null): string {
  if (!index) return "价格目录尚未就绪";
  if (!item.upstream_model_id) return "该请求没有 upstream_model 归因，无法匹配价格";
  const price = lookupPrice(index, item.upstream_model_id);
  if (!price) return `${item.upstream_model_id} 未在 models.dev 目录中匹配到价格`;
  return `${item.upstream_model_id} · 输入 ${formatPrice(price.input)} / 输出 ${formatPrice(price.output)} USD per 1M token`;
}

function callerLabel(item: RequestItem): string {
  const tier = CALLER_LABELS[String(item.caller_type || "")] || String(item.caller_type || "");
  if (!item.access_key_id) return tier;
  return `${tier}「${item.access_key_name || item.access_key_id}」`;
}

function clientAddress(addr: unknown): string {
  if (!addr) return "—";
  const text = String(addr);
  const v6 = /^\[(.+)\]:\d+$/.exec(text);
  const v4 = /^(.+):\d+$/.exec(text);
  return v6?.[1] || v4?.[1] || text;
}

function shapeText(item: RequestItem): string {
  return [
    item.api_format ? (API_FORMAT_LABELS[String(item.api_format)] || String(item.api_format)) : null,
    item.reasoning_effort ? `思考 ${String(item.reasoning_effort)}` : null,
  ].filter(Boolean).join(" · ") || "—";
}

function shapeTitle(item: RequestItem): string {
  return [
    `流式 ${item.stream === null || item.stream === undefined ? "未记录" : item.stream ? "是" : "否"}`,
    `API 格式 ${item.api_format || "未记录"}`,
    `思考强度 ${item.reasoning_effort || "未设置"}`,
  ].join("\n");
}

function decodeSpeed(item: RequestItem): { tps: number; spanMS: number } | null {
  const output = Number(item.completion_tokens || 0);
  const duration = Number(item.duration_ms || 0);
  const decode = duration - Number(item.first_token_ms || 0);
  const spanMS = decode > 0 ? decode : duration;
  if (!(output > 0) || !(spanMS > 0)) return null;
  return { tps: Math.round((output * 1000) / spanMS * 10) / 10, spanMS };
}

function requestResultText(item: RequestItem): string {
  return `${item.success ? "成功" : "失败"} · HTTP ${item.status_code ?? "无响应"}${item.retried ? " · 已重试" : ""}`;
}

function sourceTitle(item: RequestItem): string {
  const accessKey = item.access_key_id
    ? item.access_key_name ? `${item.access_key_name}（${item.access_key_id}）` : String(item.access_key_id)
    : "未记录";
  return [
    `来源地址 ${item.client_addr || "未记录"}`,
    `工作空间 ${item.workspace || "未记录"}`,
    `调用方 ${CALLER_LABELS[String(item.caller_type || "")] || item.caller_type || "未记录"}`,
    `访问密钥 ${accessKey}`,
    `User-Agent ${item.user_agent || "未记录"}`,
    `请求 #${item.id}`,
  ].join("\n");
}

function cacheReadTokens(item: RequestItem): number | null | undefined {
  // OpenAI / CPA 的缓存命中量记在 cached_tokens，显式缓存读为正时仍优先。
  const explicit = item.cache_read_input_tokens as number | null | undefined;
  return explicit != null && explicit > 0 ? explicit : item.cached_tokens ?? explicit;
}

function tokenTitle(item: RequestItem): string {
  const speed = decodeSpeed(item);
  return [
    `输入 ${formatCount(item.prompt_tokens)}（未缓存 ${formatCount(item.uncached_prompt_tokens as number | undefined)}）`,
    `输出 ${formatCount(item.completion_tokens)}`,
    `缓存读 ${formatCount(cacheReadTokens(item))}`,
    `缓存写 ${formatCount(item.cache_creation_input_tokens as number | undefined)}`,
    `缓存合计 ${formatCount(item.cached_tokens)}`,
    `总计 ${formatCount(item.total_tokens)}`,
    speed
      ? `生成速度 ${speed.tps.toFixed(1)} tok/s = 输出 ÷ 生成阶段 ${formatDuration(speed.spanMS)}`
      : "生成速度 无读数（缺输出 Token 或耗时）",
  ].join("\n");
}

function requestCostText(index: PriceIndex | null, item: RequestItem): string {
  if (!index) return "—";
  const cost = requestCost(index, item);
  return costText(index, cost);
}

function RequestRow({
  item,
  fresh,
  pricingIndex,
  onOpen,
}: {
  item: RequestItem;
  fresh: boolean;
  pricingIndex: PriceIndex | null;
  onOpen: (item: RequestItem) => void;
}) {
  const speed = decodeSpeed(item);
  const tone = !item.success ? "critical" : item.retried ? "warn" : "";
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${requestResultText(item)}，${item.model_id || "未知模型"}，${formatDateTime(item.created_at)}`}
      onClick={() => onOpen(item)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onOpen(item);
        }
      }}
      className={cn(
        "stream-row grid min-w-[980px] cursor-pointer grid-cols-[3px_72px_minmax(180px,1.3fr)_minmax(110px,.8fr)_minmax(120px,.9fr)_minmax(190px,1.2fr)_minmax(160px,1fr)_76px] items-stretch gap-x-3 border-t border-rule px-1 text-[12px] outline-offset-[-2px] first:border-t-0 hover:bg-hover focus-visible:outline-2 focus-visible:outline-focus",
        fresh && "animate-[amkr-stream-in_150ms_ease-out]",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "my-1 w-[3px] rounded-pill",
          tone === "critical" ? "bg-critical" : tone === "warn" ? "bg-warn" : "bg-transparent",
        )}
      />
      <span className="self-center py-1.5 tabular-nums text-ink-3" title={item.created_at}>
        {formatClockSeconds(item.created_at)}
      </span>
      <div className="min-w-0 self-center py-1.5">
        <div className="truncate font-medium text-ink" title={requestRouteTitle(item)}>{item.model_id || "—"}</div>
        <div className="mt-0.5 truncate text-[11px] text-ink-3" title={requestRouteTitle(item)}>{requestRouteText(item) || "—"}</div>
      </div>
      <div className="min-w-0 self-center py-1.5" title={shapeTitle(item)}>
        <div className="text-ink-2">{item.stream === null || item.stream === undefined ? "—" : item.stream ? "流式" : "非流式"}</div>
        <div className="mt-0.5 truncate text-[11px] text-ink-3">{shapeText(item)}</div>
      </div>
      <div className="min-w-0 self-center py-1.5" title={sourceTitle(item)}>
        <div className="truncate text-ink-2">{sourceText(item)}</div>
        <div className="mt-0.5 truncate text-[11px] text-ink-3">{clientAddress(item.client_addr)}</div>
      </div>
      <div className="min-w-0 self-center py-1.5 text-[11px] leading-[1.45] text-ink-2" title={tokenTitle(item)}>
        {item.total_tokens ? (
          <>
            <div>输入 {formatCompact(item.prompt_tokens)} · 输出 {formatCompact(item.completion_tokens)}</div>
            <div className="text-ink-3">缓存读 {formatCompact(cacheReadTokens(item))} · 缓存写 {formatCompact(item.cache_creation_input_tokens as number | undefined)}</div>
            <div className="text-ink-3">合计 {formatCompact(item.total_tokens)} · {speed ? `${formatCompact(speed.tps)} tok/s` : "— tok/s"}</div>
          </>
        ) : <div className="text-ink-3">无 Token 读数</div>}
      </div>
      <div className="min-w-0 self-center py-1.5">
        <div className={cn("truncate text-[11px]", !item.success ? "text-critical-ink" : item.retried ? "text-warn-ink" : "text-ink-2")}>{requestResultText(item)}</div>
        <div className="mt-0.5 text-[11px] text-ink-3">{durationText(item.duration_ms)} · 首字 {durationText(item.first_token_ms)}</div>
      </div>
      <span className="self-center text-right text-[11px] tabular-nums text-ink-2" title={requestCostTitle(item, pricingIndex)}>
        {requestCostText(pricingIndex, item)}
      </span>
    </div>
  );
}

function DetailSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section>
      <h3 className="mb-2 text-[12px] font-semibold text-ink">{title}</h3>
      <dl className="grid grid-cols-[92px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[12px] leading-5">
        {children}
      </dl>
    </section>
  );
}

function DetailField({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <>
      <dt className="text-ink-3">{label}</dt>
      <dd className={cn("min-w-0 break-words text-ink-2", mono && "font-mono")}>{value || "—"}</dd>
    </>
  );
}

function RequestDetailSheet({
  item,
  onClose,
  pricingIndex,
}: {
  item: RequestItem | null;
  onClose: () => void;
  pricingIndex: PriceIndex | null;
}) {
  if (!item) return null;
  const speed = decodeSpeed(item);
  const price = lookupPrice(pricingIndex, item.upstream_model_id);
  return (
    <Sheet
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title="请求详情"
      subtitle={`#${item.id} · ${formatDateTime(item.created_at)}`}
      width="lg"
    >
      <div className="space-y-6">
        <DetailSection title="基本信息">
          <DetailField label="调用名" value={item.requested_model_id || item.model_id} mono />
          <DetailField label="路由模型" value={item.model_id} mono />
          <DetailField label="上游模型" value={item.upstream_model_id} mono />
          <DetailField label="供应商 / Key" value={`${item.provider_id || "—"} / ${item.key_name || "—"}`} mono />
          <DetailField label="调用方" value={callerLabel(item)} />
          <DetailField label="工作空间" value={item.workspace ? String(item.workspace) : "—"} />
          <DetailField label="来源" value={`${clientAddress(item.client_addr)}${item.user_agent ? ` · ${item.user_agent}` : ""}`} />
        </DetailSection>

        <DetailSection title="请求形态">
          <DetailField label="流式" value={item.stream === null || item.stream === undefined ? "未记录" : item.stream ? "是" : "否"} />
          <DetailField label="入站格式" value={shapeText(item)} />
          <DetailField label="思考强度" value={item.reasoning_effort ? String(item.reasoning_effort) : "未设置"} />
        </DetailSection>

        <DetailSection title="Token 与成本">
          <DetailField label="输入" value={formatCount(item.prompt_tokens)} />
          <DetailField label="输出" value={formatCount(item.completion_tokens)} />
          <DetailField label="缓存读 / 写" value={`${formatCount(cacheReadTokens(item))} / ${formatCount(item.cache_creation_input_tokens as number | undefined)}`} />
          <DetailField label="合计" value={formatCount(item.total_tokens)} />
          <DetailField label="生成速度" value={speed ? `${speed.tps.toFixed(1)} tok/s（生成阶段 ${formatDuration(speed.spanMS)}）` : "—"} />
          <DetailField label="估算成本" value={requestCostText(pricingIndex, item)} />
          {price ? <DetailField label="单价" value={`输入 ${formatPrice(price.input)} / 输出 ${formatPrice(price.output)} USD / 1M token`} /> : null}
        </DetailSection>

        <DetailSection title="结果">
          <DetailField label="结果" value={requestResultText(item)} />
          <DetailField label="耗时" value={durationText(item.duration_ms)} />
          <DetailField label="首字" value={durationText(item.first_token_ms)} />
          <DetailField label="重试" value={item.retried ? "是" : "否"} />
        </DetailSection>

        <section>
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-[12px] font-semibold text-ink">原始 JSON</h3>
            <CopyChip text={JSON.stringify(item, null, 2)} label="复制" />
          </div>
          <pre className="max-h-80 overflow-auto rounded-control border border-rule bg-sunken p-3 font-mono text-[11px] leading-relaxed text-ink-2">
            {JSON.stringify(item, null, 2)}
          </pre>
        </section>
      </div>
    </Sheet>
  );
}

function OverviewPage() {
  const { state, setDimension } = useMetricFilters("6h");
  const selectFilter = useSelectFilter();
  const filters = toApiFilters(state);
  const rangeQuery = rangeToQuery(state.range);
  const rangeHours = rangeQuery.hours ?? 8760;
  const bucketSeconds = pickBucketSeconds(rangeHours);
  const filtersActive = hasFilters(state);
  const [flowMetric, setFlowMetric] = useState<FlowMetric>("requests");
  const [streamFilter, setStreamFilter] = useState<StreamFilter>("all");
  const [streamPaused, setStreamPaused] = useState(false);
  const [detailItem, setDetailItem] = useState<RequestItem | null>(null);

  const mainOptions = { ...rangeQuery, bucketSeconds, limit: 0, ...filters };
  const streamOptions = { ...rangeQuery, bucketSeconds, limit: 40, ...filters };
  const sparkOptions = { hours: 1, bucketSeconds: 60, ...filters };
  const keyUsageOptions = { ...rangeQuery, ...filters };
  const costOptions = { ...rangeQuery, groupBy: "upstream_model" as const, rankBy: "requests" as const, limit: 50, ...filters };
  const statusOptions = { ...rangeQuery, groupBy: "provider_key" as const, ...filters };
  const failureOptions = { ...rangeQuery, bucketSeconds, limit: 200, success: false, ...filters };

  const mainQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(mainOptions),
    queryFn: () => api.filteredMetrics(mainOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
  });
  const streamQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(streamOptions),
    queryFn: () => api.filteredMetrics(streamOptions),
    placeholderData: keepPreviousData,
    refetchInterval: streamPaused ? false : 10000,
    refetchIntervalInBackground: false,
  });
  const sparkQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(sparkOptions),
    queryFn: () => api.filteredMetrics(sparkOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 60000,
    refetchIntervalInBackground: false,
  });
  const keyUsageQuery = useQuery({
    queryKey: queryKeys.keyUsage(keyUsageOptions),
    queryFn: () => api.keyUsage(keyUsageOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
  });
  const costQuery = useQuery({
    queryKey: queryKeys.usageBreakdown(costOptions),
    queryFn: () => api.usageBreakdown(costOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
  });
  const statusQuery = useQuery({
    queryKey: queryKeys.statusMonitor(statusOptions),
    queryFn: () => api.statusMonitor(statusOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
  });
  const failureQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(failureOptions),
    queryFn: () => api.filteredMetrics(failureOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    retry: false,
  });
  const pricingQuery = useQuery<PriceIndex | null>({
    queryKey: queryKeys.pricing(),
    queryFn: () => loadPricing(api.pricing),
    staleTime: 60 * 60 * 1000,
    retry: false,
  });

  const pricingIndex = pricingQuery.data ?? null;
  const snapshot = mainQuery.data?.snapshot;
  const total = snapshot?.total;
  const successRate = statsSuccessRate(total);
  const avgTtft = statsAvgTtft(total);
  const streamItems = streamQuery.data?.requests?.items ?? [];

  const sparkValues = useMemo(
    () => (sparkQuery.data?.series.points ?? []).map((point) => (typeof point.requests === "number" ? point.requests : null)),
    [sparkQuery.data],
  );
  const streamCounts = useMemo(() => ({
    all: streamItems.length,
    success: streamItems.filter((item) => item.success).length,
    failure: streamItems.filter((item) => !item.success).length,
    retry: streamItems.filter((item) => item.retried).length,
  }), [streamItems]);
  const visibleStream = useMemo(() => {
    if (streamFilter === "success") return streamItems.filter((item) => item.success);
    if (streamFilter === "failure") return streamItems.filter((item) => !item.success);
    if (streamFilter === "retry") return streamItems.filter((item) => item.retried);
    return streamItems;
  }, [streamItems, streamFilter]);

  const seenIds = useRef<Set<number>>(new Set());
  const [freshIds, setFreshIds] = useState<Set<number>>(new Set());
  useEffect(() => {
    const fresh = new Set<number>();
    for (const item of streamItems) {
      if (!seenIds.current.has(item.id)) fresh.add(item.id);
      seenIds.current.add(item.id);
    }
    if (!fresh.size) return;
    setFreshIds(fresh);
    const timer = window.setTimeout(() => setFreshIds(new Set()), 220);
    return () => window.clearTimeout(timer);
  }, [streamItems]);

  const flow = useMemo(() => {
    const links: FlowLink[] = [];
    const nested = (snapshot?.model_requested_models || {}) as Record<string, Record<string, MetricStats>>;
    for (const [routeModel, requested] of Object.entries(nested)) {
      for (const [requestedModel, stats] of Object.entries(requested || {})) {
        if (Number(stats?.requests || 0) <= 0) continue;
        links.push({
          source_layer: 0,
          target_layer: 1,
          source: requestedModel,
          target: routeModel,
          requests: Number(stats.requests || 0),
          total_tokens: Number(stats.total_tokens || 0),
        });
      }
    }
    for (const row of keyUsageQuery.data?.model_keys ?? []) {
      const requests = Number(row.stats?.requests || 0);
      if (requests <= 0) continue;
      links.push({
        source_layer: 1,
        target_layer: 2,
        source: row.model_id,
        target: `${row.provider_id} / ${row.key_name}`,
        requests,
        total_tokens: Number(row.stats?.total_tokens || 0),
      });
    }
    return links;
  }, [snapshot, keyUsageQuery.data]);

  const attention = useMemo(() => {
    const rows = statusQuery.data?.matrix.rows ?? [];
    const byChannel = new Map<string, RequestItem[]>();
    for (const item of failureQuery.data?.requests?.items ?? []) {
      const key = `${item.provider_id || "∅"}|${item.key_name || "∅"}`;
      const bucket = byChannel.get(key) || [];
      bucket.push(item);
      byChannel.set(key, bucket);
    }
    return rows.map((row) => {
      const aggregate = aggregateCells(row.cells);
      const band = healthBand(aggregate, "overall");
      const score = healthScore(aggregate, "overall");
      const label = rowLabel(row, "provider_key").label;
      const channelFailures = byChannel.get(`${row.provider_id || "∅"}|${row.key_name || "∅"}`) || [];
      const reasons = errorReasons(
        { failures: aggregate.failures },
        channelFailures.length ? failureCodesFromRequests(channelFailures) : failureCodesOfCells(row.cells as StatusCell[]),
      ).rows.filter((item) => item.count > 0).sort((a, b) => b.count - a.count);
      return { row, label, band, score, requests: aggregate.requests, failure: reasons[0] || null };
    })
      .filter((item) => item.band === "bad" || item.band === "warn" || (item.score !== null && item.score < 80))
      .sort((a, b) => (a.score ?? 101) - (b.score ?? 101) || b.requests - a.requests)
      .slice(0, 5);
  }, [statusQuery.data, failureQuery.data]);

  const topReasons = useMemo(() => {
    const data = statusQuery.data;
    if (!data) return [];
    return errorReasons(data.snapshot?.total, data.failure_status_codes)
      .rows.filter((row) => row.count > 0)
      .sort((a, b) => b.count - a.count)
      .slice(0, 3);
  }, [statusQuery.data]);

  const cost = useMemo(() => {
    const groups = costQuery.data?.groups ?? [];
    if (!pricingIndex || !groups.length) {
      return { total: null as number | null, priced: 0, totalCount: groups.length };
    }
    let totalCost = 0;
    let priced = 0;
    for (const group of groups) {
      const value = aggregateCost(pricingIndex, breakdownKeyName(group.key), group.stats);
      if (value === null) continue;
      priced += 1;
      totalCost += value;
    }
    return { total: totalCost > 0 ? totalCost : null, priced, totalCount: groups.length };
  }, [costQuery.data, pricingIndex]);

  const buckets: TimeBucket[] = useMemo(
    () => (mainQuery.data?.series.points ?? []).map((point) => ({ startedAt: point.started_at, complete: point.complete })),
    [mainQuery.data],
  );
  const outcomeSeries: ChartSeries[] = useMemo(() => [
    { id: "success", label: "成功", color: "var(--good)", values: (mainQuery.data?.series.points ?? []).map((point) => Number(point.successes || 0)) },
    { id: "failure", label: "失败", color: "var(--critical)", values: (mainQuery.data?.series.points ?? []).map((point) => Number(point.failures || 0)) },
  ], [mainQuery.data]);
  const latencySeries: ChartSeries[] = useMemo(() => [{
    id: "latency",
    label: "平均耗时",
    color: "var(--neutral)",
    values: (mainQuery.data?.series.points ?? []).map((point) => {
      const requests = Number(point.requests || 0);
      return requests > 0 ? Number(point.total_duration_ms || 0) / requests : null;
    }),
  }], [mainQuery.data]);

  function openAttention(item: (typeof attention)[number]) {
    const providerId = item.row.provider_id;
    const keyName = item.row.key_name;
    if (!providerId) return;
    const next = keyName
      ? { ...state, uk: [{ providerId, keyName }] }
      : { ...state, provider: [providerId], uk: [] };
    const params = writeFilters(new URLSearchParams(), next);
    navigate("status", params);
  }

  function handleNodeClick(node: { layer: number; name: string; value: number }) {
    if (node.name === "其他") return;
    if (node.layer === 1) {
      selectFilter("model", node.name);
      return;
    }
    if (node.layer === 2) {
      const [providerId, ...rest] = node.name.split(" / ");
      const keyName = rest.join(" / ");
      if (providerId && keyName) setDimension("uk", [{ providerId, keyName }]);
    }
  }

  return (
    <div className="mx-auto flex w-full max-w-[1560px] flex-col gap-4 pb-8">
      <FilterBar defaultRange="6h" />

      {mainQuery.isError ? (
        <Banner tone="critical" title="概览数据读取失败">{String((mainQuery.error as Error)?.message || mainQuery.error)}</Banner>
      ) : null}
      {statusQuery.isError ? <Banner tone="warn">渠道健康读数暂时不可用：{String((statusQuery.error as Error)?.message || statusQuery.error)}</Banner> : null}

      <MetricStrip>
        <MetricCell
          label="请求速率"
          value={snapshot ? formatCount(snapshot.current_rpm ?? 0) : "—"}
          unit="次/分"
          hero
          hint={`近 ${snapshot?.rate_window_seconds ?? 60} 秒 · 全实例`}
          chart={<Sparkline values={sparkValues} height={32} format={(value) => `${formatCompact(value)} 次`} ariaLabel="最近 60 分钟请求数" color="var(--series-1)" />}
        />
        <MetricCell
          label="Token 速率"
          value={snapshot ? formatCompact(snapshot.current_tpm ?? 0) : "—"}
          unit="Token/分"
          hint="近 60 秒 · 全实例"
        />
        <MetricCell
          label="进行中"
          value={snapshot ? formatCount(snapshot.active_requests ?? 0) : "—"}
          hint={filtersActive ? "全局在途数，不随筛选变化" : "全局在途数"}
        />
        <MetricCell
          label="成功率"
          value={successRate === null ? "—" : formatPercentValue(successRate)}
          tone={successRate === null ? undefined : successRate < 0.95 ? "critical" : successRate >= 0.99 ? "good" : undefined}
          hint={total ? `${formatCount(total.successes ?? 0)} 成功 · ${formatCount(total.failures ?? 0)} 失败 · ${rangeLabel(state.range)}` : `窗口 ${rangeLabel(state.range)}`}
        />
        <MetricCell
          label="平均首字"
          value={avgTtft === null ? "—" : formatDurationValue(avgTtft)}
          hint={`窗口 ${rangeLabel(state.range)}`}
        />
        <MetricCell
          label="估算成本"
          value={cost.total === null ? "—" : formatCost(cost.total)}
          hint={
            !pricingIndex
              ? "价格目录尚未就绪"
              : cost.totalCount
                ? `${cost.priced}/${cost.totalCount} 个上游模型有定价`
                : "窗口内没有上游模型用量"
          }
        />
      </MetricStrip>

      <div className="grid min-w-0 grid-cols-12 items-stretch gap-4">
        <Panel
          className="col-span-12 min-w-0 lg:col-span-8"
          fill
          title="路由流向"
          description="调用名 → 路由模型 → 上游 Key；线宽随窗口用量变化"
          actions={
            <Segmented
              options={[{ value: "requests", label: "请求" }, { value: "tokens", label: "Token" }]}
              value={flowMetric}
              onValueChange={(value) => setFlowMetric(value as FlowMetric)}
              aria-label="流向指标"
            />
          }
          busy={mainQuery.isFetching || keyUsageQuery.isFetching}
        >
          <FlowMap
            links={flow}
            layerLabels={["调用名", "路由模型", "上游 Key"]}
            metric={flowMetric}
            colorLayer={1}
            colorScope="model"
            embedded
            format={(value) => flowMetric === "tokens" ? `${formatCompact(value)} Token` : `${formatCompact(value)} 次`}
            onNodeClick={handleNodeClick}
            height={360}
            ariaLabel="调用名到路由模型到上游 Key 的路由流向"
            emptyText="当前筛选窗口内没有可归因的路由流量"
          />
          <p className="mt-auto pt-2 text-[11px] text-ink-3">点击路由模型可按模型筛选；点击上游 Key 可按上下游 Key 筛选。调用名层只用于定位，不写筛选。</p>
        </Panel>

        <Panel
          className="col-span-12 min-w-0 lg:col-span-4"
          fill
          title="需要关注"
          description={<span title="矩阵检查范围">{`按健康分升序 · ${rangeLabel(state.range)}`}</span>}
          actions={<Badge tone={attention.length ? "warn" : "good"}>{attention.length ? `${attention.length} 个渠道` : "健康"}</Badge>}
          busy={statusQuery.isFetching}
          footer={
            <div className="space-y-2">
              <p className="text-[11px] leading-5 text-ink-3">
                健康分综合成功率、首字延迟与缓存命中；分数越低越需要优先处理。
              </p>
              <button
                type="button"
                className="inline-flex items-center gap-1 rounded-control text-[12px] text-ink-2 transition-colors hover:text-ink"
                onClick={() => navigate("/status")}
              >
                打开状态监控查看全部渠道
                <ChevronRight aria-hidden className="size-3.5" />
              </button>
            </div>
          }
        >
          {statusQuery.isError ? (
            <Empty title="渠道健康读数不可用" description="状态监控接口暂时没有返回数据。" />
          ) : attention.length ? (
            <div className="space-y-1.5">
              {attention.map((item) => (
                <button
                  key={`${item.row.provider_id}-${item.row.key_name}`}
                  type="button"
                  onClick={() => openAttention(item)}
                  className="flex w-full items-start gap-2 rounded-control px-2 py-2 text-left transition-colors hover:bg-hover"
                  title={`${item.label}\n健康分 ${item.score ?? "—"}\n${item.failure?.label || "失败原因未细分"}`}
                >
                  <StatusDot tone={item.band === "bad" ? "critical" : "warn"} label="" aria-label={`${item.label} ${item.band === "bad" ? "异常" : "波动"}`} />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center justify-between gap-2">
                      <span className="truncate text-[12px] text-ink">{item.label}</span>
                      <span className="shrink-0 text-[11px] tabular-nums text-ink-3">{item.score === null ? "样本不足" : `健康分 ${item.score}`}</span>
                    </span>
                    <span className="mt-0.5 flex items-center justify-between gap-2 text-[11px] text-ink-3">
                      <span className="truncate">{item.failure ? item.failure.label : "暂无失败原因"} · {formatCompact(item.requests)} 次</span>
                      <span className="shrink-0">{item.band === "bad" ? "异常" : "波动"}</span>
                    </span>
                  </span>
                  <ChevronRight aria-hidden className="mt-0.5 size-3.5 shrink-0 text-ink-3" />
                </button>
              ))}
            </div>
          ) : (
            <Empty
              icon={<CircleAlert aria-hidden />}
              title="所有渠道健康"
              description={`已检查 ${statusQuery.data?.matrix.rows.length ?? 0} 个渠道 · ${rangeLabel(state.range)} 内没有异常或波动。`}
            />
          )}
          {topReasons.length ? (
            <div className="mt-3 border-t border-rule pt-3">
              <div className="mb-1.5 text-[11px] text-ink-3">失败原因前三</div>
              <div className="space-y-1">
                {topReasons.map((reason) => (
                  <div key={reason.id} className="flex items-center justify-between gap-2 text-[11px]" title={reason.hint}>
                    <span className="inline-flex min-w-0 items-center gap-1.5 text-ink-2">
                      <StatusDot tone={reason.tone === "bad" ? "critical" : reason.tone === "warn" ? "warn" : "neutral"} label="" aria-label={reason.label} />
                      <span className="truncate">{reason.label}</span>
                    </span>
                    <span className="shrink-0 tabular-nums text-ink-3">{formatCount(reason.count)}</span>
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </Panel>
      </div>

      <Panel
        title="流量与结果"
        description={`${rangeLabel(state.range)} · 按时间桶堆叠成功与失败`}
        actions={<Badge tone="neutral">{`${formatCompactNumber(buckets.length)} 个桶`}</Badge>}
        busy={mainQuery.isFetching}
      >
        <StackedColumns
          buckets={buckets}
          series={outcomeSeries}
          format={(value) => `${formatCompact(value)} 次`}
          height={230}
          embedded
          ariaLabel="成功与失败请求随时间"
          tableUnit="次"
          emptyText="当前窗口内没有请求记录"
        />
        <div className="mt-3 border-t border-rule pt-2">
          <div className="mb-1 flex items-center gap-2 text-[11px] text-ink-3">
            <span>平均耗时</span>
            <span className="h-px flex-1 bg-rule" />
          </div>
          <TimeSeriesChart
            buckets={buckets}
            series={latencySeries}
            format={(value) => formatDurationValue(value)}
            height={105}
            showLegend={false}
            embedded
            ariaLabel="平均耗时随时间"
            tableUnit="ms"
            emptyText="当前窗口内没有耗时读数"
          />
        </div>
      </Panel>

      <Panel
        title="实时请求流"
        description="最近 40 条请求；悬停列表暂停自动刷新"
        actions={
          <div className="flex items-center gap-2">
            {streamPaused ? <Badge tone="warn">已暂停自动刷新</Badge> : <Badge tone="neutral">每 10 秒</Badge>}
            <Button
              variant="ghost"
              size="sm"
              leadingIcon={<RefreshCw aria-hidden className="size-3.5" />}
              onClick={() => { void streamQuery.refetch(); }}
            >
              刷新
            </Button>
          </div>
        }
        busy={streamQuery.isFetching && Boolean(streamQuery.data)}
        padding={false}
      >
        <div className="px-4 pt-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Segmented
              options={[
                { value: "all", label: `全部 ${streamCounts.all}` },
                { value: "success", label: `成功 ${streamCounts.success}` },
                { value: "failure", label: `失败 ${streamCounts.failure}` },
                { value: "retry", label: `重试 ${streamCounts.retry}` },
              ]}
              value={streamFilter}
              onValueChange={(value) => setStreamFilter(value as StreamFilter)}
              aria-label="按结果过滤请求流"
            />
            <span className="text-[11px] text-ink-3">点击任意行查看请求详情</span>
          </div>
        </div>
        <div
          className="mt-2 max-h-[520px] overflow-auto"
          onMouseEnter={() => setStreamPaused(true)}
          onMouseLeave={() => setStreamPaused(false)}
        >
          {streamQuery.isError ? (
            <div className="p-4"><Banner tone="critical" title="请求流读取失败">{String((streamQuery.error as Error)?.message || streamQuery.error)}</Banner></div>
          ) : visibleStream.length ? (
            <div role="list" aria-label="实时请求流">
              {visibleStream.map((item) => (
                <RequestRow
                  key={item.id}
                  item={item}
                  fresh={freshIds.has(item.id)}
                  pricingIndex={pricingIndex}
                  onOpen={setDetailItem}
                />
              ))}
            </div>
          ) : (
            <Empty title="该条件下暂无请求记录" description="窗口内没有请求时，这里会是空的。" />
          )}
        </div>
      </Panel>

      <RequestDetailSheet item={detailItem} onClose={() => setDetailItem(null)} pricingIndex={pricingIndex} />
    </div>
  );
}

export default OverviewPage;
