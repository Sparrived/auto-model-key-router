// 集成页用户流程：所有请求都由虚构响应拦截，不访问真实配置或上游。
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import IntegrationsPage from "./index";

const adminKey = "fictional-admin-key";
type RecordedCall = { path: string; method: string; body: BodyInit | null | undefined; headers: Headers };
let calls: RecordedCall[];
let modelsState: "ready" | "empty" | "error";
let applyFailures: number;
let localAuthEnabled: boolean;
let opsEnabled: boolean;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(<QueryClientProvider client={client}><ConfirmProvider><IntegrationsPage /></ConfirmProvider></QueryClientProvider>);
}

function panel(name: string) {
  const section = screen.getByRole("heading", { name }).closest("section");
  if (!section) throw new Error("missing agent panel: " + name);
  return within(section);
}

function writes() {
  return calls.filter((call) => call.method === "POST" && call.path.startsWith("/api/integrations/"));
}

async function selectModel(user: ReturnType<typeof userEvent.setup>, agent: string, model: string) {
  await waitFor(() => expect(screen.getByRole("button", { name: agent + " 默认模型" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: agent + " 默认模型" }));
  await user.type(await screen.findByPlaceholderText("搜索…"), model);
  const other = model === "beta-model" ? "alpha-model" : "beta-model";
  expect(screen.queryByRole("option", { name: other })).not.toBeInTheDocument();
  await user.click(await screen.findByRole("option", { name: model }));
}

async function openApply(user: ReturnType<typeof userEvent.setup>, agent: string) {
  await user.click(panel(agent).getByRole("button", { name: "应用" }));
  return within(await screen.findByRole("dialog"));
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
  localStorage.setItem("amkr.apiKey", adminKey);
  calls = [];
  modelsState = "ready";
  applyFailures = 0;
  localAuthEnabled = true;
  opsEnabled = true;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = new URL(String(input), "http://localhost").pathname;
    const method = init?.method ?? "GET";
    calls.push({ path, method, body: init?.body, headers: new Headers(init?.headers) });
    if (path === "/api/integrations" && method === "GET") return json({ integrations: [
      { agent: "claude-code", display_name: "Claude Code", target_path: "C:/fictional-home/.claude/settings.json", target_exists: true, backup_available: false, current_is_applied: true },
      { agent: "codex", display_name: "Codex", target_path: "C:/fictional-home/.codex/config.toml", target_exists: true, backup_available: true, current_is_applied: false },
      { agent: "pi-agent", display_name: "Pi Agent", target_path: "C:/fictional-home/.pi/models.json", target_exists: false, backup_available: false, current_is_applied: false },
    ] });
    if (path === "/api/settings") return json({ config_revision: "rev", settings: { host: "0.0.0.0", port: 8000, local_auth_enabled: localAuthEnabled } });
    if (path === "/health") return json({ status: "ok", local_auth_enabled: localAuthEnabled, ops_enabled: opsEnabled });
    if (path === "/v1/models") {
      if (modelsState === "error") return json({ detail: "虚构模型列表错误" }, 503);
      return json({ data: modelsState === "empty" ? [] : [{ id: "alpha-model" }, { id: "beta-model" }] });
    }
    if (method === "POST" && /^\/api\/integrations\/(claude-code|codex|pi-agent)(\/rollback)?$/.test(path)) {
      if (applyFailures > 0) {
        applyFailures -= 1;
        return json({ detail: "虚构写入错误" }, 500);
      }
      return json({ text: "ok" });
    }
    throw new Error("unexpected request: " + method + " " + path);
  }));
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
  vi.unstubAllGlobals();
});

describe("集成页", () => {
  it("每个 Agent 独立展示，应用前可取消且不发送写请求", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Claude Code")).toBeInTheDocument();
    expect(screen.getByText("Codex")).toBeInTheDocument();
    expect(screen.getByText("Pi Agent")).toBeInTheDocument();
    expect(screen.getByText("已接管")).toBeInTheDocument();
    expect(screen.getByText("配置已变更 · 可回退")).toBeInTheDocument();
    const dialog = await openApply(user, "Codex");
    expect(dialog.getByText(/将写入 Codex 的配置文件/)).toHaveTextContent(/备份/);
    expect(dialog.getByText(/完整管理权限/)).toBeInTheDocument();
    expect(dialog.getByText(/C:\/fictional-home\/\.codex\/config\.toml/)).toBeInTheDocument();
    expect(writes()).toHaveLength(0);
    await user.click(dialog.getByRole("button", { name: "取消" }));
    expect(writes()).toHaveLength(0);
  });

  it.each([
    ["Claude Code", "claude-code", ""],
    ["Codex", "codex", "high"],
    ["Pi Agent", "pi-agent", "high"],
  ])("搜索并为 %s 应用默认模型和支持的推理强度", async (name, agent, effort) => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByRole("heading", { name });
    await selectModel(user, name, "beta-model");
    if (effort) await user.selectOptions(screen.getByLabelText(name + " 推理强度"), effort);
    else expect(screen.queryByLabelText(name + " 推理强度")).not.toBeInTheDocument();
    const dialog = await openApply(user, name);
    expect(dialog.getByText(/新会话默认模型：beta-model/)).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "应用" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const call = writes()[0];
    expect(call.path).toBe("/api/integrations/" + agent);
    expect(call.headers.get("authorization")).toBe("Bearer " + adminKey);
    expect(call.headers.get("content-type")).toBe("application/json");
    expect(JSON.parse(String(call.body))).toEqual({ default_model: "beta-model", ...(effort ? { reasoning_effort: effort } : {}) });
  });

  it.each([["Claude Code", "claude-code"], ["Codex", "codex"], ["Pi Agent", "pi-agent"]])(
    "%s 不选配置时沿用旧接口空请求体", async (name, agent) => {
      const user = userEvent.setup();
      renderPage();
      await screen.findByRole("heading", { name });
      const dialog = await openApply(user, name);
      expect(dialog.getByText(/默认模型沿用现有行为/)).toBeInTheDocument();
      await user.click(dialog.getByRole("button", { name: "应用" }));
      await waitFor(() => expect(writes()).toHaveLength(1));
      expect(writes()[0].path).toBe("/api/integrations/" + agent);
      expect(writes()[0].body).toBeUndefined();
      expect(writes()[0].headers.get("content-type")).toBeNull();
    },
  );

  it("已选模型后可改回沿用现有行为并省略模型字段", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    await selectModel(user, "Codex", "beta-model");
    await user.click(panel("Codex").getByRole("button", { name: "清除所选模型" }));
    await user.selectOptions(screen.getByLabelText("Codex 推理强度"), "high");
    const dialog = await openApply(user, "Codex");
    expect(dialog.getByText(/默认模型沿用现有行为/)).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "应用" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(JSON.parse(String(writes()[0].body))).toEqual({ reasoning_effort: "high" });
  });

  it("应用失败后保留选择并允许按相同选项重试", async () => {
    const user = userEvent.setup();
    applyFailures = 1;
    renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    await selectModel(user, "Codex", "beta-model");
    await user.selectOptions(screen.getByLabelText("Codex 推理强度"), "high");
    let dialog = await openApply(user, "Codex");
    await user.click(dialog.getByRole("button", { name: "应用" }));
    expect(await screen.findByText(/虚构写入错误/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Codex 默认模型" })).toHaveTextContent("beta-model");
    expect(screen.getByLabelText("Codex 推理强度")).toHaveValue("high");
    dialog = await openApply(user, "Codex");
    await user.click(dialog.getByRole("button", { name: "应用" }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes().map((call) => JSON.parse(String(call.body)))).toEqual([
      { default_model: "beta-model", reasoning_effort: "high" },
      { default_model: "beta-model", reasoning_effort: "high" },
    ]);
  });

  it("状态刷新和页面重新载入不丢弃已选择的草稿", async () => {
    const user = userEvent.setup();
    const view = renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    await selectModel(user, "Codex", "beta-model");
    await user.selectOptions(screen.getByLabelText("Codex 推理强度"), "high");
    await user.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect(calls.filter((call) => call.path === "/v1/models").length).toBeGreaterThan(1));
    expect(screen.getByRole("button", { name: "Codex 默认模型" })).toHaveTextContent("beta-model");
    view.unmount();
    renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    expect(screen.getByRole("button", { name: "Codex 默认模型" })).toHaveTextContent("beta-model");
    expect(screen.getByLabelText("Codex 推理强度")).toHaveValue("high");
    expect(writes()).toHaveLength(0);
  });

  it("模型接口失败和空列表分别提示，可刷新恢复", async () => {
    const user = userEvent.setup();
    modelsState = "error";
    renderPage();
    expect(await screen.findByText("读取模型列表失败")).toBeInTheDocument();
    expect(screen.queryByText("没有可用模型")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Codex 默认模型" })).toBeDisabled();
    modelsState = "empty";
    await user.click(screen.getByRole("button", { name: "刷新状态" }));
    expect(await screen.findByText("没有可用模型")).toBeInTheDocument();
    expect(screen.queryByText("读取模型列表失败")).not.toBeInTheDocument();
    modelsState = "ready";
    await user.click(screen.getByRole("button", { name: "刷新状态" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Codex 默认模型" })).toBeEnabled());
    await selectModel(user, "Codex", "alpha-model");
    expect(screen.getByRole("button", { name: "Codex 默认模型" })).toHaveTextContent("alpha-model");
  });

  it("回退前说明覆盖风险，取消无请求，确认后发送回退", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    await user.click(panel("Codex").getByRole("button", { name: "回退" }));
    let dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText(/手工修改的当前配置也会被备份内容替换/)).toBeInTheDocument();
    await user.click(dialog.getByRole("button", { name: "取消" }));
    expect(writes()).toHaveLength(0);
    await user.click(panel("Codex").getByRole("button", { name: "回退" }));
    dialog = within(await screen.findByRole("dialog"));
    await user.click(dialog.getByRole("button", { name: "回退" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].path).toBe("/api/integrations/codex/rollback");
    expect(writes()[0].headers.get("authorization")).toBe("Bearer " + adminKey);
  });

  it("手动示例只复制占位密钥并提示管理密钥与远程地址风险", async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByRole("heading", { name: "Codex" });
    expect(screen.getByRole("link", { name: "管理访问密钥" })).toHaveAttribute("href", "#/access-keys");
    expect(screen.getByText(/从其他机器连接时/)).toHaveTextContent(/服务所在机器可达的地址/);
    expect(panel("Codex").getByText(/完整管理 Key 写入客户端配置/)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(adminKey);
    await user.click(screen.getByRole("button", { name: "复制 OpenAI 模型列表" }));
    const copied = await navigator.clipboard.readText();
    expect(copied).toContain("YOUR_ACCESS_KEY");
    expect(copied).toContain("http://127.0.0.1:8000/v1/models");
    expect(copied).not.toContain(adminKey);
    expect(screen.getByText("x-api-key")).toBeInTheDocument();
    expect(screen.getByText("anthropic-version")).toBeInTheDocument();
    expect(screen.getByText(/在请求体中填写已授权的模型名/)).toHaveTextContent("YOUR_ACCESS_KEY");
    await user.click(screen.getByRole("button", { name: "复制 Anthropic 请求地址" }));
    const anthropic = await navigator.clipboard.readText();
    expect(anthropic).toBe("http://127.0.0.1:8000/v1/messages");
    expect(anthropic).not.toContain(adminKey);
  });

  it.each([["本地鉴权未启用", false, true], ["运维接口已关闭", true, false]])(
    "%s 时不允许应用或回退", async (warning, auth, ops) => {
      localAuthEnabled = auth;
      opsEnabled = ops;
      renderPage();
      // 同一句话既是启动 Banner 的标题，也是面板上的状态徽章，只断言 Banner 正文标题。
      expect(await screen.findByText(warning, { selector: "p" })).toBeInTheDocument();
      await screen.findByRole("heading", { name: "Codex" });
      expect(panel("Codex").getByRole("button", { name: "应用" })).toBeDisabled();
      expect(panel("Codex").getByRole("button", { name: "回退" })).toBeDisabled();
      expect(writes()).toHaveLength(0);
    },
  );
});
