// 额度冻结的展示口径与查询失效。
//
// 冻结记录同时出现在两页：账号资源页（/api/cpa-accounts 响应自带 freezes）与供应商页
// （GET /api/quota-freezes）。格式化与「解冻后该刷新哪些查询」必须一致，否则同一把 Key
// 在两边会显示不同的倒计时，或一边解冻了另一边还挂着旧徽标。

import type { QueryClient } from "@tanstack/react-query";
import { formatDateTime } from "./format";
import { queryKeys } from "./query-keys";
import type { QuotaFreeze } from "./types";

// 供应商 ID 与 Key 名都可能含分隔符，用 NUL 拼索引键，避免 "a|b" + "c" 与 "a" + "b|c" 撞车。
const SEPARATOR = "\u0000";

export function freezeLookupKey(providerId: string, keyName: string): string {
  return `${providerId}${SEPARATOR}${keyName}`;
}

/** 把冻结列表索引成「供应商 + Key → 冻结记录」，逐行打徽标时按 O(1) 查。 */
export function indexQuotaFreezes(freezes: QuotaFreeze[] | null | undefined): Map<string, QuotaFreeze> {
  const index = new Map<string, QuotaFreeze>();
  for (const freeze of freezes ?? []) {
    if (!freeze || !freeze.provider_id || !freeze.key_name) continue;
    index.set(freezeLookupKey(freeze.provider_id, freeze.key_name), freeze);
  }
  return index;
}

/** 界面直接显示的人话原因；后端没给时退回一句兜底文案，不留空白。 */
export function freezeReasonText(freeze: QuotaFreeze): string {
  return freeze.reason?.trim() || `${freeze.vendor || freeze.provider_id} 的额度窗口已耗尽`;
}

/**
 * 重置倒计时。优先用窗口重置时间 reset_at；没有就退回冻结的兜底到期时间 expires_at
 * （后端拿不到 reset_at 时按 now + 10 分钟到期），两者都没有时返回空串。
 */
export function freezeResetCountdown(freeze: QuotaFreeze, now: number = Date.now()): string {
  const reset = Date.parse(String(freeze.reset_at ?? ""));
  if (Number.isFinite(reset)) return countdownText(reset - now, "重置");
  const expires = Date.parse(String(freeze.expires_at ?? ""));
  if (Number.isFinite(expires)) return countdownText(expires - now, "自动解冻");
  return "";
}

function countdownText(remainingMs: number, verb: "重置" | "自动解冻"): string {
  if (remainingMs <= 0) return verb === "重置" ? "即将重置" : "即将自动解冻";
  // 刚过一秒也显示「1 分钟后」：写「0 分钟后」等于没有信息。
  const minutes = Math.max(1, Math.round(remainingMs / 60000));
  if (minutes < 60) return `${minutes} 分钟后${verb}`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时 ${minutes % 60} 分钟后${verb}`;
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时后${verb}`;
}

/** 窗口重置的绝对时刻（tooltip 用）；没有或解析不出来时返回空串。 */
export function freezeResetAtText(freeze: QuotaFreeze): string {
  if (!freeze.reset_at) return "";
  const text = formatDateTime(freeze.reset_at);
  return text === "-" ? "" : text;
}

/** 兜底到期时刻（自动解冻）；没有时返回空串。 */
export function freezeExpiresAtText(freeze: QuotaFreeze): string {
  if (!freeze.expires_at) return "";
  const text = formatDateTime(freeze.expires_at);
  return text === "-" ? "" : text;
}

/**
 * 手工解冻后要一起失效的两份数据：账号资源响应里的 freezes 与独立的冻结列表。
 * 不失效的话，另一页（或返回本页时）会继续拿缓存里的旧徽标，好像按钮没生效。
 */
export async function invalidateFreezeViews(client: QueryClient): Promise<void> {
  await Promise.all([
    client.invalidateQueries({ queryKey: queryKeys.cpaAccounts() }),
    client.invalidateQueries({ queryKey: queryKeys.quotaFreezes() }),
  ]);
}
