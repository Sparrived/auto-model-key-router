// 任务路由页：锁住请求体形状、工作空间作用域与工作空间凭据展示。
//
// 这些断言的都是「错了不会报错、只会静默改路由」的地方：清空字段必须是 null、
// 留空的采样参数不能写进 params、切换空间必须换请求头并清掉旧列表。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Task, WorkspaceSummary } from "@/lib/types";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import TasksPage from "./index";

interface Call {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: Record<string, unknown> | undefined;
}

const MODELS = [{ id: "model-a" }, { id: "model-b" }];

let calls: Call[] = [];
let workspaces: WorkspaceSummary[] = [];
let tasksByWorkspace: Record<string, Task[]> = {};
let revision = "rev-1";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetch(): void {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = (init?.method || "GET").toUpperCase();
    const headers = new Headers(init?.headers ?? {});
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    const headerRecord: Record<string, string> = {};
    headers.forEach((value, key) => { headerRecord[key] = value; });
    calls.push({ method, path: url.pathname, headers: headerRecord, body });

    const workspace = headers.get("X-AMKR-Workspace") || "default";

    if (method === "GET" && url.pathname === "/health") {
      return json({ status: "ok", version: "6.9.1", base_url: "http://127.0.0.1:18900" });
    }
    if (method === "GET" && url.pathname === "/api/workspaces") {
      return json({ workspaces, config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/api/tasks") {
      return json({ tasks: tasksByWorkspace[workspace] ?? [], config_revision: revision });
    }
    if (method === "GET" && url.pathname === "/api/models") {
      return json({ models: MODELS, config_revision: revision });
    }
    if (method === "POST" && url.pathname === "/api/tasks") {
      const name = String(body?.name || "");
      const task: Task = {
        name,
        model: (body?.model as string | null) ?? undefined,
        display_name: (body?.display_name as string | null) ?? undefined,
        fallback_model: (body?.fallback_model as string | null) ?? undefined,
        params: body?.params as Task["params"],
      };
      tasksByWorkspace[workspace] = [...(tasksByWorkspace[workspace] ?? []), task];
      return json({ ...task, config_revision: revision }, 201);
    }
    if (method === "PUT" && url.pathname.startsWith("/api/tasks/")) {
      const name = decodeURIComponent(url.pathname.slice("/api/tasks/".length));
      const list = tasksByWorkspace[workspace] ?? [];
      const index = list.findIndex((task) => task.name === name);
      if (index < 0) return json({ detail: `任务不存在: ${name}` }, 404);
      const next: Task = { ...list[index], ...(body as Partial<Task>) };
      list[index] = next;
      return json(next);
    }
    if (method === "DELETE" && url.pathname.startsWith("/api/tasks/")) {
      const name = decodeURIComponent(url.pathname.slice("/api/tasks/".length));
      tasksByWorkspace[workspace] = (tasksByWorkspace[workspace] ?? []).filter((task) => task.name !== name);
      return json({ config_revision: revision });
    }
    if (method === "POST" && url.pathname === "/api/workspaces") {
      const created: WorkspaceSummary = { name: String(body?.name), task_count: 0, has_inference_key: true };
      workspaces = [...workspaces, created];
      return json({
        ...created,
        api_key: "amkr_ws_panel_secret",
        inference_key: "amkr_ik_infer_secret",
        config_revision: revision,
      }, 201);
    }
    if (method === "PUT" && /^\/api\/workspaces\/[^/]+$/.test(url.pathname)) {
      const old = decodeURIComponent(url.pathname.split("/").at(-1) || "");
      const nextName = String(body?.name || "");
      workspaces = workspaces.map((item) => item.name === old ? { ...item, name: nextName } : item);
      tasksByWorkspace[nextName] = tasksByWorkspace[old] ?? [];
      delete tasksByWorkspace[old];
      return json({ name: nextName, task_count: tasksByWorkspace[nextName].length });
    }
    if (method === "DELETE" && /^\/api\/workspaces\/[^/]+$/.test(url.pathname)) {
      const name = decodeURIComponent(url.pathname.split("/").at(-1) || "");
      workspaces = workspaces.filter((item) => item.name !== name);
      delete tasksByWorkspace[name];
      return json({ config_revision: revision });
    }
    if (method === "PUT" && url.pathname.endsWith("/models")) {
      const name = decodeURIComponent(url.pathname.split("/").at(-2) || "");
      const models = body?.models as string[] | null;
      workspaces = workspaces.map((item) => {
        if (item.name !== name) return item;
        if (models === null) {
          const copy = { ...item };
          delete copy.models;
          return copy;
        }
        return { ...item, models };
      });
      return json({ name, task_count: tasksByWorkspace[name]?.length ?? 0, models: models ?? undefined });
    }
    if (method === "POST" && url.pathname.endsWith("/inference-key")) {
      return json({
        name: decodeURIComponent(url.pathname.split("/").at(-2) || ""),
        task_count: 1,
        inference_key: "amkr_ik_rotated",
        config_revision: revision,
      });
    }
    return json({ detail: `unexpected ${method} ${url.pathname}` }, 500);
  }));
}

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <ConfirmProvider>
        <SecretProvider>
          <TasksPage />
        </SecretProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return { user };
}

beforeEach(() => {
  calls = [];
  workspaces = [
    { name: "default", task_count: 1, has_inference_key: false },
    { name: "teamA", task_count: 1, has_inference_key: true },
  ];
  tasksByWorkspace = {
    default: [
      {
        name: "TASK_000001",
        display_name: "旧名",
        model: "model-a",
        fallback_model: "model-b",
        params: { temperature: 0.2 },
      },
    ],
    teamA: [{ name: "TEAM_TASK", model: "model-a", fallback_model: null, params: {} }],
  };
  revision = "rev-1";
  localStorage.clear();
  window.location.hash = "#/tasks";
  installFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("任务路由页", () => {
  it("切换工作空间：请求带 X-AMKR-Workspace、写 localStorage，并清掉旧列表", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: /teamA/ }));
    await screen.findByText("TEAM_TASK");

    expect(screen.queryByText("TASK_000001")).not.toBeInTheDocument();
    expect(localStorage.getItem("amkr.workspace")).toBe("teamA");
    const scoped = calls.find((call) => call.method === "GET" && call.path === "/api/tasks"
      && call.headers["x-amkr-workspace"] === "teamA");
    expect(scoped).toBeTruthy();
  });

  it("新建任务：POST /api/tasks，留空的参数不写进 params", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: "新建任务" }));
    const sheet = await screen.findByRole("dialog", { name: "新建任务" });
    const nameInput = within(sheet).getByLabelText(/^任务名/);
    expect(nameInput).toHaveValue("TASK_000002");
    await user.clear(nameInput);
    await user.type(nameInput, "TASK_000042");
    await user.type(within(sheet).getByLabelText("显示名称（可选）"), "补全");

    // 只固定 temperature；top_p 保持留空。
    await user.click(within(sheet).getByRole("switch", { name: "temperature" }));
    await user.type(within(sheet).getByLabelText("temperature 数值"), "0.2");
    await user.click(within(sheet).getByRole("button", { name: "保存任务" }));

    await waitFor(() => {
      expect(calls.some((call) => call.method === "POST" && call.path === "/api/tasks")).toBe(true);
    });
    const call = calls.find((item) => item.method === "POST" && item.path === "/api/tasks")!;
    expect(call.headers["x-amkr-workspace"]).toBe("default");
    expect(call.body).toMatchObject({
      name: "TASK_000042",
      model: "model-a",
      display_name: "补全",
      fallback_model: null,
      params: { temperature: 0.2 },
    });
    expect(call.body?.params).not.toHaveProperty("top_p");
  });

  it("编辑任务：清空显示名/备选/首选后 PUT 发 null", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const sheet = await screen.findByRole("dialog", { name: /编辑/ });
    await user.clear(within(sheet).getByLabelText("显示名称（可选）"));

    await user.click(within(sheet).getByRole("button", { name: "首选模型" }));
    await user.click(await screen.findByText("（尚未指定）"));
    await user.click(within(sheet).getByRole("button", { name: "保存任务" }));

    await waitFor(() => {
      expect(calls.some((call) => call.method === "PUT" && call.path === "/api/tasks/TASK_000001")).toBe(true);
    });
    const call = calls.find((item) => item.method === "PUT" && item.path === "/api/tasks/TASK_000001")!;
    expect(call.body).toMatchObject({
      model: null,
      display_name: null,
      fallback_model: null,
      params: { temperature: 0.2 },
    });
    expect(call.headers["x-amkr-workspace"]).toBe("default");
  });

  it("整数参数校验：top_k 填小数时不发请求", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const sheet = await screen.findByRole("dialog", { name: /编辑/ });
    await user.click(within(sheet).getByRole("switch", { name: "top_k" }));
    await user.type(within(sheet).getByLabelText("top_k 数值"), "1.5");
    await user.click(within(sheet).getByRole("button", { name: "保存任务" }));

    expect(await within(sheet).findByText("top_k 必须是整数")).toBeInTheDocument();
    expect(calls.some((call) => call.method === "PUT" && call.path.startsWith("/api/tasks/"))).toBe(false);
  });

  it("默认空间禁用改名、删除与模型授权", async () => {
    renderPage();
    await screen.findByText("TASK_000001");

    expect(screen.getByRole("button", { name: "改名" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "模型授权" })).toBeDisabled();
    const deleteButtons = screen.getAllByRole("button", { name: "删除" });
    expect(deleteButtons[0]).toBeDisabled();
  });

  it("新建工作空间后展示两把 key 与 panel.html#k= 地址", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: "新建工作空间" }));
    const dialog = await screen.findByRole("dialog", { name: "新建工作空间" });
    await user.type(within(dialog).getByLabelText("工作空间名"), "teamB");
    await user.click(within(dialog).getByRole("button", { name: "创建" }));

    const secret = await screen.findByRole("dialog", { name: "工作空间已创建" });
    expect(within(secret).getByText("amkr_ws_panel_secret")).toBeInTheDocument();
    expect(within(secret).getByText("amkr_ik_infer_secret")).toBeInTheDocument();
    expect(within(secret).getAllByText((content) => content.includes("panel.html#k=amkr_ws_panel_secret")).length).toBeGreaterThan(0);
    expect(localStorage.getItem("amkr.workspace")).toBe("teamB");
  });

  it("删除工作空间确认文案包含任务数", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");
    await user.click(screen.getByRole("button", { name: /teamA/ }));
    await screen.findByText("TEAM_TASK");

    await user.click(screen.getAllByRole("button", { name: "删除" })[0]);
    const dialog = await screen.findByRole("dialog", { name: "删除工作空间" });
    expect(within(dialog).getByText(/其中 1 个任务会一起被删除/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
  });

  it("工作空间模型授权三态：全部禁止发 []，不限制发 null", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");
    await user.click(screen.getByRole("button", { name: /teamA/ }));
    await screen.findByText("TEAM_TASK");

    await user.click(screen.getByRole("button", { name: "模型授权" }));
    let sheet = await screen.findByRole("dialog", { name: "模型授权 · teamA" });
    await user.click(within(sheet).getByRole("radio", { name: "全部禁止" }));
    await user.click(within(sheet).getByRole("button", { name: "保存" }));
    await waitFor(() => {
      expect(calls.some((call) => call.method === "PUT" && call.path === "/api/workspaces/teamA/models")).toBe(true);
    });
    let call = calls.find((item) => item.method === "PUT" && item.path === "/api/workspaces/teamA/models")!;
    expect(call.body?.models).toEqual([]);

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "模型授权 · teamA" })).not.toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: "模型授权" }));
    sheet = await screen.findByRole("dialog", { name: "模型授权 · teamA" });
    await user.click(within(sheet).getByRole("radio", { name: "不限制" }));
    await user.click(within(sheet).getByRole("button", { name: "保存" }));

    await waitFor(() => {
      const writes = calls.filter((item) => item.method === "PUT" && item.path === "/api/workspaces/teamA/models");
      expect(writes.length).toBe(2);
    });
    call = calls.filter((item) => item.method === "PUT" && item.path === "/api/workspaces/teamA/models").at(-1)!;
    // null 是「清除限制」：序列化后仍是 JSON null，不能变成 []。
    expect(call.body?.models).toBeNull();
  });

  it("改名后选中新名字并更新 localStorage", async () => {
    const { user } = renderPage();
    await screen.findByText("TASK_000001");
    await user.click(screen.getByRole("button", { name: /teamA/ }));
    await screen.findByText("TEAM_TASK");

    await user.click(screen.getByRole("button", { name: "改名" }));
    const dialog = await screen.findByRole("dialog", { name: "重命名工作空间" });
    const input = within(dialog).getByLabelText("新的工作空间名");
    await user.clear(input);
    await user.type(input, "teamC");
    await user.click(within(dialog).getByRole("button", { name: "保存名称" }));

    await waitFor(() => {
      expect(localStorage.getItem("amkr.workspace")).toBe("teamC");
    });
    expect(await screen.findByRole("heading", { name: "teamC" })).toBeInTheDocument();
  });

  it("整行点击与回车打开编辑抽屉；复制芯片不触发行", async () => {
    const { user } = renderPage();
    const taskRow = await screen.findByRole("row", { name: /TASK_000001/ });

    await user.click(taskRow);
    const firstSheet = await screen.findByRole("dialog", { name: /编辑/ });
    await user.click(within(firstSheet).getByRole("button", { name: "取消" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /编辑/ })).not.toBeInTheDocument();
    });

    await user.click(within(taskRow).getByRole("button", { name: "复制 TASK_000001" }));
    expect(screen.queryByRole("dialog", { name: /编辑/ })).not.toBeInTheDocument();

    taskRow.focus();
    await user.keyboard("{Enter}");
    const secondSheet = await screen.findByRole("dialog", { name: /编辑/ });
    await user.click(within(secondSheet).getByRole("button", { name: "取消" }));
  });
});
