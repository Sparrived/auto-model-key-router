import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api";
import { createLoginRedirector, loginURL, versionCompatible, verifyAccess } from "./auth";

describe("auth", () => {
  it("没有健康信息时判为不可达", async () => {
    await expect(verifyAccess(null, async () => ({}))).resolves.toBe("unreachable");
  });

  it("未启用本地鉴权时直接放行", async () => {
    const probe = vi.fn(async () => ({}));
    await expect(verifyAccess({ local_auth_enabled: false }, probe)).resolves.toBe("ok");
    expect(probe).not.toHaveBeenCalled();
  });

  it("探针成功时放行", async () => {
    await expect(verifyAccess({ local_auth_enabled: true }, async () => ({}))).resolves.toBe("ok");
  });

  it("探针 401 时判为未授权", async () => {
    const error = new ApiError("unauthorized", 401, "bad key");
    await expect(
      verifyAccess({ local_auth_enabled: true }, async () => {
        throw error;
      }),
    ).resolves.toBe("unauthorized");
  });

  it("探针网络错误时判为不可达", async () => {
    await expect(
      verifyAccess({ local_auth_enabled: true }, async () => {
        throw new Error("offline");
      }),
    ).resolves.toBe("unreachable");
  });

  it("登录地址保留 next 与 reason，并带上子路径前缀", () => {
    const url = loginURL("expired", { pathname: "/amkr/ui/index.html", search: "?x=1", hash: "#/activity?hours=6" }, "/amkr");
    const parsed = new URL(url, "http://localhost");
    expect(parsed.pathname).toBe("/amkr/ui/login.html");
    expect(parsed.searchParams.get("next")).toBe("/amkr/ui/index.html?x=1#/activity?hours=6");
    expect(parsed.searchParams.get("reason")).toBe("expired");
  });

  it("根路径不写 next", () => {
    const url = loginURL(null, { pathname: "/", search: "", hash: "" }, "");
    expect(url).toBe("/ui/login.html");
  });

  it("只跳一次登录页", () => {
    const replace = vi.fn();
    const redirector = createLoginRedirector(replace);
    expect(redirector.redirect()).toBe(true);
    expect(redirector.redirect("expired")).toBe(false);
    expect(replace).toHaveBeenCalledTimes(1);
  });

  it("版本门槛与 legacy 一致", () => {
    expect(versionCompatible(undefined)).toBe(true);
    expect(versionCompatible("4.0.0")).toBe(true);
    expect(versionCompatible("6.9.1")).toBe(true);
    expect(versionCompatible("3.9.9")).toBe(false);
    expect(versionCompatible("4.0")).toBe(false);
    expect(versionCompatible("abc")).toBe(false);
  });
});
