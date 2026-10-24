// 筛选状态的读写入口：URL 是唯一真相，所有变更都用 replace，避免每次勾选都留下
// 一条浏览器历史。

import { useCallback, useMemo } from "react";
import { setParams, useRoute } from "@/app/router";
import {
  parseFilters,
  writeFilters,
  type FilterDimension,
  type MetricFilterState,
  type OutcomeFilter,
  type UpstreamKeyFilter,
} from "../filters";

export interface MetricFiltersController {
  state: MetricFilterState;
  setRange(range: string): void;
  setDimension(dim: FilterDimension, values: string[] | UpstreamKeyFilter[] | OutcomeFilter | null): void;
  toggleValue(dim: FilterDimension, value: string): void;
  removeDimension(dim: FilterDimension): void;
  clearFilters(): void;
}

function isUpstreamKey(value: unknown): value is UpstreamKeyFilter {
  if (!value || typeof value !== "object") return false;
  const pair = value as { providerId?: unknown; keyName?: unknown };
  return typeof pair.providerId === "string" && typeof pair.keyName === "string";
}

function stringList(values: string[] | UpstreamKeyFilter[] | OutcomeFilter | null): string[] {
  return Array.isArray(values) ? values.filter((value): value is string => typeof value === "string") : [];
}

function withDimension(
  state: MetricFilterState,
  dim: FilterDimension,
  values: string[] | UpstreamKeyFilter[] | OutcomeFilter | null,
): MetricFilterState {
  switch (dim) {
    case "provider":
      return { ...state, provider: stringList(values) };
    case "model":
      return { ...state, model: stringList(values) };
    case "ak":
      return { ...state, ak: stringList(values) };
    case "ws":
      return { ...state, ws: stringList(values) };
    case "caller":
      return { ...state, caller: stringList(values) };
    case "uk":
      return { ...state, uk: Array.isArray(values) ? values.filter(isUpstreamKey) : [] };
    case "outcome":
      return {
        ...state,
        outcome: values === "success" || values === "failure" ? values : null,
      };
  }
}

function toggleStringDimension(
  state: MetricFilterState,
  dim: "provider" | "model" | "ak" | "ws" | "caller",
  value: string,
): MetricFilterState {
  const current = state[dim];
  const next = current.includes(value)
    ? current.filter((item) => item !== value)
    : [...current, value];
  switch (dim) {
    case "provider":
      return { ...state, provider: next };
    case "model":
      return { ...state, model: next };
    case "ak":
      return { ...state, ak: next };
    case "ws":
      return { ...state, ws: next };
    case "caller":
      return { ...state, caller: next };
  }
}

function withoutDimension(state: MetricFilterState, dim: FilterDimension): MetricFilterState {
  switch (dim) {
    case "provider":
      return { ...state, provider: [] };
    case "model":
      return { ...state, model: [] };
    case "uk":
      return { ...state, uk: [] };
    case "ak":
      return { ...state, ak: [] };
    case "ws":
      return { ...state, ws: [] };
    case "caller":
      return { ...state, caller: [] };
    case "outcome":
      return { ...state, outcome: null };
  }
}

function clearedState(state: MetricFilterState): MetricFilterState {
  return {
    range: state.range,
    provider: [],
    model: [],
    uk: [],
    ak: [],
    ws: [],
    caller: [],
    outcome: null,
  };
}

function parseUpstreamKey(value: string): UpstreamKeyFilter | null {
  try {
    const parsed: unknown = JSON.parse(value);
    if (Array.isArray(parsed) && typeof parsed[0] === "string" && typeof parsed[1] === "string") {
      return { providerId: parsed[0], keyName: parsed[1] };
    }
  } catch {
    // toggleValue 的字符串形态对 uk 是 JSON 数组；其它写法忽略。
  }
  return null;
}

export function useMetricFilters(defaultRange = "24h"): MetricFiltersController {
  const { params } = useRoute();
  const state = useMemo(() => parseFilters(params, defaultRange), [params, defaultRange]);

  const update = useCallback(
    (change: (current: MetricFilterState) => MetricFilterState) => {
      // 更新函数里重新解析一次：连续点击时闭包里的 state 可能已经落后于 URL。
      setParams(
        (current) => writeFilters(current, change(parseFilters(current, defaultRange))),
        { replace: true },
      );
    },
    [defaultRange],
  );

  const setRange = useCallback(
    (range: string) => update((current) => ({ ...current, range })),
    [update],
  );

  const setDimension = useCallback(
    (dim: FilterDimension, values: string[] | UpstreamKeyFilter[] | OutcomeFilter | null) => {
      update((current) => withDimension(current, dim, values));
    },
    [update],
  );

  const toggleValue = useCallback(
    (dim: FilterDimension, value: string) => {
      if (dim === "uk") {
        const pair = parseUpstreamKey(value);
        if (!pair) return;
        update((current) => {
          const exists = current.uk.some(
            (item) => item.providerId === pair.providerId && item.keyName === pair.keyName,
          );
          return {
            ...current,
            uk: exists
              ? current.uk.filter(
                  (item) => item.providerId !== pair.providerId || item.keyName !== pair.keyName,
                )
              : [...current.uk, pair],
          };
        });
        return;
      }
      if (dim === "outcome") {
        update((current) => {
          const next: OutcomeFilter | null =
            value === "success" || value === "failure" ? value : null;
          return { ...current, outcome: current.outcome === next ? null : next };
        });
        return;
      }
      update((current) => toggleStringDimension(current, dim, value));
    },
    [update],
  );

  const removeDimension = useCallback(
    (dim: FilterDimension) => update((current) => withoutDimension(current, dim)),
    [update],
  );

  const clearFilters = useCallback(
    () => update(clearedState),
    [update],
  );

  return { state, setRange, setDimension, toggleValue, removeDimension, clearFilters };
}

/** 图表/表格的「仅看此项」：把一个值加进对应维度，不动其它参数与时间窗。 */
export function useSelectFilter(): (dimension: FilterDimension, value: string) => void {
  return useCallback((dimension: FilterDimension, value: string) => {
    // uk 需要 provider + key 两个值，不能从单个字符串还原；调用方用 setDimension。
    if (dimension === "uk" || !value) return;

    setParams((current) => {
      const next = new URLSearchParams(current);
      if (dimension === "outcome") {
        if (value === "success" || value === "failure") next.set("outcome", value);
        return next;
      }
      const exists = next
        .getAll(dimension)
        .some((raw) => raw.split(",").map((part) => part.trim()).includes(value));
      if (!exists) next.append(dimension, value);
      return next;
    }, { replace: true });
  }, []);
}
