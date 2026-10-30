import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import { QuotaFreezeConfigSheet } from "./quota-freeze-config";

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function renderSheet(onSaved = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(
    <QueryClientProvider client={client}>
      <ConfirmProvider>
        <QuotaFreezeConfigSheet open onOpenChange={() => {}} onSaved={onSaved} />
        <Toaster />
      </ConfirmProvider>
    </QueryClientProvider>,
  );
  return onSaved;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("额度冻结规则配置", () => {
  it("回显百分比并只把改动过的阈值写回设置接口", async () => {
    const calls: Array<{ path: string; method: string; body: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : null;
      calls.push({ path: url.pathname, method, body });
      if (url.pathname === "/api/settings" && method === "GET") {
        return json({
          config_revision: "rev-1",
          settings: {
            quota_freeze_threshold_5h: 0.05,
            quota_freeze_threshold_7d: 0.1,
            quota_freeze_threshold_whitelist: ["model-a"],
          },
        });
      }
      if (url.pathname === "/api/models") {
        return json({
          config_revision: "rev-1",
          models: [{ id: "model-a", aliases: ["alias-a"] }, { id: "model-b", aliases: [] }],
        });
      }
      if (url.pathname === "/api/settings" && method === "PUT") {
        return json({ config_revision: "rev-2", settings: body });
      }
      return json({ detail: "Not Found" }, 404);
    }));

    const user = userEvent.setup();
    const onSaved = renderSheet();
    const fiveHour = await screen.findByLabelText("5h 提前冻结底限");
    expect(fiveHour).toHaveValue(5);
    expect(screen.getByLabelText("7d 提前冻结底限")).toHaveValue(10);

    await user.clear(fiveHour);
    await user.type(fiveHour, "8");
    await user.click(screen.getByRole("button", { name: "保存规则" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledOnce());
    const update = calls.find((call) => call.path === "/api/settings" && call.method === "PUT");
    expect(update?.body).toEqual({
      config_revision: "rev-1",
      quota_freeze_threshold_5h: 0.08,
    });
  });
});
