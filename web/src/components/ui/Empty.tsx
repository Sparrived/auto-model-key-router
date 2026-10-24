import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface EmptyProps {
  icon?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}

export function Empty({ icon, title, description, action, className }: EmptyProps) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-1.5 px-4 py-12 text-center", className)}>
      {icon ? (
        <div aria-hidden className="mb-1 text-ink-3 [&_svg]:size-6">
          {icon}
        </div>
      ) : null}
      <h3 className="text-title font-semibold text-ink">{title}</h3>
      {description ? <p className="max-w-sm text-meta text-ink-3">{description}</p> : null}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  );
}
