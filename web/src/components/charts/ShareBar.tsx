// 构成条：一条 100% 宽的比例尺，用颜色段表达各系列占比。
//
// 段内文字只在宽度足够时出现；否则保留下面的图例与表格，不把文字挤成竖排。

import { useMemo, useState, type PointerEvent, type ReactNode } from "react";
import { formatPercentValue } from "@/lib/chart-math";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { Legend } from "./Legend";
import { ChartTable } from "./chart-utils";
import { seriesColor } from "@/lib/series-colors";
import { useChartSize } from "./useChartSize";

export interface ShareBarItem {
  id: string;
  label: string;
  value: number;
  color?: string;
}

export interface ShareBarProps {
  items: ShareBarItem[];
  format: (value: number) => string;
  title?: string;
  total?: number;
  ariaLabel?: string;
  unit?: string;
  busy?: boolean;
  error?: ReactNode;
  emptyText?: string;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

export function ShareBar({
  items,
  format,
  title = "构成",
  total,
  ariaLabel = "占比构成",
  unit,
  busy = false,
  error,
  emptyText = "当前时间窗口内没有构成数据",
  embedded = false,
  className,
}: ShareBarProps) {
  const { ref, width } = useChartSize();
  const [hovered, setHovered] = useState<{ id: string; x: number } | null>(null);
  const totalValue = total ?? items.reduce((sum, item) => sum + item.value, 0);
  const colors = useMemo(() => {
    const map = new Map<string, string>();
    items.forEach((item) => map.set(item.id, item.color ?? seriesColor("share-bar", item.id)));
    return map;
  }, [items]);
  const visible = items.filter((item) => item.value > 0);
  const hoveredItem = visible.find((item) => item.id === hovered?.id) ?? null;

  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      title={title}
      busy={busy}
      error={error}
      empty={!visible.length}
      emptyText={emptyText}
      legend={<Legend items={items.map((item) => ({ id: item.id, label: item.label, color: colors.get(item.id), marker: "square" }))} ariaLabel={`${ariaLabel}图例`} />}
      table={<ShareTable items={items} format={format} total={totalValue} unit={unit} />}
      embedded={embedded}
    >
      <div ref={ref} className="relative pt-1">
        <div
          className="flex h-7 w-full overflow-hidden rounded-control bg-sunken"
          role="img"
          aria-label={`${ariaLabel}；总计 ${format(totalValue)}${unit ?? ""}`}
          onPointerLeave={() => setHovered(null)}
        >
          {visible.map((item) => {
            const share = totalValue > 0 ? item.value / totalValue : 0;
            return (
              <div
                key={item.id}
                className="flex h-full min-w-0 items-center justify-center overflow-hidden text-[10px] font-medium text-white/90 transition-opacity"
                style={{
                  width: `${share * 100}%`,
                  backgroundColor: colors.get(item.id),
                  opacity: hovered && hovered.id !== item.id ? 0.45 : 1,
                }}
                onPointerMove={(event: PointerEvent<HTMLDivElement>) => {
                  const parent = ref.current?.getBoundingClientRect();
                  if (!parent) return;
                  setHovered({ id: item.id, x: event.clientX - parent.left });
                }}
                title={`${item.label} ${format(item.value)} · ${formatPercentValue(share)}`}
              >
                {share >= 0.14 ? (
                  <span className="rounded-[2px] bg-black/20 px-1 py-0.5 text-white [text-shadow:0_1px_1px_rgba(0,0,0,0.5)]">
                    {Math.round(share * 100)}%
                  </span>
                ) : null}
              </div>
            );
          })}
        </div>
        {hoveredItem ? (
          <ChartTooltip
            open
            x={hovered?.x ?? width / 2}
            y={-8}
            width={width}
            title={hoveredItem.label}
            rows={[{ id: hoveredItem.id, label: "数值", value: format(hoveredItem.value), color: colors.get(hoveredItem.id) }, { label: "占比", value: totalValue > 0 ? formatPercentValue(hoveredItem.value / totalValue) : "—" }]}
          />
        ) : null}
      </div>
    </ChartFrame>
  );
}

function ShareTable({ items, format, total, unit }: { items: ShareBarItem[]; format: (value: number) => string; total: number; unit?: string }) {
  return (
    <ChartTable
      caption="构成数据表"
      head={
        <tr>
          <th className="px-2 py-1.5 font-medium">系列</th>
          <th className="px-2 py-1.5 text-right font-medium">值{unit ? `（${unit}）` : ""}</th>
          <th className="px-2 py-1.5 text-right font-medium">占比</th>
        </tr>
      }
    >
      {items.map((item) => (
        <tr key={item.id} className="border-t border-rule">
          <td className="px-2 py-1 text-ink-2">{item.label}</td>
          <td className="px-2 py-1 text-right tabular-nums">{format(item.value)}</td>
          <td className="px-2 py-1 text-right tabular-nums text-ink-3">{total > 0 ? formatPercentValue(item.value / total) : "—"}</td>
        </tr>
      ))}
    </ChartTable>
  );
}
