import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ProvidersPage from "./index";
import { AddKeySheet } from "./add-key-sheet";
import { CONCURRENCY_INVALID, concurrencyLimit } from "./concurrency";
import { KeysTable } from "./keys-table";
import { ModelBindingSheet } from "./model-binding";
import { ProviderEditSheet } from "./provider-sheet";
import { createFakeBackend, installBackend, renderWithProviders } from "./test-fixtures";

afterEach(() => {
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("并发上限口径", () => {
  it("空 → null，正整数 → 数字，其余 → 无效", () => {
    expect(concurrencyLimit("")).toBeNull();
    expect(concurrencyLimit("  ")).toBeNull();
    expect(concurrencyLimit("8")).toBe(8);
    expect(concurrencyLimit("0")).toBeUndefined();
    expect(concurrencyLimit("-3")).toBeUndefined();
    expect(concurrencyLimit("3.5")).toBeUndefined();
    expect(concurrencyLimit("abc")).toBeUndefined();
  });

  it("非法输入阻止保存并给出提示", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <ProviderEditSheet
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={vi.fn()}
        onRefresh={async () => backend.snapshot()}
      />,
    );
    const input = screen.getByLabelText(/并发上限/);
    await user.clear(input);
    await user.type(input, "0");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText(CONCURRENCY_INVALID)).toBeInTheDocument();
    expect(backend.calls.some((call) => call.method === "PUT" && call.path === "/api/providers/openai")).toBe(false);
  });
});

describe("供应商编辑", () => {
  it("保存时提交名称、地址、路径与并发上限", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <ProviderEditSheet
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={onSaved}
        onRefresh={async () => backend.snapshot()}
      />,
    );
    await user.clear(screen.getByLabelText(/供应商名称/));
    await user.type(screen.getByLabelText(/供应商名称/), "openai-main");
    await user.clear(screen.getByLabelText(/供应商地址/));
    await user.type(screen.getByLabelText(/供应商地址/), "https://gateway.example.com");
    await user.clear(screen.getByLabelText(/并发上限/));
    await user.type(screen.getByLabelText(/并发上限/), "20");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("openai-main"));
    const update = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/providers/openai");
    expect(update?.body).toMatchObject({
      config_revision: "rev-1",
      id: "openai-main",
      base_url: "https://gateway.example.com",
      max_concurrency: 20,
      routes: {},
    });
  });
});

describe("Key 行内编辑与删除", () => {
  it("清空并发上限提交 max_concurrency: null", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <KeysTable
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        boundModels={new Map()}
        onRefresh={async () => backend.snapshot()}
        onOpenModels={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Key main 的操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    await user.clear(screen.getByLabelText(/并发上限/));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => {
      const update = backend.calls.find((call) => call.method === "PUT" && call.path === "/api/providers/openai/keys/main");
      expect(update?.body).toMatchObject({ name: "main", max_concurrency: null });
    });
  });

  it("整行点击进入编辑，开关与操作按钮不冒泡，模型芯片最多两个", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <KeysTable
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        boundModels={new Map([["openai|main", ["gpt-4o-mini", "gpt-4o", "o3-mini"]]])}
        onRefresh={async () => backend.snapshot()}
        onOpenModels={vi.fn()}
      />,
    );
    expect(screen.getByText("+1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "查看全部 3 个服务模型" })).toBeInTheDocument();
    await user.click(screen.getByText("main"));
    expect(await screen.findByLabelText(/Key 名称/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("switch", { name: "启用 Key main" }));
    expect(screen.queryByLabelText(/Key 名称/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Key main 的操作" }));
    expect(screen.queryByLabelText(/Key 名称/)).not.toBeInTheDocument();
    expect(await screen.findByRole("menuitem", { name: "编辑" })).toBeInTheDocument();
  });

  it("删除 Key 先预演再真写", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(
      <KeysTable
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        boundModels={new Map()}
        onRefresh={async () => backend.snapshot()}
        onOpenModels={vi.fn()}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Key backup 的操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除 Key" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "删除" }));
    await waitFor(() => {
      const deletes = backend.calls.filter((call) => call.method === "DELETE" && call.path === "/api/providers/openai/keys/backup");
      expect(deletes).toHaveLength(2);
    });
    const deletes = backend.calls.filter((call) => call.method === "DELETE" && call.path === "/api/providers/openai/keys/backup");
    expect(deletes[0].search).toContain("dry_run=1");
    expect(deletes[1].search).not.toContain("dry_run=1");
  });
});

describe("供应商页", () => {
  it("删除供应商先 dry-run 并更新列表", async () => {
    window.location.hash = "#/providers?id=openai";
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    renderWithProviders(<ProvidersPage />);
    expect(await screen.findByRole("heading", { name: "openai" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "更多供应商操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "删除供应商" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "删除" }));
    await waitFor(() => {
      const deletes = backend.calls.filter((call) => call.method === "DELETE" && call.path === "/api/providers/openai");
      expect(deletes).toHaveLength(2);
    });
    const deletes = backend.calls.filter((call) => call.method === "DELETE" && call.path === "/api/providers/openai");
    expect(deletes[0].search).toContain("dry_run=1");
    expect(deletes[1].search).not.toContain("dry_run=1");
  });
});

describe("添加 Key 向导", () => {
  it("保存后自动探测并进入模型选择", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <AddKeySheet
        provider={backend.state.providers[0]}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={onSaved}
        loadProviders={async () => backend.snapshot()}
      />,
    );
    await user.type(screen.getByLabelText(/Key 名称/), "third");
    await user.type(screen.getByLabelText(/API Key/), "sk-test-secret");
    await user.click(screen.getByRole("button", { name: "添加 Key" }));
    expect(await screen.findByText(/Key third 已保存/)).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "打开模型 gpt-4o-mini" })).toBeInTheDocument();
    expect(screen.getByText("新增 0 · 移除 0")).toBeInTheDocument();
    const create = backend.calls.find((call) => call.method === "POST" && call.path === "/api/providers/openai/keys");
    expect(create?.body).toMatchObject({ name: "third", api_key: "sk-test-secret" });
    expect(create?.body).not.toHaveProperty("max_concurrency");
    expect(backend.calls.some((call) => call.method === "POST" && call.path === "/api/providers/openai/keys/third/probe")).toBe(true);
  });
});

describe("Key 服务模型绑定", () => {
  it("摘要按新增/移除计数，保存走预演→确认→真写", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <ModelBindingSheet
        provider={backend.state.providers[0]}
        providerKey={backend.state.providers[0].keys[0]}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.click(await screen.findByRole("button", { name: "关闭模型 gpt-4o" }));
    expect(screen.getByText("新增 0 · 移除 1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存模型" }));
    await waitFor(() => {
      const writes = backend.calls.filter((call) => call.method === "PUT" && call.path === "/api/providers/openai/keys/main/models");
      expect(writes).toHaveLength(1);
    });
    const preview = backend.calls.filter((call) => call.method === "PUT" && call.path === "/api/providers/openai/keys/main/models")[0];
    expect(preview.search).toContain("dry_run=1");
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "保存模型" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const writes = backend.calls.filter((call) => call.method === "PUT" && call.path === "/api/providers/openai/keys/main/models");
    expect(writes).toHaveLength(2);
    expect(writes[1].search).not.toContain("dry_run=1");
    expect(writes[1].body?.models).toEqual(["gpt-4o-mini"]);
  });

  it("取消确认时不真写", async () => {
    const backend = createFakeBackend();
    installBackend(backend);
    const user = userEvent.setup();
    const onSaved = vi.fn();
    renderWithProviders(
      <ModelBindingSheet
        provider={backend.state.providers[0]}
        providerKey={backend.state.providers[0].keys[0]}
        revision={backend.state.revision}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await user.click(await screen.findByRole("button", { name: "关闭模型 gpt-4o" }));
    await user.click(screen.getByRole("button", { name: "保存模型" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByText("保存 Key 服务模型")).not.toBeInTheDocument());
    expect(onSaved).not.toHaveBeenCalled();
    const writes = backend.calls.filter((call) => call.method === "PUT" && call.path === "/api/providers/openai/keys/main/models");
    expect(writes).toHaveLength(1);
    expect(writes[0].search).toContain("dry_run=1");
  });
});
