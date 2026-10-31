import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { QuotaFreezePolicySheet } from "./quota-freeze-policy";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderSheet(scope: "cpa_instance" | "subscription" = "cpa_instance", target = "cpa-main", onSaved = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(
    <QueryClientProvider client={client}>
      <ConfirmProvider>
        <QuotaFreezePolicySheet
          open
          onOpenChange={() => {}}
          scope={scope}
          target={target}
          title="主力 CPA · 冻结规则"
          onSaved={onSaved}
        />
        <Toaster />
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return onSaved;
}

function installFetch(settings: unknown, policies: unknown[]): Array<{ path: string; method: string; body: unknown }> {
  const calls: Array<{ path: string; method: string; body: unknown }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = init?.method ?? "GET";
    const body = init?.body ? JSON.parse(String(init.body)) : null;
    calls.push({ path: url.pathname, method, body });
    if (url.pathname === "/api/settings" && method === "GET") {
      return json({
        config_revision: "rev-1",
        settings: { ...settings as object, quota_freeze_policies: policies },
      });
    }
    if (url.pathname === "/api/models") {
      return json({
        config_revision: "rev-1",
        models: [
          { id: "model-a", aliases: ["alias-a"] },
          { id: "model-b", aliases: [] },
        ],
      });
    }
    if (url.pathname === "/api/settings" && method === "PUT") {
      return json({ config_revision: "rev-2", settings: { ...settings as object, ...(body as object) } });
    }
    return json({ detail: "Not Found" }, 404);
  }));
  return calls;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("账号额度冻结策略", () => {
  it("回显账号覆盖值，并为未配置字段显示全局继承值", async () => {
    installFetch({
      quota_freeze_threshold_5h: 0.05,
      quota_freeze_threshold_7d: 0.1,
      quota_freeze_threshold_whitelist: ["model-a"],
    }, [
      {
        scope: "cpa_instance",
        target: "cpa-main",
        enabled: false,
        threshold_5h: 0.08,
        threshold_7d: null,
        whitelist: ["model-b"],
      },
    ]);

    renderSheet();

    const fiveHour = await screen.findByLabelText("5h 提前冻结底限");
    expect(fiveHour).toHaveValue(8);
    expect(screen.getByLabelText("7d 提前冻结底限")).toHaveValue(null);
    expect(screen.getByLabelText("7d 提前冻结底限")).toHaveAttribute("placeholder", "继承全局（10%）");
    expect(screen.getByRole("switch", { name: "启用自动冻结" })).not.toBeChecked();
    expect(screen.getByText(/当前 5h 5%、7d 10%、白名单 1 个/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "移除 model-b" })).toBeInTheDocument();
  });

  it("保存时只替换当前账号条目并保留其它策略", async () => {
    const settings = {
      quota_freeze_threshold_5h: 0.05,
      quota_freeze_threshold_7d: 0.1,
      quota_freeze_threshold_whitelist: ["model-a"],
    };
    const calls = installFetch(settings, [
      {
        scope: "cpa_instance",
        target: "cpa-main",
        enabled: true,
        threshold_5h: 0.02,
        threshold_7d: 0.0555,
        whitelist: null,
      },
      {
        scope: "subscription",
        target: "opencode/team",
        enabled: false,
        threshold_5h: null,
        threshold_7d: 0.2,
        whitelist: ["model-b"],
      },
    ]);

    const user = userEvent.setup();
    const onSaved = renderSheet();
    const fiveHour = await screen.findByLabelText("5h 提前冻结底限");
    await user.clear(fiveHour);
    await user.type(fiveHour, "8");
    await user.click(screen.getByRole("button", { name: "保存策略" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    const update = calls.find((call) => call.path === "/api/settings" && call.method === "PUT");
    expect(update?.body).toEqual({
      config_revision: "rev-1",
      quota_freeze_policies: [
        {
          scope: "cpa_instance",
          target: "cpa-main",
          enabled: true,
          threshold_5h: 0.08,
          // 输入框只显示到 5.6%；未修改的 7d 必须保留原始精度，不能被回写成 0.056。
          threshold_7d: 0.0555,
          whitelist: null,
        },
        {
          scope: "subscription",
          target: "opencode/team",
          enabled: false,
          threshold_5h: null,
          threshold_7d: 0.2,
          whitelist: ["model-b"],
        },
      ],
    });
  });

  it("恢复全局默认会从整份策略数组中移除当前账号条目", async () => {
    const settings = {
      quota_freeze_threshold_5h: 0.05,
      quota_freeze_threshold_7d: 0.1,
      quota_freeze_threshold_whitelist: [],
    };
    const calls = installFetch(settings, [
      { scope: "cpa_instance", target: "cpa-main", enabled: true },
      { scope: "subscription", target: "opencode/team", enabled: false },
    ]);

    const user = userEvent.setup();
    const onSaved = renderSheet();
    await screen.findByLabelText("5h 提前冻结底限");
    await user.click(screen.getByRole("button", { name: "恢复全局默认" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    const update = calls.find((call) => call.path === "/api/settings" && call.method === "PUT");
    expect(update?.body).toEqual({
      config_revision: "rev-1",
      quota_freeze_policies: [
        { scope: "subscription", target: "opencode/team", enabled: false },
      ],
    });
  });
});
