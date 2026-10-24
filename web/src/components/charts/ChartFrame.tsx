// 所有图表的统一外壳：标题、工具、忙碌/空/错误态，以及「图 / 表」切换。
//
// 表格视图不是附加功能，而是图表的可访问替代：小屏、色觉差异、屏幕阅读器用户
// 都需要能读到同一份数。因此每个图表都必须传入 table，且表头写明单位。

import { useEffect, useState, type ReactNode } from "react";
import { ChartLine, Table2 } from "lucide-react";
import { Banner } from "@/components/ui/Banner";
import { Empty } from "@/components/ui/Empty";
import { Spinner } from "@/components/ui/Spinner";
import { cn } from "@/lib/cn";

export interface ChartFrameProps {
  title?: ReactNode;
  subtitle?: ReactNode;
  /** 图例等辅助内容，放在标题下方、图上方。 */
  legend?: ReactNode;
  /** 标题右端的额外操作；表格切换按钮由组件自己渲染。 */
  actions?: ReactNode;
  busy?: boolean;
  empty?: boolean;
  emptyText?: ReactNode;
  error?: ReactNode;
  /** 表格视图；未传时仍保留图视图，只是没有切换按钮。 */
  table?: ReactNode;
  /** 初始视图。移动端矩阵等不适合压缩的图表可默认打开表格。 */
  defaultView?: "chart" | "table";
  /** 嵌入 Panel 内容区：不画外框/圆角/内边距，标题由 Panel 负责，只保留表格切换按钮。 */
  embedded?: boolean;
  /** 固定高度；趋势类图表默认由调用方控制。 */
  height?: number;
  className?: string;
  ariaLabel?: string;
  children: ReactNode;
}

export function ChartFrame({
  title,
  subtitle,
  legend,
  actions,
  busy = false,
  empty = false,
  emptyText = "当前时间窗口内没有数据",
  error,
  table,
  defaultView = "chart",
  embedded = false,
  height,
  className,
  ariaLabel,
  children,
}: ChartFrameProps) {
  const [view, setView] = useState<"chart" | "table">(defaultView);

  // 响应式容器从窄变宽（或反过来）时，让调用方给出的默认视图重新生效一次；
  // 用户手动切换仍由按钮控制，不会在普通重渲染时被覆盖。
  useEffect(() => {
    setView(defaultView);
  }, [defaultView]);

  return (
    <section
      className={cn(
        "relative min-w-0",
        embedded ? "bg-transparent" : "rounded-panel border border-rule bg-panel",
        className,
      )}
      aria-busy={busy || undefined}
      aria-label={ariaLabel}
    >
      {!embedded && (title || subtitle || actions || table) ? (
        <header className="flex flex-col gap-2 px-3 pt-3 sm:flex-row sm:items-start sm:gap-3">
          <div className="min-w-0 flex-1">
            {title ? <h3 className="text-title font-semibold text-ink">{title}</h3> : null}
            {subtitle ? <p className="mt-0.5 text-meta text-ink-3">{subtitle}</p> : null}
          </div>
          <div className="flex w-full shrink-0 flex-wrap items-center justify-end gap-1.5 sm:w-auto">
            {actions}
            {table ? (
              <button
                type="button"
                aria-label={view === "chart" ? "切换到表格视图" : "切换到图表视图"}
                aria-pressed={view === "table"}
                onClick={() => setView((current) => (current === "chart" ? "table" : "chart"))}
                className="flex size-7 items-center justify-center rounded-control text-ink-3 transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-focus"
              >
                {view === "chart" ? <Table2 aria-hidden className="size-4" /> : <ChartLine aria-hidden className="size-4" />}
              </button>
            ) : null}
          </div>
        </header>
      ) : null}

      {legend ? <div className={embedded ? "pr-8" : "px-3 pt-2"}>{legend}</div> : null}

      {embedded && table ? (
        <button
          type="button"
          aria-label={view === "chart" ? "切换到表格视图" : "切换到图表视图"}
          aria-pressed={view === "table"}
          onClick={() => setView((current) => (current === "chart" ? "table" : "chart"))}
          className="absolute right-0 top-0 z-[3] flex size-7 items-center justify-center rounded-control bg-panel/80 text-ink-3 transition-colors hover:bg-hover hover:text-ink focus-visible:outline-2 focus-visible:outline-focus"
        >
          {view === "chart" ? <Table2 aria-hidden className="size-4" /> : <ChartLine aria-hidden className="size-4" />}
        </button>
      ) : null}

      <div className={cn("relative", embedded ? "p-0" : "p-3 pt-2")} style={height ? { minHeight: height } : undefined}>
        {error ? (
          <Banner tone="critical" title="图表读取失败">
            {error}
          </Banner>
        ) : empty ? (
          <Empty title="暂无数据" description={emptyText} />
        ) : view === "table" && table ? (
          table
        ) : (
          <div className="relative" style={height ? { height } : undefined}>
            {children}
            {busy ? (
              <div className="absolute inset-0 z-[2] flex items-center justify-center rounded-control bg-panel/65 backdrop-blur-[1px]">
                <span className="flex items-center gap-2 rounded-pill bg-panel px-2.5 py-1 text-meta text-ink-2 shadow-[var(--shadow-float)]">
                  <Spinner size="sm" /> 正在刷新
                </span>
              </div>
            ) : null}
          </div>
        )}
      </div>
    </section>
  );
}
