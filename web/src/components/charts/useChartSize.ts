// 图表尺寸探针：先量到真实像素宽度再画，避免 viewBox 与容器不一致导致首帧缩放。
//
// 只观察宽度：高度由调用方按信息密度决定（趋势图 220、迷你图 32、矩阵按行数），
// 交给 CSS 固定高度比让图表自己长高更可控。ResizeObserver 不存在时（jsdom、极旧
// 浏览器）退回到一次布局测量 + fallback 宽度，图表仍然可渲染。

import { useCallback, useLayoutEffect, useRef, useState, type RefObject } from "react";

export interface ChartSize {
  ref: RefObject<HTMLDivElement | null>;
  width: number;
  /**
   * 挂载即测量的回调 ref。图表默认表格视图时，图表子树由 ChartFrame 内部
   * 切换挂载，父组件不会因此重渲染；普通 useLayoutEffect 不会触发，需靠这个
   * 回调在节点出现时立刻量到实际宽度，避免用 fallback 宽度选刻度。
   */
  setRef: (node: HTMLDivElement | null) => void;
}

export function useChartSize(fallbackWidth = 720): ChartSize {
  const ref = useRef<HTMLDivElement>(null);
  // 初值用 fallback：测量在 layoutEffect 里完成并会立即覆盖；这样即使 ref
  // 在挂载时序上晚一帧，也不会先渲染一张 width=0 的图。
  const [width, setWidth] = useState(fallbackWidth);
  const measureNode = useCallback((node: HTMLDivElement) => {
    const rect = node.getBoundingClientRect();
    const next = Math.floor(rect.width || node.clientWidth || node.parentElement?.clientWidth || 0);
    if (next > 0) setWidth((current) => (current === next ? current : next));
  }, []);
  const setRef = useCallback((node: HTMLDivElement | null) => {
    ref.current = node;
    if (node) measureNode(node);
  }, [measureNode]);

  useLayoutEffect(() => {
    const node = ref.current;
    if (!node) return;

    const measure = () => {
      const rect = node.getBoundingClientRect();
      const next = Math.floor(rect.width || node.clientWidth || node.parentElement?.clientWidth || 0);
      if (next > 0) {
        setWidth((current) => (current === next ? current : next));
      } else {
        // 测试环境没有布局引擎，也没有 ResizeObserver 回调；给一个可用的确定性
        // 宽度，保证图表结构仍可被断言。真实浏览器不会走到这里。
        setWidth((current) => (current > 0 ? current : fallbackWidth));
      }
    };

    measure();
    // 某些布局（例如刚从 display:none 切回来）在 layoutEffect 时还没有宽度；
    // 下一帧再测一次，ResizeObserver 继续兜底后续尺寸变化。
    const raf = typeof requestAnimationFrame === "undefined" ? 0 : requestAnimationFrame(measure);
    // 首帧布局在部分浏览器的 grid/嵌套 flex 里仍可能是 0；稍后再量一次，
    // 保证初次进入页面时也能拿到真实列宽。
    const timer = setTimeout(measure, 30);
    const observer = typeof ResizeObserver === "undefined"
      ? null
      : new ResizeObserver((entries) => {
        const entry = entries[0];
        const next = entry ? Math.floor(entry.contentRect.width) : 0;
        if (next > 0) setWidth((current) => (current === next ? current : next));
      });
    observer?.observe(node);
    return () => {
      if (raf) cancelAnimationFrame(raf);
      clearTimeout(timer);
      observer?.disconnect();
    };
  });

  return { ref, width, setRef };
}
