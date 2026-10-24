import { Popover as PopoverPrimitive } from "radix-ui";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

// 关键帧与组件放在一起：动效只服务于浮层本身，不必占用全局样式表（那份文件也不由本组件维护）。
// 减少动态偏好由 index.css 的全局规则统一关闭，这里不重复判断。
const MOTION = `
@keyframes amkr-popover-in { from { opacity: 0; transform: scale(0.97); } to { opacity: 1; transform: scale(1); } }
@keyframes amkr-popover-out { from { opacity: 1; transform: scale(1); } to { opacity: 0; transform: scale(0.97); } }
`;

export interface PopoverProps {
  trigger: ReactNode;
  children: ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  className?: string;
}

// Popover 只包视觉：Esc 关闭、点外关闭、关闭后焦点回到触发按钮都由 Radix 保证。
export function Popover({
  trigger, children, open, onOpenChange, side = "bottom", align = "center", className,
}: PopoverProps) {
  return (
    <PopoverPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <PopoverPrimitive.Trigger asChild>{trigger}</PopoverPrimitive.Trigger>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          side={side}
          align={align}
          sideOffset={6}
          collisionPadding={8}
          className={cn(
            "z-50 min-w-56 origin-[var(--radix-popover-content-transform-origin)] rounded-control border border-rule bg-raised p-3 text-body text-ink shadow-[var(--shadow-float)]",
            "data-[state=open]:animate-[amkr-popover-in_150ms_ease-out] data-[state=closed]:animate-[amkr-popover-out_120ms_ease-in]",
            className,
          )}
        >
          <style>{MOTION}</style>
          {children}
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}
