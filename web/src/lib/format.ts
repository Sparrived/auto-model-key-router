// 时间与数值的展示口径。
//
// 与 chart-math.ts 的分工：这里面向"人读的一行字"（表格、卡片、提示），
// chart-math 面向"画图用的数"。两边精度刻意保持一致，避免同屏两个格式。

export type DateValue = string | number | Date | null | undefined;

// —— 时间 ——
// 后端所有时间戳都是带 +08:00 偏移的 ISO 串（Asia/Shanghai）。展示时固定用
// 同一时区渲染，避免浏览器时区与后端统计口径不一致导致图表读起来错位。

export const DISPLAY_TZ = "Asia/Shanghai";

function toDate(value: DateValue): Date | null {
  if (!value) return null;
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function formatClock(value: DateValue): string {
  const date = toDate(value);
  if (!date) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: DISPLAY_TZ, hour: "2-digit", minute: "2-digit", hour12: false,
  }).format(date);
}

export function formatClockSeconds(value: DateValue): string {
  const date = toDate(value);
  if (!date) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: DISPLAY_TZ, hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
  }).format(date);
}

export function formatDateTime(value: DateValue): string {
  const date = toDate(value);
  if (!date) return "-";
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: DISPLAY_TZ, month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hour12: false,
  }).format(date);
}

// 坐标轴时间标签：格式跟着**整段跨度**走，而不是固定成时刻。
//
// 固定 "HH:MM" 在长窗口下会退化成噪声：1 年窗口（366 个日桶）里每个点都是
// 同一个月日交替的 "00:00"，读不出"这是几月"，轴就白标了。分档依据是跨度而不是
// 桶宽 —— 6 小时窗口配 5 分钟桶仍是"看时刻"，而 1 个月窗口配 1 小时桶要看日期。
export function formatAxisTime(value: DateValue, spanSeconds: number): string {
  const date = toDate(value);
  if (!date) return "-";
  const span = Number(spanSeconds) || 0;
  // 两天以内：时刻就够定位（"10:30" 落在今天还是昨天由上下文可知）。
  if (span <= 2 * 86400) return formatClock(date);
  // 两个月以内：月日足够，带上时刻会挤成一团。
  if (span <= 60 * 86400) {
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone: DISPLAY_TZ, month: "2-digit", day: "2-digit",
    }).format(date);
  }
  // 再长就看年月：跨年时只给月日会出现两个 "01-01"，无法分辨是哪一年。
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: DISPLAY_TZ, year: "numeric", month: "2-digit",
  }).format(date);
}

// 相对时间只用于"刚刚发生过"的短窗口提示，超过一天就退回绝对时间。
export function formatRelative(value: DateValue, now: number = Date.now()): string {
  const date = toDate(value);
  if (!date) return "-";
  const seconds = Math.round((now - date.getTime()) / 1000);
  if (seconds < 0) return "刚刚";
  if (seconds < 10) return "刚刚";
  if (seconds < 60) return `${seconds} 秒前`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
  return formatDateTime(date);
}

// —— 数值 ——
export function formatCount(value: number | string | null | undefined): string {
  if (value === null || value === undefined) return "-";
  const n = Number(value);
  if (!Number.isFinite(n)) return "-";
  return n.toLocaleString("zh-CN");
}

export function formatCompact(value: number | string | null | undefined): string {
  if (value === null || value === undefined) return "-";
  const n = Number(value);
  if (!Number.isFinite(n)) return "-";
  if (Math.abs(n) >= 1e9) return trim(n / 1e9) + "B";
  if (Math.abs(n) >= 1e6) return trim(n / 1e6) + "M";
  if (Math.abs(n) >= 1e3) return trim(n / 1e3) + "K";
  return formatCount(n);
}

function trim(n: number): string {
  // 1 位小数足够读，避免 1.0M / 12.3K 这类噪声撑宽坐标轴。
  return n.toFixed(1).replace(/\.0$/, "");
}

// 成功率类指标：分母为 0 时没有意义，返回 "-" 而不是 0%，否则会谎报"全失败"。
export function formatPercent(
  numerator: number | string | null | undefined,
  denominator: number | string | null | undefined,
): string {
  const total = Number(denominator);
  if (!total) return "-";
  const ratio = Number(numerator) / total;
  return formatRatio(ratio);
}

export function formatRatio(ratio: number | string | null | undefined, digits = 1): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(Number(ratio))) return "-";
  const percent = Number(ratio) * 100;
  if (percent > 0 && percent < 100) return `${percent.toFixed(digits)}%`;
  return `${Math.round(percent)}%`;
}

export function formatRate(rate: number | string | null | undefined, digits = 1): string {
  if (rate === null || rate === undefined) return "-";
  return formatRatio(rate, digits);
}

export function formatDuration(ms: number | string | null | undefined): string {
  if (ms === null || ms === undefined) return "-";
  const n = Number(ms);
  if (!Number.isFinite(n)) return "-";
  if (n >= 10000) return `${(n / 1000).toFixed(1)}s`;
  if (n >= 1000) return `${(n / 1000).toFixed(2)}s`;
  return `${Math.round(n)}ms`;
}

export function shortRevision(revision: string | number | null | undefined): string {
  return revision ? String(revision).slice(0, 8) : "读取中";
}

export function errorText(error: unknown): string {
  return error && (error as { message?: string }).message
    ? (error as { message: string }).message
    : String(error);
}

// —— 剪贴板 ——
export function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext !== false) {
    return navigator.clipboard.writeText(text);
  }
  // 非安全上下文（http://局域网 IP）没有 clipboard API，退回 execCommand。
  const area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  const ok = document.execCommand("copy");
  area.remove();
  return ok ? Promise.resolve() : Promise.reject(new Error("浏览器拒绝了复制操作"));
}

// —— 杂项 ——
export function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

export function truncate(text: string | null | undefined, max = 40): string {
  const value = String(text ?? "");
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}
