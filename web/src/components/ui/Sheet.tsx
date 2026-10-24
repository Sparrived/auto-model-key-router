import { Dialog as DialogPrimitive } from "radix-ui";
import { X } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { cn } from "@/lib/cn";
import { Button, IconButton } from "./Button";
import { useConfirm } from "./ConfirmProvider";
import { useReturnFocus } from "./use-return-focus";

const WIDTHS = {
  md: "md:w-[560px]",
  lg: "md:w-[720px]",
  xl: "md:w-[920px]",
} as const;

export interface SheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  subtitle?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: keyof typeof WIDTHS;
  dirty?: boolean;
  onSave?: () => void | Promise<void>;
  saveLabel?: string;
  saving?: boolean;
  className?: string;
}

// Sheet 用 Radix Dialog 实现「右侧抽屉」：它自带焦点约束与关闭后焦点归还，
// 视觉上只是把居中的面板改成贴右边缘的整列。
export function Sheet({
  open, onOpenChange, title, subtitle, children, footer,
  width = "md", dirty = false, onSave, saveLabel = "保存", saving = false, className,
}: SheetProps) {
  const confirm = useConfirm();
  // 确认框是独立浮层：它开着时点击/按键都算「抽屉外」，不拦住会再弹一次确认。
  const confirming = useRef(false);
  const returnFocus = useReturnFocus(open);

  // 任何关闭路径都走这里：有未保存改动时先问一句，用户点「继续编辑」就保持打开。
  async function requestClose() {
    if (confirming.current) return;
    if (dirty) {
      confirming.current = true;
      try {
        const discard = await confirm({
          title: "放弃未保存的更改？",
          message: "关闭后，这次修改不会保存。",
          confirmLabel: "放弃更改",
          cancelLabel: "继续编辑",
          danger: true,
        });
        if (!discard) return;
      } finally {
        confirming.current = false;
      }
    }
    onOpenChange(false);
  }

  // 抽屉打开时全局接管 Ctrl/⌘+S：焦点必然在抽屉内（模态），不必再判断焦点位置。
  useEffect(() => {
    const save = onSave;
    if (!open || !save) return;
    function onKeyDown(event: KeyboardEvent) {
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "s") return;
      event.preventDefault();
      if (!saving) void save?.();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onSave, saving]);

  return (
    <DialogPrimitive.Root
      open={open}
      onOpenChange={(next) => {
        // 关闭请求一律由 requestClose 处理，避免这里与 Esc/遮罩各走一遍确认。
        if (next) onOpenChange(true);
      }}
    >
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay
          data-amkr-overlay="sheet"
          className={cn(
            "fixed inset-0 z-50 bg-black/40",
            "data-[state=open]:animate-[amkr-sheet-overlay-in_160ms_ease-out] data-[state=closed]:animate-[amkr-sheet-overlay-out_120ms_ease-in]",
          )}
        />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full max-w-full flex-col border-l border-rule bg-raised shadow-[var(--shadow-float)] outline-none",
            WIDTHS[width],
            "data-[state=open]:animate-[amkr-sheet-in_200ms_ease-out] data-[state=closed]:animate-[amkr-sheet-out_160ms_ease-in]",
            className,
          )}
          onEscapeKeyDown={(event) => {
            event.preventDefault();
            void requestClose();
          }}
          onPointerDownOutside={(event) => {
            event.preventDefault();
            void requestClose();
          }}
          onCloseAutoFocus={returnFocus}
        >
          <header className="flex shrink-0 items-start gap-3 border-b border-rule px-5 py-4">
            <div className="min-w-0 flex-1">
              <DialogPrimitive.Title asChild>
                <h2 className="truncate text-title font-semibold text-ink">{title}</h2>
              </DialogPrimitive.Title>
              {subtitle ? (
                <DialogPrimitive.Description className="mt-1 text-meta text-ink-3">
                  {subtitle}
                </DialogPrimitive.Description>
              ) : null}
            </div>
            <IconButton aria-label="关闭" tooltip={null} size="sm" onClick={() => void requestClose()}>
              <X size={16} />
            </IconButton>
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
          <footer className="flex shrink-0 items-center justify-end gap-2 border-t border-rule px-5 py-3">
            {footer ?? (
              <>
                <Button variant="secondary" onClick={() => void requestClose()}>取消</Button>
                {onSave ? (
                  <Button variant="primary" loading={saving} onClick={() => void onSave()}>
                    {saveLabel}
                  </Button>
                ) : null}
              </>
            )}
          </footer>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
