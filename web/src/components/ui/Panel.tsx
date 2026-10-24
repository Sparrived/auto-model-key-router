import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export interface PanelProps {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  toolbar?: ReactNode;
  footer?: ReactNode;
  busy?: boolean;
  /** false 时内容贴边；头部与底部仍有内边距，用于表格/列表这类需要满宽的面板。 */
  padding?: boolean;
  /** true 时面板吃满栅格行高：内容区占满剩余空间，footer 贴在底边，同一行两栏因此上下对齐。 */
  fill?: boolean;
  className?: string;
  children?: ReactNode;
}

// 顶部 2px 的不确定进度条：只占右上角一小段，不遮挡内容，也不改变布局。
const BUSY_KEYFRAMES = `@keyframes amkr-panel-busy {
  from { transform: translateX(-120%); }
  to { transform: translateX(420%); }
}`;

export function Panel({
  title,
  description,
  actions,
  toolbar,
  footer,
  busy = false,
  padding = true,
  fill = false,
  className,
  children,
}: PanelProps) {
  const hasHeader = Boolean(title || description || actions || toolbar);

  return (
    <section
      aria-busy={busy || undefined}
      className={cn(
        "relative rounded-panel border border-rule bg-panel",
        fill && "flex min-h-0 flex-col",
        className,
      )}
    >
      {busy ? (
        <>
          <style>{BUSY_KEYFRAMES}</style>
          <div
            aria-hidden
            className="absolute right-0 top-0 z-20 h-0.5 w-16 overflow-hidden rounded-tr-panel bg-rule"
          >
            <div className="h-full w-1/3 rounded-pill bg-accent animate-[amkr-panel-busy_1.4s_ease-in-out_infinite]" />
          </div>
        </>
      ) : null}

      {hasHeader ? (
        <header
          className={cn(
            "px-4 pt-4",
            padding ? "pb-0" : "border-b border-rule pb-3",
          )}
        >
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              {title ? (
                <h3 className="text-[16px] font-semibold leading-tight text-ink">{title}</h3>
              ) : null}
              {description ? (
                <p className="mt-1 text-[12px] leading-5 text-ink-3">{description}</p>
              ) : null}
            </div>
            {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
          </div>
          {toolbar ? <div className="mt-3">{toolbar}</div> : null}
        </header>
      ) : null}

      <div
        className={cn(
          "transition-opacity duration-200",
          padding ? (hasHeader ? "p-4 pt-3" : "p-4") : "p-0",
          fill && "flex min-h-0 flex-1 flex-col",
          busy && "opacity-60",
        )}
      >
        {children}
      </div>

      {footer ? (
        <footer
          className={cn(
            "border-t border-rule px-4 py-3 transition-opacity duration-200",
            busy && "opacity-60",
          )}
        >
          {footer}
        </footer>
      ) : null}
    </section>
  );
}
