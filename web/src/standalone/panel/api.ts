// 工作空间面板的独立 API 客户端。
//
// 安全边界（与 legacy panel-api.js 一致）：
//   1. 凭据只从 URL fragment（#k=）来，只存在内存；
//   2. 绝不读写 localStorage——面板常与管理面同源，共享存储会让两把 Key 互相覆盖；
//   3. 绝不发送 X-AMKR-Workspace——可见空间完全由这把 key 固定。
//
// 这里刻意不复用主控制台的 @/lib/api：那个 request() 会读 amkr.apiKey，并在 401 时
// 触发管理面的全局跳转，面板不该有这两个副作用。

import type { MetricStats, WindowInfo, WorkspaceUsageLink } from "@/lib/types";

export class PanelApiError extends Error {
  status: number;
  detail: string | null;

  constructor(message: string, status: number, detail: string | null) {
    super(message);
    this.name = "PanelApiError";
    this.status = status;
    this.detail = detail;
  }

  get isUnauthorized(): boolean {
    return this.status === 401;
  }
}

export interface PanelCredential {
  key: string;
  base: string;
}

export interface PanelUsage {
  workspace?: string;
  window?: WindowInfo;
  models?: string[];
  layers?: string[];
  links?: WorkspaceUsageLink[];
  workspaces?: Array<{ name?: string; stats?: MetricStats }>;
  [key: string]: unknown;
}

export interface PanelTask {
  name: string;
  display_name?: string | null;
  model?: string | null;
  [key: string]: unknown;
}

export interface PanelTasksResponse {
  tasks?: PanelTask[];
  config_revision?: string | null;
  [key: string]: unknown;
}

export interface PanelTaskPayload {
  name?: string;
  display_name?: string | null;
  model?: string | null;
}

export function panelApiBase(pathname = location.pathname): string {
  const path = String(pathname || "");
  const index = path.lastIndexOf("/ui/");
  if (index >= 0) return path.slice(0, index);
  if (path.endsWith("/ui")) return path.slice(0, -3);
  return "";
}

export function panelCredential(hash = location.hash, pathname = location.pathname): PanelCredential {
  const params = new URLSearchParams(String(hash || "").replace(/^#/, ""));
  const key = (params.get("k") || "").trim();
  const customBase = (params.get("api") || "").trim().replace(/\/+$/, "");
  return { key, base: customBase || panelApiBase(pathname) };
}

function detailText(payload: unknown, status: number): string {
  if (payload && typeof payload === "object") {
    const error = (payload as { error?: { message?: unknown } }).error;
    if (error && typeof error.message === "string") return error.message;
    const detail = (payload as { detail?: unknown }).detail;
    if (typeof detail === "string") return detail;
    if (detail) return JSON.stringify(detail);
  }
  return `HTTP ${status}`;
}

export async function panelRequest(
  credential: PanelCredential,
  path: string,
  { method = "GET", body }: { method?: string; body?: unknown } = {},
): Promise<unknown> {
  const headers: Record<string, string> = { Authorization: `Bearer ${credential.key}` };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let response: Response;
  try {
    response = await fetch(`${credential.base}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (failure) {
    throw new PanelApiError(`无法连接 AMKR 服务: ${(failure as Error).message}`, 0, null);
  }
  if (response.status === 204) return null;
  const text = await response.text();
  let payload: unknown = null;
  if (text) {
    try { payload = JSON.parse(text); } catch { payload = text; }
  }
  if (!response.ok) {
    const detail = detailText(payload, response.status);
    throw new PanelApiError(`AMKR 请求失败（HTTP ${response.status}）: ${detail}`, response.status, detail);
  }
  return payload;
}

export function createPanelApi(credential: PanelCredential) {
  const call = (path: string, options?: { method?: string; body?: unknown }) => panelRequest(credential, path, options);
  return {
    usage: ({ hours = 24, allHistory = false }: { hours?: number; allHistory?: boolean } = {}): Promise<PanelUsage> =>
      call(allHistory
        ? "/ui/workspace-panel.json?all_history=true"
        : `/ui/workspace-panel.json?hours=${encodeURIComponent(hours)}`) as Promise<PanelUsage>,
    tasks: (): Promise<PanelTasksResponse> => call("/api/tasks") as Promise<PanelTasksResponse>,
    createTask: (revision: string, payload: PanelTaskPayload): Promise<PanelTasksResponse> =>
      call("/api/tasks", { method: "POST", body: { config_revision: revision, ...payload } }) as Promise<PanelTasksResponse>,
    updateTask: (revision: string, taskName: string, payload: PanelTaskPayload): Promise<PanelTasksResponse> =>
      call(`/api/tasks/${encodeURIComponent(taskName)}`, {
        method: "PUT",
        body: { config_revision: revision, ...payload },
      }) as Promise<PanelTasksResponse>,
    deleteTask: (revision: string, taskName: string): Promise<PanelTasksResponse> =>
      call(`/api/tasks/${encodeURIComponent(taskName)}`, {
        method: "DELETE",
        body: { config_revision: revision },
      }) as Promise<PanelTasksResponse>,
  };
}
