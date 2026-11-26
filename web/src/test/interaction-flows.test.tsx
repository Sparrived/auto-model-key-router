import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { ImpactProvider } from "@/components/ui/ImpactDialog";
import { SecretProvider } from "@/components/ui/SecretDialog";
import ProvidersPage from "@/pages/providers";
import RoutingPage from "@/pages/routing";
import AccessKeysPage from "@/pages/access-keys";
import TasksPage from "@/pages/tasks";
import PlaygroundPage from "@/pages/playground";
import LogsPage from "@/pages/logs";
import { createFakeBackend, installBackend, renderWithProviders } from "@/pages/providers/test-fixtures";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderFullPage(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ConfirmProvider>
        <SecretProvider>
          <ImpactProvider>{ui}</ImpactProvider>
        </SecretProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("amkr.apiKey", "test-key");
  window.location.hash = "";
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("交互深链与状态重置回归测试", () => {
  it("ProvidersPage 支持 ?new=1 自动打开添加供应商并清理 URL，且切换供应商重置 Key 行编辑态", async () => {
    window.location.hash = "#/providers?new=1";
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(<ProvidersPage />);

    const dialog = await screen.findByRole("dialog", { name: "添加供应商" });
    expect(dialog).toBeInTheDocument();
    await waitFor(() => expect(window.location.hash).not.toContain("new=1"));

    await user.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "添加供应商" })).not.toBeInTheDocument());

    const rows = await screen.findAllByText("main");
    await user.click(rows[0]);
    expect(await screen.findByLabelText("替换 API Key")).toBeInTheDocument();

    await user.click(screen.getByText("deepseek"));
    await waitFor(() => expect(screen.queryByLabelText("替换 API Key")).not.toBeInTheDocument());
  });

  it("RoutingPage 支持 ?new=1 自动打开添加模型弹窗并清理 URL", async () => {
    window.location.hash = "#/routing?new=1";
    const backend = createFakeBackend();
    installBackend(backend);
    renderWithProviders(<RoutingPage />);

    expect(await screen.findByRole("dialog", { name: "添加模型名称" })).toBeInTheDocument();
    await waitFor(() => expect(window.location.hash).not.toContain("new=1"));
  });

  it("AccessKeysPage 支持 ?new=1、?id=... 以及筛选空状态一键重置", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/access-keys") {
        return json({
          config_revision: "rev-ak-1",
          access_keys: [
            { id: "ak-alice", name: "Alice Key", enabled: true, key_fingerprint: "fp-a" },
          ],
        });
      }
      if (url.pathname === "/api/providers") return json({ providers: [], config_revision: "rev-ak-1" });
      if (url.pathname === "/api/models") return json({ models: [], config_revision: "rev-ak-1" });
      if (url.pathname === "/api/routes") return json({ routes: [], config_revision: "rev-ak-1" });
      if (url.pathname === "/ui/metric-dimensions.json") {
        return json({ window: {}, providers: [], models: [], requested_models: [], upstream_keys: [], workspaces: [], caller_types: [], access_keys: [] });
      }
      return json({ detail: "not found" }, 404);
    }));

    window.location.hash = "#/access-keys?id=ak-alice";
    const user = userEvent.setup();
    renderFullPage(<AccessKeysPage />);

    expect(await screen.findByRole("dialog", { name: /编辑访问密钥/ })).toBeInTheDocument();
    await waitFor(() => expect(window.location.hash).not.toContain("id=ak-alice"));

    await user.click(screen.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.type(screen.getByLabelText("搜索访问密钥"), "not-exist-keyword");
    expect(await screen.findByText("没有符合条件的访问密钥。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重置筛选" }));
    expect(await screen.findByText("Alice Key")).toBeInTheDocument();
  });

  it("TasksPage 支持 ?new=1 与 ?id=TASK_ALPHA&ws=default 深链", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/workspaces") {
        return json({ workspaces: [{ name: "default", task_count: 1 }] });
      }
      if (url.pathname === "/api/tasks") {
        return json({
          config_revision: "rev-task-1",
          tasks: [{ name: "TASK_ALPHA", display_name: "Alpha 任务", model: "gpt-4o-mini", params: {} }],
        });
      }
      if (url.pathname === "/api/models") return json({ models: [{ id: "gpt-4o-mini" }], config_revision: "rev-task-1" });
      if (url.pathname === "/health") return json({ ok: true, base_url: "http://127.0.0.1:8080" });
      return json({ detail: "not found" }, 404);
    }));

    window.location.hash = "#/tasks?id=TASK_ALPHA&ws=default";
    renderFullPage(<TasksPage />);
    expect(await screen.findByRole("dialog", { name: /编辑 · Alpha 任务/ })).toBeInTheDocument();
    await waitFor(() => expect(window.location.hash).not.toContain("id=TASK_ALPHA"));
  });

  it("PlaygroundPage 读取 ?model=... 时自动匹配模型与标签页", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/models")) {
        return json({ data: [{ id: "deepseek-chat" }, { id: "gpt-image-1" }] });
      }
      if (url.endsWith("/api/models")) {
        return json({ models: [{ id: "deepseek-chat" }, { id: "gpt-image-1" }] });
      }
      if (url.includes("/ui/model-kinds.json")) {
        return json({ catalog_available: true, models: { "deepseek-chat": "text", "gpt-image-1": "image" } });
      }
      if (url.endsWith("/api/workspaces")) {
        return json({ workspaces: [] });
      }
      return json({ detail: "not found" }, 404);
    }));

    window.location.hash = "#/playground?model=gpt-image-1";
    renderFullPage(<PlaygroundPage />);

    const imageSelect = await screen.findByLabelText("图像模型");
    expect((imageSelect as HTMLSelectElement).value).toBe("gpt-image-1");
  });

  it("LogsPage 在筛选无结果时提供一键清除筛选", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname === "/api/logs") {
        return json({
          text: "2026-10-24T17:00:00+08:00 INFO server started",
          truncated: false,
          path: "/tmp/amkr.log",
        });
      }
      return json({ detail: "not found" }, 404);
    }));

    const user = userEvent.setup();
    renderFullPage(<LogsPage />);
    expect(await screen.findByText(/server started/)).toBeInTheDocument();

    await user.type(screen.getByLabelText("在日志中搜索"), "missing-line");
    expect(await screen.findByText("没有匹配的日志行")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(await screen.findByText(/server started/)).toBeInTheDocument();
  });
});
