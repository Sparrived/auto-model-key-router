// 监控页的统一筛选条：时间窗 + 维度 chips + 「添加筛选」浮层。
//
// 只负责展示与收集用户动作，URL 读写都在 useMetricFilters 里；选项来自
// /ui/metric-dimensions.json，再与配置面的名称合并，历史值即使已从配置删除也照常可选。

import { useId, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Clock,
  Plus,
  Search,
  X,
} from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { errorText, formatCount } from "@/lib/format";
import {
  DIMENSION_LABELS,
  FILTER_DIMENSIONS,
  MAX_RANGE_HOURS,
  RANGE_PRESETS,
  rangeLabel,
  rangeToQuery,
  type FilterDimension,
  type MetricFilterState,
  type OutcomeFilter,
  type UpstreamKeyFilter,
} from "@/lib/filters";
import { useMetricFilters } from "@/lib/hooks/useMetricFilters";
import { queryKeys } from "@/lib/query-keys";
import type {
  AccessKeysResponse,
  MetricDimensionsResponse,
  ProvidersResponse,
  WorkspacesResponse,
} from "@/lib/types";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Input } from "@/components/ui/Input";
import { Popover } from "@/components/ui/Popover";
import { Select } from "@/components/ui/Select";
import { Tooltip } from "@/components/ui/Tooltip";

export interface FilterBarProps {
  supported?: FilterDimension[];
  defaultRange?: string;
  hideDimensions?: boolean;
}

const CALLER_LABELS: Record<string, string> = {
  local: "本机",
  workspace: "工作空间凭据",
  access_key: "访问密钥",
};

const OUTCOME_OPTIONS: Array<{ value: OutcomeFilter | null; label: string }> = [
  { value: null, label: "全部" },
  { value: "success", label: "成功" },
  { value: "failure", label: "失败" },
];

const CHIP_CLASS = "inline-flex h-6 max-w-full items-center gap-1 rounded-control border border-rule bg-panel px-2 text-meta";

function hasDimensionValue(state: MetricFilterState, dim: FilterDimension): boolean {
  switch (dim) {
    case "provider":
      return state.provider.length > 0;
    case "model":
      return state.model.length > 0;
    case "uk":
      return state.uk.length > 0;
    case "ak":
      return state.ak.length > 0;
    case "ws":
      return state.ws.length > 0;
    case "caller":
      return state.caller.length > 0;
    case "outcome":
      return state.outcome !== null;
  }
}

function chipText(
  dim: FilterDimension,
  state: MetricFilterState,
  accessKeyName: (id: string) => string,
): string {
  const prefix = `${DIMENSION_LABELS[dim]}：`;
  switch (dim) {
    case "provider":
      return prefix + state.provider.join("、");
    case "model":
      return prefix + state.model.join("、");
    case "uk":
      return prefix + state.uk.map((pair) => `${pair.providerId} / ${pair.keyName}`).join("、");
    case "ak":
      return prefix + state.ak.map(accessKeyName).join("、");
    case "ws":
      return prefix + state.ws.join("、");
    case "caller":
      return prefix + state.caller.map((value) => CALLER_LABELS[value] ?? value).join("、");
    case "outcome":
      return prefix + (state.outcome === "success" ? "成功" : "失败");
  }
}

export function FilterBar({ supported = FILTER_DIMENSIONS, defaultRange = "24h", hideDimensions = false }: FilterBarProps) {
  const { state, setRange, setDimension, toggleValue, removeDimension, clearFilters } = useMetricFilters(defaultRange);
  const rangeQuery = rangeToQuery(state.range);

  const dimensionsQuery = useQuery({
    queryKey: queryKeys.metricDimensions(rangeQuery),
    queryFn: () => api.metricDimensions(rangeQuery),
    enabled: !hideDimensions,
  });
  const providersQuery = useQuery({
    queryKey: queryKeys.providers(),
    queryFn: api.providers,
    enabled: !hideDimensions,
  });
  const accessKeysQuery = useQuery({
    queryKey: queryKeys.accessKeys(),
    queryFn: api.accessKeys,
    enabled: !hideDimensions,
  });
  const workspacesQuery = useQuery({
    queryKey: queryKeys.workspaces(),
    queryFn: api.workspaces,
    enabled: !hideDimensions,
  });

  const accessKeyNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const key of accessKeysQuery.data?.access_keys ?? []) names.set(key.id, key.name);
    // 配置名称优先；配置里没有的历史值退回维度接口附带的 name。
    for (const key of dimensionsQuery.data?.access_keys ?? []) {
      if (key.name && !names.has(key.id)) names.set(key.id, key.name);
    }
    return names;
  }, [accessKeysQuery.data, dimensionsQuery.data]);

  const activeDimensions = FILTER_DIMENSIONS.filter((dim) => hasDimensionValue(state, dim));

  const sharedEditor = {
    supported,
    state,
    dimensions: dimensionsQuery.data,
    loading: dimensionsQuery.isPending,
    error: dimensionsQuery.error,
    onRetry: () => {
      void dimensionsQuery.refetch();
    },
    providers: providersQuery.data,
    accessKeys: accessKeysQuery.data,
    workspaces: workspacesQuery.data,
    onSetDimension: setDimension,
    onToggle: toggleValue,
  };

  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <RangePicker range={state.range} onSelect={setRange} />
      {hideDimensions ? null : (
        <>
          {supported.length ? (
            <Popover
              align="start"
              className="w-72 p-0"
              trigger={
                <Button
                  variant="secondary"
                  size="sm"
                  className="border-rule"
                  leadingIcon={<Plus aria-hidden className="size-3.5" />}
                  aria-label="添加筛选"
                >
                  添加筛选
                </Button>
              }
            >
              <FilterEditor initialDimension={null} {...sharedEditor} />
            </Popover>
          ) : null}

          {activeDimensions.map((dim) => (
            <FilterChip
              key={dim}
              dimension={dim}
              supported={supported.includes(dim)}
              text={chipText(dim, state, (id) => accessKeyNames.get(id) || id)}
              onRemove={() => removeDimension(dim)}
            >
              <FilterEditor initialDimension={dim} {...sharedEditor} />
            </FilterChip>
          ))}

          {activeDimensions.length ? (
            <Button variant="ghost" size="sm" onClick={clearFilters} aria-label="清除全部">
              清除全部
            </Button>
          ) : null}
        </>
      )}
    </div>
  );
}

function RangePicker({ range, onSelect }: { range: string; onSelect: (range: string) => void }) {
  const [open, setOpen] = useState(false);
  const [amount, setAmount] = useState("");
  const [unit, setUnit] = useState<"h" | "d">("h");

  const custom = /^(\d+)([hd])$/.exec(range);
  const trimmed = amount.trim();
  const numeric = /^\d+$/.test(trimmed);
  const parsedAmount = numeric ? Number(trimmed) : 0;
  const hours = parsedAmount * (unit === "d" ? 24 : 1);
  const valid = numeric && parsedAmount > 0 && hours <= MAX_RANGE_HOURS;
  const error = !trimmed
    ? ""
    : !numeric || parsedAmount <= 0
      ? "请输入大于 0 的整数"
      : hours > MAX_RANGE_HOURS
        ? `最多 ${MAX_RANGE_HOURS} 小时`
        : "";

  function handleOpen(next: boolean) {
    setOpen(next);
    if (next) {
      const match = /^(\d+)([hd])$/.exec(range);
      setAmount(match?.[1] ?? "");
      setUnit(match?.[2] === "d" ? "d" : "h");
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={handleOpen}
      align="start"
      className="w-64 p-0"
      trigger={
        <Button
          variant="secondary"
          size="sm"
          className="border-rule"
          leadingIcon={<Clock aria-hidden className="size-3.5" />}
          trailingIcon={<ChevronDown aria-hidden className="size-3.5" />}
          aria-label={`时间范围：${rangeLabel(range)}`}
        >
          {rangeLabel(range)}
        </Button>
      }
    >
      <div className="p-1.5">
        {RANGE_PRESETS.map((preset) => {
          const selected = preset.value === range;
          return (
            <button
              key={preset.value}
              type="button"
              aria-pressed={selected}
              onClick={() => {
                onSelect(preset.value);
                setOpen(false);
              }}
              className={cn(
                "flex w-full items-center justify-between rounded-control px-2.5 py-1.5 text-left text-meta",
                selected ? "bg-select text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
              )}
            >
              <span>{preset.label}</span>
              {selected ? <Check aria-hidden className="size-4" strokeWidth={3} /> : null}
            </button>
          );
        })}
      </div>
      <div className="border-t border-rule p-2.5">
        <p className="mb-1.5 text-meta text-ink-3">自定义</p>
        <form
          className="flex items-center gap-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            if (!valid) return;
            onSelect(`${parsedAmount}${unit}`);
            setOpen(false);
          }}
        >
          <Input
            size="sm"
            type="number"
            min={1}
            max={MAX_RANGE_HOURS}
            step={1}
            inputMode="numeric"
            value={amount}
            onChange={(event) => setAmount(event.target.value)}
            placeholder={custom ? undefined : "数值"}
            aria-label="自定义时间窗数值"
            invalid={Boolean(error)}
            className="min-w-0 flex-1"
          />
          <Select
            className="h-7 w-[4.5rem]"
            value={unit}
            onChange={(event) => setUnit(event.target.value === "d" ? "d" : "h")}
            aria-label="自定义时间窗单位"
            options={[
              { value: "h", label: "小时" },
              { value: "d", label: "天" },
            ]}
          />
          <Button size="sm" type="submit" disabled={!valid}>
            应用
          </Button>
        </form>
        {error ? <p className="mt-1 text-meta text-critical-ink">{error}</p> : null}
      </div>
    </Popover>
  );
}

interface FilterEditorProps {
  initialDimension: FilterDimension | null;
  supported: FilterDimension[];
  state: MetricFilterState;
  dimensions: MetricDimensionsResponse | undefined;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  providers: ProvidersResponse | undefined;
  accessKeys: AccessKeysResponse | undefined;
  workspaces: WorkspacesResponse | undefined;
  onSetDimension: (dim: FilterDimension, values: string[] | UpstreamKeyFilter[] | OutcomeFilter | null) => void;
  onToggle: (dim: FilterDimension, value: string) => void;
}

interface FilterOptionRow {
  key: string;
  label: string;
  search: string;
  requests: number;
  selected: boolean;
  missing: boolean;
  toggle: () => void;
}

function FilterEditor({
  initialDimension,
  supported,
  state,
  dimensions,
  loading,
  error,
  onRetry,
  providers,
  accessKeys,
  workspaces,
  onSetDimension,
  onToggle,
}: FilterEditorProps) {
  const [dimension, setDimension] = useState<FilterDimension | null>(initialDimension);
  const [search, setSearch] = useState("");
  const baseId = useId();

  const providerIds = useMemo(
    () => (providers ? new Set(providers.providers.map((provider) => provider.id)) : null),
    [providers],
  );
  const accessKeyIds = useMemo(
    () => (accessKeys ? new Set(accessKeys.access_keys.map((key) => key.id)) : null),
    [accessKeys],
  );
  const workspaceNames = useMemo(
    () => (workspaces ? new Set(workspaces.workspaces.map((workspace) => workspace.name)) : null),
    [workspaces],
  );
  const accessKeyNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const key of accessKeys?.access_keys ?? []) names.set(key.id, key.name);
    for (const key of dimensions?.access_keys ?? []) {
      if (key.name && !names.has(key.id)) names.set(key.id, key.name);
    }
    return names;
  }, [accessKeys, dimensions]);

  function optionRows(current: FilterDimension): FilterOptionRow[] {
    if (!dimensions) return [];
    switch (current) {
      case "provider":
        return dimensions.providers.map((item) => ({
          key: item.id,
          label: item.id,
          search: item.id.toLowerCase(),
          requests: item.requests,
          selected: state.provider.includes(item.id),
          missing: providerIds ? !providerIds.has(item.id) : false,
          toggle: () => onToggle("provider", item.id),
        }));
      case "model":
        return dimensions.models.map((item) => ({
          key: item.id,
          label: item.id,
          search: item.id.toLowerCase(),
          requests: item.requests,
          selected: state.model.includes(item.id),
          missing: false,
          toggle: () => onToggle("model", item.id),
        }));
      case "uk":
        return dimensions.upstream_keys.map((item) => {
          const pair: UpstreamKeyFilter = { providerId: item.provider_id, keyName: item.key_name };
          const selected = state.uk.some(
            (entry) => entry.providerId === pair.providerId && entry.keyName === pair.keyName,
          );
          return {
            key: `${item.provider_id}\u0000${item.key_name}`,
            label: `${item.provider_id} / ${item.key_name}`,
            search: `${item.provider_id} ${item.key_name}`.toLowerCase(),
            requests: item.requests,
            selected,
            missing: false,
            toggle: () =>
              onSetDimension(
                "uk",
                selected
                  ? state.uk.filter(
                      (entry) =>
                        entry.providerId !== pair.providerId || entry.keyName !== pair.keyName,
                    )
                  : [...state.uk, pair],
              ),
          };
        });
      case "ak":
        return dimensions.access_keys.map((item) => {
          const name = accessKeyNames.get(item.id) || item.name || "";
          const label = name ? `${name}(${item.id})` : item.id;
          return {
            key: item.id,
            label,
            search: `${name} ${item.id}`.toLowerCase(),
            requests: item.requests,
            selected: state.ak.includes(item.id),
            missing: accessKeyIds ? !accessKeyIds.has(item.id) : false,
            toggle: () => onToggle("ak", item.id),
          };
        });
      case "ws":
        return dimensions.workspaces.map((item) => ({
          key: item.name,
          label: item.name,
          search: item.name.toLowerCase(),
          requests: item.requests,
          selected: state.ws.includes(item.name),
          missing: workspaceNames ? !workspaceNames.has(item.name) : false,
          toggle: () => onToggle("ws", item.name),
        }));
      case "caller":
        return dimensions.caller_types.map((item) => ({
          key: item.id,
          label: CALLER_LABELS[item.id] ?? item.id,
          search: `${item.id} ${CALLER_LABELS[item.id] ?? ""}`.toLowerCase(),
          requests: item.requests,
          selected: state.caller.includes(item.id),
          missing: false,
          toggle: () => onToggle("caller", item.id),
        }));
      case "outcome":
        return [];
    }
  }

  const rows = dimension ? optionRows(dimension) : [];
  const keyword = search.trim().toLowerCase();
  const visible = keyword ? rows.filter((row) => row.search.includes(keyword)) : rows;

  if (!dimension) {
    return (
      <div className="p-1.5">
        {supported.map((dim) => (
          <button
            key={dim}
            type="button"
            onClick={() => setDimension(dim)}
            className="flex w-full items-center justify-between rounded-control px-2.5 py-1.5 text-left text-meta text-ink-2 hover:bg-hover hover:text-ink"
          >
            <span>{DIMENSION_LABELS[dim]}</span>
            <ChevronRight aria-hidden className="size-3.5 text-ink-3" />
          </button>
        ))}
        {error ? (
          <div className="mt-1 border-t border-rule px-1 pt-2">
            <p className="text-meta text-critical-ink">加载筛选项失败：{errorText(error)}</p>
            <Button size="sm" variant="secondary" className="mt-2" onClick={onRetry}>
              重试
            </Button>
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className="flex flex-col">
      <div className="flex items-center gap-1 border-b border-rule px-2 py-2">
        {initialDimension === null ? (
          <button
            type="button"
            onClick={() => {
              setDimension(null);
              setSearch("");
            }}
            aria-label="返回维度列表"
            className="-ml-1 flex size-6 items-center justify-center rounded-control text-ink-3 hover:bg-hover hover:text-ink"
          >
            <ChevronLeft aria-hidden className="size-3.5" />
          </button>
        ) : null}
        <span className="text-meta font-medium text-ink">{DIMENSION_LABELS[dimension]}</span>
      </div>

      {dimension === "outcome" ? (
        <OutcomePicker value={state.outcome} onChange={(value) => onSetDimension("outcome", value)} />
      ) : (
        <>
          <div className="p-2">
            <Input
              autoFocus
              size="sm"
              leadingIcon={<Search aria-hidden className="size-3.5" />}
              placeholder={`搜索${DIMENSION_LABELS[dimension]}`}
              aria-label={`搜索${DIMENSION_LABELS[dimension]}`}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <div className="max-h-64 overflow-y-auto px-1 pb-1">
            {loading ? (
              <p className="px-2 py-3 text-meta text-ink-3">加载中…</p>
            ) : error ? (
              <div className="px-2 py-2">
                <p className="text-meta text-critical-ink">加载筛选项失败：{errorText(error)}</p>
                <Button size="sm" variant="secondary" className="mt-2" onClick={onRetry}>
                  重试
                </Button>
              </div>
            ) : visible.length === 0 ? (
              <p className="px-2 py-3 text-meta text-ink-3">没有匹配项</p>
            ) : (
              visible.map((row, index) => {
                const id = `${baseId}-${index}`;
                return (
                  <label
                    key={row.key}
                    htmlFor={id}
                    className="flex w-full cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 hover:bg-hover"
                  >
                    <Checkbox
                      id={id}
                      checked={row.selected}
                      onCheckedChange={() => row.toggle()}
                      aria-label={row.label}
                    />
                    <span className="min-w-0 flex-1 truncate text-meta text-ink">
                      {row.label}
                      {row.missing ? <span className="text-ink-3">（已不在配置中）</span> : null}
                    </span>
                    <span className="shrink-0 text-meta tabular-nums text-ink-3">
                      {formatCount(row.requests)}
                    </span>
                  </label>
                );
              })
            )}
          </div>
        </>
      )}
    </div>
  );
}

function OutcomePicker({
  value,
  onChange,
}: {
  value: OutcomeFilter | null;
  onChange: (value: OutcomeFilter | null) => void;
}) {
  return (
    <div role="radiogroup" aria-label="请求结果" className="p-1.5">
      {OUTCOME_OPTIONS.map((option) => {
        const selected = value === option.value;
        return (
          <button
            key={option.value ?? "all"}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(option.value)}
            className={cn(
              "flex w-full items-center justify-between rounded-control px-2.5 py-1.5 text-left text-meta",
              selected ? "bg-select text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
            )}
          >
            <span>{option.label}</span>
            {selected ? <Check aria-hidden className="size-4" strokeWidth={3} /> : null}
          </button>
        );
      })}
    </div>
  );
}

function FilterChip({
  dimension,
  text,
  supported,
  onRemove,
  children,
}: {
  dimension: FilterDimension;
  text: string;
  supported: boolean;
  onRemove: () => void;
  children: ReactNode;
}) {
  const chip = (
    <span className={cn(CHIP_CLASS, supported ? "text-ink-2" : "text-ink-3")}>
      {supported ? (
        <Popover
          align="start"
          className="w-72 p-0"
          trigger={
            <button
              type="button"
              aria-label={`编辑 ${DIMENSION_LABELS[dimension]}筛选`}
              className="min-w-0 max-w-64 truncate text-left hover:text-ink"
            >
              {text}
            </button>
          }
        >
          {children}
        </Popover>
      ) : (
        <span className="min-w-0 max-w-64 truncate line-through">{text}</span>
      )}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`移除 ${DIMENSION_LABELS[dimension]}筛选`}
        className="-mr-1 flex size-4 shrink-0 items-center justify-center rounded-[3px] text-ink-3 hover:bg-hover hover:text-ink"
      >
        <X aria-hidden className="size-3" />
      </button>
    </span>
  );

  return supported ? chip : <Tooltip content={`本页不支持按 ${DIMENSION_LABELS[dimension]} 筛选`}>{chip}</Tooltip>;
}
