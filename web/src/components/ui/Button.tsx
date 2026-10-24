import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/cn";
import { Tooltip } from "./Tooltip";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-ghost";
export type ButtonSize = "sm" | "md";

const base =
  "inline-flex select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-medium transition-colors duration-[var(--motion-hover)] disabled:pointer-events-none disabled:opacity-50";

const variantClasses: Record<ButtonVariant, string> = {
  primary: "bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "border border-rule-strong bg-panel text-ink hover:bg-hover",
  ghost: "text-ink-2 hover:bg-hover hover:text-ink",
  danger: "bg-critical text-white hover:opacity-90",
  "danger-ghost": "text-critical-ink hover:bg-critical-bg",
};

const sizeClasses: Record<ButtonSize, string> = {
  sm: "h-7 px-2.5 text-meta",
  md: "h-8 px-3 text-body",
};

const iconOnlySizeClasses: Record<ButtonSize, string> = {
  sm: "h-7 w-7 px-0 text-meta",
  md: "h-8 w-8 px-0 text-body",
};

const loaderSizeClasses: Record<ButtonSize, string> = {
  sm: "size-3.5",
  md: "size-4",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  leadingIcon?: ReactNode;
  trailingIcon?: ReactNode;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    className,
    variant = "secondary",
    size = "md",
    loading = false,
    leadingIcon,
    trailingIcon,
    disabled,
    type = "button",
    children,
    ...props
  },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(base, variantClasses[variant], sizeClasses[size], className)}
      {...props}
    >
      {loading ? (
        <Loader2 aria-hidden className={cn("animate-spin motion-reduce:animate-none", loaderSizeClasses[size])} />
      ) : (
        leadingIcon
      )}
      {children}
      {trailingIcon}
    </button>
  );
});

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label"> {
  "aria-label": string;
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  /** 默认复用 aria-label；传 null 表示不显示提示。 */
  tooltip?: ReactNode;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  {
    className,
    variant = "ghost",
    size = "md",
    loading = false,
    tooltip,
    children,
    disabled,
    type = "button",
    ...props
  },
  ref,
) {
  const button = (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(base, variantClasses[variant], iconOnlySizeClasses[size], className)}
      {...props}
    >
      {loading ? (
        <Loader2 aria-hidden className={cn("animate-spin motion-reduce:animate-none", loaderSizeClasses[size])} />
      ) : (
        children
      )}
    </button>
  );

  return tooltip === null ? button : <Tooltip content={tooltip ?? props["aria-label"]}>{button}</Tooltip>;
});
