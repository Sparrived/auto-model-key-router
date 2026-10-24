// 状态监控验收：矩阵窗口固定、着色切换不发请求、失败原因七档顺序、渠道抽屉
// 带上游 Key 筛选。断言的是用户操作后真实发生的网络请求与可见文本。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import type { FilteredMetricsResponse, StatusMonitorResponse } from "@/lib/types";
import StatusPage from "./index";

interface Call {
  path: string;
  search: URLSearchParams;
}

function stats(overrides: Record<string, unknown> = {}) {
  return {
    requests: 20,
    successes: 18,
    failures: 2,
    prompt_tokens: 2000,
    cached_tokens: 500,
    total_first_token_ms: 3000,
    total_duration_ms: 8000,
    ...overrides,
  };
}

function point(started_at: string, overrides: Record<string, unknown> = {}) {
  return { started_at, ended_at: started_at, complete: true, ...stats(overrides) };
}

function statusResponse(groupBy: string): StatusMonitorResponse {
  const rows = groupBy === "provider_key"
    ? [
        { provider_id: "openai", upstream_model_id: null, key_name: "main", cells: [point("2026-10-24T16:00:00+08:00", { requests: 20, successes: 20, failures: 0 })] },
        { provider_id: "deepseek", upstream_model_id: null, key_name: "legacy", cells: [point("2026-10-24T16:00:00+08:00", { requests: 5, successes: 1, failures: 4 })] },
      ]
    : [
        { provider_id: "openai", upstream_model_id: null, key_name: null, cells: [point("2026-10-24T16:00:00+08:00", { requests: 20, successes: 20, failures: 0 })] },
        { provider_id: "deepseek", upstream_model_id: null, key_name: null, cells: [point("2026-10-24T16:00:00+08:00", { requests: 5, successes: 1, failures: 4 })] },
      ];
  return {
    window: { from: "2026-10-24T16:00:00+08:00", to: "2026-10-24T17:00:00+08:00", hours: 24 },
    filters: {},
    group_by: groupBy,
    bucket_seconds: 900,
    snapshot: {
      window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 24 },
      total: stats({ requests: 100, successes: 80, failures: 20, cached_tokens: 5000 }),
    },
    failure_status_codes: { "401": 8, "429": 3, "500": 5, "400": 2, "418": 1, "200": 1 },
    series: { bucket_seconds: 180, points: [point("2026-10-24T16:00:00+08:00")] },
    matrix: { group_by: groupBy, rows_truncated: 0, rows },
  } as unknown as StatusMonitorResponse;
}

function breakdownResponse() {
  return {
    buckets: [{ started_at: "2026-10-24T16:00:00+08:00", complete: true }],
    groups: [{ key: "gpt-4o-mini", stats: stats({ requests: 20, successes: 20, failures: 0 }), series: { requests: [20] } }],
    other: null,
    total_groups: 1,
  };
}

function filteredResponse(): FilteredMetricsResponse {
  return {
    window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 24 },
    filters: {},
    snapshot: { window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 24 }, total: stats({ requests: 2, successes: 0, failures: 2 }) },
    series: { bucket_seconds: 180, points: [point("2026-10-24T16:00:00+08:00", { requests: 2, successes: 0, failures: 2 })] },
    requests: {
      items: [
        { id: 1, created_at: "2026-10-24T16:59:00+08:00", model_id: "deepseek-chat", provider_id: "deepseek", key_name: "legacy", success: false, status_code: 401, duration_ms: 30 },
      ],
      next_before_id: null,
    },
  } as unknown as FilteredMetricsResponse;
}

function installFetch(): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({ path: url.pathname, search: url.searchParams });
    if (url.pathname === "/ui/metric-dimensions.json") {
      return json({ window: { from: null, to: "2026-10-24T17:00:00+08:00", hours: 24 }, providers: [], models: [], requested_models: [], upstream_keys: [], workspaces: [], caller_types: [], access_keys: [] });
    }
    if (url.pathname === "/ui/status-monitor.json") {
      return json(statusResponse(String(url.searchParams.get("group_by") || "provider")));
    }
    if (url.pathname === "/ui/usage-breakdown.json") return json(breakdownResponse());
    if (url.pathname === "/ui/filtered-metrics.json") return json(filteredResponse());
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", base_url: "https://api.openai.com" }, { id: "deepseek", base_url: "https://api.deepseek.com" }] });
    return json({ detail: "not found" }, 404);
  }));
  return calls;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ConfirmProvider>
        <StatusPage />
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  cleanup();
  window.location.hash = "#/status";
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("状态监控页", () => {
  it("健康矩阵固定请求 12 小时 / 15 分钟桶，切换着色口径不重取", async () => {
    const calls = installFetch();
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("渠道健康矩阵")).toBeInTheDocument();
    const matrixCalls = () => calls.filter((call) => call.path === "/ui/status-monitor.json" && call.search.get("hours") === "12");
    await waitFor(() => expect(matrixCalls()).toHaveLength(1));
    expect(matrixCalls()[0].search.get("bucket_seconds")).toBe("900");
    expect(matrixCalls()[0].search.get("group_by")).toBe("provider");

    const before = matrixCalls().length;
    await user.click(screen.getByRole("radio", { name: "错误率" }));
    await waitFor(() => expect(screen.getByRole("radio", { name: "错误率" })).toHaveAttribute("aria-checked", "true"));
    expect(matrixCalls()).toHaveLength(before);
  });

  it("失败原因固定七档并按既定顺序展示", async () => {
    installFetch();
    renderPage();
    expect(await screen.findByText("渠道健康矩阵")).toBeInTheDocument();
    expect(await screen.findByText("认证失败")).toBeInTheDocument();
    const panel = screen.getByRole("heading", { name: "失败原因" }).closest("section");
    expect(panel).not.toBeNull();
    const labels = within(panel as HTMLElement).getAllByText(/^(认证失败|限流|请求错误|上游故障|其它 4xx|响应异常 \/ 2xx 失败|未收到上游状态码)$/);
    expect(labels.map((label) => label.textContent)).toEqual([
      "认证失败", "限流", "请求错误", "上游故障", "其它 4xx", "响应异常 / 2xx 失败", "未收到上游状态码",
    ]);
  });

  it("打开供应商·Key 行详情时，最近失败请求带 upstream_key 筛选", async () => {
    const calls = installFetch();
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("渠道健康矩阵")).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "供应商·Key" }));
    const rowLabel = await screen.findByText("openai / main");
    await user.click(rowLabel);
    expect(await screen.findByRole("heading", { name: "渠道详情" })).toBeInTheDocument();
    await waitFor(() => {
      const detail = calls.find((call) =>
        call.path === "/ui/filtered-metrics.json"
        && call.search.get("limit") === "200"
        && call.search.get("success") === "false",
      );
      expect(detail).toBeDefined();
      expect(JSON.parse(detail?.search.get("upstream_key") || "[]")).toEqual(["openai", "main"]);
    });
  });
});
