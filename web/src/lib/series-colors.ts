// 系列颜色分配：同一维度里的同一个 key 在同一会话内始终拿到同一种颜色。
//
// 颜色不能按数组下标现算：筛选、排序或图例隐藏都会改变下标，同一条模型曲线
// 就会在两次渲染之间换色，读图的人会以为数据变了。这里按 scope（模型、供应商、
// 流向等）+ key 记住第一次出现时分配到的槽位，后续渲染永远复用。
//
// 前 8 个系列使用 --series-1..8，第 9 个及以后统一 --series-other。超过 8 色后
// 人眼已经无法可靠区分，继续生成近似色只会制造"看起来像同一条"的假象。

import { useMemo } from "react";

export const SERIES_SLOT_COUNT = 8;

const PALETTE = Array.from({ length: SERIES_SLOT_COUNT }, (_, index) => `var(--series-${index + 1})`);
export const OTHER_SERIES_COLOR = "var(--series-other)";

/** 模块级分配表。只增不减：即使某个系列暂时被筛掉，回来时颜色也不跳。 */
const assignments = new Map<string, Map<string, string>>();

function scopeMap(scope: string): Map<string, string> {
  let map = assignments.get(scope);
  if (!map) {
    map = new Map();
    assignments.set(scope, map);
  }
  return map;
}

/** 纯函数版：给定 scope 和 key，返回稳定颜色；第 9 个起返回 other。 */
export function seriesColor(scope: string, key: string): string {
  const map = scopeMap(scope);
  const existing = map.get(key);
  if (existing) return existing;
  // 只按已占用的槽位数分配，不按当前 keys 下标 —— 否则删除一条系列后，
  // 后面所有系列都会整体前移换色。
  const used = new Set(map.values());
  for (let index = 0; index < PALETTE.length; index += 1) {
    const color = PALETTE[index];
    if (!used.has(color)) {
      map.set(key, color);
      return color;
    }
  }
  map.set(key, OTHER_SERIES_COLOR);
  return OTHER_SERIES_COLOR;
}

/**
 * 在组件里按 keys 预分配颜色，返回 `(key) => color`。
 *
 * 用 keys.join 作为 memo 依赖而不是数组引用：调用方通常每帧都新建数组，
 * 引用依赖会让 memo 失效，但颜色本身仍然稳定。
 */
export function useSeriesColors(scope: string, keys: string[]): (key: string) => string {
  const signature = keys.join("\u0000");
  return useMemo(() => {
    for (const key of keys) seriesColor(scope, key);
    return (key: string) => seriesColor(scope, key);
    // signature 已经概括了 keys 的内容；keys 本身不参与依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, signature]);
}

/** 仅供测试：清空模块级分配，避免用例之间互相污染。 */
export function resetSeriesColors(): void {
  assignments.clear();
}
