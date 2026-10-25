// 账号资源页验收：额度口径、故障隔离、刷新请求数、重置卡按需读取与危险确认。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import AccountsPage from "./index";

interface Call {
  path: string;
  method: string;
  body: unknown;
}

const report = {
  fetched_at: "2026-10-24T17:00:00+08:00",
  instances: [
    {
      id: "cpa-main",
      label: "主力 CPA",
      base_url: "http://127.0.0.1:8317",
      ok: true,
      observed_at: "2026-10-24T17:00:00+08:00",
      accounts: [
        {
          auth_index: "0",
          name: "codex-main.json",
          email: "main@example.com",
          provider: "codex",
          status: "active",
          account_type: "oauth",
          project_id: "aicode-consumers",
          supports_quota: true,
          success: 90,
          failed: 10,
          windows: [{ key: "5h", label: "5 小时", remaining: 0.04, reset_at: "2026-10-24T18:00:00+08:00" }],
          recent_requests: [{ time: "17:00", success: 2, failed: 0 }, { time: "16:50", success: 0, failed: 0 }],
        },
        {
          auth_index: "1",
          name: "claude-team.json",
          provider: "claude",
          status: "active",
          supports_quota: true,
          success: 18,
          failed: 2,
          windows: [{ key: "weekly", label: "每周", remaining: 0.15 }],
          recent_requests: [{ time: "17:00", success: 1, failed: 0 }],
        },
      ],
    },
    {
      id: "cpa-broken",
      label: "备用 CPA",
      base_url: "http://127.0.0.1:8318",
      ok: false,
      error: "无法连接 CPA: connection refused",
      accounts: [],
    },
  ],
  subscriptions: [
    {
      id: "sub-1",
      kind: "opencode_go",
      vendor: "OpenCode Go",
      provider_id: "opencode",
      key_name: "team",
      base_url: "https://opencode.ai",
      ok: true,
      windows: [{ key: "monthly", label: "本月", remaining: 0.5 }],
    },
  ],
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ConfirmProvider><SecretProvider><AccountsPage /></SecretProvider></ConfirmProvider>
    </QueryClientProvider>,
  );
}

function installFetch(options: { resetStatus?: number; useStatus?: number; report?: typeof report } = {}): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    let body: unknown = null;
    try { body = init?.body ? JSON.parse(String(init.body)) : null; } catch { body = init?.body; }
    calls.push({ path: url.pathname, method, body });
    if (url.pathname === "/api/cpa-accounts") return json(options.report ?? report);
    if (url.pathname === "/api/cpa-reset-credits") {
      if (options.resetStatus && options.resetStatus !== 200) return json({ detail: "没有这个能力" }, options.resetStatus);
      return json({
        fetched_at: "2026-10-24T17:00:00+08:00",
        instances: [{
          id: "cpa-main",
          label: "主力 CPA",
          ok: true,
          accounts: [
            { auth_index: "0", provider: "codex", reset_credits: { provider: "codex", available_count: 2, redeemable_count: 2, eligible: true, credits: [{ id: "c1", expires_at: "2026-10-25T00:00:00Z", usable: true }] } },
            { auth_index: "1", provider: "claude", reset_credits: { provider: "claude", available_count: 1, redeemable_count: 1, eligible: true, credits: [] } },
          ],
        }],
      });
    }
    if (url.pathname === "/api/cpa-reset-credits/use") {
      if (options.useStatus && options.useStatus !== 200) return json({ detail: "消耗失败" }, options.useStatus);
      return json({ instance: "cpa-main", auth_index: "0", credit_id: "c1" });
    }
    if (url.pathname === "/api/cpa-instances") return json({ config_revision: "rev-1", instances: {} });
    return json({ detail: "not found" }, 404);
  }));
  return calls;
}

beforeEach(() => {
  cleanup();
  localStorage.clear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("账号资源页", () => {
  it("额度显示剩余比例，并在 5% / 20% 处标出 critical / warn", async () => {
    installFetch();
    renderPage();
    const meters = await screen.findAllByRole("meter");
    expect(meters.map((meter) => meter.getAttribute("aria-valuenow"))).toEqual(expect.arrayContaining(["0.04", "0.15", "0.5"]));
    const critical = screen.getByText("5 小时 剩余额度").closest("[data-tone]");
    const warning = screen.getByText("每周 剩余额度").closest("[data-tone]");
    expect(critical?.getAttribute("data-tone")).toBe("critical");
    expect(warning?.getAttribute("data-tone")).toBe("warn");
  });

  it("额度卡的倒计时只出现一次，授权与项目各自成徽标", async () => {
    installFetch();
    renderPage();
    const codexCard = (await screen.findByText("codex-main.json")).closest("article") as HTMLElement;
    // 有 reset_at 的窗口显示倒计时（“N 天后重置”或“即将重置”），没有的窗口回落到来源说明。
    expect(within(codexCard).getAllByText(/后重置|即将重置/)).toHaveLength(1);
    const claudeCard = screen.getByText("claude-team.json").closest("article") as HTMLElement;
    expect(within(claudeCard).getAllByText("现场查询")).toHaveLength(1);
    expect(within(codexCard).getByText("授权 oauth")).toBeInTheDocument();
    expect(within(codexCard).getByText("项目 aicode-consumers")).toBeInTheDocument();
  });

  it("搜索跨实例生效，且单个实例失败不影响另一个实例", async () => {
    const user = userEvent.setup();
    installFetch();
    renderPage();
    expect(await screen.findByText("主力 CPA")).toBeInTheDocument();
    expect(screen.getByText("无法连接 CPA: connection refused")).toBeInTheDocument();
    await user.type(screen.getByLabelText("搜索账号、邮箱、供应商或项目"), "claude-team");
    await waitFor(() => expect(screen.queryByText("codex-main.json")).not.toBeInTheDocument());
    expect(screen.getAllByText("claude-team.json").length).toBeGreaterThan(0);
    expect(screen.getByText("主力 CPA")).toBeInTheDocument();
  });

  it("刷新只发一次账号请求，且不清空搜索框", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();
    const search = await screen.findByLabelText("搜索账号、邮箱、供应商或项目");
    await user.type(search, "codex");
    const before = calls.filter((call) => call.path === "/api/cpa-accounts").length;
    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(calls.filter((call) => call.path === "/api/cpa-accounts").length).toBe(before + 1));
    expect(search).toHaveValue("codex");
    await waitFor(() => expect(document.activeElement).toBe(search));
  });

  it("重置卡 404 时整个入口隐藏", async () => {
    const user = userEvent.setup();
    installFetch({ resetStatus: 404 });
    renderPage();
    await screen.findByText("codex-main.json");
    await user.click(screen.getAllByRole("button", { name: "查看重置卡" })[0]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "查看重置卡" })).not.toBeInTheDocument());
  });

  it("使用重置卡必须经过危险确认，默认不带 credit_id（服务端选最快过期）", async () => {
    const user = userEvent.setup();
    const calls = installFetch();
    renderPage();
    await screen.findByText("codex-main.json");
    await user.click(screen.getAllByRole("button", { name: "查看重置卡" })[0]);
    await screen.findByText(/可用 2 次重置/);
    const card = screen.getByText("codex-main.json").closest("article") as HTMLElement;
    await user.click(within(card).getByRole("button", { name: "使用一张" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/默认使用最快过期的一张/)).toBeInTheDocument();
    expect(calls.filter((call) => call.path === "/api/cpa-reset-credits/use")).toHaveLength(0);
    await user.click(within(dialog).getByRole("button", { name: "使用一张" }));
    await waitFor(() => expect(calls.filter((call) => call.path === "/api/cpa-reset-credits/use")).toHaveLength(1));
    const useCall = calls.find((call) => call.path === "/api/cpa-reset-credits/use");
    expect(useCall?.body).toMatchObject({ instance: "cpa-main", auth_index: "0" });
    expect(useCall?.body).not.toHaveProperty("credit_id");
  });
});
