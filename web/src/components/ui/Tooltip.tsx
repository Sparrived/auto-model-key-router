import type { ReactElement, ReactNode } from "react";
import { Tooltip as TooltipPrimitive } from "radix-ui";
import { cn } from "@/lib/cn";

export const TooltipProvider = TooltipPrimitive.Provider;

export interface TooltipProps {
  content: ReactNode;
  children: ReactElement;
  side?: "top" | "right" | "bottom" | "left";
  delayDuration?: number;
}

/**
 * 内容为空时直接渲染子元素：调用方不必为「有时没有提示」写条件。
 * Provider 放在这里而不是要求根节点统一提供，否则单独使用会直接抛错；
 * 根上已有 Provider 时嵌套是合法的，内层只覆盖延迟设置。
 */
export function Tooltip({ content, children, side = "top", delayDuration = 200 }: TooltipProps) {
  if (content === null || content === undefined || content === "") return children;

  return (
    <TooltipPrimitive.Provider delayDuration={delayDuration}>
      <TooltipPrimitive.Root>
        <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            sideOffset={6}
            collisionPadding={8}
            className={cn(
              "z-50 max-w-64 rounded-control border border-rule bg-raised px-2 py-1 text-meta leading-snug text-ink",
              "shadow-[var(--shadow-float)] transition-opacity duration-[var(--motion-panel)]",
              "data-[state=closed]:opacity-0",
            )}
          >
            {content}
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  );
}
