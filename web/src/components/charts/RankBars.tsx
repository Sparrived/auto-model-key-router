// 横向排行：主指标按最大值归一化，右侧给出占比；行本身可点击，悬停出现
// 「仅看此项」。排行不是图表装饰，长名称也必须在行内可读，所以名称超长时
// 用 title 保留全文，而不是省略到只剩前缀。

import { useId, type ReactNode } from "react";
import { ArrowUpRight, Filter } from "lucide-react";
import { formatPercentValue } from "@/lib/chart-math";
import { ChartFrame } from "./ChartFrame";
import { ChartTable } from "./chart-utils";
import { OTHER_SERIES_COLOR, useSeriesColors } from "@/lib/series-colors";

export interface RankBarItem {
  id: string;
  label: ReactNode;
  /** 纯文本名称，用于 title / 表格 / aria；label 可以是带标记的节点。 */
  name?: string;
  value: number;
  color?: string;
  /** 聚合出来的「其他」项使用中性灰，且不占用前 8 个分类色槽位。 */
  isOther?: boolean;
  hint?: ReactNode;
}

export interface RankBarsProps {
  items: RankBarItem[];
  format: (value: number) => string;
  title?: string;
  total?: number;
  max?: number;
  height?: number;
  ariaLabel?: string;
  unit?: string;
  busy?: boolean;
  error?: ReactNode;
  onSelect?: (item: RankBarItem) => void;
  onRowClick?: (item: RankBarItem) => void;
  emptyText?: string;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  /**
   * 取色作用域。同一个排行榜里的实体应在同一作用域，不同排行榜不能共用作用域；
   * 否则一个长排行会先占满 8 个槽位，后面的排行只能拿到灰色。
   *
   * 不传时按组件实例生成独立作用域，避免默认调用方意外互相挤占。
   */
  colorScope?: string;
  className?: string;
}

export function RankBars({
  items,
  format,
  title = "排行",
  total,
  max,
  ariaLabel = "排行",
  unit,
  busy = false,
  error,
  onSelect,
  onRowClick,
  emptyText = "当前时间窗口内没有排行数据",
  embedded = false,
  colorScope,
  className,
}: RankBarsProps) {
  const instanceScope = useId();
  const colorOf = useSeriesColors(
    colorScope ?? `rank-bars:${instanceScope}`,
    items.filter((item) => !item.color && !item.isOther).map((item) => item.id),
  );
  const peak = max ?? Math.max(0, ...items.map((item) => item.value));
  const sum = total ?? items.reduce((value, item) => value + item.value, 0);
  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      title={title}
      busy={busy}
      error={error}
      empty={!items.length}
      emptyText={emptyText}
      table={<RankTable items={items} format={format} total={sum} unit={unit} />}
      embedded={embedded}
    >
      <ol className="space-y-1" aria-label={ariaLabel}>
        {items.map((item, index) => {
          const width = peak > 0 ? Math.max(0, Math.min(100, (item.value / peak) * 100)) : 0;
          const share = sum > 0 ? item.value / sum : null;
          const color = item.color ?? (item.isOther ? OTHER_SERIES_COLOR : colorOf(item.id));
          return (
            <li key={item.id}>
              <div
                role={onRowClick ? "button" : undefined}
                tabIndex={onRowClick ? 0 : undefined}
                onClick={() => onRowClick?.(item)}
                onKeyDown={(event) => {
                  if (!onRowClick) return;
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    onRowClick(item);
                  }
                }}
                className={`group relative flex min-h-9 min-w-0 items-center gap-2 rounded-[3px] px-1 py-1 ${onRowClick ? "cursor-pointer hover:bg-sunken" : ""}`}
              >
                <span className="w-4 shrink-0 text-right text-[11px] tabular-nums text-ink-3">{index + 1}</span>
                <span aria-hidden className="size-2 shrink-0 rounded-[2px]" style={{ backgroundColor: color }} />
                <span className="min-w-0 flex-1 truncate text-meta text-ink-2" title={item.name ?? (typeof item.label === "string" ? item.label : undefined)}>
                  {item.label}
                </span>
                <span className="relative h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-sunken" aria-hidden>
                  <span className="absolute inset-y-0 left-0 rounded-full" style={{ width: `${width}%`, backgroundColor: color }} />
                </span>
                <span className="w-14 shrink-0 text-right text-meta font-medium tabular-nums text-ink sm:w-16">{format(item.value)}</span>
                <span className="hidden w-12 shrink-0 text-right text-[11px] tabular-nums text-ink-3 sm:block">{share === null ? "—" : formatPercentValue(share)}</span>
                {onSelect ? (
                  <button
                    type="button"
                    aria-label={`仅看 ${item.name ?? item.id}`}
                    onClick={(event) => {
                      event.stopPropagation();
                      onSelect(item);
                    }}
                    className="ml-0.5 flex size-6 shrink-0 items-center justify-center rounded-control text-ink-3 opacity-0 transition-opacity hover:bg-hover hover:text-ink group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100"
                  >
                    <Filter aria-hidden className="size-3.5" />
                  </button>
                ) : onRowClick ? (
                  <ArrowUpRight aria-hidden className="size-3.5 shrink-0 text-ink-3 opacity-0 group-hover:opacity-100" />
                ) : null}
              </div>
            </li>
          );
        })}
      </ol>
    </ChartFrame>
  );
}

function RankTable({ items, format, total, unit }: { items: RankBarItem[]; format: (value: number) => string; total: number; unit?: string }) {
  return (
    <ChartTable
      caption="排行数据表"
      head={
        <tr>
          <th className="px-2 py-1.5 font-medium">名称</th>
          <th className="px-2 py-1.5 text-right font-medium">值{unit ? `（${unit}）` : ""}</th>
          <th className="px-2 py-1.5 text-right font-medium">占比</th>
        </tr>
      }
    >
      {items.map((item) => (
        <tr key={item.id} className="border-t border-rule">
          <td className="px-2 py-1 text-ink-2">{item.label}</td>
          <td className="px-2 py-1 text-right tabular-nums">{format(item.value)}</td>
          <td className="px-2 py-1 text-right tabular-nums text-ink-3">
            {total > 0 ? formatPercentValue(item.value / total) : "—"}
          </td>
        </tr>
      ))}
    </ChartTable>
  );
}
