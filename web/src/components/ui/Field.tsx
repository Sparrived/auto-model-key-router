import { cloneElement, useId, type ReactElement, type ReactNode } from "react";
import { cn } from "@/lib/cn";

/** Field 只要求注入这四项，任何原生控件都能被它包裹。 */
export interface FieldControlProps {
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean | "false" | "grammar" | "spelling" | "true";
}

export interface FieldProps {
  label: ReactNode;
  children: ReactElement<FieldControlProps>;
  hint?: ReactNode;
  error?: ReactNode;
  required?: boolean;
  htmlFor?: string;
  className?: string;
}

export function Field({ label, children, hint, error, required = false, htmlFor, className }: FieldProps) {
  const generatedId = useId();
  const controlId = htmlFor ?? generatedId;
  const hintId = hint ? `${controlId}-hint` : undefined;
  const errorId = error ? `${controlId}-error` : undefined;
  const describedBy = [errorId, hintId].filter(Boolean).join(" ") || undefined;

  // 只在真正存在时写入属性，避免用 undefined 覆盖控件自身的 aria-invalid。
  const injected: FieldControlProps = { id: controlId };
  if (describedBy) injected["aria-describedby"] = describedBy;
  if (error) injected["aria-invalid"] = true;

  return (
    <div className={cn("flex w-full flex-col gap-1", className)}>
      <label htmlFor={controlId} className="text-meta font-medium text-ink-2">
        {label}
        {required ? (
          <>
            <span aria-hidden className="ml-0.5 text-critical-ink">
              *
            </span>
            <span className="sr-only">（必填）</span>
          </>
        ) : null}
      </label>
      {cloneElement(children, injected)}
      {error ? (
        <p id={errorId} className="text-meta text-critical-ink">
          {error}
        </p>
      ) : hint ? (
        <p id={hintId} className="text-meta text-ink-3">
          {hint}
        </p>
      ) : null}
    </div>
  );
}
