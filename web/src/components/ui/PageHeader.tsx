import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export interface PageHeaderProps {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}

/**
 * 页内说明行。页面标题已经由顶栏渲染成 h1，这里不再重复一个可见标题；
 * title 只作为无障碍名称留在 region 上，避免读屏时缺少页面上下文。
 */
export function PageHeader({ title, description, actions, className }: PageHeaderProps) {
  return (
    <div
      role="region"
      aria-label={typeof title === "string" ? title : undefined}
      className={cn("flex flex-wrap items-start justify-between gap-3", className)}
    >
      <div className="min-w-0 flex-1">
        {description ? (
          <p className="text-[13px] leading-5 text-ink-3">{description}</p>
        ) : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </div>
  );
}
