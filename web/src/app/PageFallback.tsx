import { Skeleton } from "@/components/ui/Skeleton";

export function PagePlaceholderFallback() {
  return (
    <div className="space-y-3" aria-label="页面加载中">
      <Skeleton className="h-6 w-40" />
      <Skeleton variant="block" className="h-64 w-full" />
    </div>
  );
}
