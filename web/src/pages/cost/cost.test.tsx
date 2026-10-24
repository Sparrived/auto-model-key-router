// 成本页回归：无单价不显示金额、四项成本可加总、覆盖率按 Token 算，以及目录
// 不可用时的降级页绝不出金额。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UsageBreakdownResponse } from "@/lib/types";
import { __setIndexForTest, type PriceIndex } from "@/lib/pricing";
import CostPage from "./index";
import { buildCostSummary, costSeriesForGroup, money } from "./cost-model";

const BUCKETS = [
  { started_at: "2026-10-24T15:00:00+08:00", complete: true },
  { started_at: "2026-10-24T16:00:00+08:00", complete: false },
];

function series(prompt: number[], completion: number[], cached: number[]) {
  return {
    requests: prompt.map(() => 10),
    successes: prompt.map(() => 9),
    failures: prompt.map(() => 1),
    retries: prompt.map(() => 0),
    prompt_tokens: prompt,
    completion_tokens: completion,
    total_tokens: prompt.map((value, index) => value + completion[index]),
    cached_tokens: cached,
    duration_ms: prompt.map(() => 1000),
    first_token_ms: prompt.map(() => 100),
  };
}

const BREAKDOWN: UsageBreakdownResponse = {
  count_semantics: "upstream_attempt",
  window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
  group_by: "upstream_model",
  rank_by: "tokens",
  bucket_seconds: 3600,
  buckets: BUCKETS,
  total_groups: 2,
  other: null,
  groups: [
    {
      key: { provider_id: "openai", upstream_model_id: "gpt-4o-mini" },
      stats: {
        requests: 20,
        successes: 18,
        failures: 2,
        prompt_tokens: 1_000_000,
        completion_tokens: 1_000_000,
        total_tokens: 2_000_000,
        cached_tokens: 200_000,
        cache_read_tokens: 200_000,
        total_duration_ms: 20_000,
        total_first_token_ms: 2_000,
      },
      series: series([500_000, 500_000], [500_000, 500_000], [100_000, 100_000]),
    },
    {
      key: { provider_id: "deepseek", upstream_model_id: "mystery-model" },
      stats: {
        requests: 5,
        successes: 5,
        failures: 0,
        prompt_tokens: 250_000,
        completion_tokens: 250_000,
        total_tokens: 500_000,
      },
      series: series([125_000, 125_000], [125_000, 125_000], [0, 0]),
    },
  ],
};

function pricingIndex(): PriceIndex {
  return {
    models: new Map([
      ["gpt-4o-mini", { input: 0.15, output: 0.6, cacheRead: 0.075, cacheWrite: null }],
    ]),
    updatedAt: "2026-10-24T08:52:21Z",
    error: null,
    source: "https://models.dev/api.json",
  };
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetch(calls: Array<{ path: string; search: string }>): void {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({ path: url.pathname, search: url.search });
    if (url.pathname === "/ui/metric-dimensions.json") {
      return json({
        window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
        providers: [{ id: "openai", requests: 20 }, { id: "deepseek", requests: 5 }],
        models: [],
        requested_models: [],
        upstream_keys: [],
        access_keys: [],
        workspaces: [],
        caller_types: [],
      });
    }
    if (url.pathname === "/api/providers") {
      return json({ providers: [{ id: "openai", base_url: null }, { id: "deepseek", base_url: null }] });
    }
    if (url.pathname === "/api/access-keys") return json({ access_keys: [], config_revision: "r" });
    if (url.pathname === "/api/workspaces") return json({ workspaces: [] });
    if (url.pathname === "/ui/pricing.json") {
      return json({
        version: 1,
        source: "https://models.dev/api.json",
        updated_at: "2026-10-24T08:52:21Z",
        models: { "gpt-4o-mini": { input: 0.15, output: 0.6, cache_read: 0.075 } },
      });
    }
    if (url.pathname === "/ui/filtered-metrics.json") {
      return json({
        count_semantics: "upstream_attempt",
        window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
        filters: {},
        snapshot: {
          window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
          total: {
            requests: 25,
            successes: 23,
            failures: 2,
            prompt_tokens: 1_250_000,
            completion_tokens: 1_250_000,
            total_tokens: 2_500_000,
            cached_tokens: 200_000,
          },
        },
        series: { bucket_seconds: 3600, points: [] },
        requests: {
          items: [
            {
              id: 1,
              created_at: "2026-10-24T16:50:00+08:00",
              model_id: "gpt-4o-mini",
              upstream_model_id: "gpt-4o-mini",
              total_tokens: 1000,
              prompt_tokens: 800,
              completion_tokens: 200,
              cached_tokens: 100,
            },
            {
              id: 2,
              created_at: "2026-10-24T16:49:00+08:00",
              model_id: "mystery-model",
              upstream_model_id: "mystery-model",
              total_tokens: 500,
              prompt_tokens: 400,
              completion_tokens: 100,
            },
          ],
        },
      });
    }
    if (url.pathname === "/ui/usage-breakdown.json") {
      return json(BREAKDOWN);
    }
    return json({ detail: `unexpected ${url.pathname}` }, 500);
  }));
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={client}>
      <CostPage />
    </QueryClientProvider>,
  );
  return { user };
}

beforeEach(() => {
  window.location.hash = "#/cost?range=24h";
  localStorage.clear();
  __setIndexForTest(null);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  localStorage.clear();
  __setIndexForTest(null);
  window.location.hash = "";
});

describe("成本口径", () => {
  it("总额等于四部分之和，覆盖率按 Token 计算，零成本显示破折号", () => {
    const summary = buildCostSummary(pricingIndex(), BREAKDOWN, 2_500_000, 24);
    expect(summary.totalCost).toBeCloseTo(0.735, 10);
    expect(summary.parts.input + summary.parts.cacheRead + summary.parts.cacheWrite + summary.parts.output)
      .toBeCloseTo(summary.totalCost, 10);
    expect(summary.coverage).toBeCloseTo(0.8, 10);
    expect(summary.pricedRows).toHaveLength(1);
    expect(summary.unpricedRows).toHaveLength(1);
    expect(money(0)).toBe("—");
    expect(money(null)).toBe("—");
  });

  it("逐桶成本使用同一套价格，而不是把总额平均到桶", () => {
    const group = BREAKDOWN.groups[0];
    const values = costSeriesForGroup(group, pricingIndex().models.get("gpt-4o-mini") ?? null, BUCKETS);
    // 每桶 500k 输入（其中 100k 缓存命中）+ 500k 输出：0.06 + 0.0075 + 0.3。
    expect(values).toHaveLength(2);
    expect(values[0]).toBeCloseTo(0.3675, 10);
    expect(values[1]).toBeCloseTo(0.3675, 10);
  });
});

describe("成本页", () => {
  it("有目录时展示金额、覆盖率与未计价模型，行操作按供应商收窄", async () => {
    const calls: Array<{ path: string; search: string }> = [];
    installFetch(calls);
    const { user } = renderPage();

    expect(await screen.findByText("估算成本（窗口）")).toBeInTheDocument();
    expect(screen.getAllByText("$0.7350").length).toBeGreaterThan(0);
    expect(screen.getByText("80.0%")).toBeInTheDocument();
    const unpricedRow = await screen.findByRole("row", { name: /mystery-model/ });
    expect(unpricedRow.textContent).toContain("—");
    expect(unpricedRow.textContent).not.toContain("$0");
    expect(calls.some((call) => call.path === "/ui/usage-breakdown.json"
      && new URLSearchParams(call.search).get("group_by") === "upstream_model")).toBe(true);

    const pricedRow = screen.getByRole("row", { name: /gpt-4o-mini/ });
    await user.click(within(pricedRow).getAllByRole("button", { name: "仅看此供应商" })[0]);
    await waitFor(() => expect(window.location.hash).toContain("provider=openai"));
  });

  it("价格目录 503 时只显示 Token 读数，全页不出现金额", async () => {
    const calls: Array<{ path: string; search: string }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      calls.push({ path: url.pathname, search: url.search });
      if (url.pathname === "/ui/pricing.json") return json({ detail: "价格目录还在获取" }, 503);
      if (url.pathname === "/ui/metric-dimensions.json") {
        return json({
          window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
          providers: [], models: [], requested_models: [], upstream_keys: [], access_keys: [], workspaces: [], caller_types: [],
        });
      }
      if (url.pathname === "/api/providers") return json({ providers: [] });
      if (url.pathname === "/api/access-keys") return json({ access_keys: [], config_revision: "r" });
      if (url.pathname === "/api/workspaces") return json({ workspaces: [] });
      if (url.pathname === "/ui/filtered-metrics.json") {
        return json({
          count_semantics: "upstream_attempt",
          window: { from: BUCKETS[0].started_at, to: BUCKETS[1].started_at, hours: 24 },
          filters: {},
          snapshot: {
            window: {},
            total: { requests: 1, prompt_tokens: 600, completion_tokens: 400, total_tokens: 1000, cached_tokens: 0 },
          },
          series: { bucket_seconds: 3600, points: [] },
          requests: { items: [] },
        });
      }
      if (url.pathname === "/ui/usage-breakdown.json") return json({ ...BREAKDOWN, groups: [], total_groups: 0 });
      return json({ detail: `unexpected ${url.pathname}` }, 500);
    }));

    renderPage();
    expect(await screen.findByText("价格目录暂不可用")).toBeInTheDocument();
    expect(screen.getByText("总 Token")).toBeInTheDocument();
    expect(screen.queryByText("估算成本（窗口）")).not.toBeInTheDocument();
    expect(document.body.textContent ?? "").not.toContain("$0");
  });
});
