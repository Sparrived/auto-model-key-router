// 设置页验收：运行设置只提交改动字段；固定底栏报告脏项；重置本地 Key 会更新本机凭据。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import SettingsPage from "./index";

interface Call { path: string; method: string; body: unknown }

const SETTINGS = {
  host: "127.0.0.1",
  port: 8000,
  request_timeout: 120,
  stream_first_byte_timeout: 60,
  stream_idle_timeout: 300,
  max_retries: 2,
  local_auth_enabled: true,
  local_api_key_fingerprint: "ab12cd34",
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function installFetch(): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    let body: unknown = null;
    try { body = init?.body ? JSON.parse(String(init.body)) : null; } catch { body = init?.body; }
    calls.push({ path: url.pathname, method, body });
    if (url.pathname === "/api/settings" && method === "GET") return json({ config_revision: "rev-1", settings: SETTINGS });
    if (url.pathname === "/api/settings" && method === "PUT") {
      return json({ config_revision: "rev-2", settings: { ...SETTINGS, ...(body as object) } });
    }
    if (url.pathname === "/api/settings/local-api-key") {
      return json({ config_revision: "rev-3", local_api_key: "new-secret", local_api_key_fingerprint: "ff00", settings: { ...SETTINGS, local_api_key_fingerprint: "ff00" } });
    }
    if (url.pathname === "/api/tool") return json({ version: "1.0.0", latest_version: "1.0.0", webui_available: true, webui_enabled: true, webui_mounted: true });
    if (url.pathname === "/ui/update/status") return json({ supported: true, available: true, current_version: "1.0.0" });
    if (url.pathname === "/health") return json({ status: "ok", version: "1.0.0" });
    if (url.pathname === "/metrics") return json({ window: { hours: 1 }, total: { requests: 12 }, started_at: "2026-10-24T10:00:00+08:00", database_path: "C:/tmp/amkr.db", count_semantics: "每个请求记一次" });
    return json({ detail: "not found" }, 404);
  }));
  return calls;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ConfirmProvider><SecretProvider><SettingsPage /></SecretProvider></ConfirmProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("设置页", () => {
  it("只提交发生变化的运行设置，并在固定底栏显示脏项数", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();
    const port = await screen.findByRole("spinbutton", { name: /端口/ });
    expect(port).toHaveValue(8000);
    await user.clear(port);
    await user.type(port, "9000");
    expect(screen.getByText("有 1 项未保存的更改")).toBeInTheDocument();
    const bar = screen.getByRole("status");
    await user.click(within(bar).getByRole("button", { name: "保存运行设置" }));
    await waitFor(() => {
      const put = calls.find((call) => call.path === "/api/settings" && call.method === "PUT");
      expect(put).toBeTruthy();
      expect(put?.body).toMatchObject({ port: 9000 });
      expect(put?.body).not.toHaveProperty("host");
      expect(put?.body).not.toHaveProperty("request_timeout");
    });
    await waitFor(() => expect(screen.queryByText("有 1 项未保存的更改")).not.toBeInTheDocument());
  });

  it("重置本地鉴权 Key 前要求确认，成功后同步更新 localStorage", async () => {
    const user = userEvent.setup();
    installFetch();
    localStorage.setItem("amkr.apiKey", "old-secret");
    renderPage();
    expect(await screen.findByText("ab12cd34")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重置本地鉴权 Key" }));
    const confirmDialog = await screen.findByRole("dialog");
    expect(within(confirmDialog).getByText(/现有客户端需要改用新 Key/)).toBeInTheDocument();
    await user.click(within(confirmDialog).getByRole("button", { name: "重置" }));
    await waitFor(() => expect(localStorage.getItem("amkr.apiKey")).toBe("new-secret"));
    expect(await screen.findByText("新的本地鉴权 Key")).toBeInTheDocument();
    expect(localStorage.getItem("amkr.apiKey")).toBe("new-secret");
  });
});
