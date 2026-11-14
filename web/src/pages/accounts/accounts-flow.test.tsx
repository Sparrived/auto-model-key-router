// 账号资源页操作流：首屏只留三项核心读数，低频动作收进菜单，诊断信息默认折叠。
// 这些用例按真实用户路径点击（菜单、筛选、展开详情），不直接断言内部状态。

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import AccountsPage from "./index";

const report = {
  fetched_at: "2026-10-24T17:00:00+08:00",
  guard: { enabled: true, interval_seconds: 60, threshold_5h: 0.05, threshold_7d: 0 },
  instances: [
    {
      id: "cpa-main",
      label: "主力 CPA",
      base_url: "http://127.0.0.1:8317",
      ok: true,
      observed_at: "2026-10-24T17:00:00+08:00",
      linked_providers: ["codex-main"],
      accounts: [
        {
          auth_index: "0", name: "codex-main.json", provider: "codex", status: "active",
          success: 90, failed: 10,
          windows: [{ key: "5h", label: "5 小时", remaining: 0.04, reset_at: "2026-10-24T18:00:00+08:00" }],
        },
        { auth_index: "1", name: "claude-team.json", provider: "claude", status: "active", disabled: true },
        { auth_index: "2", name: "gemini-idle.json", provider: "gemini", status: "active", unavailable: true },
        { auth_index: "3", name: "codex-backup.json", provider: "codex", status: "active", windows: [{ key: "5h", label: "5 小时", remaining: 0.5 }] },
      ],
    },
  ],
  subscriptions: [],
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

interface Call { path: string; method: string; body: unknown }

function installFetch(options: { report?: unknown; settings?: unknown; runtimePatchStatus?: number } = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    let body: unknown = null;
    try { body = init?.body ? JSON.parse(String(init.body)) : null; } catch { body = init?.body; }
    calls.push({ path: url.pathname, method, body });
    if (url.pathname === "/api/cpa-accounts") return json(options.report ?? report);
    if (url.pathname === "/api/settings") return json(options.settings ?? { config_revision: "rev-1", settings: {} });
    if (/^\/api\/cpa-instances\/[^/]+\/accounts\/status$/.test(url.pathname)) {
      return json({ instance: "cpa-main", auth_index: "0", action: "status" });
    }
    if (/^\/api\/cpa-instances\/[^/]+\/accounts\/fields$/.test(url.pathname)) {
      return json({ instance: "cpa-main", auth_index: "0", action: "fields" });
    }
    if (/^\/api\/cpa-instances\/[^/]+\/accounts\/routing-reset$/.test(url.pathname)) {
      return json({ instance: "cpa-main", auth_index: "0", action: "routing-reset" });
    }
    if (/^\/api\/cpa-instances\/[^/]+\/runtime-config$/.test(url.pathname)) {
      if (method === "PATCH" && options.runtimePatchStatus && options.runtimePatchStatus !== 200) {
        return json({ detail: "CPA 配置已被其它操作修改，请重新读取后再保存" }, options.runtimePatchStatus);
      }
      return json({ instance: "cpa-main", etag: "\"v1\"", debug: false, request_retry: 0, strategy: "round-robin", client_api_keys: [] });
    }
    return json({ detail: "not found" }, 404);
  }));
  return calls;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ConfirmProvider>
        <SecretProvider>
          <AccountsPage />
          <Toaster />
        </SecretProvider>
      </ConfirmProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => { cleanup(); localStorage.clear(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("账号资源页操作流", () => {
  it("首屏只保留可用账号、额度告警、冻结 Key 三项指标", async () => {
    installFetch();
    renderPage();

    expect(await screen.findByText("可用账号")).toBeInTheDocument();
    expect(screen.getByText("额度告警")).toBeInTheDocument();
    expect(screen.getByText("冻结 Key")).toBeInTheDocument();
    // 旧的四瓦片口径不再占据首屏（限定在指标条内，避免与卡片徽标文案撞名）。
    const strip = screen.getByText("可用账号").parentElement?.parentElement as HTMLElement;
    expect(within(strip).queryByText("额度冻结")).not.toBeInTheDocument();
    expect(screen.queryByText("调度总量")).not.toBeInTheDocument();
    // 实例与订阅数量退到辅助说明里。
    expect(screen.getByText(/1 个实例 · 0 条订阅/)).toBeInTheDocument();
  });

  it("「需处理」把停用、冷却与低额度账号合并成一个筛选", async () => {
    const user = userEvent.setup();
    installFetch();
    renderPage();

    expect(await screen.findByText(/显示 4 \/ 4 个账号/)).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /需处理/ }));
    // 停用、冷却、低额度各一个；健康账号被筛掉。
    await waitFor(() => expect(screen.getByText(/显示 3 \/ 4 个账号/)).toBeInTheDocument());
    expect(screen.queryByText("codex-backup.json")).not.toBeInTheDocument();

    // 可用只要求未停用、未冷却：健康主账号与备用账号各一个，额度低也算可用。
    await user.click(screen.getByRole("radio", { name: /可用/ }));
    await waitFor(() => expect(screen.getByText(/显示 2 \/ 4 个账号/)).toBeInTheDocument());
    expect(screen.getByText("codex-backup.json")).toBeInTheDocument();
    expect(screen.getByText("codex-main.json")).toBeInTheDocument();
    expect(screen.queryByText("claude-team.json")).not.toBeInTheDocument();
  });

  it("实例操作菜单里再打开实例配置与冻结规则", async () => {
    const user = userEvent.setup();
    installFetch();
    renderPage();

    expect(await screen.findByText("主力 CPA")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "实例操作" }));
    expect(await screen.findByRole("menuitem", { name: "实例配置" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "冻结规则" })).toBeInTheDocument();

    await user.click(screen.getByRole("menuitem", { name: "实例配置" }));
    expect(await screen.findByRole("dialog", { name: "主力 CPA · 实例配置" })).toBeInTheDocument();
  });

  it("账号菜单里编辑配置，停用账号仍需危险确认", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();

    expect(await screen.findByText("codex-backup.json")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "codex-backup.json 的账号操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "账号配置" }));
    const configSheet = await screen.findByRole("dialog", { name: "codex-backup.json · 账号配置" });
    expect(within(configSheet).getByLabelText("显示名称")).toBeInTheDocument();
    await user.click(within(configSheet).getByRole("button", { name: "取消" }));

    await user.click(screen.getByRole("button", { name: "codex-backup.json 的账号操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "停用账号" }));
    const confirmDialog = await screen.findByRole("dialog");
    expect(within(confirmDialog).getByText(/该账号会立即退出 CPA 自己的候选池/)).toBeInTheDocument();
    await user.click(within(confirmDialog).getByRole("button", { name: "停用账号" }));

    await waitFor(() => expect(
      calls.filter((call) => call.method === "PATCH" && call.path === "/api/cpa-instances/cpa-main/accounts/status"),
    ).toHaveLength(1));
    const patch = calls.find((call) => call.method === "PATCH" && call.path === "/api/cpa-instances/cpa-main/accounts/status");
    expect(patch?.body).toMatchObject({ auth_index: "3", name: "codex-backup.json", disabled: true });
  });

  it("诊断信息默认折叠，展开后才显示授权、项目与调用统计", async () => {
    installFetch();
    renderPage();

    expect(await screen.findByText("codex-main.json")).toBeInTheDocument();
    const card = screen.getByText("codex-main.json").closest("article") as HTMLElement;
    const details = within(card).getByText("诊断信息").closest("details") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    // 折叠时内容仍在 DOM 里，但不占据首屏视觉。
    expect(within(details).getByText(/调用 100 次/)).toBeInTheDocument();

    fireEvent.click(within(card).getByText("诊断信息"));
    expect(details.open).toBe(true);
    expect(within(card).getByText(/凭据标识：/)).toBeInTheDocument();
  });

  it("实例详情默认折叠，展开后显示端点与关联供应商", async () => {
    installFetch();
    renderPage();

    expect(await screen.findByText("主力 CPA")).toBeInTheDocument();
    const panel = screen.getByRole("heading", { name: "主力 CPA" }).closest("section") as HTMLElement;
    const details = within(panel).getByText("实例详情").closest("details") as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(within(details).getByText(/关联 1 个供应商/)).toBeInTheDocument();

    fireEvent.click(within(panel).getByText("实例详情"));
    expect(details.open).toBe(true);
  });

  it("清除冷却先确认，再按 auth_index 发路由重置", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();

    expect(await screen.findByText("codex-backup.json")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "codex-backup.json 的账号操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "清除冷却" }));

    const dialog = await screen.findByRole("dialog");
    // 文案必须说明只清 CPA 自身状态，不解除 AMKR 冻结、不恢复真实额度。
    expect(within(dialog).getByText(/不会解除 AMKR 额度冻结/)).toBeInTheDocument();
    expect(within(dialog).getByText(/不会恢复上游真实额度/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "清除冷却状态" }));

    await waitFor(() => expect(
      calls.filter((call) => call.method === "POST" && call.path === "/api/cpa-instances/cpa-main/accounts/routing-reset"),
    ).toHaveLength(1));
    const reset = calls.find((call) => call.method === "POST" && call.path === "/api/cpa-instances/cpa-main/accounts/routing-reset");
    expect(reset?.body).toEqual({ auth_index: "3" });
  });

  it("账号配置只提交改动过的显示名称", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();

    expect(await screen.findByText("codex-backup.json")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "codex-backup.json 的账号操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "账号配置" }));
    const sheet = await screen.findByRole("dialog", { name: "codex-backup.json · 账号配置" });
    await user.type(within(sheet).getByLabelText("显示名称"), "备用账号");
    await user.click(within(sheet).getByRole("button", { name: "保存到 CPA" }));

    await waitFor(() => expect(
      calls.filter((call) => call.method === "PATCH" && call.path === "/api/cpa-instances/cpa-main/accounts/fields"),
    ).toHaveLength(1));
    const patch = calls.find((call) => call.method === "PATCH" && call.path === "/api/cpa-instances/cpa-main/accounts/fields");
    expect(patch?.body).toMatchObject({ auth_index: "3", name: "codex-backup.json", label: "备用账号" });
    // 未改动的备注不应出现在请求里。
    expect(patch?.body).not.toHaveProperty("note");
  });

  it("实例配置冲突时保留草稿并锁住再次保存", async () => {
    const user = userEvent.setup();
    installFetch({ runtimePatchStatus: 409 });
    renderPage();

    expect(await screen.findByText("主力 CPA")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "实例操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "实例配置" }));
    const sheet = await screen.findByRole("dialog", { name: "主力 CPA · 实例配置" });

    await user.selectOptions(within(sheet).getByLabelText("账号路由策略"), "fill-first");
    await user.click(within(sheet).getByRole("button", { name: "保存到 CPA" }));

    expect(await within(sheet).findByText(/配置已被其它操作更新/)).toBeInTheDocument();
    // 草稿仍在，但保存入口消失，必须先关闭重开核对最新配置。
    expect(within(sheet).getByLabelText("账号路由策略")).toHaveValue("fill-first");
    expect(within(sheet).queryByRole("button", { name: "保存到 CPA" })).not.toBeInTheDocument();
  });
});
