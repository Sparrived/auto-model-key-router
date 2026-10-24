import { cn } from "@/lib/cn";

export type SkeletonVariant = "text" | "block" | "circle" | "chart" | "table";

export interface SkeletonProps {
  variant?: SkeletonVariant;
  className?: string;
}

const variantClasses: Record<SkeletonVariant, string> = {
  text: "h-3 w-full rounded-[3px]",
  block: "h-16 w-full rounded-control",
  circle: "size-8 rounded-pill",
  chart: "h-40 w-full rounded-panel",
  table: "h-32 w-full rounded-panel",
};

export function Skeleton({ variant = "text", className }: SkeletonProps) {
  return <div aria-hidden className={cn("animate-pulse bg-select motion-reduce:animate-none", variantClasses[variant], className)} />;
}
