// 集成页冒烟验收：三个 Agent 各自成面板，状态、配置路径与入口地址可读，应用前有确认。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import IntegrationsPage from "./index";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><ConfirmProvider><IntegrationsPage /></ConfirmProvider></QueryClientProvider>);
}

beforeEach(() => {
  cleanup();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/api/integrations") return json({ integrations: [
      { agent: "claude-code", display_name: "Claude Code", target_path: "C:/Users/me/.claude/settings.json", target_exists: true, backup_available: false, current_is_applied: true },
      { agent: "codex", display_name: "Codex", target_path: "C:/Users/me/.codex/config.toml", target_exists: true, backup_available: true, current_is_applied: false },
      { agent: "pi-agent", display_name: "Pi Agent", target_path: "C:/Users/me/.pi/config.json", target_exists: false, backup_available: false, current_is_applied: false },
    ] });
    if (url.pathname === "/api/settings") return json({ config_revision: "rev", settings: { host: "127.0.0.1", port: 8000, local_auth_enabled: true } });
    if (url.pathname === "/health") return json({ status: "ok", local_auth_enabled: true });
    if (url.pathname === "/api/integrations/codex") return json({ text: "ok" });
    return json({ detail: "not found" }, 404);
  }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("集成页", () => {
  it("每个 Agent 独立展示，应用按钮会先弹确认", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Pi Agent")).toBeInTheDocument();
    expect(screen.getByText("已接管")).toBeInTheDocument();
    expect(screen.getByText("配置已变更 · 可回退")).toBeInTheDocument();
    expect(screen.getAllByText("http://127.0.0.1:8000").length).toBeGreaterThan(0);
    const applyButtons = screen.getAllByRole("button", { name: "应用" });
    await user.click(applyButtons[1]);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/将写入 Codex 的配置文件/)).toBeInTheDocument());
  });
});
