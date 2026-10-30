// 账号资源页验收：额度口径、故障隔离、刷新请求数、重置卡按需读取与危险确认，
// 以及额度冻结的展示与手工解冻（徽标/原因/倒计时、实例级联动横幅、刷新范围）。

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { SecretProvider } from "@/components/ui/SecretDialog";
import { queryKeys } from "@/lib/query-keys";
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

function renderPage(client: QueryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })) {
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

function installFetch(options: {
  resetStatus?: number;
  useStatus?: number;
  report?: unknown;
  /** 按顺序回放账号资源响应；用完之后重复最后一份（模拟解冻后的重新读取）。 */
  reports?: unknown[];
} = {}): Call[] {
  const calls: Call[] = [];
  let accountReads = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    let body: unknown = null;
    try { body = init?.body ? JSON.parse(String(init.body)) : null; } catch { body = init?.body; }
    calls.push({ path: url.pathname, method, body });
    if (url.pathname === "/api/cpa-accounts") {
      const queue = options.reports;
      const current = queue ? queue[Math.min(accountReads, queue.length - 1)] : options.report ?? report;
      accountReads += 1;
      return json(current);
    }
    if (url.pathname.startsWith("/api/quota-freezes/") && method === "DELETE") {
      const [, , , providerId, keyName] = url.pathname.split("/");
      return json({
        released: true,
        provider_id: decodeURIComponent(providerId),
        key_name: decodeURIComponent(keyName),
        suppressed_until: "2026-10-24T18:10:00+08:00",
      });
    }
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

  describe("额度冻结", () => {
    const resetAt = new Date(Date.now() + 2 * 3_600_000).toISOString();
    const subscriptionReason = "本月窗口已用尽（剩余 0%），额度重置后自动解除";

    function frozenSubscription(freezeOverrides: Record<string, unknown> = {}) {
      return {
        ...report.subscriptions[0],
        freeze: {
          provider_id: "opencode",
          key_name: "team",
          vendor: "OpenCode Go",
          scope: "subscription",
          window_label: "本月",
          remaining: 0,
          status: "rejected",
          trigger: "hard",
          reset_at: resetAt,
          expires_at: resetAt,
          reason: subscriptionReason,
          ...freezeOverrides,
        },
      };
    }

    function frozenSubscriptionReport(freezeOverrides: Record<string, unknown> = {}) {
      const subscription = frozenSubscription(freezeOverrides);
      return {
        ...report,
        guard: {
          enabled: true,
          interval_seconds: 60,
          threshold_5h: 0.1,
          threshold_7d: 0,
          last_evaluated_at: "2026-10-24T17:00:00+08:00",
          last_error: "",
        },
        freezes: [subscription.freeze],
        subscriptions: [subscription],
      };
    }

    function clearedSubscriptionReport() {
      return { ...report, freezes: [], subscriptions: [report.subscriptions[0]] };
    }

    function allExhaustedReport() {
      const base = report.instances[0];
      return {
        ...report,
        guard: { enabled: true, interval_seconds: 60 },
        freezes: [
          {
            provider_id: "cpa-main", key_name: "key-a", scope: "cpa_instance", instance_id: "cpa-main",
            window_label: "5 小时", remaining: 0, status: "rejected", expires_at: resetAt, reason: "5 小时窗口已用尽",
          },
          {
            provider_id: "cpa-main", key_name: "key-b", scope: "cpa_instance", instance_id: "cpa-main",
            window_label: "每周", remaining: 0, status: "rejected", expires_at: resetAt, reason: "每周窗口已用尽",
          },
        ],
        instances: [
          {
            ...base,
            linked_providers: ["openai", "deepseek"],
            all_accounts_exhausted: true,
            frozen_keys: [
              { provider_id: "cpa-main", key_name: "key-a" },
              { provider_id: "cpa-main", key_name: "key-b" },
            ],
            accounts: base.accounts.map((account) => ({ ...account, quota_exhausted: true })),
          },
          report.instances[1],
        ],
      };
    }

    it("订阅冻结显示徽标、原因与自动解冻倒计时，解冻后同时失效账号资源与冻结列表", async () => {
      const user = userEvent.setup();
      const calls = installFetch({ reports: [frozenSubscriptionReport(), clearedSubscriptionReport()] });
      const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
      const invalidate = vi.spyOn(client, "invalidateQueries");
      renderPage(client);

      // 页头说明按后端给的巡检间隔与底限写出来（不从配置猜）。
      expect(await screen.findByText(/后台每 60 秒自动检测/)).toBeInTheDocument();
      expect(screen.getByText(/提前冻结底限：5h 10%、7d 未启用/)).toBeInTheDocument();

      const panel = (await screen.findByRole("heading", { name: "OpenCode Go" })).closest("section") as HTMLElement;
      expect(within(panel).getByText("额度冻结")).toBeInTheDocument();
      expect(within(panel).getByText("此 Key 已被自动冻结")).toBeInTheDocument();
      expect(within(panel).getByText(subscriptionReason)).toBeInTheDocument();
      expect(within(panel).getByText(/后重置|即将重置|自动解冻/)).toBeInTheDocument();

      await user.click(within(panel).getByRole("button", { name: "解除冻结" }));
      await waitFor(() => expect(
        calls.filter((call) => call.method === "DELETE" && call.path === "/api/quota-freezes/opencode/team"),
      ).toHaveLength(1));
      // 解冻成功后账号资源重新读取一次，冻结横幅随之消失。
      await waitFor(() => expect(calls.filter((call) => call.path === "/api/cpa-accounts")).toHaveLength(2));
      await waitFor(() => expect(screen.queryByText("此 Key 已被自动冻结")).not.toBeInTheDocument());

      // 两个查询都要失效：账号资源（当前活跃）与冻结列表（供应商页共用同一份缓存）。
      const invalidatedKeys = invalidate.mock.calls.map(
        ([arg]) => JSON.stringify((arg as { queryKey?: unknown } | undefined)?.queryKey),
      );
      expect(invalidatedKeys).toEqual(expect.arrayContaining([
        JSON.stringify(queryKeys.cpaAccounts()),
        JSON.stringify(queryKeys.quotaFreezes()),
      ]));
      expect(await screen.findByText(/已解除 opencode · team 的额度冻结/)).toBeInTheDocument();
    });

    it("threshold 冻结显示提前冻结徽标与文案，倒计时仍以自动解冻为准", async () => {
      installFetch({
        report: frozenSubscriptionReport({
          trigger: "threshold",
          threshold: 0.05,
          remaining: 0.03,
          reset_at: new Date(Date.now() + 46 * 60_000).toISOString(),
          expires_at: new Date(Date.now() + 2 * 60_000).toISOString(),
        }),
      });
      renderPage();

      const panel = (await screen.findByRole("heading", { name: "OpenCode Go" })).closest("section") as HTMLElement;
      expect(within(panel).getByText("提前冻结")).toBeInTheDocument();
      expect(within(panel).queryByText("额度冻结")).not.toBeInTheDocument();
      expect(within(panel).getByText("此 Key 已被提前冻结")).toBeInTheDocument();
      // reset_at 更晚，但倒计时必须跟 expires_at 走。
      expect(within(panel).getByText(/2 分钟后自动解冻/)).toBeInTheDocument();
      expect(within(panel).queryByText(/46 分钟/)).not.toBeInTheDocument();
    });

    it("冻结到 expires_at 自动重新拉取账号资源，徽标不需要手动刷新就消失", async () => {
      const expiresAt = new Date(Date.now() + 500).toISOString();
      const calls = installFetch({
        reports: [
          frozenSubscriptionReport({ expires_at: expiresAt }),
          clearedSubscriptionReport(),
        ],
      });
      renderPage();

      expect(await screen.findByText("此 Key 已被自动冻结")).toBeInTheDocument();
      expect(calls.filter((call) => call.path === "/api/cpa-accounts").length).toBeGreaterThanOrEqual(1);

      // 页面不轮询，只有 expires_at 到点的那一次 refetch；测试用很短的到期时间代替
      // fake timers，避免 React Query 的调度也被假时钟冻结。
      await waitFor(
        () => expect(calls.filter((call) => call.path === "/api/cpa-accounts")).toHaveLength(2),
        { timeout: 3000 },
      );
      await waitFor(() => expect(screen.queryByText("此 Key 已被自动冻结")).not.toBeInTheDocument());
      expect(screen.queryByText("提前冻结")).not.toBeInTheDocument();
    });

    it("账号低于底限时显示 warn 徽标，实例级横幅说明全部账号低于冻结底限", async () => {
      const base = report.instances[0];
      const belowFloorFreeze = {
        provider_id: "cpa-main",
        key_name: "key-a",
        scope: "cpa_instance",
        instance_id: "cpa-main",
        window_label: "5 小时",
        remaining: 0.03,
        status: "allowed",
        trigger: "threshold",
        threshold: 0.05,
        expires_at: resetAt,
        reason: "5 小时窗口剩余 3%，低于 5% 底限",
      };
      installFetch({
        report: {
          ...report,
          guard: { enabled: true, interval_seconds: 60, threshold_5h: 0.05, threshold_7d: 0 },
          freezes: [belowFloorFreeze],
          instances: [
            {
              ...base,
              all_accounts_exhausted: false,
              all_accounts_below_floor: true,
              frozen_keys: [{ provider_id: "cpa-main", key_name: "key-a" }],
              accounts: base.accounts.map((account) => ({ ...account, quota_below_floor: true })),
            },
            report.instances[1],
          ],
        },
      });
      renderPage();

      expect(await screen.findByText(
        "实例内全部账号额度均已低于冻结底限，已冻结指向该实例的 1 把 Key",
      )).toBeInTheDocument();
      expect(screen.getAllByText("低于底限")).toHaveLength(2);
      expect(screen.queryByText("额度用尽")).not.toBeInTheDocument();

      const banner = screen.getByText(
        "实例内全部账号额度均已低于冻结底限，已冻结指向该实例的 1 把 Key",
      ).closest('[role="status"]') as HTMLElement;
      expect(within(banner).getByText(/cpa-main · key-a/)).toBeInTheDocument();
      expect(within(banner).getByRole("button", { name: "解冻" })).toBeInTheDocument();
    });

    it("实例内账号全部耗尽时显示联动横幅，逐把解冻并刷新", async () => {
      const user = userEvent.setup();
      const calls = installFetch({ reports: [allExhaustedReport(), clearedSubscriptionReport()] });
      renderPage();

      expect(await screen.findByText(
        "实例内全部账号额度均已用尽，已冻结指向该实例的 2 把 Key",
      )).toBeInTheDocument();
      expect(screen.getByText("关联 2 个供应商")).toBeInTheDocument();
      // 账号卡片标出「额度用尽」——这是额度状态，不改配置里的停用/冷却。
      expect(screen.getAllByText("额度用尽")).toHaveLength(2);
      // 冻结计数进指标条（label 唯一：本场景没有订阅冻结徽标）。
      const metric = screen.getByText("额度冻结").parentElement as HTMLElement;
      expect(within(metric).getByText("2")).toBeInTheDocument();

      const banner = screen.getByText(
        "实例内全部账号额度均已用尽，已冻结指向该实例的 2 把 Key",
      ).closest('[role="alert"]') as HTMLElement;
      const rows = within(banner).getAllByRole("listitem");
      expect(rows).toHaveLength(2);
      expect(within(rows[0]).getByText(/cpa-main · key-a/)).toBeInTheDocument();
      expect(within(rows[1]).getByText(/cpa-main · key-b/)).toBeInTheDocument();
      expect(within(banner).getAllByRole("button", { name: "解冻" })).toHaveLength(2);

      await user.click(within(banner).getAllByRole("button", { name: "解冻" })[0]);
      await waitFor(() => expect(
        calls.filter((call) => call.method === "DELETE" && call.path === "/api/quota-freezes/cpa-main/key-a"),
      ).toHaveLength(1));
      await waitFor(() => expect(screen.queryByText(/实例内全部账号额度均已用尽/)).not.toBeInTheDocument());
    });

    it("顶层 freezes 缺省时用逐条 freeze 字段兜底，计数与徽标一致", async () => {
      installFetch({
        report: {
          ...report,
          guard: { enabled: true, interval_seconds: 60 },
          subscriptions: [frozenSubscription()],
        },
      });
      renderPage();

      const panel = (await screen.findByRole("heading", { name: "OpenCode Go" })).closest("section") as HTMLElement;
      expect(within(panel).getByText("额度冻结")).toBeInTheDocument();
      // 指标条里是同一份计数：不能出现「卡片说冻结、指标说 0」。
      // 「额度告警」既是筛选项标签又是指标标题，取不是按钮的那个。
      const alertLabel = screen.getAllByText("额度告警").find((element) => element.tagName === "DIV") as HTMLElement;
      const strip = alertLabel.parentElement?.parentElement as HTMLElement;
      const freezeLabel = within(strip).getByText("额度冻结");
      expect(within(freezeLabel.parentElement as HTMLElement).getByText("1")).toBeInTheDocument();
    });
  });
});
