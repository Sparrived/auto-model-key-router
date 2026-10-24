import { useId, type ReactNode } from "react";
import { Switch as SwitchPrimitive } from "radix-ui";
import { cn } from "@/lib/cn";

export interface SwitchProps {
  checked?: boolean;
  onCheckedChange?: (checked: boolean) => void;
  disabled?: boolean;
  label?: ReactNode;
  description?: ReactNode;
  id?: string;
  name?: string;
  className?: string;
  "aria-label"?: string;
}

export function Switch({
  checked,
  onCheckedChange,
  disabled = false,
  label,
  description,
  id,
  name,
  className,
  "aria-label": ariaLabel,
}: SwitchProps) {
  const generatedId = useId();
  const switchId = id ?? generatedId;
  const descriptionId = description ? `${switchId}-description` : undefined;

  return (
    <div className={cn("flex items-start gap-2.5", className)}>
      <SwitchPrimitive.Root
        id={switchId}
        name={name}
        checked={checked}
        onCheckedChange={onCheckedChange}
        disabled={disabled}
        aria-label={label ? undefined : ariaLabel}
        aria-describedby={descriptionId}
        className={cn(
          "relative inline-flex h-5 w-9 shrink-0 items-center rounded-pill bg-rule-strong transition-colors duration-[var(--motion-hover)]",
          "data-[state=checked]:bg-accent disabled:cursor-not-allowed disabled:opacity-50",
        )}
      >
        <SwitchPrimitive.Thumb
          className={cn(
            "block size-4 rounded-pill bg-raised shadow-sm transition-transform duration-[var(--motion-hover)]",
            "data-[state=checked]:translate-x-[18px] data-[state=unchecked]:translate-x-0.5",
          )}
        />
      </SwitchPrimitive.Root>
      {label || description ? (
        <div className="flex flex-col gap-0.5">
          {label ? (
            <label htmlFor={switchId} className="cursor-pointer text-body leading-5 text-ink">
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
  );
}
