import { Dialog as DialogPrimitive } from "radix-ui";
import { X } from "lucide-react";
import type { ComponentPropsWithoutRef, ReactNode } from "react";
import { cn } from "@/lib/cn";
import { IconButton } from "./Button";
import { useReturnFocus } from "./use-return-focus";

const SIZES = {
  sm: "max-w-[400px]",
  md: "max-w-[520px]",
  lg: "max-w-[720px]",
} as const;

export interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  size?: keyof typeof SIZES;
  className?: string;
  preventClose?: boolean;
  // 一次性凭据这类对话框只允许走底部显式按钮，连右上角 X 也不能留。
  hideClose?: boolean;
}

export function Dialog({
  open, onOpenChange, title, description, children, footer, size = "md", className,
  preventClose = false, hideClose = false,
}: DialogProps) {
  const returnFocus = useReturnFocus(open);
  return (
    <DialogPrimitive.Root
      open={open}
      onOpenChange={(next) => {
        // preventClose 时把关闭请求全部交回调用方，只有显式按钮能结束对话框。
        if (!next && preventClose) return;
        onOpenChange(next);
      }}
    >
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay
          data-amkr-overlay="dialog"
          className={cn(
            "fixed inset-0 z-50 bg-black/40",
            "data-[state=open]:animate-[amkr-overlay-in_160ms_ease-out] data-[state=closed]:animate-[amkr-overlay-out_120ms_ease-in]",
          )}
        />
        <DialogPrimitive.Content
          className={cn(
            "fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100vh-2rem)] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-dialog border border-rule bg-raised shadow-[var(--shadow-float)] outline-none",
            SIZES[size],
            className,
          )}
          onEscapeKeyDown={(event) => {
            if (preventClose) event.preventDefault();
          }}
          onPointerDownOutside={(event) => {
            if (preventClose) event.preventDefault();
          }}
          onFocusOutside={(event) => {
            if (preventClose) event.preventDefault();
          }}
          onCloseAutoFocus={returnFocus}
        >
          <div className="flex shrink-0 items-start gap-3 px-5 pb-3 pt-5">
            <div className="min-w-0 flex-1">
              <DialogPrimitive.Title asChild>
                <h2 className="text-title font-semibold text-ink">{title}</h2>
              </DialogPrimitive.Title>
              {description ? (
                <DialogPrimitive.Description className="mt-1 text-meta text-ink-3">
                  {description}
                </DialogPrimitive.Description>
              ) : null}
            </div>
            {hideClose ? null : (
              <IconButton aria-label="关闭" tooltip={null} size="sm" onClick={() => onOpenChange(false)}>
                <X size={16} />
              </IconButton>
            )}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 pb-5">{children}</div>
          {footer ? (
            <div className="flex shrink-0 items-center justify-end gap-2 border-t border-rule px-5 py-3">
              {footer}
            </div>
          ) : null}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

// Radix 的 Close 需要放在 Dialog 内才能拿到上下文；包一层是为了页面不必直接依赖 radix-ui。
export function DialogClose(props: ComponentPropsWithoutRef<typeof DialogPrimitive.Close>) {
  return <DialogPrimitive.Close {...props} />;
}
