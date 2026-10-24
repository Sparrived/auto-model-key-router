// 工作空间页验收：明细行点击必须把工作空间名带进任务路由深链。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import WorkspacesPage from "./index";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><WorkspacesPage /></QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  window.location.hash = "#/workspaces";
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/ui/workspace-usage.json") {
      return json({
        count_semantics: "按请求的工作空间归属计数",
        window: { from: "2026-10-17T00:00:00+08:00", to: "2026-10-24T00:00:00+08:00", hours: 168 },
        workspaces: [
          { name: "team-a", stats: { requests: 80, successes: 76, failures: 4, total_tokens: 123456 } },
          { name: "team-b", stats: { requests: 20, successes: 20, failures: 0, total_tokens: 2000 } },
        ],
        unattributed: { requests: 5 },
        layers: ["workspace", "requested_model_id", "model_id", "provider_id", "key_name", "upstream_model_id"],
        links: [
          { source_layer: 0, target_layer: 1, source: "team-a", target: "gpt-4o", requests: 80, total_tokens: 123456 },
        ],
      });
    }
    return json({ detail: "not found" }, 404);
  }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("工作空间页", () => {
  it("明细行点击跳转到 tasks 并携带 ws 参数", async () => {
    const user = userEvent.setup();
    renderPage();
    const table = await screen.findByRole("table", { name: "工作空间明细" });
    expect(within(table).getByText("team-a")).toBeInTheDocument();
    expect(within(table).getByText("team-b")).toBeInTheDocument();
    const row = within(table).getByText("team-a").closest("tr");
    expect(row).not.toBeNull();
    await user.click(row as HTMLElement);
    await waitFor(() => expect(window.location.hash).toBe("#/tasks?ws=team-a"));
  });
});
