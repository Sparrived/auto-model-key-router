// 访客看板：只用 amkr.guestAccessKey、只读、绝不暴露管理面 Key。
//
// 访客页与面板部署在同源静态资源上，但凭据语义完全不同：这里只能 GET 自己的用量。
// 401（无效/轮换）与 403（已停用）要给不同指引，否则用户会一直重填一把已被停用的 key。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { GuestPage } from "./GuestPage";

interface Call {
  method: string;
  path: string;
  search: string;
  headers: Record<string, string>;
  body: string | undefined;
}

const USAGE = {
  window: { from: "2026-10-24T00:00:00+08:00", to: "2026-10-24T16:00:00+08:00", hours: 24 },
  access_key_id: "ak-1",
  access_key_name: "Alice 试用",
  stats: { requests: 3, successes: 3, failures: 0, total_tokens: 300, cached_tokens: 0, avg_duration_ms: 88 },
  dimensions: {
    model_id: { "gpt-4o-mini": { requests: 3 } },
    provider_id: { openai: { requests: 3 } },
    upstream_model_id: { "gpt-4o-mini": { requests: 3, total_tokens: 300 } },
  },
  recent_requests: [
    {
      created_at: "2026-10-24T15:00:00+08:00",
      model_id: "gpt-4o-mini",
      upstream_model_id: "gpt-4o-mini",
      provider_id: "openai",
      status_code: 200,
      success: true,
      total_tokens: 100,
      duration_ms: 80,
    },
  ],
};

let calls: Call[] = [];
let usageStatus = 200;
let usagePayload: unknown = USAGE;

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
    calls.push({
      method,
      path: url.pathname,
      search: url.search,
      headers: (init?.headers || {}) as Record<string, string>,
      body: init?.body === undefined ? undefined : String(init.body),
    });
    if (url.pathname === "/ui/access-key-usage.json") return json(usagePayload, usageStatus);
    if (url.pathname === "/ui/pricing.json") return json({ models: {} });
    return json({ detail: `unexpected ${method} ${url.pathname}` }, 500);
  }));
}

function renderGuest() {
  const user = userEvent.setup();
  render(<GuestPage />);
  return { user };
}

beforeEach(() => {
  calls = [];
  usageStatus = 200;
  usagePayload = USAGE;
  localStorage.clear();
  window.history.replaceState(null, "", "/ui/guest.html");
  installFetch();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("GuestPage 凭据边界", () => {
  it("只用 guestAccessKey 发 GET，页面只显示掩码尾号", async () => {
    localStorage.setItem("amkr.apiKey", "admin-secret");
    localStorage.setItem("amkr.guestAccessKey", "guest-secret");
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    renderGuest();

    expect(await screen.findByText("Alice 试用")).toBeInTheDocument();
    expect(await screen.findByText("…secret")).toBeInTheDocument();

    const usageCall = calls.find((call) => call.path === "/ui/access-key-usage.json");
    expect(usageCall?.headers.Authorization).toBe("Bearer guest-secret");
    expect(calls.length).toBeGreaterThan(0);
    expect(calls.every((call) => call.method === "GET")).toBe(true);
    for (const call of calls) {
      expect(`${call.path}${call.search}`).not.toContain("admin-secret");
      expect(call.body || "").not.toContain("admin-secret");
    }

    // 明文 key 只出现在请求头里，绝不出现在页面文本中。
    expect(document.body.textContent).not.toContain("guest-secret");
    expect(localStorage.getItem("amkr.apiKey")).toBe("admin-secret");
    expect(setItem.mock.calls.some(([key]) => key === "amkr.apiKey")).toBe(false);
  });

  it("401 提示密钥无效，与 403 的停用指引不同", async () => {
    localStorage.setItem("amkr.guestAccessKey", "guest-secret");
    usageStatus = 401;
    usagePayload = { error: { message: "invalid access key" } };
    renderGuest();

    expect(await screen.findByText(/这把访问密钥无效（可能已被删除或轮换）/)).toBeInTheDocument();
    expect(screen.queryByText(/访问密钥已被停用/)).toBeNull();
    expect(screen.queryByText(/请联系 AMKR 管理员/)).toBeNull();
  });

  it("403 提示已停用并要求联系 AMKR 管理员", async () => {
    localStorage.setItem("amkr.guestAccessKey", "guest-secret");
    usageStatus = 403;
    usagePayload = { detail: "access key disabled" };
    renderGuest();

    expect(await screen.findByText(/访问密钥已被停用/)).toBeInTheDocument();
    expect(screen.getByText(/请联系 AMKR 管理员/)).toBeInTheDocument();
    expect(screen.queryByText(/这把访问密钥无效/)).toBeNull();
  });

  it("表单提交只写 amkr.guestAccessKey，不写 amkr.apiKey", async () => {
    localStorage.setItem("amkr.apiKey", "admin-secret");
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const { user } = renderGuest();

    await user.type(screen.getByLabelText("访问密钥"), "amkr_ak_new");
    await user.click(screen.getByRole("button", { name: "查看看板" }));

    await waitFor(() => {
      const usageCall = calls.find((call) => call.path === "/ui/access-key-usage.json");
      expect(usageCall?.headers.Authorization).toBe("Bearer amkr_ak_new");
    });
    expect(localStorage.getItem("amkr.guestAccessKey")).toBe("amkr_ak_new");
    expect(localStorage.getItem("amkr.apiKey")).toBe("admin-secret");
    expect(setItem.mock.calls.map(([key]) => key)).toEqual(["amkr.guestAccessKey"]);
  });
});
