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

async function pickComboboxOption(user: ReturnType<typeof userEvent.setup>, label: string | RegExp, optionText: string) {
  await user.click(screen.getByRole("button", { name: label }));
  const listbox = await screen.findByRole("dialog", { name: "选项列表" });
  await user.click(within(listbox).getByText(optionText));
}

describe("路由编辑", () => {
  it("保存提交别名数组（含失焦自动提交）、未改名不发 id、空上游回落路由名与端点计划", async () => {
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
    // 不按回车，切焦点到别处时应自动提交别名芯片。
    await user.type(screen.getByLabelText(/别名/), "fast");
    await user.clear(screen.getByLabelText("openai / main 的上游模型名"));
    await pickComboboxOption(user, "图像模型", "gpt-4o-mini");
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

  it("有未保存修改时点取消按钮也会弹确认，不会静默丢弃", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderWithProviders(
      <RouteEditSheet
        route={backend.state.routes[0]}
        providers={backend.state.providers}
        models={[{ id: "gpt-4o-mini", keys: [{ name: "main", enabled: true }] }]}
        kinds={null}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={onClose}
        onSaved={vi.fn()}
      />,
    );
    await user.type(screen.getByLabelText(/别名/), "unsaved-tag{enter}");
    await user.click(screen.getByRole("button", { name: "取消" }));
    const confirm = await screen.findByRole("dialog", { name: "放弃未保存的更改？" });
    expect(onClose).not.toHaveBeenCalled();
    await user.click(within(confirm).getByRole("button", { name: "放弃更改" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("可在普通路由编辑抽屉修改默认推理强度", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <RouteEditSheet
        route={backend.state.routes[0]}
        providers={backend.state.providers}
        models={[{ id: "gpt-4o-mini", reasoning_effort: "medium", keys: [{ name: "main", enabled: true }] }]}
        kinds={null}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.selectOptions(screen.getByLabelText("默认推理强度"), "high");
    await user.click(screen.getByRole("button", { name: "保存路由" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("gpt-4o-mini"));
    const effortCall = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/models/gpt-4o-mini");
    expect(effortCall?.body).toMatchObject({ reasoning_effort: "high" });
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
    const dialog = await screen.findByRole("dialog", { name: "删除模型路由" });
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

  it("移走原路由最后一条目标前会先预演连带影响并二次确认", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onMoved = vi.fn();
    // routes[2] = deepseek-chat，只有 1 条目标。
    renderWithProviders(
      <MoveTargetDialog
        route={backend.state.routes[2]}
        targetIndex={0}
        routes={backend.state.routes}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onMoved={onMoved}
      />,
    );
    // 默认选到 gpt-4o-mini（已有 deepseek/main/deepseek-chat），先切到 gpt-4o 避开重复。
    await pickComboboxOption(user, "目标路由", "gpt-4o");
    await user.click(screen.getByRole("button", { name: "移动" }));
    const impactDialog = await screen.findByRole("dialog", { name: "移动并删除原路由" });
    expect(backend.calls.some((call) => call.method === "DELETE" && call.path === "/api/routes/deepseek-chat" && call.search.includes("dry_run=1"))).toBe(true);
    await user.click(within(impactDialog).getByRole("button", { name: "移动并删除原路由" }));
    await waitFor(() => expect(onMoved).toHaveBeenCalledWith("gpt-4o", true));
  });

  it("目标路由已存在相同目标时阻止重复移动", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    renderWithProviders(
      <MoveTargetDialog
        route={backend.state.routes[2]}
        targetIndex={0}
        routes={backend.state.routes}
        revision={backend.state.revision}
        loadRoutes={async () => ({ routes: structuredClone(backend.state.routes), config_revision: backend.state.revision })}
        onClose={vi.fn()}
        onMoved={vi.fn()}
      />,
    );
    // routes[0] (gpt-4o-mini) 已包含 deepseek / main / deepseek-chat
    expect(await screen.findByText(/已经包含相同的目标/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "移动" })).toBeDisabled();
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
    await user.selectOptions(screen.getByLabelText("推理强度"), "high");
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

  it("409 冲突后可点击重新载入最新配置并重新保存", async () => {
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
        revision="stale-rev"
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.click(screen.getByRole("button", { name: "保存" }));
    const reloadBtn = await screen.findByRole("button", { name: "重新载入最新配置" });
    await user.click(reloadBtn);
    await waitFor(() => expect(screen.queryByRole("button", { name: "重新载入最新配置" })).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
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
    expect(create?.body).toMatchObject({ id: "brand-new", routing_mode: "round_robin", aliases: [] });
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
  it("详情页头用独立 Badge，别名只留在路由图，支持直接快捷切换路由模式", async () => {
    window.location.hash = "#/routing?id=deepseek-chat";
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(<RoutingPage />);
    const heading = await screen.findByRole("heading", { name: "deepseek-chat" });
    const header = heading.closest("header");
    expect(header).not.toBeNull();
    expect(within(header as HTMLElement).getByText("1 个目标")).toBeInTheDocument();
    expect(within(header as HTMLElement).getByText("优先级")).toBeInTheDocument();
    expect(screen.queryByText("别名 ds")).not.toBeInTheDocument();
    expect(screen.getByText("ds")).toBeInTheDocument();

    const modeGroup = screen.getByRole("group", { name: "快捷切换路由模式" });
    await user.click(within(modeGroup).getByRole("button", { name: "首 Key" }));
    await waitFor(() => {
      const put = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/routes/deepseek-chat");
      expect(put?.body?.routing_mode).toBe("only_first");
    });
  });

  it("默认展示 unified-model、默认文本族与六项专用端点族", async () => {
    window.location.hash = "#/routing";
    const backend = createFakeBackend();
    installBackend(backend);
    renderWithProviders(<RoutingPage />);
    expect(await screen.findByRole("heading", { name: /unified-model/ })).toBeInTheDocument();
    expect(await screen.findByText("文本（对话）")).toBeInTheDocument();
    expect(await screen.findByText("嵌入模型")).toBeInTheDocument();
  });

  it("支持按供应商/Key 搜索与无结果空态重置，并展示禁用与冻结状态", async () => {
    window.location.hash = "#/routing?id=gpt-4o";
    const backend = createFakeBackend();
    backend.state.providers[0].keys[1].enabled = false;
    backend.state.freezes = [{
      provider_id: "openai",
      key_name: "main",
      trigger: "hard",
      reason: "5h 窗口已耗尽",
      expires_at: new Date(Date.now() + 3600_000).toISOString(),
    }];
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(<RoutingPage />);

    expect(await screen.findByText("额度冻结")).toBeInTheDocument();
    expect(screen.getByText("已禁用")).toBeInTheDocument();
    expect(screen.getByText(/此路由存在不可用目标/)).toBeInTheDocument();

    const searchInput = screen.getByPlaceholderText("搜索模型、别名、供应商或 Key");
    await user.type(searchInput, "backup");
    expect(screen.getByRole("button", { name: /^gpt-4o/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^deepseek-chat/ })).not.toBeInTheDocument();

    await user.clear(searchInput);
    await user.type(searchInput, "non-existent-xyz");
    expect(await screen.findByText("没有匹配的模型路由。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重置筛选" }));
    expect(await screen.findByRole("button", { name: /^deepseek-chat/ })).toBeInTheDocument();
  });
});
