import { Loader2 } from "lucide-react";
import { cn } from "@/lib/cn";

export interface SpinnerProps {
  size?: "sm" | "md";
  className?: string;
}

export function Spinner({ size = "sm", className }: SpinnerProps) {
  return (
    <Loader2
      role="status"
      aria-label="加载中"
      className={cn(
        "animate-spin text-ink-3 motion-reduce:animate-none",
        size === "sm" ? "size-3.5" : "size-4",
        className,
      )}
    />
  );
}
