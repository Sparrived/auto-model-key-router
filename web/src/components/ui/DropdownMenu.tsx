import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui";
import { forwardRef, type ComponentPropsWithoutRef, type ReactNode } from "react";
import { cn } from "@/lib/cn";

// 与 Popover 同一套浮层语言；关键帧就近定义，避免依赖全局样式表的维护时序。
const MOTION = `
@keyframes amkr-menu-in { from { opacity: 0; transform: scale(0.97); } to { opacity: 1; transform: scale(1); } }
@keyframes amkr-menu-out { from { opacity: 1; transform: scale(1); } to { opacity: 0; transform: scale(0.97); } }
`;

export interface DropdownMenuProps {
  trigger: ReactNode;
  children: ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  sideOffset?: number;
  className?: string;
}

// DropdownMenu 只约定「触发器 + 菜单项」两段结构；方向键导航、Esc、焦点归还由 Radix 负责。
export function DropdownMenu({
  trigger, children, open, onOpenChange, side = "bottom", align = "start", sideOffset = 6, className,
}: DropdownMenuProps) {
  return (
    <DropdownMenuPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DropdownMenuPrimitive.Trigger asChild>{trigger}</DropdownMenuPrimitive.Trigger>
      <DropdownMenuPrimitive.Portal>
        <DropdownMenuPrimitive.Content
          side={side}
          align={align}
          sideOffset={sideOffset}
          collisionPadding={8}
          className={cn(
            "z-50 min-w-44 origin-[var(--radix-dropdown-menu-content-transform-origin)] rounded-control border border-rule bg-raised p-1 text-body text-ink shadow-[var(--shadow-float)]",
            "data-[state=open]:animate-[amkr-menu-in_150ms_ease-out] data-[state=closed]:animate-[amkr-menu-out_120ms_ease-in]",
            className,
          )}
        >
          <style>{MOTION}</style>
          {children}
        </DropdownMenuPrimitive.Content>
      </DropdownMenuPrimitive.Portal>
    </DropdownMenuPrimitive.Root>
  );
}

export interface DropdownMenuItemProps extends ComponentPropsWithoutRef<typeof DropdownMenuPrimitive.Item> {
  danger?: boolean;
  icon?: ReactNode;
  shortcut?: string;
}

export const DropdownMenuItem = forwardRef<HTMLDivElement, DropdownMenuItemProps>(function DropdownMenuItem(
  { danger, icon, shortcut, className, children, ...rest },
  ref,
) {
  return (
    <DropdownMenuPrimitive.Item
      ref={ref}
      className={cn(
        "flex cursor-default select-none items-center gap-2 rounded-chip px-2 py-1.5 text-dense outline-none",
        "data-[highlighted]:bg-select data-[highlighted]:text-ink",
        "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        // 危险项平时也用警示色文字，避免用户只有按下才意识到它不可逆。
        danger && "text-critical-ink data-[highlighted]:bg-critical-bg data-[highlighted]:text-critical-ink",
        className,
      )}
      {...rest}
    >
      {icon ? <span className="flex size-4 shrink-0 items-center justify-center [&_svg]:size-4">{icon}</span> : null}
      <span className="min-w-0 flex-1">{children}</span>
      {shortcut ? <kbd className="shrink-0 font-mono text-axis text-ink-3">{shortcut}</kbd> : null}
    </DropdownMenuPrimitive.Item>
  );
});

export function DropdownMenuSeparator({ className }: { className?: string }) {
  return <DropdownMenuPrimitive.Separator className={cn("-mx-1 my-1 h-px bg-rule", className)} />;
}

export function DropdownMenuLabel({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <DropdownMenuPrimitive.Label className={cn("px-2 py-1.5 text-meta text-ink-3", className)}>
      {children}
    </DropdownMenuPrimitive.Label>
  );
}
