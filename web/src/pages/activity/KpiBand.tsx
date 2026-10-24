// 用量页顶部的六个读数。
//
// Token 一格下面必须同时给出「输入（未缓存）/ 缓存读 / 输出」的构成：只看一个
// total_tokens 会把缓存命中带来的成本差异完全藏起来。这里用与图表同一套系列色，
// 但压缩成单元格高度，避免仪表带变成第七张图。

import type { MetricStats } from "@/lib/types";
import type { DailyUsage } from "@/lib/chart-math";
import { formatCompactNumber, formatPercentValue } from "@/lib/chart-math";
import { formatCount, formatDuration, formatPercent } from "@/lib/format";
import { useSeriesColors } from "@/lib/series-colors";
import { MetricCell } from "@/components/ui/MetricStrip";

function count(stats: MetricStats | undefined, field: keyof MetricStats): number {
  const value = Number(stats?.[field]);
  return Number.isFinite(value) ? value : 0;
}

function dash(value: string): string {
  return value === "-" || value === "" ? "—" : value;
}

export interface KpiBandProps {
  totals: MetricStats | undefined;
  days: DailyUsage[];
  windowDays: number;
  loading: boolean;
}

export function KpiBand({ totals, days, windowDays, loading }: KpiBandProps) {
  const requests = count(totals, "requests");
  const tokens = count(totals, "total_tokens");
  const prompt = count(totals, "prompt_tokens");
  const completion = count(totals, "completion_tokens");
  const cacheRead = count(totals, "cache_read_tokens") || count(totals, "cached_tokens");
  const uncached = Math.max(0, prompt - cacheRead);
  const success = dash(formatPercent(count(totals, "successes"), requests));
  const dailyAverage = requests / Math.max(1, windowDays);
  const peak = days.reduce<DailyUsage | null>(
    (best, day) => (!best || day.requests > best.requests ? day : best),
    null,
  );
  const duration = requests > 0 ? count(totals, "total_duration_ms") / requests : null;
  const firstToken = requests > 0 ? count(totals, "total_first_token_ms") / requests : null;

  return (
    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-panel border border-rule bg-rule md:grid-cols-3 xl:grid-cols-6">
      <MetricCell
        className="min-w-0 bg-panel"
        label="请求数"
        value={loading ? "—" : formatCount(requests)}
        unit="次"
        hint="按上游尝试计数"
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="Token"
        value={loading ? "—" : formatCompactNumber(tokens)}
        unit="Token"
        hint="输入 + 输出"
        chart={<TokenComposition uncached={uncached} cacheRead={cacheRead} output={completion} empty={tokens <= 0} />}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="成功率"
        value={loading ? "—" : success}
        hint={`${formatCount(count(totals, "successes"))} 成功 / ${formatCount(count(totals, "failures"))} 失败`}
        tone={requests > 0 && count(totals, "failures") / requests > 0.1 ? "warn" : undefined}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="日均请求"
        value={loading ? "—" : formatCompactNumber(dailyAverage)}
        unit="次"
        hint={peak
          ? `峰值 ${peak.date.slice(5)} · ${formatCompactNumber(peak.requests)} 次`
          : "窗口内还没有请求"}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="平均耗时"
        value={loading || duration === null ? "—" : formatDuration(duration)}
        hint="按请求数加权"
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="平均首字"
        value={loading || firstToken === null ? "—" : formatDuration(firstToken)}
        hint="按请求数加权"
      />
    </div>
  );
}

function TokenComposition({
  uncached,
  cacheRead,
  output,
  empty,
}: {
  uncached: number;
  cacheRead: number;
  output: number;
  empty: boolean;
}) {
  const colorOf = useSeriesColors("activity-token-parts", ["input", "cache", "output"]);
  const total = uncached + cacheRead + output;
  const parts = [
    { id: "input", label: "输入（未缓存）", value: uncached, color: colorOf("input") },
    { id: "cache", label: "缓存读", value: cacheRead, color: colorOf("cache") },
    { id: "output", label: "输出", value: output, color: colorOf("output") },
  ];
  return (
    <div aria-label="Token 构成" className="min-w-0">
      <div className="flex h-1.5 w-full overflow-hidden rounded-full bg-sunken" aria-hidden>
        {!empty && total > 0
          ? parts.map((part) => (
            <span
              key={part.id}
              className="block h-full"
              style={{ width: `${(part.value / total) * 100}%`, backgroundColor: part.color }}
              title={`${part.label} ${formatCompactNumber(part.value)} Token`}
            />
          ))
          : null}
      </div>
      <div className="mt-1 space-y-0.5 text-[10px] leading-3 text-ink-3">
        {parts.map((part) => (
          <div key={part.id} className="flex items-center justify-between gap-2">
            <span className="flex min-w-0 items-center gap-1">
              <span aria-hidden className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: part.color }} />
              <span className="whitespace-nowrap">{part.label}</span>
            </span>
            <span className="shrink-0 tabular-nums">
              {empty ? "—" : (
                <>
                  <span className="sm:hidden">{formatPercentValue(part.value / total)}</span>
                  <span className="hidden sm:inline">
                    {formatCompactNumber(part.value)} · {formatPercentValue(part.value / total)}
                  </span>
                </>
              )}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}
