import type { ComponentProps } from "react";
import { Tabs as TabsPrimitive } from "radix-ui";
import { cn } from "@/lib/cn";

export function Tabs({ className, ...props }: ComponentProps<typeof TabsPrimitive.Root>) {
  return <TabsPrimitive.Root className={cn("flex flex-col gap-3", className)} {...props} />;
}

export function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("inline-flex w-fit items-center gap-0.5 rounded-control border border-rule bg-sunken p-0.5", className)}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "inline-flex h-6 items-center rounded-[6px] border border-transparent px-3 text-meta text-ink-2 transition-colors duration-[var(--motion-hover)]",
        "hover:text-ink disabled:cursor-not-allowed disabled:opacity-50",
        "data-[state=active]:border-rule data-[state=active]:bg-panel data-[state=active]:font-medium data-[state=active]:text-ink",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("rounded-panel", className)} {...props} />;
}
