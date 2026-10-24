import { Children, type CSSProperties, type ReactNode } from "react";
import { ChevronLeft } from "lucide-react";
import { cn } from "../../lib/cn";
import { Input } from "./Input";

export interface SplitViewProps {
  list: ReactNode;
  detail: ReactNode;
  selected?: boolean;
  onBack?: () => void;
  /** 桌面端左栏宽度，默认 280。 */
  listWidth?: number;
  className?: string;
}

/**
 * 列表 / 详情两栏。>=768px 始终并排（768–1023px 左栏固定 220px，
 * >=1024px 用 listWidth）；只有手机宽度才按 selected 单栏切换，
 * 详情页用「返回列表」回到列表，避免小屏出现两屏高的整页滚动。
 */
export function SplitView({
  list,
  detail,
  selected = false,
  onBack,
  listWidth = 280,
  className,
}: SplitViewProps) {
  const style = { "--split-list-w": `${listWidth}px` } as CSSProperties;

  return (
    <div style={style} className={cn("flex h-full min-h-0 w-full", className)}>
      <div
        className={cn(
          "min-h-0 w-full shrink-0 border-rule md:w-[220px] md:border-r lg:w-[var(--split-list-w)]",
          selected ? "hidden md:block" : "block",
        )}
      >
        {list}
      </div>
      <div className={cn("min-w-0 flex-1", selected ? "block" : "hidden md:block")}>
        {selected && onBack ? (
          <div className="border-b border-rule px-3 py-2 md:hidden">
            <button
              type="button"
              onClick={onBack}
              className="inline-flex items-center gap-1 rounded-control px-1 py-0.5 text-[13px] text-ink-2 transition-colors duration-150 hover:text-ink"
            >
              <ChevronLeft aria-hidden className="size-4" />
              返回列表
            </button>
          </div>
        ) : null}
        {detail}
      </div>
    </div>
  );
}

export interface ListPaneSearch {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
}

export interface ListPaneProps {
  title?: ReactNode;
  search?: ListPaneSearch;
  actions?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  empty?: ReactNode;
  className?: string;
}

export function ListPane({
  title,
  search,
  actions,
  children,
  footer,
  empty,
  className,
}: ListPaneProps) {
  const isEmpty = Children.toArray(children).length === 0;

  return (
    <div className={cn("flex h-full min-h-0 flex-col", className)}>
      {title || actions ? (
        <div className="flex shrink-0 items-center justify-between gap-2 px-3 pb-1 pt-3">
          {title ? (
            <div className="min-w-0 truncate text-[14px] font-semibold text-ink">{title}</div>
          ) : (
            <span />
          )}
          {actions ? <div className="flex shrink-0 items-center gap-1">{actions}</div> : null}
        </div>
      ) : null}
      {search ? (
        <div className="shrink-0 px-3 pb-2 pt-2">
          <Input
            value={search.value}
            onChange={(event) => search.onChange(event.target.value)}
            placeholder={search.placeholder ?? "搜索"}
            aria-label={search.placeholder ?? "搜索"}
            size="sm"
          />
        </div>
      ) : null}
      <div className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        {isEmpty && empty ? (
          <div className="flex h-full items-center justify-center px-4 py-8 text-center text-[13px] text-ink-3">
            {empty}
          </div>
        ) : (
          children
        )}
      </div>
      {footer ? <div className="shrink-0 border-t border-rule p-2">{footer}</div> : null}
    </div>
  );
}

export interface ListPaneItemProps {
  children: ReactNode;
  selected?: boolean;
  onClick?: () => void;
  leading?: ReactNode;
  meta?: ReactNode;
  disabled?: boolean;
  className?: string;
}

/** 列表行约定：选中态 = select 底 + 左侧 2px accent 竖条 + aria-current="page"。 */
export function ListPaneItem({
  children,
  selected = false,
  onClick,
  leading,
  meta,
  disabled = false,
  className,
}: ListPaneItemProps) {
  return (
    <button
      type="button"
      aria-current={selected ? "page" : undefined}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "relative flex w-full items-center gap-2 rounded-control px-2.5 py-2 text-left text-[13px] transition-colors duration-150",
        selected ? "bg-select text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
        disabled && "cursor-not-allowed opacity-50 hover:bg-transparent",
        className,
      )}
    >
      {selected ? (
        <span aria-hidden className="absolute inset-y-1 left-0 w-0.5 rounded-pill bg-accent" />
      ) : null}
      {leading ? <span className="flex shrink-0 items-center">{leading}</span> : null}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {meta ? <span className="ml-1 shrink-0 text-[12px] text-ink-3">{meta}</span> : null}
    </button>
  );
}
