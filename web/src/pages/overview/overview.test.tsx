// 概览页验收：成本缺失时不能伪装成 $0、筛选必须传到同一组读数、实时流计数与筛选
// 必须来自真实返回的请求行。测试通过用户可见的控件操作，不读组件内部状态。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { __setIndexForTest } from "@/lib/pricing";
import type { FilteredMetricsResponse, RequestItem, StatusMonitorResponse, UsageBreakdownResponse } from "@/lib/types";
import OverviewPage from "./index";

interface Call {
  path: string;
  search: URLSearchParams;
}

const STREAM_ITEMS: RequestItem[] = [
  { id: 1, requests: 1, created_at: "2026-10-24T17:20:00+08:00", model_id: "gpt-4o-mini", provider_id: "openai", key_name: "main", success: true, status_code: 200, stream: true, api_format: "chat/completions", prompt_tokens: 100, completion_tokens: 20, total_tokens: 120, duration_ms: 500, first_token_ms: 120 },
  { id: 2, requests: 1, created_at: "2026-10-24T17:19:00+08:00", model_id: "gpt-4o", provider_id: "openai", key_name: "main", success: true, status_code: 200, stream: false, api_format: "chat/completions", prompt_tokens: 200, completion_tokens: 40, total_tokens: 240, duration_ms: 800, first_token_ms: 300 },
  { id: 3, requests: 1, created_at: "2026-10-24T17:18:00+08:00", model_id: "deepseek-chat", provider_id: "deepseek", key_name: "legacy", success: true, status_code: 200, retried: true, stream: true, api_format: "chat/completions", prompt_tokens: 80, completion_tokens: 10, total_tokens: 90, duration_ms: 400, first_token_ms: 100 },
  { id: 4, requests: 1, created_at: "2026-10-24T17:17:00+08:00", model_id: "deepseek-chat", provider_id: "deepseek", key_name: "legacy", success: false, status_code: 401, stream: false, api_format: "chat/completions", duration_ms: 50, first_token_ms: 0 },
  { id: 5, requests: 1, created_at: "2026-10-24T17:16:00+08:00", model_id: "claude-sonnet-4", provider_id: "anthropic", key_name: "team", success: false, status_code: 500, stream: false, api_format: "messages", duration_ms: 700, first_token_ms: 0 },
];

function stats(overrides: Record<string, unknown> = {}) {
  return {
    requests: 10,
    successes: 9,
    failures: 1,
    prompt_tokens: 1000,
    completion_tokens: 200,
    total_tokens: 1200,
    cached_tokens: 200,
    total_first_token_ms: 2000,
    total_duration_ms: 5000,
    ...overrides,
  };
}

function seriesPoint(started_at: string, overrides: Record<string, unknown> = {}) {
  return { started_at, ended_at: started_at, complete: true, ...stats(overrides) };
}

function mainResponse(): FilteredMetricsResponse {
  return {
    window: { from: "2026-10-24T11:00:00+08:00", to: "2026-10-24T17:00:00+08:00", hours: 6 },
    filters: {},
    snapshot: {
      window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 6 },
      current_rpm: 12,
      current_tpm: 3456,
      active_requests: 2,
      total: stats(),
      model_requested_models: {
        "gpt-4o-mini": { "gpt-4o-mini": stats({ requests: 8 }) },
        "claude-sonnet-4": { "unified-model": stats({ requests: 2 }) },
      },
    },
    series: { bucket_seconds: 60, points: [seriesPoint("2026-10-24T16:00:00+08:00"), seriesPoint("2026-10-24T16:01:00+08:00")] },
    requests: null,
  } as unknown as FilteredMetricsResponse;
}

function streamResponse(items = STREAM_ITEMS): FilteredMetricsResponse {
  return { ...mainResponse(), requests: { items, next_before_id: null } };
}

function statusResponse(): StatusMonitorResponse {
  return {
    window: { from: "2026-10-24T11:00:00+08:00", to: "2026-10-24T17:00:00+08:00", hours: 6 },
    filters: {},
    group_by: "provider_key",
    bucket_seconds: 60,
    snapshot: { window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 6 }, total: stats() },
    failure_status_codes: { "401": 5, "500": 3 },
    series: { bucket_seconds: 60, points: [seriesPoint("2026-10-24T16:00:00+08:00")] },
    matrix: {
      group_by: "provider_key",
      rows_truncated: 0,
      rows: [
        { provider_id: "deepseek", upstream_model_id: null, key_name: "legacy", cells: [seriesPoint("2026-10-24T16:00:00+08:00", { requests: 5, successes: 1, failures: 4 })] },
        { provider_id: "openai", upstream_model_id: null, key_name: "main", cells: [seriesPoint("2026-10-24T16:00:00+08:00", { requests: 20, successes: 20, failures: 0 })] },
      ],
    },
  } as unknown as StatusMonitorResponse;
}

function breakdownResponse(): UsageBreakdownResponse {
  return {
    buckets: [{ started_at: "2026-10-24T16:00:00+08:00", complete: true }],
    groups: [
      { key: "gpt-4o-mini", stats: stats({ requests: 8, successes: 8, failures: 0 }), series: { requests: [8] } },
      { key: "claude-sonnet-4", stats: stats({ requests: 2, successes: 1, failures: 1 }), series: { requests: [2] } },
    ],
    other: null,
    total_groups: 2,
  } as unknown as UsageBreakdownResponse;
}

function installFetch(options: { pricing?: boolean } = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({ path: url.pathname, search: url.searchParams });
    if (url.pathname === "/ui/metric-dimensions.json") {
      return json({
        window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 6 },
        providers: [{ id: "openai", name: "OpenAI", requests: 20 }],
        models: [{ id: "gpt-4o-mini", requests: 8 }],
        requested_models: [], upstream_keys: [], workspaces: [], caller_types: [], access_keys: [],
      });
    }
    if (url.pathname === "/ui/filtered-metrics.json") {
      const limit = Number(url.searchParams.get("limit") || 0);
      const hours = Number(url.searchParams.get("hours") || 0);
      if (limit === 200) return json({ ...streamResponse(STREAM_ITEMS.filter((item) => !item.success)), snapshot: { ...mainResponse().snapshot, total: stats({ requests: 2, successes: 0, failures: 2 }) } });
      if (limit === 40) return json(streamResponse());
      if (hours === 1) return json(mainResponse());
      return json(mainResponse());
    }
    if (url.pathname === "/ui/status-monitor.json") return json(statusResponse());
    if (url.pathname === "/ui/key-usage.json") {
      return json({
        window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 6 },
        upstream_keys: [], access_keys: [], unattributed: stats({ requests: 0 }),
        model_keys: [
          { model_id: "gpt-4o-mini", provider_id: "openai", key_name: "main", stats: stats({ requests: 8 }) },
          { model_id: "claude-sonnet-4", provider_id: "anthropic", key_name: "team", stats: stats({ requests: 2 }) },
        ],
      });
    }
    if (url.pathname === "/ui/usage-breakdown.json") return json(breakdownResponse());
    if (url.pathname === "/ui/pricing.json") {
      if (options.pricing) {
        return json({ version: 1, updated_at: "2026-10-24T00:00:00Z", models: { "gpt-4o-mini": { input: 1, output: 2 } } });
      }
      return json({ detail: "not configured" }, 404);
    }
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", base_url: "https://api.openai.com" }, { id: "anthropic", base_url: "https://api.anthropic.com" }, { id: "deepseek", base_url: "https://api.deepseek.com" }] });
    return json({ detail: "not found" }, 404);
  }));
  return calls;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><OverviewPage /></QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
  __setIndexForTest(null);
  window.location.hash = "#/overview";
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("概览页", () => {
  it("价格目录不可用时估算成本显示 —，不显示 $0", async () => {
    installFetch();
    renderPage();
    expect(await screen.findByText("路由流向")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("价格目录尚未就绪")).toBeInTheDocument());
    const tile = screen.getByText("估算成本").parentElement;
    expect(tile).not.toBeNull();
    expect(within(tile as HTMLElement).getByText("—")).toBeInTheDocument();
    expect(screen.queryByText("$0")).not.toBeInTheDocument();
  });

  it("URL 里的筛选条件传到概览所有数据面板", async () => {
    const calls = installFetch();
    window.location.hash = "#/overview?range=6h&provider=openai&model=gpt-4o-mini&outcome=failure";
    renderPage();
    expect(await screen.findByText("路由流向")).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((call) => call.path === "/ui/status-monitor.json").length).toBeGreaterThan(0));
    const panelCalls = calls.filter((call) =>
      ["/ui/filtered-metrics.json", "/ui/status-monitor.json", "/ui/key-usage.json", "/ui/usage-breakdown.json"].includes(call.path),
    );
    expect(panelCalls.length).toBeGreaterThanOrEqual(4);
    for (const call of panelCalls) {
      expect(call.search.get("provider_id")).toBe("openai");
      expect(call.search.get("model_id")).toBe("gpt-4o-mini");
      expect(call.search.get("success")).toBe("false");
    }
  });

  it("实时请求流按返回行计数，失败页签只留下失败行", async () => {
    installFetch();
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("实时请求流")).toBeInTheDocument();
    const list = await screen.findByRole("list", { name: "实时请求流" });
    expect(screen.getByRole("radio", { name: "全部 5" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "成功 3" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "失败 2" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "重试 1" })).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "失败 2" }));
    await waitFor(() => expect(within(list).getAllByRole("button")).toHaveLength(2));
    expect(screen.getByText("失败 · HTTP 401")).toBeInTheDocument();
    expect(screen.queryByText("成功 · HTTP 200")).not.toBeInTheDocument();
  });
});
