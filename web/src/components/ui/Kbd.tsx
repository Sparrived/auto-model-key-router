import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface KbdProps {
  children?: ReactNode;
  className?: string;
  "aria-hidden"?: boolean;
}

export function Kbd({ children, className, "aria-hidden": ariaHidden = true }: KbdProps) {
  return (
    <kbd
      aria-hidden={ariaHidden || undefined}
      className={cn(
        "inline-flex h-5 min-w-5 items-center justify-center rounded-[4px] border border-rule bg-sunken px-1 font-mono text-axis text-ink-3",
        className,
      )}
    >
      {children}
    </kbd>
  );
}
