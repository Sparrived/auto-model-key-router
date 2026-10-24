// 图表可访问性与交互回归：只覆盖用户真正会做的事——读百分比轴、切到表格视图、
// 点图例隐藏系列、逐格读热力图/健康矩阵的读屏标签。
//
// 不测 SVG 路径坐标和快照：几何由 chart-math 的纯函数单测兜底，这里只验证
// 组件把同一份数据以「看得见 + 读得到」两种方式交付。

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatPercentValue } from "@/lib/chart-math";
import { resetSeriesColors } from "@/lib/series-colors";
import { ChartFrame } from "./ChartFrame";
import { FlowMap } from "./FlowMap";
import { HealthMatrix, type HealthMatrixColumn, type HealthMatrixRow } from "./HealthMatrix";
import { Heatmap } from "./Heatmap";
import { RankBars } from "./RankBars";
import { ShareBar } from "./ShareBar";
import { StackedColumns } from "./StackedColumns";
import { TimeSeriesChart } from "./TimeSeriesChart";

beforeEach(() => {
  resetSeriesColors();
});

afterEach(() => {
  vi.restoreAllMocks();
});

function mockChartWidth(width: number) {
  return vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    width,
    height: 220,
    top: 0,
    left: 0,
    right: width,
    bottom: 220,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect);
}

const START = Date.parse("2026-07-14T09:00:00+08:00");

function clock(minutes: number): string {
  return new Date(START + minutes * 60_000).toISOString();
}

describe("ChartFrame", () => {
  it("embedded 时去掉外框、内边距和标题，只留右上角表格切换按钮", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <ChartFrame
        embedded
        title="Panel 已经负责的标题"
        table={<div>嵌入后的数据表</div>}
      >
        <div>图表内容</div>
      </ChartFrame>,
    );
    const section = container.querySelector("section");
    expect(section).not.toBeNull();
    expect(section).not.toHaveClass("rounded-panel");
    expect(section).not.toHaveClass("border");
    expect(screen.queryByText("Panel 已经负责的标题")).not.toBeInTheDocument();
    expect(screen.getByText("图表内容")).toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: "切换到表格视图" });
    expect(toggle).toHaveClass("absolute", "right-0", "top-0");
    await user.click(toggle);
    expect(screen.getByText("嵌入后的数据表")).toBeInTheDocument();
  });
});

describe("TimeSeriesChart", () => {
  it("百分比轴从 0% 到 100%，图例可切换，表格视图可往返", async () => {
    const user = userEvent.setup();
    const buckets = Array.from({ length: 6 }, (_, index) => ({
      startedAt: clock(index * 15),
      // 最后一个桶尚未闭合，用来验证尾段虚线的数据分支不会崩。
      complete: index < 5,
    }));
    const series = [
      { id: "model-a", label: "模型 A", values: [0.82, 0.76, null, 0.7, 0.68, 0.6] },
      { id: "model-b", label: "模型 B", values: [0.18, 0.24, 0.3, null, 0.32, 0.4] },
    ];

    render(
      <TimeSeriesChart
        buckets={buckets}
        series={series}
        format={(value) => formatPercentValue(value)}
        percent
        ariaLabel="模型成功率趋势"
      />,
    );

    // 图表本身是一个可聚焦的图形，读屏能读到用途与键盘提示。
    expect(screen.getByRole("img", { name: /模型成功率趋势；使用左右方向键读取各时间点/ })).toBeInTheDocument();
    // percent 轴固定 0–100%，两条曲线都含 null 缺口也不影响刻度。
    expect(screen.getByText("0%")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();

    // 点图例：aria-pressed 跟着显隐状态翻转，再点一次恢复。
    const legend = screen.getByRole("list", { name: "模型成功率趋势图例" });
    const toggleB = within(legend).getByRole("button", { name: "模型 B" });
    expect(toggleB).toHaveAttribute("aria-pressed", "true");
    await user.click(toggleB);
    expect(toggleB).toHaveAttribute("aria-pressed", "false");
    await user.click(toggleB);
    expect(toggleB).toHaveAttribute("aria-pressed", "true");

    // 表格视图是同一份数据的可读替代，两个系列的表头都在。
    await user.click(screen.getByRole("button", { name: "切换到表格视图" }));
    const table = screen.getByRole("table", { name: "时间序列数据表" });
    expect(within(table).getByRole("columnheader", { name: "模型 A" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "模型 B" })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /模型成功率趋势；/ })).not.toBeInTheDocument();

    // 再点一次回到图表。
    await user.click(screen.getByRole("button", { name: "切换到图表视图" }));
    expect(screen.queryByRole("table", { name: "时间序列数据表" })).not.toBeInTheDocument();
    expect(screen.getByRole("img", { name: /模型成功率趋势；/ })).toBeInTheDocument();
  });

  it("7 天窗口的 x 轴落在日界，600/800/1200 宽度下位置与时间一致", () => {
    const start = Date.parse("2026-10-17T21:30:00+08:00");
    const buckets = Array.from({ length: 337 }, (_, index) => ({
      startedAt: new Date(start + index * 30 * 60_000).toISOString(),
      complete: true,
    }));
    const values = buckets.map((_, index) => index + 1);
    const expectedLabels = ["10/18", "10/19", "10/20", "10/21", "10/22", "10/23", "10/24"];
    const first = start;
    const last = start + 336 * 30 * 60_000;

    for (const width of [600, 800, 1200]) {
      mockChartWidth(width);
      const plotWidth = width - 70;
      const { container, unmount } = render(
        <TimeSeriesChart
          buckets={buckets}
          series={[{ id: "requests", label: "请求", values }]}
          format={(value) => `${Math.round(value)}`}
          ariaLabel={`7 天趋势 ${width}`}
        />,
      );
      const labels = [...container.querySelectorAll("svg text")]
        .filter((node) => node.getAttribute("y") === "212");
      expect(labels.map((node) => node.textContent)).toEqual(expectedLabels);
      for (const node of labels) {
        const label = node.textContent ?? "";
        const [, month, day] = label.match(/^(\d+)\/(\d+)$/) ?? [];
        const time = Date.parse(`2026-${month.padStart(2, "0")}-${day.padStart(2, "0")}T00:00:00+08:00`);
        const expectedX = 54 + ((time - first) / (last - first)) * plotWidth;
        expect(Math.abs(Number(node.getAttribute("x")) - expectedX)).toBeLessThan(1);
      }
      unmount();
    }
  });

  it("单系列默认不显示图例，显式 showLegend 仍可打开", () => {
    const buckets = Array.from({ length: 3 }, (_, index) => ({ startedAt: clock(index * 15), complete: true }));
    const { rerender } = render(
      <TimeSeriesChart
        buckets={buckets}
        series={[{ id: "one", label: "累计用量", values: [1, 2, 3] }]}
        format={(value) => `${value} 次`}
        ariaLabel="累计用量"
      />,
    );
    expect(screen.queryByRole("list", { name: "累计用量图例" })).not.toBeInTheDocument();

    rerender(
      <TimeSeriesChart
        buckets={buckets}
        series={[
          { id: "one", label: "累计用量", values: [1, 2, 3] },
          { id: "two", label: "另一条", values: [3, 2, 1] },
        ]}
        format={(value) => `${value} 次`}
        ariaLabel="累计用量"
      />,
    );
    expect(screen.getByRole("list", { name: "累计用量图例" })).toBeInTheDocument();
  });
});

describe("Heatmap", () => {
  it("week 模式渲染 7×24 共 168 格，每格带星期与小时的可读标签", () => {
    const weekStart = Date.parse("2026-07-13T00:00:00+08:00"); // 周一 00:00（北京时间）
    const points = Array.from({ length: 168 }, (_, index) => ({
      started_at: new Date(weekStart + index * 3_600_000).toISOString(),
      complete: true,
      requests: 2,
    }));

    render(<Heatmap mode="week" points={points} />);

    expect(screen.getByRole("grid", { name: "周 × 小时热力图" })).toBeInTheDocument();
    expect(screen.getAllByRole("gridcell")).toHaveLength(168);

    // 读屏用户拿到的不是色块，而是「周一 00:00-01:00：2」这种完整读数。
    const mondayMidnight = screen.getByRole("gridcell", { name: /周一 00:00-01:00/ });
    expect(mondayMidnight.getAttribute("aria-label")).toContain("周一 00:00-01:00：2");
  });

  it("自定义 format 已带单位时，aria/tooltip 不再重复拼 unit", () => {
    const weekStart = Date.parse("2026-07-13T00:00:00+08:00");
    const points = [{ started_at: new Date(weekStart).toISOString(), complete: true, requests: 9 }];
    render(
      <Heatmap
        mode="week"
        points={points}
        format={(value) => `${value} 次`}
        unit="次"
      />,
    );
    const cell = screen.getByRole("gridcell", { name: /周一 00:00-01:00/ });
    expect(cell.getAttribute("aria-label")).toContain("9 次");
    expect(cell.getAttribute("aria-label")).not.toContain("次 次");
  });
});

describe("HealthMatrix", () => {
  it("2 行 × 49 列渲染 98 格，每格 title 带完整健康读数", () => {
    const columns: HealthMatrixColumn[] = Array.from({ length: 49 }, (_, index) => ({
      startedAt: clock(index * 15),
      endedAt: clock((index + 1) * 15),
      complete: true,
    }));
    const cells = columns.map((column) => ({
      started_at: column.startedAt,
      ended_at: column.endedAt,
      complete: true,
      requests: 5,
      successes: 5,
      total_first_token_ms: 5_000,
      prompt_tokens: 100,
      cached_tokens: 30,
    }));
    const rows: HealthMatrixRow[] = [
      { id: "openai", label: "OpenAI", cells },
      { id: "deepseek", label: "DeepSeek", cells },
    ];

    render(<HealthMatrix columns={columns} rows={rows} />);

    const renderedCells = screen.getAllByRole("gridcell");
    expect(renderedCells).toHaveLength(98);

    // title 是悬停提示的完整文本：分档、请求数、成功率都在里面，颜色不是唯一信息。
    const title = renderedCells[0].getAttribute("title") ?? "";
    expect(title).toContain("着色口径 综合：健康");
    expect(title).toContain("请求 5 次 · 成功 5 · 失败 0");
    expect(title).toContain("成功率 100.0%");

    const clockHeaders = screen.getAllByRole("columnheader")
      .map((header) => header.textContent?.trim() ?? "")
      .filter((text) => /^\d{1,2}:\d{2}$/.test(text));
    expect(clockHeaders.length).toBeGreaterThan(0);
    expect(new Set(clockHeaders).size).toBe(clockHeaders.length);
    for (const header of clockHeaders) {
      expect(header.endsWith(":00")).toBe(true);
    }
  });

  it("窄屏从表格切回图表时立刻按真实宽度选刻度，不先用 fallback 挤压", async () => {
    const user = userEvent.setup();
    mockChartWidth(390);
    vi.spyOn(window, "matchMedia").mockReturnValue({
      matches: true,
      media: "(max-width: 639px)",
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false,
    } as MediaQueryList);
    const columns: HealthMatrixColumn[] = Array.from({ length: 49 }, (_, index) => ({
      startedAt: clock(index * 15),
      endedAt: clock((index + 1) * 15),
      complete: true,
    }));
    const rows: HealthMatrixRow[] = [{
      id: "openai",
      label: "OpenAI",
      cells: columns.map((column) => ({
        started_at: column.startedAt,
        ended_at: column.endedAt,
        complete: true,
        requests: 5,
        successes: 5,
        total_first_token_ms: 5_000,
        prompt_tokens: 100,
        cached_tokens: 30,
      })),
    }];

    render(<HealthMatrix columns={columns} rows={rows} />);
    await user.click(screen.getByRole("button", { name: "切换到图表视图" }));
    const headers = screen.getAllByRole("columnheader")
      .map((header) => header.textContent?.trim() ?? "")
      .filter((text) => /^\d{1,2}:\d{2}$/.test(text));
    expect(headers).toEqual(["12:00", "16:00", "20:00"]);
  });
});

describe("ShareBar", () => {
  it("切换到表格视图后能看到系列、值与占比表头", async () => {
    const user = userEvent.setup();
    render(
      <ShareBar
        items={[
          { id: "provider-a", label: "供应商 A", value: 60 },
          { id: "provider-b", label: "供应商 B", value: 40 },
        ]}
        format={(value) => `${value} 次`}
        unit="次"
      />,
    );

    await user.click(screen.getByRole("button", { name: "切换到表格视图" }));

    const table = screen.getByRole("table", { name: "构成数据表" });
    expect(within(table).getByRole("columnheader", { name: "系列" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "值（次）" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "占比" })).toBeInTheDocument();
  });
});

describe("StackedColumns", () => {
  it("单系列默认隐藏图例，xTickCount 控制标签数量", () => {
    const buckets = Array.from({ length: 8 }, (_, index) => ({ startedAt: clock(index * 15), complete: true }));
    const { container } = render(
      <StackedColumns
        buckets={buckets}
        series={[{ id: "one", label: "请求", values: [1, 2, 3, 4, 5, 6, 7, 8] }]}
        format={(value) => `${value} 次`}
        ariaLabel="请求构成"
        xTickCount={2}
      />,
    );
    expect(screen.queryByRole("list", { name: "请求构成图例" })).not.toBeInTheDocument();
    const xLabels = container.querySelectorAll('svg text[text-anchor="middle"]');
    expect(xLabels).toHaveLength(2);
  });

  it("7 天 30 分钟桶的柱状图刻度落在精确桶的柱心", () => {
    mockChartWidth(600);
    const start = Date.parse("2026-10-17T21:30:00+08:00");
    const buckets = Array.from({ length: 337 }, (_, index) => ({
      startedAt: new Date(start + index * 30 * 60_000).toISOString(),
      complete: true,
    }));
    const { container } = render(
      <StackedColumns
        buckets={buckets}
        series={[{ id: "requests", label: "请求", values: buckets.map((_, index) => index + 1) }]}
        format={(value) => `${value} 次`}
        ariaLabel="7 天堆叠柱"
      />,
    );
    const labels = [...container.querySelectorAll('svg text[text-anchor="middle"]')];
    expect(labels.map((node) => node.textContent)).toEqual(["10/18", "10/19", "10/20", "10/21", "10/22", "10/23", "10/24"]);
    const band = (600 - 70) / 337;
    for (const node of labels) {
      const [, month, day] = (node.textContent ?? "").match(/^(\d+)\/(\d+)$/) ?? [];
      const time = Date.parse(`2026-${month.padStart(2, "0")}-${day.padStart(2, "0")}T00:00:00+08:00`);
      const index = Math.round((time - start) / (30 * 60_000));
      const expectedX = 54 + (index + 0.5) * band;
      expect(Math.abs(Number(node.getAttribute("x")) - expectedX)).toBeLessThan(1);
    }
  });
});

describe("RankBars", () => {
  it("不同排行使用独立作用域，都从 slot 1 起分配，前 8 个实体全部有色", () => {
    const { container } = render(
      <>
        <RankBars
          ariaLabel="模型排行"
          items={Array.from({ length: 8 }, (_, index) => ({ id: `model-${index}`, label: `模型 ${index}`, value: 80 - index }))}
          format={(value) => `${value} 次`}
          colorScope="rank-test:model"
        />
        <RankBars
          ariaLabel="供应商排行"
          items={[{ id: "openai", label: "openai", value: 10 }]}
          format={(value) => `${value} 次`}
          colorScope="rank-test:provider"
        />
      </>,
    );

    const lists = [...container.querySelectorAll("ol[aria-label]")];
    expect(lists).toHaveLength(2);
    const swatches = (list: Element) => [...list.querySelectorAll("li > div > span[aria-hidden='true']")]
      .filter((node) => Boolean((node as HTMLElement).style.backgroundColor));
    expect(swatches(lists[0]).map((node) => (node as HTMLElement).style.backgroundColor)).toEqual(
      Array.from({ length: 8 }, (_, index) => `var(--series-${index + 1})`),
    );
    // 第二个排行是全新作用域，不能继承第一个排行的槽位；openai 必须是 series-1。
    expect((swatches(lists[1])[0] as HTMLElement).style.backgroundColor).toBe("var(--series-1)");
  });

  it("「其他」项使用中性灰，不占用分类色槽位", () => {
    const { container } = render(
      <RankBars
        ariaLabel="含其他的排行"
        items={[
          ...Array.from({ length: 8 }, (_, index) => ({ id: `m${index}`, label: `模型 ${index}`, value: 20 - index })),
          { id: "other", label: "其他", value: 1, isOther: true },
        ]}
        format={(value) => `${value} 次`}
        colorScope="rank-test:other"
      />,
    );

    const swatches = [...container.querySelectorAll("li > div > span[aria-hidden='true']")]
      .filter((node) => Boolean((node as HTMLElement).style.backgroundColor));
    expect(swatches.slice(0, 8).map((node) => (node as HTMLElement).style.backgroundColor)).toEqual(
      Array.from({ length: 8 }, (_, index) => `var(--series-${index + 1})`),
    );
    expect((swatches[8] as HTMLElement).style.backgroundColor).toBe("var(--series-other)");
  });

  it("不传 colorScope 时每个实例仍独立取色，不会跨排行挤占", () => {
    const { container } = render(
      <>
        <RankBars
          ariaLabel="排行 A"
          items={Array.from({ length: 7 }, (_, index) => ({ id: `a${index}`, label: `A${index}`, value: 20 - index }))}
          format={(value) => `${value} 次`}
        />
        <RankBars
          ariaLabel="排行 B"
          items={[{ id: "b0", label: "B0", value: 1 }]}
          format={(value) => `${value} 次`}
        />
      </>,
    );

    const lists = [...container.querySelectorAll("ol[aria-label]")];
    const first = lists[0].querySelector("li > div > span[aria-hidden='true']") as HTMLElement;
    const second = lists[1].querySelector("li > div > span[aria-hidden='true']") as HTMLElement;
    expect(first.style.backgroundColor).toBe("var(--series-1)");
    expect(second.style.backgroundColor).toBe("var(--series-1)");
  });
});

describe("FlowMap", () => {
  it("着色层每个节点都拿到分类色，作用域与其它图表隔离", () => {
    const { container } = render(
      <FlowMap
        links={[
          { source_layer: 0, target_layer: 1, source: "default", target: "gpt-4o", requests: 5 },
          { source_layer: 0, target_layer: 1, source: "teamA", target: "gpt-4o", requests: 4 },
          { source_layer: 0, target_layer: 1, source: "worker", target: "gpt-4o", requests: 3 },
        ]}
        layerLabels={["工作空间", "请求模型"]}
        metric="requests"
        colorLayer={0}
        colorScope="workspace"
        format={(value) => `${value} 次`}
      />,
    );
    const fills = [...container.querySelectorAll("rect[fill]")]
      .map((node) => node.getAttribute("fill"))
      .filter((fill): fill is string => Boolean(fill?.startsWith("var(--series-")));
    expect(new Set(fills)).toEqual(new Set(["var(--series-1)", "var(--series-2)", "var(--series-3)"]));
  });
});
