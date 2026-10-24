import { forwardRef, type InputHTMLAttributes, type ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "size"> {
  invalid?: boolean;
  size?: "sm" | "md";
  leadingIcon?: ReactNode;
  trailingIcon?: ReactNode;
}

const heightClasses: Record<NonNullable<InputProps["size"]>, string> = {
  sm: "h-7",
  md: "h-8",
};

const textClasses: Record<NonNullable<InputProps["size"]>, string> = {
  sm: "px-2.5 text-meta",
  md: "px-3 text-body",
};

const adornerClasses: Record<NonNullable<InputProps["size"]>, string> = {
  sm: "[&_svg]:size-3.5",
  md: "[&_svg]:size-4",
};

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, invalid = false, size = "md", leadingIcon, trailingIcon, "aria-invalid": ariaInvalid, ...props },
  ref,
) {
  // Field 只注入 aria-invalid（不传私有 prop），这里同时把它当作视觉错误态，
  // 否则辅助技术知道出错、用户却看不到红框。
  const isInvalid = invalid || Boolean(ariaInvalid && ariaInvalid !== "false");
  return (
    <div className={cn("relative flex w-full items-center", heightClasses[size], className)}>
      {leadingIcon ? (
        <span
          aria-hidden
          className={cn("pointer-events-none absolute left-2.5 flex items-center text-ink-3", adornerClasses[size])}
        >
          {leadingIcon}
        </span>
      ) : null}
      <input
        ref={ref}
        aria-invalid={isInvalid || undefined}
        className={cn(
          "h-full w-full min-w-0 rounded-control border bg-sunken text-ink transition-colors duration-[var(--motion-hover)]",
          "placeholder:text-ink-3 disabled:cursor-not-allowed disabled:opacity-60",
          isInvalid ? "border-critical" : "border-rule-strong",
          textClasses[size],
          leadingIcon && "pl-8",
          trailingIcon && "pr-8",
        )}
        {...props}
      />
      {trailingIcon ? (
        <span
          aria-hidden
          className={cn("pointer-events-none absolute right-2.5 flex items-center text-ink-3", adornerClasses[size])}
        >
          {trailingIcon}
        </span>
      ) : null}
    </div>
  );
});
