// 主题管理：跟随系统、浅色、深色三档，外加可换的主色。
//
// 规范：
// 1. 默认"跟随系统"（system），自动响应 OS prefers-color-scheme 的深浅色变化。
// 2. 存储键使用 amkr.theme，与鉴权凭据隔离；旧版的 "custom" 主题已取消，读取时按 light 处理。
// 3. 主色存 amkr.accent：ink（默认墨色）/ cobalt / pine / #rrggbb。
// 4. 隐私模式下 localStorage 异常时安全回退，不阻塞页面。
//
// 这里只写 data-theme / data-accent 与自定义主色的 CSS 变量；具体色值在 tokens.css。

import { readStorage, removeStorage, writeStorage } from "./storage";

export const THEME_STORAGE_KEY = "amkr.theme";
export const ACCENT_STORAGE_KEY = "amkr.accent";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";
export type AccentPreference = "ink" | "cobalt" | "pine" | (string & {});

export interface ThemeInfo {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  accent: AccentPreference;
}

export const THEME_OPTIONS: Array<{ id: ThemePreference; label: string; hint: string }> = [
  { id: "system", label: "跟随系统", hint: "自动同步系统深色/浅色偏好" },
  { id: "light", label: "浅色", hint: "明快清爽的调度台配色" },
  { id: "dark", label: "深色", hint: "低照度深色控制台配色" },
];

export const ACCENT_OPTIONS: Array<{ id: "ink" | "cobalt" | "pine"; label: string; hint: string }> = [
  { id: "ink", label: "墨", hint: "默认：骨架保持单色，颜色只留给数据" },
  { id: "cobalt", label: "钴蓝", hint: "冷色强调，适合长时间监控" },
  { id: "pine", label: "松绿", hint: "低饱和绿色强调" },
];

const listeners = new Set<(info: ThemeInfo) => void>();
let mediaListenerAttached = false;

export function getThemePreference(): ThemePreference {
  const saved = readStorage(THEME_STORAGE_KEY);
  // 旧值 custom 视为 light：暖色赤陶主题已取消，继续把它当自定义会落回无样式状态。
  if (saved === "custom") return "light";
  if (saved === "light" || saved === "dark" || saved === "system") return saved;
  return "system";
}

export function getSystemScheme(): ResolvedTheme {
  try {
    if (typeof window !== "undefined" && window.matchMedia) {
      return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    }
  } catch {
    // matchMedia 不可用（测试/旧浏览器）时按浅色。
  }
  return "light";
}

export function getResolvedTheme(preference: ThemePreference = getThemePreference()): ResolvedTheme {
  if (preference === "system") return getSystemScheme();
  return preference;
}

export function getAccentPreference(): AccentPreference {
  const saved = readStorage(ACCENT_STORAGE_KEY);
  if (saved === "cobalt" || saved === "pine" || saved === "ink") return saved;
  if (saved && /^#[0-9a-fA-F]{6}$/.test(saved)) return saved;
  return "ink";
}

export function setThemePreference(preference: ThemePreference): void {
  if (!THEME_OPTIONS.some((option) => option.id === preference)) return;
  writeStorage(THEME_STORAGE_KEY, preference);
  applyTheme();
  notifyThemeListeners();
}

// 循环切换：跟随系统 → 浅色 → 深色 → 跟随系统。
export function cycleTheme(): ThemePreference {
  const order: ThemePreference[] = ["system", "light", "dark"];
  const index = order.indexOf(getThemePreference());
  const next = order[(index + 1) % order.length];
  setThemePreference(next);
  return next;
}

export function setAccentPreference(accent: AccentPreference): void {
  const value = String(accent || "").trim();
  if (value === "ink") {
    removeStorage(ACCENT_STORAGE_KEY);
  } else if (value === "cobalt" || value === "pine" || /^#[0-9a-fA-F]{6}$/.test(value)) {
    writeStorage(ACCENT_STORAGE_KEY, value);
  } else {
    return;
  }
  applyTheme();
  notifyThemeListeners();
}

// setAccent 是 setAccentPreference 的短别名，页面代码读起来更顺。
export const setAccent = setAccentPreference;

function getDoc(): HTMLElement | null {
  if (typeof document !== "undefined" && document.documentElement) {
    return document.documentElement;
  }
  return null;
}

export function applyTheme(): void {
  const doc = getDoc();
  if (!doc) return;
  const preference = getThemePreference();
  const resolved = getResolvedTheme(preference);

  doc.setAttribute("data-theme-preference", preference);
  doc.setAttribute("data-theme", resolved);
  applyAccent(doc, getAccentPreference());
  updateThemeColorMeta(resolved);
}

// applyAccent 只负责主色：命名预设交给 tokens.css 的 [data-accent] 选择器，
// 自定义 hex 则写内联变量。切回命名预设时必须清掉内联变量，否则旧的 hex 会继续覆盖。
function applyAccent(doc: HTMLElement, accent: AccentPreference): void {
  doc.style.removeProperty("--accent");
  doc.style.removeProperty("--accent-hover");
  doc.style.removeProperty("--on-accent");

  if (accent === "ink") {
    doc.removeAttribute("data-accent");
    return;
  }
  if (accent === "cobalt" || accent === "pine") {
    doc.setAttribute("data-accent", accent);
    return;
  }
  if (!/^#[0-9a-fA-F]{6}$/.test(accent)) {
    doc.removeAttribute("data-accent");
    return;
  }

  const resolved = getResolvedTheme();
  doc.setAttribute("data-accent", "custom");
  doc.style.setProperty("--accent", accent);
  doc.style.setProperty("--accent-hover", adjust(accent, resolved === "dark" ? 0.14 : -0.12));
  // on-accent 取与自定义色对比度更高的黑或白。两者中较大的对比度对任意颜色都 ≥ 4.5，
  // 因此不需要再兜底一个中性色。
  doc.style.setProperty("--on-accent", bestOnColor(accent));
}

function updateThemeColorMeta(resolved: ResolvedTheme): void {
  try {
    if (typeof document === "undefined") return;
    let meta = document.querySelector('meta[name="theme-color"]');
    if (!meta) {
      meta = document.createElement("meta");
      meta.setAttribute("name", "theme-color");
      document.head?.appendChild(meta);
    }
    meta.setAttribute("content", resolved === "dark" ? "#121518" : "#f2f3f1");
  } catch {
    // 纯装饰：失败不影响主题本身。
  }
}

function notifyThemeListeners(): void {
  const info = themeInfo();
  for (const listener of listeners) {
    try {
      listener(info);
    } catch (error) {
      console.warn("theme listener failed", error);
    }
  }
}

export function themeInfo(): ThemeInfo {
  return {
    preference: getThemePreference(),
    resolved: getResolvedTheme(),
    accent: getAccentPreference(),
  };
}

export function onThemeChange(listener: (info: ThemeInfo) => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// onSystemSchemeChange 订阅系统深浅色变化；返回取消订阅函数。
export function onSystemSchemeChange(listener: (resolved: ResolvedTheme) => void): () => void {
  if (typeof window === "undefined" || !window.matchMedia) return () => {};
  const query = window.matchMedia("(prefers-color-scheme: dark)");
  const handler = (event: MediaQueryListEvent) => listener(event.matches ? "dark" : "light");
  query.addEventListener?.("change", handler);
  return () => query.removeEventListener?.("change", handler);
}

// initTheme 在应用挂载前调用：应用当前主题并开始跟踪系统偏好。
export function initTheme(): void {
  if (typeof window !== "undefined" && window.matchMedia && !mediaListenerAttached) {
    try {
      const query = window.matchMedia("(prefers-color-scheme: dark)");
      query.addEventListener?.("change", () => {
        // 只有"跟随系统"时才需要重绘；显式浅/深色不应被系统切换覆盖。
        if (getThemePreference() === "system") {
          applyTheme();
          notifyThemeListeners();
        }
      });
      mediaListenerAttached = true;
    } catch {
      // matchMedia 在极端环境下会抛异常：忽略，主题仍按当前偏好应用一次。
    }
  }
  applyTheme();
}

// —— 颜色算术（自定义主色用）——

function parseHex(hex: string): [number, number, number] | null {
  const match = /^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$/.exec(hex);
  if (!match) return null;
  return [parseInt(match[1], 16), parseInt(match[2], 16), parseInt(match[3], 16)];
}

function toHex(rgb: [number, number, number]): string {
  const part = (value: number) => Math.max(0, Math.min(255, Math.round(value))).toString(16).padStart(2, "0");
  return `#${part(rgb[0])}${part(rgb[1])}${part(rgb[2])}`;
}

function channelLuminance(value: number): number {
  const c = value / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const rgb = parseHex(hex);
  if (!rgb) return 0;
  return 0.2126 * channelLuminance(rgb[0])
    + 0.7152 * channelLuminance(rgb[1])
    + 0.0722 * channelLuminance(rgb[2]);
}

function contrast(a: string, b: string): number {
  const la = luminance(a);
  const lb = luminance(b);
  const [light, dark] = la > lb ? [la, lb] : [lb, la];
  return (light + 0.05) / (dark + 0.05);
}

// 与背景混合：amount 为正往白走、为负往黑走。用于推导 hover 色。
function adjust(hex: string, amount: number): string {
  const rgb = parseHex(hex);
  if (!rgb) return hex;
  const target = amount >= 0 ? 255 : 0;
  const ratio = Math.abs(amount);
  return toHex([
    rgb[0] + (target - rgb[0]) * ratio,
    rgb[1] + (target - rgb[1]) * ratio,
    rgb[2] + (target - rgb[2]) * ratio,
  ]);
}

function bestOnColor(background: string): string {
  const light = "#fcfcfb";
  const dark = "#121518";
  return contrast(background, light) >= contrast(background, dark) ? light : dark;
}

// 只给测试/诊断用：判断自定义色在当前主题下的实际对比度。
export function accentContrast(accent: string, onColor?: string): number {
  return contrast(accent, onColor || bestOnColor(accent));
}
