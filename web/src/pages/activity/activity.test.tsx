// 用量统计页的交互回归：URL 是筛选与拆分状态的唯一真相，拆分请求必须和整页
// 读数共享同一组筛选；「仅看此项」只对全局筛选支持的维度出现。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ActivityPage from "./index";

const BUCKETS = [
  { started_at: "2026-10-24T14:00:00+08:00", ended_at: "2026-10-24T15:00:00+08:00", complete: true },
  { started_at: "2026-10-24T15:00:00+08:00", ended_at: "2026-10-24T16:00:00+08:00", complete: true },
  { started_at: "2026-10-24T16:00:00+08:00", ended_at: "2026-10-24T17:00:00+08:00", complete: false },
];

const STATS = {
  requests: 120,
  successes: 110,
  failures: 10,
  retries: 2,
  prompt_tokens: 600_000,
  completion_tokens: 400_000,
  total_tokens: 1_000_000,
  cached_tokens: 100_000,
  cache_read_tokens: 100_000,
  total_duration_ms: 120_000,
  total_first_token_ms: 24_000,
};

function point(index: number) {
  return {
    ...STATS,
    started_at: BUCKETS[index].started_at,
    ended_at: BUCKETS[index].ended_at,
    complete: BUCKETS[index].complete,
    requests: [40, 50, 30][index],
    successes: [38, 48, 24][index],
    failures: [2, 2, 6][index],
    prompt_tokens: [200_000, 250_000, 150_000][index],
    completion_tokens: [130_000, 170_000, 100_000][index],
    total_tokens: [330_000, 420_000, 250_000][index],
  };
}

function series(values: number[]) {
  return {
    requests: values,
    successes: values.map((value) => Math.max(0, value - 1)),
    failures: values.map(() => 1),
    retries: values.map(() => 0),
    prompt_tokens: values.map((value) => value * 6),
    completion_tokens: values.map((value) => value * 4),
    total_tokens: values.map((value) => value * 10),
    cached_tokens: values.map((value) => value),
    duration_ms: values.map((value) => value * 1000),
    first_token_ms: values.map((value) => value * 200),
  };
}

const GROUPS = [
  {
    key: { model_id: "gpt-4o-mini", provider_id: "openai", key_name: "main", upstream_model_id: "gpt-4o-mini", status_code: 200 },
    stats: { ...STATS, requests: 80, successes: 78, failures: 2, total_tokens: 800_000, prompt_tokens: 480_000, completion_tokens: 320_000 },
    series: series([30, 35, 15]),
  },
  {
    key: { model_id: "gpt-4o", provider_id: "openai", key_name: "backup", upstream_model_id: "gpt-4o", status_code: 500 },
    stats: { ...STATS, requests: 40, successes: 32, failures: 8, total_tokens: 200_000, prompt_tokens: 120_000, completion_tokens: 80_000 },
    series: series([10, 15, 15]),
  },
];

interface Call {
  path: string;
  search: string;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetch(calls: Call[]): void {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    calls.push({ path: url.pathname, search: url.search });
    if (url.pathname === "/ui/metric-dimensions.json") {
      return json({
        window: { from: BUCKETS[0].started_at, to: BUCKETS[2].ended_at, hours: 168 },
        providers: [{ id: "openai", requests: 120 }],
        models: [{ id: "gpt-4o-mini", requests: 80 }, { id: "gpt-4o", requests: 40 }],
        requested_models: [],
        upstream_keys: [{ provider_id: "openai", key_name: "main", requests: 80 }],
        access_keys: [],
        workspaces: [],
        caller_types: [],
      });
    }
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", base_url: null }] });
    if (url.pathname === "/api/access-keys") return json({ access_keys: [], config_revision: "r" });
    if (url.pathname === "/api/workspaces") return json({ workspaces: [] });
    if (url.pathname === "/ui/filtered-metrics.json") {
      return json({
        count_semantics: "upstream_attempt",
        window: { from: BUCKETS[0].started_at, to: BUCKETS[2].ended_at, hours: 168 },
        filters: {},
        snapshot: { window: { from: BUCKETS[0].started_at, to: BUCKETS[2].ended_at, hours: 168 }, total: STATS },
        series: { bucket_seconds: 3600, points: BUCKETS.map((_, index) => point(index)) },
        requests: null,
      });
    }
    if (url.pathname === "/ui/usage-breakdown.json") {
      return json({
        count_semantics: "upstream_attempt",
        window: { from: BUCKETS[0].started_at, to: BUCKETS[2].ended_at, hours: 168 },
        group_by: url.searchParams.get("group_by"),
        rank_by: url.searchParams.get("rank_by"),
        bucket_seconds: 3600,
        buckets: BUCKETS,
        groups: GROUPS,
        other: null,
        total_groups: GROUPS.length,
      });
    }
    return json({ detail: `unexpected ${url.pathname}` }, 500);
  }));
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={client}>
      <ActivityPage />
    </QueryClientProvider>,
  );
  return { user };
}

beforeEach(() => {
  window.location.hash = "#/activity";
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("用量统计", () => {
  it("按当前筛选请求拆分，并把维度/指标/形态写回 URL", async () => {
    window.location.hash = "#/activity?model=gpt-4o-mini&by=model&metric=requests&shape=area";
    const calls: Call[] = [];
    installFetch(calls);
    const { user } = renderPage();

    await waitFor(() => {
      const breakdown = calls.find((call) => call.path === "/ui/usage-breakdown.json");
      expect(breakdown).toBeTruthy();
      const params = new URLSearchParams(breakdown?.search);
      expect(params.get("group_by")).toBe("model");
      expect(params.get("rank_by")).toBe("requests");
      expect(params.getAll("model_id")).toEqual(["gpt-4o-mini"]);
    });

    const dim = screen.getByLabelText("拆分维度");
    await user.selectOptions(dim, "status");
    await waitFor(() => expect(window.location.hash).toContain("by=status"));

    const metrics = screen.getByRole("radiogroup", { name: "拆分指标" });
    await user.click(within(metrics).getByRole("radio", { name: "Token" }));
    await waitFor(() => {
      expect(window.location.hash).toContain("metric=tokens");
      const callsWithStatus = calls.filter((call) => call.path === "/ui/usage-breakdown.json");
      const last = new URLSearchParams(callsWithStatus[callsWithStatus.length - 1]?.search);
      expect(last.get("group_by")).toBe("status");
      expect(last.get("rank_by")).toBe("tokens");
    });

    const shapes = screen.getByRole("radiogroup", { name: "拆分形态" });
    await user.click(within(shapes).getByRole("radio", { name: "100% 占比" }));
    await waitFor(() => expect(window.location.hash).toContain("shape=percent"));
    expect(within(shapes).getByRole("radio", { name: "100% 占比" })).toHaveAttribute("aria-checked", "true");
  });

  it("模型筛选下默认按上游 Key 拆，并可用「仅看此项」把 Key 加入筛选", async () => {
    window.location.hash = "#/activity?model=gpt-4o-mini";
    const calls: Call[] = [];
    installFetch(calls);
    const { user } = renderPage();

    await waitFor(() => {
      const breakdown = calls.find((call) => call.path === "/ui/usage-breakdown.json");
      const params = new URLSearchParams(breakdown?.search);
      expect(params.get("group_by")).toBe("upstream_key");
      expect(window.location.hash).toContain("by=upstream_key");
    });
    expect(await screen.findByText("main")).toBeInTheDocument();

    const actions = screen.getAllByRole("button", { name: /仅看此项/ });
    await user.click(actions[0]);
    await waitFor(() => {
      const decoded = decodeURIComponent(window.location.hash);
      expect(decoded).toContain('uk=[["openai","main"]]');
    });
  });

  it("全部历史用 all_history=true，并在跨度超过 8760 小时时提示只覆盖最近 1 年", async () => {
    window.location.hash = "#/activity?range=all";
    const calls: Call[] = [];
    installFetch(calls);
    // 把整页序列改成跨两年的两个点，服务端 all_history 的截断提示必须照常出现。
    const oldFetch = globalThis.fetch as unknown as (input: RequestInfo | URL) => Promise<Response>;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/ui/filtered-metrics.json") {
        calls.push({ path: url.pathname, search: url.search });
        return json({
          count_semantics: "upstream_attempt",
          window: { from: "2025-01-01T00:00:00+08:00", to: "2026-10-24T17:00:00+08:00", hours: null },
          filters: {},
          snapshot: { window: {}, total: STATS },
          series: {
            bucket_seconds: 86400,
            points: [
              { ...point(0), started_at: "2025-01-01T00:00:00+08:00", ended_at: "2026-01-01T00:00:00+08:00" },
              { ...point(2), started_at: "2026-01-01T00:00:00+08:00", ended_at: "2026-10-24T17:00:00+08:00" },
            ],
          },
          requests: null,
        });
      }
      return oldFetch(input);
    }));

    renderPage();
    expect(await screen.findAllByText(/仅覆盖最近 1 年/)).not.toHaveLength(0);
    await waitFor(() => {
      const all = calls.filter((call) => call.search.includes("all_history=true"));
      expect(all.length).toBeGreaterThan(0);
      const filtered = calls.find((call) => call.path === "/ui/filtered-metrics.json");
      expect(new URLSearchParams(filtered?.search).get("all_history")).toBe("true");
      expect(new URLSearchParams(filtered?.search).has("hours")).toBe(false);
    });
  });
});
