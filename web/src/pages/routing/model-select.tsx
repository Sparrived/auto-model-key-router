import { useMemo, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";
import { Command } from "cmdk";
import { Popover } from "radix-ui";
import { modelGroups, type ModelSummary } from "@/lib/endpoint-plans";
import type { ModelKindsIndex } from "@/lib/model-kinds";
import { freezeTriggerText } from "@/lib/quota-freeze";
import type { ModelSummaryResponse, Provider, QuotaFreeze } from "@/lib/types";
import { Combobox, type ComboboxOption } from "@/components/ui/Combobox";
import { Input } from "@/components/ui/Input";
import { cn } from "@/lib/cn";
import { allKeyOptions } from "./shared";

// Combobox 用空串当 value 时会和 placeholder 混在一起，因此空项用哨兵值再映射回空串。
const EMPTY_MODEL = "__empty__";
const AUTO_KEY = "__auto__";

export interface ModelSelectProps {
  models: ModelSummaryResponse[];
  kinds: ModelKindsIndex | null | undefined;
  kind: string;
  value: string;
  emptyLabel?: string;
  disabled?: boolean;
  id?: string;
  "aria-label"?: string;
  onChange: (value: string) => void;
}

// ModelSelect 是按类型分组、可搜索的模型下拉。
//
// 分组规则来自 endpoint-plans.modelGroups：本族在前、其它类型居中、未分类垫底，
// 且**任何模型都不会被筛掉**。
export function ModelSelect({
  models, kinds, kind, value, emptyLabel = "", disabled = false, id, onChange, ...rest
}: ModelSelectProps) {
  const options = useMemo<ComboboxOption[]>(() => {
    const groups = modelGroups(models as unknown as ModelSummary[], kinds, kind);
    const list: ComboboxOption[] = [];
    if (emptyLabel) {
      list.push({ value: EMPTY_MODEL, label: emptyLabel, keywords: ["清空", "不配置", "无"] });
    }
    for (const group of groups) {
      for (const name of group.names) {
        list.push({
          value: name,
          label: name,
          group: group.label || undefined,
        });
      }
    }
    return list;
  }, [emptyLabel, kind, kinds, models]);

  return (
    <Combobox
      id={id}
      aria-label={rest["aria-label"]}
      options={options}
      value={value || (emptyLabel ? EMPTY_MODEL : "")}
      placeholder={emptyLabel || "选择模型"}
      disabled={disabled}
      onValueChange={(next) => {
        const picked = String(next);
        onChange(picked === EMPTY_MODEL ? "" : picked);
      }}
    />
  );
}

export interface KeySelectProps {
  models: ModelSummaryResponse[];
  modelId: string;
  value: string;
  includeAuto?: boolean;
  disabled?: boolean;
  id?: string;
  "aria-label"?: string;
  placeholder?: string;
  onChange: (value: string) => void;
}

export function KeySelect({
  models, modelId, value, includeAuto = false, disabled = false, id, placeholder, onChange, ...rest
}: KeySelectProps) {
  const options = useMemo<ComboboxOption[]>(() => {
    const list: ComboboxOption[] = [];
    if (includeAuto) {
      list.push({ value: AUTO_KEY, label: "自动路由", keywords: ["自动", "auto"] });
    }
    const keys = models.find((model) => model.id === modelId)?.keys || [];
    const seen = new Set<string>();
    for (const key of keys) {
      if (!key.enabled) continue;
      seen.add(key.name);
      list.push({ value: key.name, label: key.name });
    }
    // 既有配置指向了已禁用/缺失的 Key 时保留显示并加注，避免界面静默空白。
    if (value && !seen.has(value)) {
      const disabledMatch = keys.find((key) => key.name === value && !key.enabled);
      list.push({
        value,
        label: value,
        note: disabledMatch ? "已禁用" : "不可用",
      });
    }
    return list;
  }, [includeAuto, modelId, models, value]);

  return (
    <Combobox
      id={id}
      aria-label={rest["aria-label"]}
      options={options}
      value={value || (includeAuto ? AUTO_KEY : "")}
      placeholder={placeholder || (includeAuto ? "自动路由" : "选择 Key")}
      disabled={disabled || !modelId}
      onValueChange={(next) => {
        const picked = String(next);
        onChange(picked === AUTO_KEY ? "" : picked);
      }}
    />
  );
}

export interface ProviderKeyComboboxProps {
  providers: Provider[];
  freezes?: Map<string, QuotaFreeze>;
  value: string;
  disabled?: boolean;
  id?: string;
  className?: string;
  "aria-label"?: string;
  onChange: (value: string) => void;
}

/** 按供应商分组、可搜索的 (供应商, Key) 选择器，并标注禁用/冻结/已探测模型数。 */
export function ProviderKeyCombobox({
  providers, freezes, value, disabled = false, id, className, onChange, ...rest
}: ProviderKeyComboboxProps) {
  const options = useMemo<ComboboxOption[]>(() => {
    return allKeyOptions(providers, freezes).map((option) => {
      const notes: string[] = [];
      if (!option.enabled) notes.push("已禁用");
      if (option.freeze) notes.push(freezeTriggerText(option.freeze));
      notes.push(option.upstreamCount ? `${option.upstreamCount} 个模型` : "未探测");
      return {
        value: option.value,
        label: `${option.providerId} / ${option.keyName}`,
        group: option.providerId,
        note: notes.join(" · "),
        keywords: [option.providerId, option.keyName, ...notes],
      };
    });
  }, [freezes, providers]);

  return (
    <Combobox
      id={id}
      aria-label={rest["aria-label"]}
      options={options}
      value={value}
      placeholder={options.length ? "选择供应商 / Key" : "没有可用的 Key"}
      disabled={disabled || !options.length}
      className={className}
      onValueChange={(next) => onChange(String(next))}
    />
  );
}

export interface UpstreamModelInputProps {
  value: string;
  candidates: string[];
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  inputClassName?: string;
  "aria-label"?: string;
  onChange: (value: string) => void;
  onEnter?: () => void;
}

/**
 * 上游模型名输入框：既可直接手填/清空（留空 = 与路由名相同），也可展开可搜索的候选浮层，
 * 避免原生 <datalist> 在输入框有值时隐藏其余候选的问题。
 */
export function UpstreamModelInput({
  value, candidates, placeholder, disabled = false, className, inputClassName, onChange, onEnter, ...rest
}: UpstreamModelInputProps) {
  const [open, setOpen] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Anchor asChild>
        <div className={cn("relative flex items-center", className)}>
          <Input
            ref={inputRef}
            value={value}
            disabled={disabled}
            placeholder={placeholder}
            aria-label={rest["aria-label"]}
            className={cn(candidates.length > 0 && "pr-8", inputClassName)}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" && candidates.length > 0 && !open) {
                event.preventDefault();
                setOpen(true);
                return;
              }
              if (event.key === "Enter" && onEnter) {
                event.preventDefault();
                onEnter();
              }
            }}
          />
          {candidates.length > 0 ? (
            <Popover.Trigger asChild disabled={disabled}>
              <button
                type="button"
                aria-label="选择已探测的上游模型"
                title={`展开已探测的 ${candidates.length} 个上游模型`}
                className="absolute right-1 top-1/2 inline-flex size-6 -translate-y-1/2 items-center justify-center rounded-chip text-ink-3 transition-colors hover:bg-hover hover:text-ink disabled:pointer-events-none disabled:opacity-50"
              >
                <ChevronDown aria-hidden className={cn("size-3.5 transition-transform duration-150", open && "rotate-180")} />
              </button>
            </Popover.Trigger>
          ) : null}
        </div>
      </Popover.Anchor>
      {candidates.length > 0 ? (
        <Popover.Portal>
          <Popover.Content
            side="bottom"
            align="start"
            sideOffset={4}
            aria-label="已探测的上游模型"
            className="z-50 w-[max(var(--radix-popover-trigger-width),260px)] max-w-[360px] overflow-hidden rounded-control border border-rule bg-raised shadow-[var(--shadow-float)]"
          >
            <Command shouldFilter loop>
              <div className="border-b border-rule px-2 py-1.5">
                <Command.Input
                  autoFocus
                  placeholder="搜索已探测的上游模型…"
                  className="h-7 w-full bg-transparent px-1 text-[13px] text-ink outline-none placeholder:text-ink-3"
                />
              </div>
              <Command.List className="max-h-56 overflow-y-auto overscroll-contain p-1">
                <Command.Empty className="px-2 py-4 text-center text-meta text-ink-3">
                  没有匹配的候选，可直接关闭并在输入框手填
                </Command.Empty>
                {candidates.map((candidate) => {
                  const active = candidate === value;
                  return (
                    <Command.Item
                      key={candidate}
                      value={candidate}
                      onSelect={() => {
                        onChange(candidate);
                        setOpen(false);
                        requestAnimationFrame(() => inputRef.current?.focus());
                      }}
                      className="flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 font-mono text-[12px] text-ink outline-none data-[selected=true]:bg-select"
                    >
                      <span aria-hidden className="flex w-4 shrink-0 justify-center">
                        {active ? <Check className="size-3.5" /> : null}
                      </span>
                      <span className="min-w-0 flex-1 truncate">{candidate}</span>
                    </Command.Item>
                  );
                })}
              </Command.List>
            </Command>
          </Popover.Content>
        </Popover.Portal>
      ) : null}
    </Popover.Root>
  );
}
