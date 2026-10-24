// 服务日志页验收：级别过滤与关键字匹配只影响显示，日志原文仍保留。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import LogsPage from "./index";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><LogsPage /></QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/logs") {
      return json({
        text: [
          "2026-10-24T17:00:00+08:00 INFO server started",
          "2026-10-24T17:01:00+08:00 WARN upstream slow",
          "2026-10-24T17:02:00+08:00 ERROR request failed",
          "2026-10-24T17:03:00+08:00 INFO healthy",
        ].join("\n"),
        truncated: false,
        path: "/tmp/amkr.log",
      });
    }
    return json({ detail: "not found" }, 404);
  }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("服务日志页", () => {
  it("仅错误只留下错误行，搜索会高亮匹配文本", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText(/server started/)).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "仅错误" }));
    expect(screen.getByText(/request failed/)).toBeInTheDocument();
    expect(screen.queryByText(/server started/)).not.toBeInTheDocument();
    expect(screen.queryByText(/upstream slow/)).not.toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: "全部" }));
    const search = screen.getByLabelText("在日志中搜索");
    await user.type(search, "failed");
    await waitFor(() => expect(screen.getByText("failed")).toBeInTheDocument());
    expect(screen.queryByText(/server started/)).not.toBeInTheDocument();
  });

  it("暂停刷新后显示暂停状态，且仍可切换换行", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText(/server started/)).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: "暂停" }));
    expect(screen.getByText("已暂停")).toBeInTheDocument();
    const wrap = screen.getByRole("switch", { name: "自动换行" });
    expect(wrap).toBeChecked();
    await user.click(wrap);
    expect(wrap).not.toBeChecked();
  });
});
