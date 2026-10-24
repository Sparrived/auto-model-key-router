import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// Radix 的浮层与键盘交互依赖浏览器里天然存在的几个 API；jsdom 没有实现它们。
// 只补测试需要的最小行为，不改变组件逻辑。

if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
}

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
}
if (!Element.prototype.setPointerCapture) {
  Element.prototype.setPointerCapture = () => undefined;
}
if (!Element.prototype.releasePointerCapture) {
  Element.prototype.releasePointerCapture = () => undefined;
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => undefined;
}
if (!window.HTMLElement.prototype.inert) {
  Object.defineProperty(window.HTMLElement.prototype, "inert", {
    configurable: true,
    get() {
      return false;
    },
    set() {},
  });
}
if (!window.scrollTo) {
  window.scrollTo = vi.fn();
}
