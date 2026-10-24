// 用量页的次级面板：累计、节律、按天/小时、性能。
//
// 它们都只读同一份 filteredMetrics 的 series，不再各自发一条统计请求；唯一的例外是
// 「7 天内但当前桶宽大于 1 小时」的节律图：热力格要求小时格，所以单独按 3600 秒取一次。

import { useMemo, useState } from "react";
import { Badge } from "@/components/ui/Badge";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Heatmap, type HeatmapDay } from "@/components/charts/Heatmap";
import { SmallMultiples } from "@/components/charts/SmallMultiples";
import { StackedColumns } from "@/components/charts/StackedColumns";
import { TimeSeriesChart } from "@/components/charts/TimeSeriesChart";
import type { ChartSeries, TimeBucket } from "@/components/charts/chart-utils";
import {
  beijingDate,
  beijingSlot,
  bucketLabel,
  dailyUsage,
  formatCompactNumber,
  formatDurationValue,
} from "@/lib/chart-math";
import type { MetricSeriesPoint } from "@/lib/types";
import { formatCount } from "@/lib/format";
import { useSeriesColors } from "@/lib/series-colors";
import type { ReactNode } from "react";

function number(value: unknown): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function CumulativePanel({
  points,
  totalRequests,
  totalTokens,
  busy,
  error,
  windowLabel,
}: {
  points: MetricSeriesPoint[];
  totalRequests?: number;
  totalTokens?: number;
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}) {
  const [metric, setMetric] = useState<"requests" | "tokens">("requests");
  const colorOf = useSeriesColors("activity-cumulative", ["requests", "tokens"]);
  const values = points.map((point) => (
    metric === "requests" ? number(point.requests) : number(point.total_tokens)
  ));
  const series: ChartSeries[] = [{
    id: metric,
    label: metric === "requests" ? "累计请求" : "累计 Token",
    color: colorOf(metric),
    values,
  }];
  const bucketTotal = values.reduce((sum, value) => sum + value, 0);
  // 服务端的序列会包含窗口起点所在的整桶；快照只截取窗口内的部分。徽标用快照
  // 口径，累积曲线仍按桶画，避免两个数字在同一屏上相差一个桶边界。
  const total = metric === "requests" ? (totalRequests ?? bucketTotal) : (totalTokens ?? bucketTotal);
  const buckets: TimeBucket[] = points.map((point) => ({
    startedAt: point.started_at,
    complete: point.complete !== false,
  }));

  return (
    <Panel
      title="累计用量"
      description={`从${windowLabel}起点累加，回答总量是怎么堆起来的。`}
      busy={busy}
      toolbar={(
        <div className="flex flex-wrap items-center gap-2">
          <Segmented
            aria-label="累计指标"
            value={metric}
            onValueChange={(value) => setMetric(value as "requests" | "tokens")}
            options={[
              { value: "requests", label: "请求" },
              { value: "tokens", label: "Token" },
            ]}
          />
          <Badge tone="neutral">
            {metric === "requests" ? `${formatCount(total)} 次` : `${formatCompactNumber(total)} Token`}
          </Badge>
        </div>
      )}
      footer="按桶内读数逐段累加；最后一个桶未闭合时仍会标「累加中」，不会把半天读成掉头向下。"
    >
      <TimeSeriesChart
        buckets={buckets}
        series={series}
        format={formatCompactNumber}
        height={236}
        variant="area"
        cumulative
        ariaLabel={`累计${metric === "requests" ? "请求" : "Token"}用量`}
        tableUnit={metric === "requests" ? "次" : "Token"}
        busy={busy}
        error={error}
        emptyText="当前筛选下没有累计用量"
        embedded
      />
    </Panel>
  );
}

export function RhythmPanel({
  points,
  bucketSeconds,
  mode,
  from,
  to,
  busy,
  error,
  windowLabel,
}: {
  points: MetricSeriesPoint[];
  bucketSeconds: number;
  mode: "week" | "calendar";
  from?: string;
  to?: string;
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}) {
  const [metric, setMetric] = useState<"requests" | "tokens">("requests");
  const format = (value: number) => (
    metric === "requests" ? `${formatCompactNumber(value)} 次` : `${formatCompactNumber(value)} Token`
  );
  const value = (point: { requests?: number; total_tokens?: number }) => (
    metric === "requests" ? number(point.requests) : number(point.total_tokens)
  );
  const days: HeatmapDay[] = useMemo(() => dailyUsage(points).map((day) => ({
    date: day.date,
    value: metric === "requests" ? day.requests : day.total_tokens,
    buckets: day.buckets,
    partial: day.partial,
    covered: true,
  })), [points, metric]);
  const coarse = bucketSeconds > 3600;

  return (
    <Panel
      title="用量节律"
      description={mode === "week"
        ? `按星期 × 小时看${windowLabel}的使用习惯。`
        : `窗口超过 30 天，改用日历日看${windowLabel}的用量起伏。`}
      busy={busy}
      toolbar={(
        <Segmented
          aria-label="节律指标"
          value={metric}
          onValueChange={(next) => setMetric(next as "requests" | "tokens")}
          options={[
            { value: "requests", label: "请求" },
            { value: "tokens", label: "Token" },
          ]}
        />
      )}
      footer={mode === "week" && coarse
        ? `当前桶宽 ${bucketLabel(bucketSeconds)}，热力格按桶起点归入；窗口太宽时无法在不超点数上限的前提下拆到 1 小时。`
        : "热力格里的数值是落在该格内的桶读数之和，不是速率；未覆盖的格子用斜纹表示「没有采样」，与真实的 0 分开。"}
    >
      <Heatmap
        mode={mode}
        points={mode === "week" ? points : undefined}
        days={mode === "calendar" ? days : undefined}
        value={value}
        format={format}
        from={from}
        to={to}
        height={mode === "week" ? 292 : 258}
        unit={metric === "requests" ? "次" : "Token"}
        ariaLabel={`用量节律（${metric === "requests" ? "请求" : "Token"}）`}
        busy={busy}
        error={error}
        emptyText="当前筛选下没有节律数据"
        embedded
      />
    </Panel>
  );
}

export function DailyUsagePanel({
  points,
  hours,
  bucketSeconds,
  busy,
  error,
  windowLabel,
}: {
  points: MetricSeriesPoint[];
  hours: number | null;
  bucketSeconds: number;
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}) {
  const daily = hours === null || hours >= 72;
  const colorOf = useSeriesColors("activity-token-composition", ["input", "output"]);
  const days = useMemo(() => dailyUsage(points), [points]);
  const { buckets, input, output } = useMemo(() => {
    if (daily) {
      const dailyBuckets: TimeBucket[] = days.map((day) => ({
        startedAt: `${day.date}T00:00:00+08:00`,
        complete: !day.partial,
      }));
      return {
        buckets: dailyBuckets,
        input: days.map((day) => day.prompt_tokens),
        output: days.map((day) => day.completion_tokens),
      };
    }
    const byHour = new Map<string, { startedAt: string; input: number; output: number; complete: boolean }>();
    for (const point of points) {
      const date = beijingDate(point.started_at);
      const slot = beijingSlot(point.started_at);
      if (!date || !slot) continue;
      const hour = String(Math.floor(slot.slot / 2)).padStart(2, "0");
      const key = `${date}T${hour}:00:00+08:00`;
      const current = byHour.get(key) ?? { startedAt: key, input: 0, output: 0, complete: true };
      current.input += number(point.prompt_tokens);
      current.output += number(point.completion_tokens);
      if (point.complete === false) current.complete = false;
      byHour.set(key, current);
    }
    const rows = [...byHour.values()].sort((a, b) => a.startedAt.localeCompare(b.startedAt));
    return {
      buckets: rows.map((row) => ({ startedAt: row.startedAt, complete: row.complete })),
      input: rows.map((row) => row.input),
      output: rows.map((row) => row.output),
    };
  }, [daily, days, points]);

  const series: ChartSeries[] = [
    { id: "input", label: "输入 Token", color: colorOf("input"), values: input },
    { id: "output", label: "输出 Token", color: colorOf("output"), values: output },
  ];
  const lastPartial = buckets.length > 0 && !buckets[buckets.length - 1].complete;
  const title = daily ? "按天用量" : "按小时用量";

  return (
    <Panel
      title={title}
      description={daily
        ? `每个北京日历日一根柱，拆成输入与输出；当前窗口 ${windowLabel}。`
        : `窗口不足 3 天，改按小时看；每根柱是一个北京小时，由 ${bucketLabel(bucketSeconds)} 桶聚合。`}
      busy={busy}
      toolbar={(
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="neutral">{daily ? "输入 / 输出" : "每小时"}</Badge>
          {lastPartial ? <Badge tone="warn">尾桶累加中</Badge> : null}
        </div>
      )}
      footer={lastPartial
        ? "最后一根柱仍在累加（斜纹）；它不代表用量下降。"
        : "输入/输出按桶内的 prompt/completion token 统计，缓存命中已经包含在输入里。"}
    >
      <StackedColumns
        buckets={buckets}
        series={series}
        format={(value) => `${formatCompactNumber(value)} Token`}
        height={248}
        ariaLabel={`${title}（输入与输出 Token）`}
        tableUnit="Token"
        busy={busy}
        error={error}
        emptyText="当前筛选下没有 Token 用量"
        embedded
      />
    </Panel>
  );
}

export function PerformancePanel({
  points,
  busy,
  error,
  windowLabel,
}: {
  points: MetricSeriesPoint[];
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}) {
  const colorOf = useSeriesColors("activity-performance", ["duration", "first-token"]);
  const duration = points.map((point) => {
    const requests = number(point.requests);
    return requests > 0 ? number(point.total_duration_ms) / requests : null;
  });
  const firstToken = points.map((point) => {
    const requests = number(point.requests);
    return requests > 0 ? number(point.total_first_token_ms) / requests : null;
  });
  const buckets: TimeBucket[] = points.map((point) => ({
    startedAt: point.started_at,
    complete: point.complete !== false,
  }));
  const latestDuration = [...duration].reverse().find((value) => value !== null);
  const latestFirst = [...firstToken].reverse().find((value) => value !== null);

  return (
    <Panel
      title="性能趋势"
      description={`${windowLabel}内两项延迟的走势；各自独立纵轴，避免数量级差异把首字曲线压平。`}
      busy={busy}
      toolbar={(
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge tone="neutral">平均耗时 {latestDuration === undefined ? "—" : formatDurationValue(latestDuration)}</Badge>
          <Badge tone="neutral">平均首字 {latestFirst === undefined ? "—" : formatDurationValue(latestFirst)}</Badge>
        </div>
      )}
      footer="两项都按桶内请求数加权，而不是对桶平均值再取平均；无请求的桶没有均值，跨过它的虚线只表示走势延续。"
    >
      {points.length ? (
        <SmallMultiples
          buckets={buckets}
          panels={[
            {
              id: "duration",
              label: "平均耗时",
              series: [{ id: "duration", label: "平均耗时", color: colorOf("duration"), values: duration }],
              format: formatDurationValue,
              tableUnit: "毫秒",
            },
            {
              id: "first-token",
              label: "平均首字",
              series: [{ id: "first-token", label: "平均首字", color: colorOf("first-token"), values: firstToken }],
              format: formatDurationValue,
              tableUnit: "毫秒",
            },
          ]}
          format={formatDurationValue}
          height={106}
          ariaLabel="性能趋势"
          embedded
        />
      ) : (
        <div className="py-12 text-center text-meta text-ink-3">
          {error ?? "当前筛选下没有性能数据"}
        </div>
      )}
    </Panel>
  );
}

