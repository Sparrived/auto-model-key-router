// 登录页：开放重定向防护、提交后的跳转与三种失败分支。
//
// `next` 完全由攻击者控制，所以这里既测纯函数也测真实提交流程：
// 用户在本站输入 Key 后必须只被送到同源路径；401 才清空已存的 Key，
// 网络不可达保留 Key（否则服务恢复后要重新粘贴）。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  setKey: vi.fn(),
  health: vi.fn(),
  settings: vi.fn(),
}));

// 只替换登录页真正用到的三个导出，保留真实的 ApiError，让 `instanceof` 判断仍然成立。
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    setKey: mocks.setKey,
    api: { ...actual.api, health: mocks.health, settings: mocks.settings },
  };
});

import { ApiError } from "@/lib/api";
import { LoginPage } from "./LoginPage";
import { currentNext, safeNext } from "./safe-next";

function setLocation(search: string): void {
  window.history.replaceState(null, "", `/ui/login.html${search}`);
}

function renderLogin() {
  const navigate = vi.fn();
  const user = userEvent.setup();
  render(<LoginPage navigate={navigate} homeUrl="/ui/" healthIntervalMs={0} />);
  return { navigate, user };
}

async function submitKey(user: ReturnType<typeof userEvent.setup>, value = "secret-key"): Promise<void> {
  await user.type(screen.getByLabelText("本地鉴权 Key"), value);
  await user.click(screen.getByRole("button", { name: "连接" }));
}

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
  mocks.health.mockResolvedValue({ local_auth_enabled: true, version: "7.0.0" });
  mocks.settings.mockResolvedValue({});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("safeNext 开放重定向防护", () => {
  it("接受同源绝对路径并保留查询串", () => {
    expect(safeNext("/providers?x=1")).toBe("/providers?x=1");
    expect(safeNext("/ui/tasks")).toBe("/ui/tasks");
  });

  it("拒绝协议相对、反斜杠、站外绝对地址与空值", () => {
    expect(safeNext("//evil.example")).toBeNull();
    expect(safeNext("/\\evil.example")).toBeNull();
    expect(safeNext("https://evil.example")).toBeNull();
    expect(safeNext("")).toBeNull();
    expect(safeNext(null)).toBeNull();
  });

  it("currentNext 从查询串中取出 next", () => {
    expect(currentNext("?next=/providers?tab=keys")).toBe("/providers?tab=keys");
    expect(currentNext("?next=//evil.example")).toBeNull();
  });
});

describe("LoginPage 提交流程", () => {
  it("验证通过后跳转到同源 next", async () => {
    setLocation("?next=/providers?tab=keys&reason=expired");
    const { navigate, user } = renderLogin();
    await submitKey(user);

    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/providers?tab=keys"));
    expect(mocks.setKey).toHaveBeenCalledWith("secret-key");
  });

  it("恶意的协议相对 next 回落到 homeUrl，绝不传给 navigate", async () => {
    setLocation("?next=//evil.example");
    const { navigate, user } = renderLogin();
    await submitKey(user);

    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/ui/"));
    expect(navigate.mock.calls.flat().join(" ")).not.toContain("evil.example");
  });

  it("401 清空 Key 并提示 Key 无效", async () => {
    setLocation("");
    mocks.settings.mockRejectedValueOnce(new ApiError("AMKR 请求失败（HTTP 401）: invalid", 401, "invalid"));
    const { navigate, user } = renderLogin();
    await submitKey(user);

    expect(await screen.findByText(/本地鉴权 Key 无效/)).toBeInTheDocument();
    expect(mocks.setKey).toHaveBeenCalledWith("");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("服务不可达不清空 Key，并提示无法连接", async () => {
    setLocation("");
    mocks.settings.mockRejectedValueOnce(new Error("network down"));
    const { navigate, user } = renderLogin();
    await submitKey(user);

    expect(await screen.findByText(/无法连接 AMKR 服务：network down/)).toBeInTheDocument();
    expect(mocks.setKey).toHaveBeenCalledWith("secret-key");
    expect(mocks.setKey).not.toHaveBeenCalledWith("");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("reason=expired 时显示 Key 已失效提示", async () => {
    setLocation("?reason=expired");
    renderLogin();
    expect(await screen.findByText("本地鉴权 Key 已失效，请重新输入。")).toBeInTheDocument();
  });
});
