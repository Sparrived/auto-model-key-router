import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import RoutingPage from "./index";
import { AddModelDialog } from "./add-model-dialog";
import { MoveTargetDialog } from "./move-target-dialog";
import { RouteEditSheet } from "./route-edit-sheet";
import { UnifiedSheet } from "./unified-sheet";
import { createFakeBackend, installBackend, renderWithProviders } from "../providers/test-fixtures";
import type { UnifiedModel } from "@/lib/types";

afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("路由编辑", () => {
  it("保存提交别名数组、未改名不发 id、空上游回落路由名与端点计划", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const route = backend.state.routes[0];
    renderWithProviders(
      <RouteEditSheet
        route={route}
        providers={backend.state.providers}
        models={[{ id: "gpt-4o-mini", keys: [{ name: "main", enabled: true }] }]}
        kinds={null}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.type(screen.getByLabelText(/别名/), "fast{enter}");
    await user.clear(screen.getByLabelText("openai / main 的上游模型名"));
    await user.selectOptions(screen.getByLabelText("图像模型模型"), "gpt-4o-mini");
    await user.click(screen.getByRole("button", { name: "保存路由" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("gpt-4o-mini"));
    const update = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/routes/gpt-4o-mini");
    expect(update?.body).toMatchObject({
      config_revision: "rev-1",
      aliases: ["fast-mini", "fast"],
      routing_mode: "round_robin",
      targets: [
        { provider: "openai", key: "main", upstream_model: "gpt-4o-mini" },
        { provider: "deepseek", key: "main", upstream_model: "deepseek-chat" },
      ],
    });
    expect(update?.body).not.toHaveProperty("id");
    expect(update?.body?.endpoints).toEqual({ image: { primary: { model: "gpt-4o-mini", key: null } } });
  });

  it("目标清空后保存走删除预演确认", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <RouteEditSheet
        route={backend.state.routes[0]}
        providers={backend.state.providers}
        models={[]}
        kinds={null}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.click(screen.getByRole("button", { name: "移除目标 1" }));
    await user.click(screen.getByRole("button", { name: "移除目标 1" }));
    await user.click(screen.getByRole("button", { name: "保存路由" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/目标已清空/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "删除路由" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(null));
    const deletes = backend.calls.filter((call) => call.method === "DELETE" && call.path === "/api/routes/gpt-4o-mini");
    expect(deletes).toHaveLength(2);
    expect(deletes[0].search).toContain("dry_run=1");
    expect(deletes[1].search).not.toContain("dry_run=1");
  });
});

describe("目标迁移", () => {
  it("先写接收方再写来源，两次都不带 endpoints", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onMoved = vi.fn();
    renderWithProviders(
      <MoveTargetDialog
        route={backend.state.routes[0]}
        targetIndex={0}
        routes={backend.state.routes}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onMoved={onMoved}
      />,
    );
    await user.click(screen.getByRole("button", { name: "移动" }));
    await waitFor(() => expect(onMoved).toHaveBeenCalledWith("gpt-4o-mini", false));
    const puts = backend.calls.filter((call) => call.method === "PUT" && call.path.startsWith("/api/routes/"));
    expect(puts.map((call) => call.path)).toEqual(["/api/routes/gpt-4o", "/api/routes/gpt-4o-mini"]);
    expect(puts[0].body).not.toHaveProperty("endpoints");
    expect(puts[1].body).not.toHaveProperty("endpoints");
    expect((puts[0].body?.targets as unknown[]).length).toBe(3);
    expect((puts[1].body?.targets as unknown[]).length).toBe(1);
  });
});

describe("统一模型", () => {
  it("先写推理强度、再写七个计划键，未配置族为 null", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <UnifiedSheet
        unified={backend.state.unified}
        models={[
          { id: "gpt-4o-mini", reasoning_effort: "medium", keys: [{ name: "main", enabled: true }] },
          { id: "deepseek-chat", reasoning_effort: null, keys: [{ name: "main", enabled: true }] },
        ]}
        kinds={null}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.selectOptions(screen.getByLabelText(/推理强度/), "high");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const effort = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/models/gpt-4o-mini");
    expect(effort?.body).toMatchObject({ config_revision: "rev-1", reasoning_effort: "high" });
    const unified = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/unified-model");
    expect(unified?.body?.config_revision).toBe("rev-2");
    expect(Object.keys(unified?.body || {}).filter((key) => key !== "config_revision").sort()).toEqual([
      "default", "embeddings", "image", "rerank", "speech", "transcriptions", "video",
    ]);
    expect(unified?.body?.image).toBeNull();
    expect(unified?.body?.speech).toBeNull();
    expect(unified?.body?.transcriptions).toBeNull();
    expect(unified?.body?.video).toBeNull();
    expect(unified?.body?.rerank).toBeNull();
    expect(unified?.body?.default).toMatchObject({
      primary: { model: "gpt-4o-mini", key: null },
      fallback: { model: "deepseek-chat", key: null },
    });
  });

  it("回退模型与主模型相同时拒绝保存", async () => {
    const backend = createFakeBackend({
      unified: {
        default: {
          primary: { model: "gpt-4o-mini", key: null },
          fallback: { model: "gpt-4o-mini", key: null },
        } as unknown as UnifiedModel["default"],
        image: null,
        embeddings: null,
        speech: null,
        transcriptions: null,
        video: null,
        rerank: null,
      },
    });
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <UnifiedSheet
        unified={backend.state.unified}
        models={[
          { id: "gpt-4o-mini", reasoning_effort: "medium", keys: [{ name: "main", enabled: true }] },
          { id: "deepseek-chat", reasoning_effort: null, keys: [{ name: "main", enabled: true }] },
        ]}
        kinds={null}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText("回退模型不能与主模型相同。")).toBeInTheDocument();
    expect(backend.calls.some((call) => call.path === "/api/unified-model" && call.method === "PUT")).toBe(false);
  });
});

describe("添加模型", () => {
  it("创建成功后回调新名称并关闭对话框", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onCreated = vi.fn();
    const onClose = vi.fn();
    renderWithProviders(
      <AddModelDialog
        revision={backend.state.revision}
        providers={backend.state.providers}
        onClose={onClose}
        onCreated={onCreated}
      />,
    );
    await user.type(screen.getByPlaceholderText("对外模型名，例如 my-model"), "brand-new");
    await user.click(screen.getByRole("button", { name: "创建模型" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith("brand-new"));
    expect(onClose).toHaveBeenCalled();
    const create = backend.calls.find((call) => call.method === "POST" && call.path === "/api/routes");
    expect(create?.body).toMatchObject({ id: "brand-new", routing_mode: "", aliases: [] });
    expect((create?.body?.targets as unknown[]).length).toBe(1);
  });

  it("409 时错误留在对话框内并复位按钮", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <AddModelDialog
        revision={backend.state.revision}
        providers={backend.state.providers}
        onClose={vi.fn()}
        onCreated={vi.fn()}
      />,
    );
    await user.type(screen.getByPlaceholderText("对外模型名，例如 my-model"), "gpt-4o-mini");
    await user.click(screen.getByRole("button", { name: "创建模型" }));
    expect(await screen.findByText(/创建失败/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "创建模型" })).toBeEnabled();
    expect(screen.getByText("添加模型名称")).toBeInTheDocument();
  });
});

describe("路由页", () => {
  it("详情页头用独立 Badge，别名只留在路由图", async () => {
    window.location.hash = "#/routing?id=deepseek-chat";
    const backend = createFakeBackend();
    installBackend(backend);
    renderWithProviders(<RoutingPage />);
    const heading = await screen.findByRole("heading", { name: "deepseek-chat" });
    const header = heading.closest("header");
    expect(header).not.toBeNull();
    expect(within(header as HTMLElement).getByText("1 个目标")).toBeInTheDocument();
    expect(within(header as HTMLElement).getByText("优先级")).toBeInTheDocument();
    expect(screen.queryByText("别名 ds")).not.toBeInTheDocument();
    expect(screen.getByText("ds")).toBeInTheDocument();
  });

  it("默认展示 unified-model 与其七族计划", async () => {
    window.location.hash = "#/routing";
    const backend = createFakeBackend();
    installBackend(backend);
    renderWithProviders(<RoutingPage />);
    expect(await screen.findByText("unified-model")).toBeInTheDocument();
    expect(await screen.findByText("文本（对话）")).toBeInTheDocument();
    expect(await screen.findByText("嵌入模型")).toBeInTheDocument();
  });
});
