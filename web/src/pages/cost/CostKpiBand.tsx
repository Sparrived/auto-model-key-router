// 成本页的四个金额读数。全部来自同一份 costParts 合计，避免「总额」与「构成」
// 两处各算一次后在屏幕上互相矛盾。

import { formatPercentValue } from "@/lib/chart-math";
import { MetricCell } from "@/components/ui/MetricStrip";
import type { CostSummary } from "./cost-model";
import { money } from "./cost-model";

export function CostKpiBand({ summary, loading }: { summary: CostSummary; loading: boolean }) {
  const daily = summary.totalCost / Math.max(1, summary.dayCount);
  const average = summary.pricedRequests > 0 ? summary.totalCost / summary.pricedRequests : null;
  const coverage = summary.coverage === null ? "—" : formatPercentValue(summary.coverage);

  return (
    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-panel border border-rule bg-rule md:grid-cols-4">
      <MetricCell
        className="min-w-0 bg-panel"
        label="估算成本（窗口）"
        value={loading ? "—" : money(summary.totalCost)}
        hint={`${summary.pricedRows.length} 个已计价上游模型`}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="日均成本"
        value={loading ? "—" : money(daily)}
        hint={`按 ${summary.dayCount} 天折算`}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="单次请求平均成本"
        value={loading ? "—" : money(average)}
        hint={`已计价请求 ${summary.pricedRequests.toLocaleString("zh-CN")} 次`}
      />
      <MetricCell
        className="min-w-0 bg-panel"
        label="计价覆盖率"
        value={loading ? "—" : coverage}
        hint={`${summary.pricedModelCount}/${summary.modelCount} 个上游模型有定价`}
      />
    </div>
  );
}
