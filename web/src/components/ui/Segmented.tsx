import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface SegmentedOption {
  value: string;
  label: ReactNode;
  disabled?: boolean;
}

export interface SegmentedProps {
  options: SegmentedOption[];
  value: string;
  onValueChange: (value: string) => void;
  "aria-label": string;
  className?: string;
}

export function Segmented({ options, value, onValueChange, "aria-label": ariaLabel, className }: SegmentedProps) {
  const buttonRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const selectableIndexes = options.flatMap((option, index) => (option.disabled ? [] : [index]));
  const selectedIndex = options.findIndex((option) => option.value === value);
  const tabStopIndex =
    selectedIndex >= 0 && !options[selectedIndex]?.disabled ? selectedIndex : (selectableIndexes[0] ?? -1);

  function selectIndex(index: number) {
    const option = options[index];
    if (!option || option.disabled) return;
    buttonRefs.current[index]?.focus();
    onValueChange(option.value);
  }

  // 方向键在可用项之间循环；跳过的禁用项不能成为落点。
  function step(from: number, delta: 1 | -1) {
    if (selectableIndexes.length === 0) return;
    const position = selectableIndexes.indexOf(from);
    const next = position < 0 ? 0 : (position + delta + selectableIndexes.length) % selectableIndexes.length;
    selectIndex(selectableIndexes[next]);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const current = options.findIndex((_, index) => buttonRefs.current[index] === document.activeElement);
    switch (event.key) {
      case "ArrowRight":
      case "ArrowDown":
        event.preventDefault();
        step(current, 1);
        break;
      case "ArrowLeft":
      case "ArrowUp":
        event.preventDefault();
        step(current, -1);
        break;
      case "Home":
        event.preventDefault();
        if (selectableIndexes.length > 0) selectIndex(selectableIndexes[0]);
        break;
      case "End":
        event.preventDefault();
        if (selectableIndexes.length > 0) selectIndex(selectableIndexes[selectableIndexes.length - 1]);
        break;
      default:
        break;
    }
  }

  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      onKeyDown={handleKeyDown}
      className={cn("inline-flex w-fit items-center gap-0.5 rounded-control border border-rule bg-sunken p-0.5", className)}
    >
      {options.map((option, index) => {
        const selected = option.value === value;
        return (
          <button
            key={option.value}
            ref={(node) => {
              buttonRefs.current[index] = node;
            }}
            type="button"
            role="radio"
            aria-checked={selected}
            tabIndex={index === tabStopIndex ? 0 : -1}
            disabled={option.disabled}
            onClick={() => onValueChange(option.value)}
            className={cn(
              "inline-flex h-6 items-center rounded-[6px] border px-3 text-meta transition-colors duration-[var(--motion-hover)]",
              "disabled:cursor-not-allowed disabled:opacity-40",
              selected
                ? "border-rule bg-panel font-medium text-ink"
                : "border-transparent text-ink-2 hover:text-ink",
            )}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
