import { useMemo, useRef, useState, type ReactNode } from "react";
import { Check, ChevronDown, X } from "lucide-react";
import { Command } from "cmdk";
import { Popover } from "radix-ui";
import { cn } from "../../lib/cn";

export interface ComboboxOption {
  value: string;
  label: ReactNode;
  group?: string;
  note?: ReactNode;
  disabled?: boolean;
  keywords?: string[];
}

export interface ComboboxProps {
  options: ComboboxOption[];
  value: string | string[];
  onValueChange: (value: string | string[]) => void;
  multiple?: boolean;
  placeholder?: string;
  disabled?: boolean;
  invalid?: boolean;
  id?: string;
  className?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
}

/** 只从 ReactNode 里取可搜索的文本；图标等节点不参与过滤。 */
function textOf(node: ReactNode): string {
  return typeof node === "string" || typeof node === "number" ? String(node) : "";
}

export function Combobox({
  options,
  value,
  onValueChange,
  multiple = false,
  placeholder = "请选择",
  disabled = false,
  invalid = false,
  id,
  className,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
  "aria-describedby": ariaDescribedBy,
}: ComboboxProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const optionMap = useMemo(() => {
    const map = new Map<string, ComboboxOption>();
    for (const option of options) map.set(option.value, option);
    return map;
  }, [options]);

  const selectedValues = useMemo(() => {
    if (Array.isArray(value)) return multiple ? value : value.slice(0, 1);
    return value ? [value] : [];
  }, [multiple, value]);

  // 分组按选项首次出现的顺序排，避免每次打开都换位置。
  const { ungrouped, grouped } = useMemo(() => {
    const groups = new Map<string, ComboboxOption[]>();
    const loose: ComboboxOption[] = [];
    for (const option of options) {
      if (option.group) {
        const list = groups.get(option.group);
        if (list) list.push(option);
        else groups.set(option.group, [option]);
      } else {
        loose.push(option);
      }
    }
    return { ungrouped: loose, grouped: Array.from(groups.entries()) };
  }, [options]);

  function removeValue(target: string) {
    onValueChange(selectedValues.filter((item) => item !== target));
    // 被移除的芯片按钮会随列表消失；把焦点交给搜索框（弹层开着）或触发器，
    // 否则键盘用户按一次移除就落回 body，后面无处可去。
    requestAnimationFrame(() => {
      if (open) searchRef.current?.focus();
      else triggerRef.current?.focus();
    });
  }

  function handleSelect(optionValue: string) {
    if (multiple) {
      const next = selectedValues.includes(optionValue)
        ? selectedValues.filter((item) => item !== optionValue)
        : [...selectedValues, optionValue];
      onValueChange(next);
      return;
    }
    onValueChange(optionValue);
    setOpen(false);
  }

  const selectedText = selectedValues
    .map((item) => textOf(optionMap.get(item)?.label) || item)
    .join("，");
  // 触发器是覆盖整个控件的透明按钮，可见文字在它上层且不接收指针事件；
  // 这样芯片的移除按钮可以是真的 <button>（合法 HTML、可聚焦），又不会触发开关。
  const accessibleName = selectedText || placeholder;

  function renderItem(option: ComboboxOption) {
    const active = selectedValues.includes(option.value);
    const keywords = [textOf(option.label), ...(option.keywords ?? [])].filter(Boolean);
    return (
      <Command.Item
        key={option.value}
        value={option.value}
        keywords={keywords}
        disabled={option.disabled}
        onSelect={() => handleSelect(option.value)}
        className="flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 text-[13px] text-ink outline-none data-[disabled=true]:cursor-not-allowed data-[disabled=true]:opacity-50 data-[selected=true]:bg-select"
      >
        <span aria-hidden className="flex w-4 shrink-0 justify-center">
          {active ? <Check className="size-3.5" /> : null}
        </span>
        <span className="min-w-0 flex-1 truncate">{option.label}</span>
        {option.note !== undefined && option.note !== null ? (
          <span className="max-w-[50%] shrink-0 truncate text-[12px] text-ink-3">{option.note}</span>
        ) : null}
      </Command.Item>
    );
  }

  return (
    <div
      ref={rootRef}
      data-invalid={invalid ? "" : undefined}
      className={cn(
        "group relative flex min-h-8 w-full items-center rounded-control border border-rule-strong bg-sunken transition-colors duration-150",
        open && "border-ink-3",
        !disabled && "hover:border-ink-3",
        disabled && "cursor-not-allowed opacity-50",
        invalid && "border-critical hover:border-critical",
        className,
      )}
    >
      <Popover.Root open={open} onOpenChange={setOpen}>
        <Popover.Trigger asChild disabled={disabled}>
          <button
            ref={triggerRef}
            id={id}
            type="button"
            aria-label={ariaLabel}
            aria-labelledby={ariaLabelledBy}
            aria-describedby={ariaDescribedBy}
            aria-invalid={invalid || undefined}
            className="absolute inset-0 z-0 rounded-control bg-transparent text-left"
          >
            <span className="sr-only">{accessibleName}</span>
          </button>
        </Popover.Trigger>
        <span className="pointer-events-none relative z-10 flex min-w-0 flex-1 flex-wrap items-center gap-1 py-1 pl-2.5 pr-8 text-[13px]">
          {selectedValues.length === 0 ? (
            <span className="truncate text-ink-3">{placeholder}</span>
          ) : multiple ? (
            selectedValues.map((item) => (
              <span
                key={item}
                className="pointer-events-none inline-flex max-w-full items-center gap-1 rounded-chip border border-rule bg-panel py-0.5 pl-1.5 pr-0.5 text-[12px] leading-4 text-ink"
              >
                <span className="min-w-0 truncate">{optionMap.get(item)?.label ?? item}</span>
                {!disabled ? (
                  <button
                    type="button"
                    aria-label={`移除 ${textOf(optionMap.get(item)?.label) || item}`}
                    className="pointer-events-auto inline-flex size-4 shrink-0 items-center justify-center rounded-chip text-ink-3 transition-colors duration-150 hover:bg-hover hover:text-ink"
                    onClick={(event) => {
                      event.preventDefault();
                      event.stopPropagation();
                      removeValue(item);
                    }}
                  >
                    <X aria-hidden className="size-3" />
                  </button>
                ) : null}
              </span>
            ))
          ) : (
            <span className="truncate">{optionMap.get(selectedValues[0])?.label ?? selectedValues[0]}</span>
          )}
        </span>
        <ChevronDown
          aria-hidden
          className={cn(
            "pointer-events-none absolute right-2.5 top-1/2 z-10 size-4 -translate-y-1/2 text-ink-3 transition-transform duration-150",
            open && "rotate-180",
          )}
        />
        <Popover.Portal>
          <Popover.Content
            side="bottom"
            align="start"
            sideOffset={4}
            aria-label="选项列表"
            className="z-50 w-[var(--radix-popover-trigger-width)] min-w-[220px] overflow-hidden rounded-control border border-rule bg-raised shadow-[var(--shadow-float)]"
            // 芯片的移除按钮在触发器容器内、但在浮层外；不拦住这次外部交互，
            // 点一下移除就会先把整个弹层关掉。
            onInteractOutside={(event) => {
              if (rootRef.current?.contains(event.target as Node)) event.preventDefault();
            }}
          >
            <Command shouldFilter loop>
              <div className="border-b border-rule px-2 py-1.5">
                <Command.Input
                  ref={searchRef}
                  autoFocus
                  placeholder="搜索…"
                  className="h-7 w-full bg-transparent px-1 text-[13px] text-ink outline-none placeholder:text-ink-3"
                />
              </div>
              <Command.List className="max-h-64 overflow-y-auto overscroll-contain p-1">
                <Command.Empty className="px-2 py-6 text-center text-[13px] text-ink-3">
                  没有匹配项
                </Command.Empty>
                {ungrouped.map(renderItem)}
                {grouped.map(([group, items]) => (
                  <Command.Group
                    key={group}
                    heading={group}
                    className="[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:pt-2 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&_[cmdk-group-heading]]:text-ink-3"
                  >
                    {items.map(renderItem)}
                  </Command.Group>
                ))}
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
    </div>
  );
}
