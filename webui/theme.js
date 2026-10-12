// 主题管理：支持跟随系统、浅色、深色以及自定义（默认当前暖色赤陶）主题。
//
// 规范：
// 1. 默认"跟随系统"（system），自动响应 OS prefers-color-scheme 的深浅色变化。
// 2. 浅色主题（light）提供明亮、高对比度的数据工作台风格。
// 3. 深色主题（dark）提供低照度、沉浸式现代控制台风格。
// 4. 自定义主题（custom）以项目原有的暖色赤陶（warm-dashboard）为基准，并支持定制关键色彩。
// 5. 存储键使用 amkr.theme，与鉴权凭据隔离，隐私模式异常时安全回退。

export const THEME_STORAGE_KEY = "amkr.theme";
export const CUSTOM_COLORS_STORAGE_KEY = "amkr.themeCustom";

export const THEMES = [
  { id: "system", label: "跟随系统", icon: "monitor", desc: "自动同步系统深色/浅色偏好" },
  { id: "light", label: "浅色主题", icon: "sun", desc: "明快清爽的标准工作台配色" },
  { id: "dark", label: "深色主题", icon: "moon", desc: "深色低照度现代控制台配色" },
  { id: "custom", label: "自定义主题", icon: "palette", desc: "当前暖色赤陶配色，支持定制关键色彩" },
];

export const DEFAULT_CUSTOM_COLORS = Object.freeze({
  canvas: "#c9967a",
  surface: "#faf8f5",
  action: "#a34f3d",
  text: "#49392f",
});

const listeners = new Set();
let mediaListenerAttached = false;

function safeStorageGet(key) {
  try {
    return globalThis.localStorage?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

function safeStorageSet(key, value) {
  try {
    globalThis.localStorage?.setItem(key, value);
  } catch {}
}

function safeStorageRemove(key) {
  try {
    globalThis.localStorage?.removeItem(key);
  } catch {}
}

function getDoc() {
  if (typeof document !== "undefined" && document.documentElement) {
    return document.documentElement;
  }
  return null;
}

export function getThemePreference() {
  const saved = safeStorageGet(THEME_STORAGE_KEY);
  if (saved && THEMES.some((t) => t.id === saved)) {
    return saved;
  }
  return "system";
}

export function getSystemScheme() {
  try {
    if (typeof window !== "undefined" && window.matchMedia) {
      return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    }
  } catch {}
  return "light";
}

export function getResolvedTheme(preference = getThemePreference()) {
  if (preference === "system") {
    return getSystemScheme();
  }
  return preference;
}

export function getCustomColors() {
  const raw = safeStorageGet(CUSTOM_COLORS_STORAGE_KEY);
  if (raw) {
    try {
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === "object") {
        return {
          canvas: parsed.canvas || DEFAULT_CUSTOM_COLORS.canvas,
          surface: parsed.surface || DEFAULT_CUSTOM_COLORS.surface,
          action: parsed.action || DEFAULT_CUSTOM_COLORS.action,
          text: parsed.text || DEFAULT_CUSTOM_COLORS.text,
        };
      }
    } catch {}
  }
  return { ...DEFAULT_CUSTOM_COLORS };
}

export function setCustomColors(colors) {
  const merged = {
    canvas: colors?.canvas || DEFAULT_CUSTOM_COLORS.canvas,
    surface: colors?.surface || DEFAULT_CUSTOM_COLORS.surface,
    action: colors?.action || DEFAULT_CUSTOM_COLORS.action,
    text: colors?.text || DEFAULT_CUSTOM_COLORS.text,
  };
  safeStorageSet(CUSTOM_COLORS_STORAGE_KEY, JSON.stringify(merged));
  applyTheme();
  notifyThemeListeners();
}

export function resetCustomColors() {
  safeStorageRemove(CUSTOM_COLORS_STORAGE_KEY);
  applyTheme();
  notifyThemeListeners();
}

export function isCustomColorsModified() {
  const current = getCustomColors();
  return (
    current.canvas !== DEFAULT_CUSTOM_COLORS.canvas ||
    current.surface !== DEFAULT_CUSTOM_COLORS.surface ||
    current.action !== DEFAULT_CUSTOM_COLORS.action ||
    current.text !== DEFAULT_CUSTOM_COLORS.text
  );
}

export function setThemePreference(preference) {
  if (!THEMES.some((t) => t.id === preference)) return;
  safeStorageSet(THEME_STORAGE_KEY, preference);
  applyTheme();
  notifyThemeListeners();
}

export function cycleTheme() {
  const current = getThemePreference();
  const index = THEMES.findIndex((t) => t.id === current);
  const next = THEMES[(index + 1) % THEMES.length].id;
  setThemePreference(next);
  return next;
}

function updateThemeColorMeta(color) {
  try {
    if (typeof document === "undefined") return;
    let meta = document.querySelector('meta[name="theme-color"]');
    if (!meta) {
      meta = document.createElement("meta");
      meta.name = "theme-color";
      document.head?.appendChild(meta);
    }
    meta.content = color;
  } catch {}
}

export function applyTheme() {
  const doc = getDoc();
  if (!doc) return;
  const preference = getThemePreference();
  const resolved = getResolvedTheme(preference);

  doc.setAttribute("data-theme-preference", preference);
  doc.setAttribute("data-theme", resolved);

  if (resolved === "custom") {
    const colors = getCustomColors();
    if (isCustomColorsModified()) {
      doc.style.setProperty("--ui-canvas", colors.canvas);
      doc.style.setProperty("--ui-surface", colors.surface);
      doc.style.setProperty("--ui-action", colors.action);
      doc.style.setProperty("--ui-text", colors.text);
      doc.style.setProperty("--ui-text-on-canvas", colors.text);
      doc.style.setProperty("--ui-focus", colors.action);
    } else {
      doc.style.removeProperty("--ui-canvas");
      doc.style.removeProperty("--ui-surface");
      doc.style.removeProperty("--ui-action");
      doc.style.removeProperty("--ui-text");
      doc.style.removeProperty("--ui-text-on-canvas");
      doc.style.removeProperty("--ui-focus");
    }
    updateThemeColorMeta(colors.canvas);
  } else {
    doc.style.removeProperty("--ui-canvas");
    doc.style.removeProperty("--ui-surface");
    doc.style.removeProperty("--ui-action");
    doc.style.removeProperty("--ui-text");
    doc.style.removeProperty("--ui-text-on-canvas");
    doc.style.removeProperty("--ui-focus");
    updateThemeColorMeta(resolved === "dark" ? "#0f172a" : "#f1f5f9");
  }
}

function notifyThemeListeners() {
  const info = {
    preference: getThemePreference(),
    resolved: getResolvedTheme(),
    customColors: getCustomColors(),
  };
  for (const listener of listeners) {
    try {
      listener(info);
    } catch (error) {
      console.warn("theme listener failed", error);
    }
  }
}

export function onThemeChange(listener) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function initTheme() {
  if (typeof window !== "undefined" && window.matchMedia && !mediaListenerAttached) {
    try {
      const mql = window.matchMedia("(prefers-color-scheme: dark)");
      mql.addEventListener?.("change", () => {
        if (getThemePreference() === "system") {
          applyTheme();
          notifyThemeListeners();
        }
      });
      mediaListenerAttached = true;
    } catch {}
  }
  applyTheme();
}
