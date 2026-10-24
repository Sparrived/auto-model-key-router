import {
  useMemo,
  useState,
  type CSSProperties,
  type Key,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import { cn } from "../../lib/cn";

export type DataTableAlign = "left" | "center" | "right";

export interface DataTableColumn<T> {
  key: string;
  header: ReactNode;
  cell: (row: T, index: number) => ReactNode;
  align?: DataTableAlign;
  width?: string | number;
  /** 提供即可排序；返回 null / undefined 的行始终排在最后。 */
  sortValue?: (row: T) => string | number | null | undefined;
  tabular?: boolean;
  className?: string;
  headerClassName?: string;
}

export interface DataTableProps<T> {
  columns: DataTableColumn<T>[];
  rows: T[];
  rowKey?: (row: T, index: number) => Key;
  rowActions?: (row: T) => ReactNode;
  onRowClick?: (row: T) => void;
  emptyState?: ReactNode;
  /** 行高 32px（true）或 40px（false）。 */
  dense?: boolean;
  loading?: boolean;
  loadingRows?: number;
  /** 表格标题，只给读屏软件。 */
  caption?: string;
  className?: string;
  "aria-label"?: string;
}

interface SortState {
  key: string;
  direction: "asc" | "desc";
}

const ALIGN: Record<DataTableAlign, string> = {
  left: "text-left",
  center: "text-center",
  right: "text-right",
};

function compareValues(a: string | number, b: string | number): number {
  if (typeof a === "number" && typeof b === "number") return a - b;
  return String(a).localeCompare(String(b), "zh-CN", { numeric: true, sensitivity: "base" });
}

function isInteractiveTarget(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  return Boolean(
    target.closest(
      'button, a, input, select, textarea, label, [role="button"], [role="link"], [role="menuitem"], [data-row-action]',
    ),
  );
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  rowActions,
  onRowClick,
  emptyState,
  dense = false,
  loading = false,
  loadingRows = 5,
  caption,
  className,
  "aria-label": ariaLabel,
}: DataTableProps<T>) {
  const [sort, setSort] = useState<SortState | null>(null);

  const sortedRows = useMemo(() => {
    if (!sort) return rows;
    const column = columns.find((item) => item.key === sort.key);
    const getValue = column?.sortValue;
    if (!getValue) return rows;

    return rows
      .map((row, index) => ({ row, index, value: getValue(row) }))
      .sort((a, b) => {
        // 缺值不参与方向翻转，永远沉底。
        if (a.value === null || a.value === undefined || b.value === null || b.value === undefined) {
          if (a.value === null || a.value === undefined) {
            return b.value === null || b.value === undefined ? a.index - b.index : 1;
          }
          return -1;
        }
        const result = compareValues(a.value, b.value);
        if (result === 0) return a.index - b.index;
        return sort.direction === "asc" ? result : -result;
      })
      .map((item) => item.row);
  }, [columns, rows, sort]);

  function toggleSort(column: DataTableColumn<T>) {
    if (!column.sortValue) return;
    setSort((current) => {
      if (!current || current.key !== column.key) return { key: column.key, direction: "asc" };
      return current.direction === "asc" ? { key: column.key, direction: "desc" } : null;
    });
  }

  const columnCount = columns.length + (rowActions ? 1 : 0);
  const showSkeleton = loading && rows.length === 0;

  return (
    <div className={cn("relative overflow-auto", className)} aria-busy={loading || undefined}>
      <table
        className="min-w-full border-separate border-spacing-0 text-[13px]"
        aria-label={ariaLabel}
      >
        {caption ? <caption className="sr-only">{caption}</caption> : null}
        <thead>
          <tr>
            {columns.map((column) => {
              const active = sort?.key === column.key;
              const ariaSort = column.sortValue
                ? active
                  ? sort.direction === "asc"
                    ? "ascending"
                    : "descending"
                  : "none"
                : undefined;
              const widthStyle: CSSProperties | undefined =
                column.width === undefined ? undefined : { width: column.width };
              return (
                <th
                  key={column.key}
                  scope="col"
                  aria-sort={ariaSort}
                  style={widthStyle}
                  onClick={column.sortValue ? () => toggleSort(column) : undefined}
                  className={cn(
                    "sticky top-0 z-10 border-b border-rule bg-sunken px-3 text-[12px] font-medium text-ink-3",
                    dense ? "h-8" : "h-10",
                    ALIGN[column.align ?? "left"],
                    column.sortValue && "cursor-pointer",
                    column.headerClassName,
                  )}
                >
                  {column.sortValue ? (
                    <button
                      type="button"
                      onClick={(event) => {
                        // 整格可点；按钮自己处理键盘，别让点击再冒泡触发一次排序。
                        event.stopPropagation();
                        toggleSort(column);
                      }}
                      className={cn(
                        "flex w-full items-center gap-1 rounded-chip transition-colors duration-150 hover:text-ink",
                        column.align === "right" && "flex-row-reverse justify-start",
                        column.align === "center" && "justify-center",
                      )}
                    >
                      {column.header}
                      {active ? (
                        sort.direction === "asc" ? (
                          <ArrowUp aria-hidden className="size-3 shrink-0" />
                        ) : (
                          <ArrowDown aria-hidden className="size-3 shrink-0" />
                        )
                      ) : (
                        <ChevronsUpDown aria-hidden className="size-3 shrink-0 opacity-50" />
                      )}
                    </button>
                  ) : (
                    column.header
                  )}
                </th>
              );
            })}
            {rowActions ? (
              <th
                scope="col"
                className={cn(
                  "sticky top-0 z-10 w-px border-b border-rule bg-sunken px-2",
                  dense ? "h-8" : "h-10",
                )}
              >
                <span className="sr-only">操作</span>
              </th>
            ) : null}
          </tr>
        </thead>
        <tbody>
          {showSkeleton
            ? Array.from({ length: loadingRows }, (_, rowIndex) => (
                <tr key={`skeleton-${rowIndex}`}>
                  {columns.map((column, columnIndex) => (
                    <td
                      key={column.key}
                      className={cn(
                        "border-b border-rule px-3",
                        dense ? "h-8 py-1" : "h-10 py-2",
                        ALIGN[column.align ?? "left"],
                      )}
                    >
                      <div
                        className={cn(
                          "h-3 animate-pulse rounded-chip bg-rule",
                          column.tabular || column.align === "right" ? "ml-auto w-12" : "w-2/3",
                        )}
                        style={{ maxWidth: `${Math.max(35, 75 - columnIndex * 8)}%` }}
                      />
                    </td>
                  ))}
                  {rowActions ? (
                    <td className="border-b border-rule px-2">
                      <div className="h-3 w-10 animate-pulse rounded-chip bg-rule" />
                    </td>
                  ) : null}
                </tr>
              ))
            : null}

          {!showSkeleton && sortedRows.length === 0 ? (
            <tr>
              <td colSpan={columnCount} className="px-3 py-10 text-center text-[13px] text-ink-3">
                {emptyState ?? "暂无数据"}
              </td>
            </tr>
          ) : null}

          {!showSkeleton
            ? sortedRows.map((row, index) => (
                <tr
                  key={rowKey?.(row, index) ?? index}
                  tabIndex={onRowClick ? 0 : undefined}
                  onClick={
                    onRowClick
                      ? (event: MouseEvent<HTMLTableRowElement>) => {
                          if (isInteractiveTarget(event.target)) return;
                          onRowClick(row);
                        }
                      : undefined
                  }
                  onKeyDown={
                    onRowClick
                      ? (event: KeyboardEvent<HTMLTableRowElement>) => {
                          // 只响应行本身的 Enter/Space；行内按钮的按键不能冒泡成“打开行”。
                          if (event.target !== event.currentTarget) return;
                          if (event.key === "Enter" || event.key === " ") {
                            event.preventDefault();
                            onRowClick(row);
                          }
                        }
                      : undefined
                  }
                  className={cn(
                    "group transition-colors duration-150 hover:bg-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-focus [&:last-child>td]:border-b-0",
                    onRowClick && "cursor-pointer",
                  )}
                >
                  {columns.map((column) => (
                    <td
                      key={column.key}
                      className={cn(
                        "border-b border-rule px-3 text-ink",
                        dense ? "h-8 py-1" : "h-10 py-2",
                        ALIGN[column.align ?? "left"],
                        column.tabular && "tabular-nums",
                        column.className,
                      )}
                    >
                      {column.cell(row, index)}
                    </td>
                  ))}
                  {rowActions ? (
                    <td
                      data-row-action
                      className="w-px whitespace-nowrap border-b border-rule px-2"
                    >
                      <div className="flex items-center justify-end gap-1 opacity-0 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100 [@media(hover:none)]:opacity-100">
                        {rowActions(row)}
                      </div>
                    </td>
                  ) : null}
                </tr>
              ))
            : null}
        </tbody>
      </table>
    </div>
  );
}
