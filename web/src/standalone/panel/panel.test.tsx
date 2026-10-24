// 工作空间面板：凭据只来自 URL fragment、只存在内存，且从不发送工作空间头。
//
// 面板常与管理面同源，若复用管理面的 localStorage Key 或 X-AMKR-Workspace，
// 一个空间的面板就能读到另一个空间的数据。这里逐项锁住这三条边界。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PanelPage } from "./PanelPage";
import { panelCredential } from "./api";

interface Call {
  method: string;
  path: string;
  search: string;
  headers: Record<string, string>;
  body: Record<string, unknown> | undefined;
}

const USAGE = {
  workspace: "demo-space",
  window: { from: "2026-10-24T00:00:00+08:00", to: "2026-10-24T16:00:00+08:00", hours: 24 },
  models: ["gpt-4o-mini", "deepseek-chat"],
  layers: ["workspace", "requested_model_id", "model_id", "provider_id"],
  links: [
    { source_layer: 0, target_layer: 1, source: "demo-space", target: "TASK_000001", requests: 8, total_tokens: 800 },
    { source_layer: 1, target_layer: 2, source: "TASK_000001", target: "gpt-4o-mini", requests: 8, total_tokens: 800 },
    { source_layer: 2, target_layer: 3, source: "gpt-4o-mini", target: "openai", requests: 8, total_tokens: 800 },
  ],
  stats: {
    requests: 12,
    successes: 11,
    failures: 1,
    total_tokens: 1234,
    cached_tokens: 10,
    avg_duration_ms: 120,
  },
  recent_requests: [],
};

let calls: Call[] = [];
let tasks: Array<Record<string, unknown>> = [];

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
    const headers = (init?.headers || {}) as Record<string, string>;
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined;
    calls.push({ method, path: url.pathname, search: url.search, headers, body });

    if (method === "GET" && url.pathname === "/ui/workspace-panel.json") return json(USAGE);
    if (method === "GET" && url.pathname === "/api/tasks") {
      return json({ tasks, config_revision: "rev-1" });
    }
    if (method === "PUT" && url.pathname.startsWith("/api/tasks/")) {
      const name = decodeURIComponent(url.pathname.split("/").at(-1) || "");
      tasks = tasks.map((task) => task.name === name
        ? { ...task, display_name: body?.display_name ?? null, model: body?.model ?? null }
        : task);
      return json({ tasks, config_revision: "rev-2" });
    }
    if (method === "POST" && url.pathname === "/api/tasks") {
      tasks = [...tasks, { name: body?.name, display_name: body?.display_name ?? null, model: body?.model ?? null }];
      return json({ tasks, config_revision: "rev-2" });
    }
    return json({ detail: `unexpected ${method} ${url.pathname}` }, 500);
  }));
}

function spyStorage() {
  return {
    getItem: vi.spyOn(Storage.prototype, "getItem"),
    setItem: vi.spyOn(Storage.prototype, "setItem"),
  };
}

function renderPanel() {
  const user = userEvent.setup();
  render(<PanelPage />);
  return { user };
}

beforeEach(() => {
  calls = [];
  tasks = [
    { name: "TASK_000001", display_name: "旧显示名", model: "gpt-4o-mini", params: { temperature: 0.2 } },
    { name: "TASK_000002", display_name: null, model: null },
  ];
  localStorage.clear();
  localStorage.setItem("amkr.apiKey", "admin-secret");
  localStorage.setItem("amkr.workspace", "admin-space");
  window.history.replaceState(null, "", "/ui/panel.html#k=panel-secret");
  installFetch();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("panelCredential", () => {
  it("只从 URL fragment 取 key，绝不读管理面的 localStorage", () => {
    const { getItem, setItem } = spyStorage();
    const credential = panelCredential();
    expect(credential.key).toBe("panel-secret");
    expect(credential.base).toBe("");
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();

    // 没有 fragment 时也不能退回去读 amkr.apiKey。
    window.history.replaceState(null, "", "/ui/panel.html");
    expect(panelCredential().key).toBe("");
    expect(getItem).not.toHaveBeenCalled();
  });
});

describe("PanelPage", () => {
  it("用 fragment 里的 key 鉴权，显示数据且不碰存储、不发工作空间头", async () => {
    const { getItem, setItem } = spyStorage();
    renderPanel();

    expect(await screen.findByRole("heading", { name: "demo-space" })).toBeInTheDocument();
    expect(await screen.findAllByText("TASK_000001")).not.toHaveLength(0);
    expect(screen.getByText("旧显示名")).toBeInTheDocument();

    const usageCall = calls.find((call) => call.path === "/ui/workspace-panel.json");
    const tasksCall = calls.find((call) => call.path === "/api/tasks");
    expect(usageCall?.headers.Authorization).toBe("Bearer panel-secret");
    expect(tasksCall?.headers.Authorization).toBe("Bearer panel-secret");
    for (const call of calls) {
      expect(call.headers).not.toHaveProperty("X-AMKR-Workspace");
    }
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
  });

  it("编辑任务只提交 config_revision/model/display_name，不带 params", async () => {
    const { user } = renderPanel();
    await screen.findAllByText("TASK_000001");

    await user.click(screen.getAllByRole("button", { name: "编辑" })[0]);
    const displayName = screen.getByLabelText("显示名");
    await user.clear(displayName);
    await user.type(displayName, "新显示名");
    await user.selectOptions(screen.getByLabelText("模型"), "deepseek-chat");
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    await waitFor(() => expect(calls.some((call) => call.method === "PUT")).toBe(true));
    const put = calls.find((call) => call.method === "PUT");
    expect(put?.path).toBe("/api/tasks/TASK_000001");
    expect(put?.body).toEqual({
      config_revision: "rev-1",
      display_name: "新显示名",
      model: "deepseek-chat",
    });
    expect(put?.body).not.toHaveProperty("params");
  });

  it("新建任务提交 name 与同样的三态字段", async () => {
    const { user } = renderPanel();
    await screen.findAllByText("TASK_000001");

    await user.click(screen.getByRole("button", { name: "新建任务" }));
    await user.type(screen.getByLabelText("任务名"), "TASK_NEW");
    await user.type(screen.getByLabelText("显示名"), "新任务");
    await user.selectOptions(screen.getByLabelText("模型"), "deepseek-chat");
    await user.click(screen.getByRole("button", { name: "创建任务" }));

    await waitFor(() => expect(calls.some((call) => call.method === "POST")).toBe(true));
    const post = calls.find((call) => call.method === "POST");
    expect(post?.path).toBe("/api/tasks");
    expect(post?.body).toEqual({
      config_revision: "rev-1",
      name: "TASK_NEW",
      display_name: "新任务",
      model: "deepseek-chat",
    });
    expect(post?.body).not.toHaveProperty("params");
  });
});
