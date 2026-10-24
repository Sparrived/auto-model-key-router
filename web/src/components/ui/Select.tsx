import { forwardRef, type ReactNode, type SelectHTMLAttributes } from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "@/lib/cn";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "children"> {
  options?: SelectOption[];
  children?: ReactNode;
  invalid?: boolean;
  /** 空值项的文案，用于「未选择」状态。 */
  placeholder?: string;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, options, children, invalid = false, placeholder, "aria-invalid": ariaInvalid, ...props },
  ref,
) {
  const isInvalid = invalid || Boolean(ariaInvalid && ariaInvalid !== "false");
  return (
    <div className={cn("relative flex h-8 w-full items-center", className)}>
      <select
        ref={ref}
        aria-invalid={isInvalid || undefined}
        className={cn(
          "h-full w-full appearance-none rounded-control border bg-sunken pl-3 pr-8 text-body text-ink transition-colors duration-[var(--motion-hover)]",
          "disabled:cursor-not-allowed disabled:opacity-60",
          isInvalid ? "border-critical" : "border-rule-strong",
        )}
        {...props}
      >
        {placeholder ? <option value="">{placeholder}</option> : null}
        {options?.map((option) => (
          <option key={option.value} value={option.value} disabled={option.disabled}>
            {option.label}
          </option>
        ))}
        {children}
      </select>
      <ChevronDown aria-hidden className="pointer-events-none absolute right-2.5 size-4 text-ink-3" />
    </div>
  );
});
