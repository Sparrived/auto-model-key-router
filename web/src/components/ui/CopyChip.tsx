import type { MouseEvent } from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/cn";
import { copyText } from "@/lib/format";

export interface CopyChipProps {
  text: string;
  /** 前置说明文案，同时作为无障碍名称。 */
  label?: string;
  /** 悬停标题，默认展示全文（正文可能被截断）。 */
  title?: string;
  className?: string;
}

export function CopyChip({ text, label, title, className }: CopyChipProps) {
  async function handleCopy(event: MouseEvent<HTMLButtonElement>) {
    // 芯片常嵌在可点击的行里，复制不应顺带打开详情。
    event.stopPropagation();
    try {
      await copyText(text);
      toast.success("已复制");
    } catch {
      toast.error("复制失败");
    }
  }

  return (
    <button
      type="button"
      onClick={handleCopy}
      title={title ?? text}
      aria-label={label ? `复制 ${label}` : `复制 ${text}`}
      className={cn(
        "inline-flex h-6 max-w-full items-center gap-1.5 rounded-chip border border-rule bg-sunken px-2 text-meta text-ink-2",
        "transition-colors duration-[var(--motion-hover)] hover:bg-hover hover:text-ink",
        className,
      )}
    >
      {label ? <span className="shrink-0 text-ink-3">{label}</span> : null}
      <span className="max-w-64 truncate font-mono">{text}</span>
      <Copy aria-hidden className="size-3 shrink-0 text-ink-3" />
    </button>
  );
}
