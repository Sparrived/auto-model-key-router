import { forwardRef, type TextareaHTMLAttributes } from "react";
import { cn } from "@/lib/cn";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, invalid = false, "aria-invalid": ariaInvalid, ...props },
  ref,
) {
  const isInvalid = invalid || Boolean(ariaInvalid && ariaInvalid !== "false");
  return (
    <textarea
      ref={ref}
      aria-invalid={isInvalid || undefined}
      className={cn(
        "min-h-20 w-full resize-y rounded-control border bg-sunken px-3 py-2 text-body text-ink transition-colors duration-[var(--motion-hover)]",
        "placeholder:text-ink-3 disabled:cursor-not-allowed disabled:opacity-60",
        isInvalid ? "border-critical" : "border-rule-strong",
        className,
      )}
      {...props}
    />
  );
});
