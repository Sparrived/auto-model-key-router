import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  ACCENT_STORAGE_KEY,
  applyTheme,
  getAccentPreference,
  normalizeAccentPreference,
  setAccentPreference,
} from "./theme";

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-accent");
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.style.cssText = "";
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-accent");
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.style.cssText = "";
});

describe("工业工程配色主题预设", () => {
  it("默认回落到工业信号橙（signal），不写 data-accent 属性", () => {
    expect(getAccentPreference()).toBe("signal");
    applyTheme();
    expect(document.documentElement.getAttribute("data-accent")).toBeNull();
  });

  it("旧版 cobalt 自动迁移映射为 mineral，pine 保持独立预设", () => {
    expect(normalizeAccentPreference("cobalt")).toBe("mineral");
    localStorage.setItem(ACCENT_STORAGE_KEY, "cobalt");
    expect(getAccentPreference()).toBe("mineral");
    applyTheme();
    expect(document.documentElement.getAttribute("data-accent")).toBe("mineral");

    setAccentPreference("pine");
    expect(localStorage.getItem(ACCENT_STORAGE_KEY)).toBe("pine");
    expect(document.documentElement.getAttribute("data-accent")).toBe("pine");
  });

  it("切回 signal 时清理存储键与内联自定义颜色变量", () => {
    setAccentPreference("#e35d35");
    expect(document.documentElement.getAttribute("data-accent")).toBe("custom");
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("#e35d35");

    setAccentPreference("signal");
    expect(localStorage.getItem(ACCENT_STORAGE_KEY)).toBeNull();
    expect(document.documentElement.getAttribute("data-accent")).toBeNull();
    expect(document.documentElement.style.getPropertyValue("--accent")).toBe("");
  });
});
