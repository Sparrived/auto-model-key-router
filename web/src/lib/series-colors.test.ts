// 系列配色回归：颜色必须跟着 scope + key 走，而不是跟着数组下标走。
//
// 分配表是模块级的，所以每个用例前都要 resetSeriesColors()，否则上一个用例
// 占掉的槽位会让"前 8 个系列"这类断言随执行顺序漂移。

import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import {
  OTHER_SERIES_COLOR,
  resetSeriesColors,
  seriesColor,
  useSeriesColors,
} from "./series-colors";

beforeEach(() => {
  resetSeriesColors();
});

describe("seriesColor", () => {
  it("同一 scope 的同一 key 始终拿到同一种颜色，中间插入系列也不改变", () => {
    const first = seriesColor("模型", "gpt-4o");
    seriesColor("模型", "claude");
    seriesColor("模型", "deepseek");

    expect(seriesColor("模型", "gpt-4o")).toBe(first);
    expect(first).toBe("var(--series-1)");
  });

  it("前 8 个系列依次占用 --series-1..8，第 9 个起落到 --series-other", () => {
    const colors = Array.from({ length: 9 }, (_, index) => seriesColor("模型", `m${index}`));

    expect(colors.slice(0, 8)).toEqual(
      Array.from({ length: 8 }, (_, index) => `var(--series-${index + 1})`),
    );
    expect(colors[8]).toBe(OTHER_SERIES_COLOR);
    expect(OTHER_SERIES_COLOR).toBe("var(--series-other)");
    // 已经拿到 --series-other 的 key 同样保持稳定。
    expect(seriesColor("模型", "m8")).toBe(OTHER_SERIES_COLOR);
  });

  it("不同 scope 各自从第一个槽位分配，互不挤占", () => {
    expect(seriesColor("模型", "same-key")).toBe("var(--series-1)");
    expect(seriesColor("供应商", "same-key")).toBe("var(--series-1)");
    expect(seriesColor("模型", "another")).toBe("var(--series-2)");
  });
});

describe("useSeriesColors", () => {
  it("两个作用域的槽位独立计数，互不挤占", () => {
    const workspace = renderHook(() => useSeriesColors("workspace", ["default", "teamA", "worker"]));
    const model = renderHook(() => useSeriesColors("model", ["default", "gpt-4o"]));

    expect(workspace.result.current("default")).toBe("var(--series-1)");
    expect(workspace.result.current("teamA")).toBe("var(--series-2)");
    expect(workspace.result.current("worker")).toBe("var(--series-3)");
    expect(model.result.current("default")).toBe("var(--series-1)");
    expect(model.result.current("gpt-4o")).toBe("var(--series-2)");
  });

  it("相同内容的 keys 重渲染不换色，新 key 只占用下一个空槽位", () => {
    const { result, rerender } = renderHook(
      ({ keys }: { keys: string[] }) => useSeriesColors("趋势图", keys),
      { initialProps: { keys: ["a", "b"] } },
    );

    expect(result.current("a")).toBe("var(--series-1)");
    expect(result.current("b")).toBe("var(--series-2)");

    rerender({ keys: ["a", "b"] });
    expect(result.current("a")).toBe("var(--series-1)");
    expect(result.current("c")).toBe("var(--series-3)");
    expect(result.current("a")).toBe("var(--series-1)");
  });
});
