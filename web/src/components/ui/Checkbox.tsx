import { useId, type ReactNode } from "react";
import { Checkbox as CheckboxPrimitive } from "radix-ui";
import { Check } from "lucide-react";
import { cn } from "@/lib/cn";

export interface CheckboxProps {
  checked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
  disabled?: boolean;
  label?: ReactNode;
  description?: ReactNode;
  error?: ReactNode;
  id?: string;
  name?: string;
  className?: string;
  "aria-label"?: string;
}

export function Checkbox({
  checked,
  onCheckedChange,
  disabled = false,
  label,
  description,
  error,
  id,
  name,
  className,
  "aria-label": ariaLabel,
}: CheckboxProps) {
  const generatedId = useId();
  const boxId = id ?? generatedId;
  const descriptionId = description ? `${boxId}-description` : undefined;
  const errorId = error ? `${boxId}-error` : undefined;
  const describedBy = [descriptionId, errorId].filter(Boolean).join(" ") || undefined;

  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <div className="flex items-start gap-2">
        <CheckboxPrimitive.Root
          id={boxId}
          name={name}
          checked={checked}
          onCheckedChange={(value) => onCheckedChange?.(value === true)}
          disabled={disabled}
          aria-label={label ? undefined : ariaLabel}
          aria-describedby={describedBy}
          aria-invalid={error ? true : undefined}
          className={cn(
            "mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-[4px] border bg-sunken text-on-accent transition-colors duration-[var(--motion-hover)]",
            "data-[state=checked]:border-accent data-[state=checked]:bg-accent disabled:cursor-not-allowed disabled:opacity-50",
            error ? "border-critical" : "border-rule-strong",
          )}
        >
          <CheckboxPrimitive.Indicator>
            <Check aria-hidden strokeWidth={3} className="size-3" />
          </CheckboxPrimitive.Indicator>
        </CheckboxPrimitive.Root>
        {label || description ? (
          <div className="flex flex-col gap-0.5">
            {label ? (
              <label htmlFor={boxId} className="cursor-pointer text-body leading-5 text-ink">
                {label}
              </label>
            ) : null}
            {description ? (
              <p id={descriptionId} className="text-meta text-ink-3">
                {description}
              </p>
            ) : null}
          </div>
        ) : null}
      </div>
      {error ? (
        <p id={errorId} className="pl-6 text-meta text-critical-ink">
          {error}
        </p>
      ) : null}
    </div>
  );
}
