// 访客看板的独立 API 客户端。
//
// 访客 = 访问密钥持有者，只能读自己的用量。安全边界（与 legacy guest-api.js 一致）：
//   - 只用专门的存储键 amkr.guestAccessKey，绝不读写管理面的 amkr.apiKey；
//   - 只发 GET；本客户端根本不提供写方法；
//   - 不发送 workspace / key_id，能看多少完全由服务端按这把 key 决定；
//   - 401（凭据无效）与 403（已停用）保留服务端状态，页面据此给不同指引。
//
// 刻意不复用 @/lib/api：那个客户端会读 amkr.apiKey，并在 401 时触发管理面跳转。

import type { MetricStats, WindowInfo } from "@/lib/types";

const GUEST_KEY = "amkr.guestAccessKey";

export class GuestApiError extends Error {
  status: number;
  detail: string | null;

  constructor(message: string, status: number, detail: string | null) {
    super(message);
    this.name = "GuestApiError";
    this.status = status;
    this.detail = detail;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  get isDisabled(): boolean {
    return this.status === 403;
  }
}

export interface GuestRecentRequest {
  created_at?: string;
  model_id?: string | null;
  upstream_model_id?: string | null;
  provider_id?: string | null;
  status_code?: number | null;
  success?: boolean;
  retried?: boolean;
  total_tokens?: number;
  duration_ms?: number;
  [key: string]: unknown;
}

export interface GuestUsageResponse {
  window?: WindowInfo;
  access_key_id?: string;
  access_key_name?: string | null;
  stats?: MetricStats;
  dimensions?: Record<string, Record<string, MetricStats>>;
  recent_requests?: GuestRecentRequest[];
  [key: string]: unknown;
}

export interface GuestPricingResponse {
  models?: Record<string, unknown>;
  [key: string]: unknown;
}

export function guestApiBase(pathname = location.pathname): string {
  const path = String(pathname || "");
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) return path.slice(0, index);
  if (path.endsWith("/ui")) return path.slice(0, -3);
  return "";
}

export function guestCredential(): string {
  try {
    return globalThis.localStorage?.getItem(GUEST_KEY) || "";
  } catch {
    return "";
  }
}

export function saveGuestCredential(key: string): void {
  try {
    if (key) globalThis.localStorage?.setItem(GUEST_KEY, key);
    else globalThis.localStorage?.removeItem(GUEST_KEY);
  } catch {
    // 隐私模式不影响本次会话；页面手上仍有 key。
  }
}

function errorDetail(payload: unknown, status: number): string {
  if (payload && typeof payload === "object") {
    const error = (payload as { error?: { message?: unknown } }).error;
    if (error && typeof error.message === "string") return error.message;
    const detail = (payload as { detail?: unknown }).detail;
    if (typeof detail === "string") return detail;
  }
  return `HTTP ${status}`;
}

export async function guestRequest(key: string, path: string): Promise<unknown> {
  const headers: Record<string, string> = {};
  // 空 key 是公开价格目录：不发 `Bearer ` 的空头，否则服务端会把它当成无效凭据。
  if (key) headers.Authorization = `Bearer ${key}`;
  let response: Response;
  try {
    response = await fetch(`${guestApiBase()}${path}`, { method: "GET", headers });
  } catch (failure) {
    throw new GuestApiError(`无法连接 AMKR 服务: ${(failure as Error).message}`, 0, null);
  }
  const text = await response.text();
  let payload: unknown = null;
  if (text) {
    try { payload = JSON.parse(text); } catch { payload = text; }
  }
  if (!response.ok) {
    const detail = errorDetail(payload, response.status);
    throw new GuestApiError(String(detail), response.status, detail);
  }
  return payload;
}

export function createGuestApi(key: string) {
  return {
    usage: ({ hours = 24, allHistory = false }: { hours?: number; allHistory?: boolean } = {}): Promise<GuestUsageResponse> =>
      guestRequest(key, allHistory
        ? "/ui/access-key-usage.json?all_history=true"
        : `/ui/access-key-usage.json?hours=${encodeURIComponent(hours)}`) as Promise<GuestUsageResponse>,
    // 价格目录是公开数据；传空 key 不附带 Authorization。
    pricing: (): Promise<GuestPricingResponse> =>
      guestRequest("", "/ui/pricing.json") as Promise<GuestPricingResponse>,
  };
}

export { GUEST_KEY };
