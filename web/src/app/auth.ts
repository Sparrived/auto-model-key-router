import { useEffect, useState } from "react";
import { ApiError, api, apiBase, onUnauthorized, setKey } from "@/lib/api";
import type { HealthResponse } from "@/lib/types";

export type AuthOutcome = "ok" | "unauthorized" | "unreachable";

export interface HealthLike {
  local_auth_enabled?: boolean;
  version?: string;
  status?: string;
  [key: string]: unknown;
}

export interface LocationLike {
  pathname: string;
  search?: string;
  hash?: string;
}

// 启动鉴权只回答三件事：可进、Key 不对、服务连不上。把判断做成纯函数，登录页与
// 主界面就不会各自解释一遍“什么算未授权”。
export async function verifyAccess(
  health: HealthLike | null,
  probe: () => Promise<unknown>,
): Promise<AuthOutcome> {
  if (!health) return "unreachable";
  if (!health.local_auth_enabled) return "ok";
  try {
    await probe();
    return "ok";
  } catch (error) {
    if (error instanceof ApiError && error.isUnauthorized) return "unauthorized";
    return "unreachable";
  }
}

// 登录页地址必须从 apiBase() 推导：独立部署是 /ui/，嵌在宿主里可能是 /amkr/ui/。
// next 保留当前 path + search + hash，登录成功后能回到原来的深链。
export function loginURL(
  reason: string | null | undefined = null,
  locationLike: LocationLike = window.location,
  base: string = apiBase(),
): string {
  const params = new URLSearchParams();
  const next = `${locationLike.pathname}${locationLike.search ?? ""}${locationLike.hash ?? ""}`;
  if (next && next !== "/") params.set("next", next);
  if (reason) params.set("reason", reason);
  const query = params.toString();
  return `${base}/ui/login.html${query ? `?${query}` : ""}`;
}

export interface LoginRedirector {
  redirect: (reason?: string | null) => boolean;
  readonly redirected: boolean;
}

// 健康轮询会反复触发鉴权失败；只跳一次，避免历史记录里堆出多条登录页。
export function createLoginRedirector(
  replace: (url: string) => void = (url) => window.location.replace(url),
): LoginRedirector {
  let redirected = false;
  return {
    redirect(reason = null) {
      if (redirected) return false;
      redirected = true;
      replace(loginURL(reason));
      return true;
    },
    get redirected() {
      return redirected;
    },
  };
}

export const redirectToLogin = createLoginRedirector((url) => {
  // dev 下 Vite 从根路径提供入口，登录页也在 /login.html；直接用代理到后端的
  // /ui/login.html 会打开旧版 WebUI。生产构建仍按 apiBase 拼正确子路径。
  if (import.meta.env.DEV) {
    const parsed = new URL(url, window.location.origin);
    window.location.replace(`/login.html${parsed.search}`);
    return;
  }
  window.location.replace(url);
});

export const MIN_VERSION = "4.0.0";

// 与 legacy app.js 的 versionCompatible 一致：未知版本放行，格式不合法或低于
// 4.0.0 时只开放设置页。
export function versionCompatible(version: string | null | undefined): boolean {
  if (!version) return true;
  const parse = (value: string) => value.split(".").slice(0, 3).map((part) => parseInt(part, 10));
  const current = parse(version);
  const minimum = parse(MIN_VERSION);
  if (current.length !== 3 || current.some(Number.isNaN)) return false;
  for (let i = 0; i < 3; i += 1) {
    if (current[i] !== minimum[i]) return current[i] > minimum[i];
  }
  return true;
}

export interface AuthState {
  status: "checking" | AuthOutcome;
}

// 入口用的薄 hook：先 /health 再验 Key；未通过就 replace 到登录页，并让调用方
// 保持“什么都不渲染”。错误 Key 清掉，连不上则保留，避免网络抖动误删有效凭据。
export function useAuth(): AuthState {
  const [status, setStatus] = useState<AuthState["status"]>("checking");

  useEffect(() => {
    let alive = true;
    void (async () => {
      let health: HealthResponse | null = null;
      try {
        health = await api.health();
      } catch {
        health = null;
      }
      const outcome = await verifyAccess(health, () => api.settings());
      if (!alive) return;
      if (outcome === "ok") {
        setStatus("ok");
        return;
      }
      if (outcome === "unauthorized") setKey("");
      setStatus(outcome);
      redirectToLogin.redirect();
    })();
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    if (status !== "ok") return;
    onUnauthorized(() => {
      redirectToLogin.redirect("expired");
    });
    return () => onUnauthorized(null);
  }, [status]);

  return { status };
}
