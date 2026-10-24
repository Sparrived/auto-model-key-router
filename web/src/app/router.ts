import { useMemo, useSyncExternalStore } from "react";
import { ALL_PAGES, pageById } from "./pages/registry";

export interface Route {
  page: string;
  params: URLSearchParams;
}

export type NavigateParams =
  | URLSearchParams
  | Record<string, string | number | boolean | null | undefined>;

export interface NavigateOptions {
  replace?: boolean;
}

const PAGE_IDS = new Set(ALL_PAGES.map((page) => page.id));

// #/usage 是旧深链，页面 id 与标题都统一到 activity。
export function normalizePage(page: string): string {
  const id = page.trim().replace(/^\/+/, "");
  if (id === "usage") return "activity";
  if (id === "" || !PAGE_IDS.has(id)) return "overview";
  return id;
}

export function parseHash(hash: string): Route {
  const raw = hash.replace(/^#/, "").replace(/^\/+/, "");
  const queryAt = raw.indexOf("?");
  const pagePart = queryAt >= 0 ? raw.slice(0, queryAt) : raw;
  const query = queryAt >= 0 ? raw.slice(queryAt + 1) : "";
  let decoded = "";
  try {
    decoded = decodeURIComponent(pagePart);
  } catch {
    // 手工拼出的坏链接不应让整个控制台崩掉；按未知页面回落到概览。
    decoded = "";
  }
  return {
    page: normalizePage(decoded),
    params: new URLSearchParams(query),
  };
}

export function routeHash(page: string, params?: URLSearchParams): string {
  const query = params?.toString() ?? "";
  return `#/${normalizePage(page)}${query ? `?${query}` : ""}`;
}

function toParams(params: NavigateParams | undefined): URLSearchParams | null {
  if (params === undefined) return null;
  if (params instanceof URLSearchParams) return new URLSearchParams(params);
  const next = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === null || value === undefined || value === "") continue;
    next.set(key, String(value));
  }
  return next;
}

function currentRoute(): Route {
  return parseHash(window.location.hash);
}

function emitRouteChange(): void {
  window.dispatchEvent(new Event("hashchange"));
}

// 监控页共享同一组筛选条件：从概览切到用量统计时，用户刚选的供应商/模型不能丢。
function preserveMonitoringParams(target: string): URLSearchParams {
  const current = currentRoute();
  const targetDef = pageById(target);
  const currentDef = pageById(current.page);
  return targetDef.monitoring && currentDef.monitoring
    ? new URLSearchParams(current.params)
    : new URLSearchParams();
}

export function navigate(page: string, params?: NavigateParams, { replace = false }: NavigateOptions = {}): void {
  const target = normalizePage(page);
  const explicit = toParams(params);
  const nextParams = explicit ?? preserveMonitoringParams(target);
  const hash = routeHash(target, nextParams);
  if (window.location.hash === hash) return;

  if (replace) {
    const url = `${window.location.pathname}${window.location.search}${hash}`;
    window.history.replaceState(null, "", url);
    emitRouteChange();
    return;
  }
  window.location.hash = hash;
}

export type ParamsUpdater = (params: URLSearchParams) => URLSearchParams | void;

export function setParams(updater: ParamsUpdater, { replace = false }: NavigateOptions = {}): void {
  const route = currentRoute();
  const next = new URLSearchParams(route.params);
  const result = updater(next);
  const params = result ?? next;
  const hash = routeHash(route.page, params);
  if (window.location.hash === hash) return;

  if (replace) {
    const url = `${window.location.pathname}${window.location.search}${hash}`;
    window.history.replaceState(null, "", url);
    emitRouteChange();
    return;
  }
  window.location.hash = hash;
}

function subscribe(listener: () => void): () => void {
  window.addEventListener("hashchange", listener);
  return () => window.removeEventListener("hashchange", listener);
}

function getSnapshot(): string {
  return window.location.hash;
}

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  return useMemo(() => parseHash(hash), [hash]);
}
