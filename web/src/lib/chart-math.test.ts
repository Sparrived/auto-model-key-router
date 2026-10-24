// 图表口径回归：从 legacy 的 webui_chart_probe.mjs 逐条搬过来的断言。
//
// 这些函数不带 DOM，算错的后果（速率没换算、比率二次平均、残桶当成真实下跌）都
// 只体现在数字上；用真实模块断言比在浏览器里"看着对"可靠。

import { describe, expect, it } from "vitest";
import * as m from "./chart-math";

const LADDER = [15, 30, 60, 120, 180, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400];

describe("时间窗与桶宽", () => {
  it("取阶梯里刚好不超点数上限的最小桶", () => {
    expect(m.pickBucketSeconds(1)).toBe(15);
    expect(m.pickBucketSeconds(24)).toBe(180);
    expect(m.pickBucketSeconds(168)).toBe(1800);
    for (const hours of [1, 6, 24, 72, 168, 720, 2160, 4320, 8760]) {
      const bucket = m.pickBucketSeconds(hours);
      const points = Math.ceil((hours * 3600) / bucket) + 1;
      expect(points, `${hours}h => ${bucket}s`).toBeLessThanOrEqual(m.MAX_SERIES_POINTS);
      const index = LADDER.indexOf(bucket);
      expect(index, `bucket ${bucket} 不在阶梯里`).toBeGreaterThanOrEqual(0);
      if (index > 0) {
        const smaller = LADDER[index - 1];
        expect(Math.ceil((hours * 3600) / smaller) + 1, `${smaller}s 也未超上限，选桶偏粗`).toBeGreaterThan(m.MAX_SERIES_POINTS);
      }
    }
  });

  it("实时档与历史档分开，全部历史由运行时推导", () => {
    expect(m.TIME_RANGES.map((range) => range.hours).join(",")).toBe("1,6,24,72,168");
    expect(m.LONG_RANGES.map((range) => range.hours).join(",")).toBe("720,2160,4320,8760");
    expect(m.USAGE_RANGES.at(-1)?.hours).toBe(m.ALL_HISTORY);
    expect(m.USAGE_RANGES.length).toBe(m.TIME_RANGES.length + m.LONG_RANGES.length + 1);
    expect(m.MAX_HISTORY_HOURS).toBe(8760);
    for (const range of m.LONG_RANGES) {
      const spec = m.rangeSpec(range.hours, {});
      expect(Math.ceil((spec.hours * 3600) / m.pickBucketSeconds(spec.hours)) + 1).toBeLessThanOrEqual(m.MAX_SERIES_POINTS);
    }
  });

  it("全部历史的小时数向上取整、夹紧并标记截断", () => {
    const now = Date.parse("2026-06-01T00:00:00+08:00");
    expect(m.historyHours("2026-05-31T22:30:00+08:00", now)).toBe(2);
    expect(m.historyHours("2000-01-01T00:00:00+08:00", now)).toBe(m.MAX_HISTORY_HOURS);
    expect(m.historyHours(null, now)).toBeNull();
    expect(m.historyHours("2026-07-01T00:00:00+08:00", now)).toBeNull();
    const all = m.rangeSpec(m.ALL_HISTORY, { allHours: m.MAX_HISTORY_HOURS });
    expect(all.all).toBe(true);
    expect(all.truncated).toBe(true);
    expect(m.rangeSpec(m.ALL_HISTORY, { allHours: 100 }).truncated).toBe(false);
    expect(m.rangeSpec(m.ALL_HISTORY, {}).hours).toBe(m.MAX_HISTORY_HOURS);
  });
});

describe("指标换算与加权", () => {
  it("速率按桶宽折算，比率与计数不折算", () => {
    expect(m.metricValue({ requests: 30 }, m.METRIC_MAP.rpm, 60)).toBe(30);
    expect(m.metricValue({ requests: 3 }, m.METRIC_MAP.rpm, 15)).toBe(12);
    expect(m.metricValue({ requests: 500 }, m.METRIC_MAP.rpm, 300)).toBe(100);
    expect(m.metricValue({ total_tokens: 9000 }, m.METRIC_MAP.tpm, 180)).toBe(3000);
    expect(m.metricValue({ prompt_tokens: 100, cached_tokens: 40 }, m.METRIC_MAP.cache, 300)).toBe(0.4);
    expect(m.metricValue({ requests: 30 }, m.METRIC_MAP.requests, 15)).toBe(30);
  });

  it("窗口聚合按请求/Token 加权，不做比率的平均", () => {
    const points = [
      { requests: 1, successes: 0, prompt_tokens: 10, cached_tokens: 0, total_duration_ms: 1000, total_first_token_ms: 100 },
      { requests: 999, successes: 999, prompt_tokens: 10000, cached_tokens: 5000, total_duration_ms: 999000, total_first_token_ms: 99900 },
    ];
    const sums = m.windowSums(points);
    expect(sums.requests).toBe(1000);
    expect(m.METRIC_MAP.success.total(sums)).toBeCloseTo(0.999);
    expect(m.METRIC_MAP.cache.total(sums)).toBeCloseTo(5000 / 10010);
    expect(m.METRIC_MAP.latency.total(sums)).toBeCloseTo(1000);
    expect(m.windowSums([{ requests: 0 }, { requests: 2 }]).zeroBuckets).toBe(1);
    expect(m.windowSums([{ requests: null, total_tokens: null }]).requests).toBe(0);
  });

  it("分母为零返回 null，零请求的 RPM 仍是真实 0", () => {
    expect(m.METRIC_MAP.success.total(m.windowSums([]))).toBeNull();
    expect(m.metricValue({ requests: 0, successes: 0 }, m.METRIC_MAP.success, 60)).toBeNull();
    expect(m.metricValue({ requests: 0, total_duration_ms: 0 }, m.METRIC_MAP.latency, 60)).toBeNull();
    expect(m.metricValue({ requests: 0 }, m.METRIC_MAP.rpm, 60)).toBe(0);
  });
});

describe("序列与缺口", () => {
  it("标记残桶，并从序列取最后可读值", () => {
    const partial = m.series([
      { started_at: "2026-01-01T10:00:00+08:00", requests: 10, complete: true },
      { started_at: "2026-01-01T10:01:00+08:00", requests: 3, complete: false },
    ], m.METRIC_MAP.rpm, 60);
    expect(partial[1].partial).toBe(true);
    expect(partial[0].partial).toBe(false);
    expect(m.completeOnly([{ complete: true }, { complete: false }, { complete: true }])).toHaveLength(2);
    expect(m.latestValue(partial)?.value).toBe(3);
    expect(m.latestValue([{ value: 5 }, { value: null }])?.value).toBe(5);
    expect(m.latestValue([])).toBeNull();
  });

  it("只桥接两侧都有锚点的缺口", () => {
    const bridged = m.gapBridges([{ value: 1 }, { value: null }, { value: null }, { value: 2 }, { value: 3 }, { value: null }]);
    expect(bridged).toHaveLength(1);
    expect(bridged[0]).toEqual({ from: 0, to: 3 });
    expect(bridged.every((bridge) => bridge.to !== 5)).toBe(true);
    expect(m.gapBridges([{ value: null }, { value: 1 }, { value: 2 }])).toHaveLength(0);
    expect(m.gapBridges([{ value: 1 }, { value: 2 }])).toHaveLength(0);
    expect(m.gapBridges([{ value: 0 }, { value: 0 }, { value: 0 }])).toHaveLength(0);
    expect(m.readable({ value: 0 })).toBe(true);
    expect(m.readable({ value: null })).toBe(false);
    expect(m.readable({ value: NaN })).toBe(false);
    expect(m.gapBridges([])).toHaveLength(0);
    expect(m.gapBridges(null)).toHaveLength(0);
    const sparse = Array.from({ length: 30 }, (_, index) => ({ value: index % 3 === 0 ? 100 + index : null }));
    expect(m.gapBridges(sparse)).toHaveLength(9);
  });
});

describe("坐标轴与时间刻度", () => {
  it("刻度覆盖数据、从零开始且值干净", () => {
    const axis = m.axisScale([0, 3, 17, 42]);
    expect(axis.max).toBeGreaterThanOrEqual(42);
    expect(axis.ticks.at(-1)).toBeCloseTo(axis.max);
    expect(axis.ticks[0]).toBe(0);
    const fractional = m.axisScale([0.1, 0.23, 0.4]);
    expect(fractional.ticks.every((value) => String(value).length <= 6)).toBe(true);
    expect(m.axisScale([0, 0, 0]).max).toBeGreaterThan(0);
    expect(m.axisScale([0.3, 0.9], { percent: true }).max).toBe(100);
    expect(m.axisScale([7, 7, 7]).max).toBeGreaterThanOrEqual(7);
    expect(m.niceStep(0.9)).toBe(1);
    expect(m.niceStep(1.4)).toBe(2);
    expect(m.niceStep(2.2)).toBe(2.5);
    expect(m.niceStep(7)).toBe(10);
  });

  it("24 小时窗口跨天时统一用 M/D HH:mm，标签不重复", () => {
    const start = Date.parse("2026-10-24T21:30:00+08:00");
    const points = Array.from({ length: 49 }, (_, index) => ({
      started_at: new Date(start + index * 30 * 60_000).toISOString(),
    }));
    const first = Date.parse(points[0].started_at);
    const last = Date.parse(points[48].started_at);
    const ticks = m.timeAxisTicks(points, {
      plotWidth: 390 - 70,
      mode: "line",
      maxTicks: 4,
      x: (point) => 54 + ((point.time - first) / (last - first)) * 320,
    });
    expect(ticks.length).toBeGreaterThanOrEqual(2);
    expect(ticks.every((tick) => /^\d+\/\d+ \d{2}:\d{2}$/.test(tick.label))).toBe(true);
    expect(new Set(ticks.map((tick) => tick.label)).size).toBe(ticks.length);
  });

  it("7 天 30 分钟桶：刻度落上海日界，标签不缺失不重复，位置与时间一致", () => {
    const start = Date.parse("2026-10-17T21:30:00+08:00");
    const points = Array.from({ length: 337 }, (_, index) => ({
      started_at: new Date(start + index * 30 * 60_000).toISOString(),
    }));
    const expected = ["10/18", "10/19", "10/20", "10/21", "10/22", "10/23", "10/24"];

    for (const width of [600, 800, 1200]) {
      const plotWidth = width - 70;
      const first = Date.parse(points[0].started_at);
      const last = Date.parse(points[337 - 1].started_at);
      const ticks = m.timeAxisTicks(points, {
        plotWidth,
        mode: "line",
        maxTicks: 7,
        x: (point) => 54 + ((point.time - first) / (last - first)) * plotWidth,
      });
      expect(ticks.map((tick) => tick.label)).toEqual(expected);
      expect(new Set(ticks.map((tick) => tick.label)).size).toBe(ticks.length);
      expect(ticks.every((tick) => tick.boundary)).toBe(true);
      expect(ticks.every((tick) => (tick.time + 8 * 3_600_000) % 86_400_000 === 0)).toBe(true);
      expect(ticks.some((tick) => tick.label === "10/17")).toBe(false);
      const ordered = [...ticks].sort((left, right) => left.x - right.x);
      let previousRight = -Infinity;
      for (const tick of ordered) {
        const half = m.textWidth(tick.label, 10) / 2;
        expect(tick.x - half).toBeGreaterThanOrEqual(previousRight + 12);
        previousRight = tick.x + half;
      }
      for (const tick of ticks) {
        const expectedX = 54 + ((tick.time - first) / (last - first)) * plotWidth;
        expect(Math.abs(tick.x - expectedX)).toBeLessThan(1);
      }
    }
  });

  it("6 小时 1 分钟桶：小时边界、HH:mm 标签、位置误差小于 1px", () => {
    const start = Date.parse("2026-10-24T10:00:00+08:00");
    const points = Array.from({ length: 361 }, (_, index) => ({
      started_at: new Date(start + index * 60_000).toISOString(),
    }));
    const first = Date.parse(points[0].started_at);
    const last = Date.parse(points[360].started_at);
    const plotWidth = 800 - 70;
    const ticks = m.timeAxisTicks(points, {
      plotWidth,
      mode: "line",
      maxTicks: 7,
      x: (point) => 54 + ((point.time - first) / (last - first)) * plotWidth,
    });
    expect(ticks.map((tick) => tick.label)).toEqual(["10:00", "11:00", "12:00", "13:00", "14:00", "15:00", "16:00"]);
    expect(ticks.every((tick) => (tick.time + 8 * 3_600_000) % 3_600_000 === 0)).toBe(true);
    for (const tick of ticks) {
      const expectedX = 54 + ((tick.time - first) / (last - first)) * plotWidth;
      expect(Math.abs(tick.x - expectedX)).toBeLessThan(1);
    }
  });

  it("12 小时 15 分钟桶：2 小时间隔，标签不重复", () => {
    const start = Date.parse("2026-10-24T10:00:00+08:00");
    const points = Array.from({ length: 49 }, (_, index) => ({
      started_at: new Date(start + index * 15 * 60_000).toISOString(),
    }));
    const first = Date.parse(points[0].started_at);
    const last = Date.parse(points[48].started_at);
    const plotWidth = 800 - 70;
    const ticks = m.timeAxisTicks(points, {
      plotWidth,
      mode: "line",
      maxTicks: 7,
      x: (point) => 54 + ((point.time - first) / (last - first)) * plotWidth,
    });
    expect(ticks.map((tick) => tick.label)).toEqual(["10:00", "12:00", "14:00", "16:00", "18:00", "20:00", "22:00"]);
    expect(new Set(ticks.map((tick) => tick.label)).size).toBe(ticks.length);
    for (const tick of ticks) {
      const expectedX = 54 + ((tick.time - first) / (last - first)) * plotWidth;
      expect(Math.abs(tick.x - expectedX)).toBeLessThan(1);
    }
  });

  it("band 模式只把刻度放在边界正好对应的柱子上", () => {
    const start = Date.parse("2026-10-17T21:30:00+08:00");
    const points = Array.from({ length: 337 }, (_, index) => ({
      started_at: new Date(start + index * 30 * 60_000).toISOString(),
    }));
    const plotWidth = 800 - 70;
    const band = plotWidth / 337;
    const ticks = m.timeAxisTicks(points, {
      plotWidth,
      mode: "band",
      maxTicks: 7,
      x: (point) => 54 + (point.index + 0.5) * band,
    });
    expect(ticks.map((tick) => tick.label)).toEqual(["10/18", "10/19", "10/20", "10/21", "10/22", "10/23", "10/24"]);
    for (const tick of ticks) {
      expect(tick.boundary).toBe(true);
      expect(points[tick.index].started_at).toBe(new Date(tick.time).toISOString());
      expect(Math.abs(tick.x - (54 + (tick.index + 0.5) * band))).toBeLessThan(1);
    }
  });

  it("日桶比边界粗时退化为按柱子抽稀，标签仍唯一且落在柱心", () => {
    const start = Date.parse("2026-10-17T21:30:00+08:00");
    const points = Array.from({ length: 7 }, (_, index) => ({
      started_at: new Date(start + index * 86_400_000).toISOString(),
    }));
    const plotWidth = 390 - 70;
    const band = plotWidth / 7;
    const ticks = m.timeAxisTicks(points, {
      plotWidth,
      mode: "band",
      maxTicks: 4,
      x: (point) => 54 + (point.index + 0.5) * band,
    });
    expect(ticks.length).toBeLessThanOrEqual(4);
    expect(ticks.every((tick) => !tick.boundary)).toBe(true);
    expect(new Set(ticks.map((tick) => tick.label)).size).toBe(ticks.length);
    expect(ticks.map((tick) => tick.label)).toEqual(["10/17", "10/19", "10/21", "10/23"]);
    for (const tick of ticks) {
      expect(Math.abs(tick.x - (54 + (tick.index + 0.5) * band))).toBeLessThan(1);
    }
  });

  it("空数据和 series 的 at 字段都能处理", () => {
    expect(m.timeAxisTicks([], { plotWidth: 100, x: () => 0 })).toHaveLength(0);
    const points = Array.from({ length: 60 }, (_, index) => ({
      started_at: `2026-01-01T10:${String(index).padStart(2, "0")}:00+08:00`,
    }));
    const asSeries = m.series(
      points.map((point) => ({ ...point, requests: 1, complete: true })),
      m.METRIC_MAP.rpm,
      60,
    );
    const ticks = m.timeAxisTicks(asSeries, { plotWidth: 400, maxTicks: 4, x: (point) => point.time });
    expect(ticks.length).toBeGreaterThan(0);
    expect(ticks.every((tick) => tick.label.includes(":"))).toBe(true);
  });});

describe("排行、状态码与环比", () => {
  it("排行排序、限量并丢掉零值", () => {
    expect(m.rank({ a: { requests: 1 }, b: { requests: 9 }, c: { requests: 5 } }).map((row) => row.name).join(",")).toBe("b,c,a");
    expect(m.rank({ a: { requests: 0 } })).toHaveLength(0);
    const groups = m.statusGroups({ "200": 5, "401": 2, "500": 1, "302": 3 });
    expect(groups).toEqual({ ok: 5, client: 2, server: 1, other: 3 });
  });

  it("前半段为零时不给百分比，数据不足返回 null", () => {
    expect(m.trend([{ value: 0 }, { value: 0 }, { value: 5 }, { value: 5 }])).toBeNull();
    expect(m.trend([{ value: 1 }, { value: 1 }, { value: 2 }, { value: 2 }])?.change).toBeCloseTo(1);
    expect(m.trend([{ value: 1 }, { value: 2 }])).toBeNull();
  });
});

describe("数字格式", () => {
  it("极小百分比不被显示成 0%", () => {
    expect(m.formatPercentValue(0.0001)).toBe("<0.5%");
    expect(m.formatPercentValue(0.999)).toBe(">99.5%");
    expect(m.formatPercentValue(0.4231)).toBe("42.3%");
    expect(m.formatPercentValue(NaN)).toBe("-");
    expect(m.formatCompactNumber(2_500_000)).toBe("2.5M");
    expect(m.formatCompactNumber(12_300)).toBe("12.3K");
    expect(m.formatCompactNumber(42)).toBe("42");
    expect(m.formatDurationValue(2500)).toBe("2.50s");
    expect(m.formatDurationValue(240)).toBe("240ms");
  });
});

describe("热力图", () => {
  const heatPoints = [
    { started_at: "2026-01-01T10:00:00+08:00", requests: 5, total_tokens: 500, complete: true },
    { started_at: "2026-01-01T10:00:00+08:00", requests: 7, total_tokens: 700, complete: true },
    { started_at: "2026-01-01T10:00:00+08:00", requests: 3, total_tokens: 300, complete: false },
    { started_at: "2026-01-04T23:00:00+08:00", requests: 100, total_tokens: 9000, complete: true },
  ];

  it("按北京时间星期与半小时落格", () => {
    expect(m.beijingSlot("2026-01-01T10:00:00+08:00")).toEqual({ weekday: 3, slot: 20 });
    expect(m.beijingSlot("2026-01-04T23:30:00+08:00")).toEqual({ weekday: 6, slot: 47 });
    expect(m.beijingSlot("2026-01-01T10:00:00+08:00")).toEqual(m.beijingSlot("2026-01-01T02:00:00Z"));
    expect(m.beijingSlot("2026-01-01T00:30:00+08:00")?.slot).toBe(1);
    expect(m.beijingSlot("2026-01-01T10:30:00+08:00")?.slot).toBe(21);
    expect(m.beijingSlot("2026-01-01T10:59:00+08:00")?.slot).toBe(21);
    expect(m.beijingSlot("not-a-date")).toBeNull();
    expect(m.slotLabel(21)).toBe("10:30");
    expect(m.slotLabel(0)).toBe("00:00");
    expect(m.HEATMAP_SLOTS).toBe(48);
  });

  it("同格求和、标记残桶、空桶与未覆盖区分", () => {
    const cells = m.heatmapCells(heatPoints, { value: (point) => point.requests ?? 0 });
    expect(cells).toHaveLength(7);
    expect(cells.every((row) => row.length === 48)).toBe(true);
    expect(cells[3][20].value).toBe(15);
    expect(cells[3][20].buckets).toBe(3);
    expect(cells[3][20].partial).toBe(true);
    expect(cells[6][46].value).toBe(100);
    expect(cells[6][46].buckets).toBe(1);
    expect(cells[0][0].buckets).toBe(0);
    expect(cells[0][0].value).toBe(0);
    expect(m.heatmapCells([undefined, {}])[0][0].buckets).toBe(0);
    expect(m.heatmapCells(heatPoints, { value: m.HEATMAP_METRIC_MAP.tokens.pick })[3][20].value).toBe(1500);

    const neighbour = m.heatmapCells([
      { started_at: "2026-01-01T10:00:00+08:00", requests: 5, complete: true },
      { started_at: "2026-01-01T10:30:00+08:00", requests: 9, complete: true },
    ], { value: (point) => point.requests ?? 0 });
    expect(neighbour[3][20].value).toBe(5);
    expect(neighbour[3][21].value).toBe(9);
  });

  it("色阶与档位只看有数据的格子", () => {
    const cells = m.heatmapCells(heatPoints, { value: (point) => point.requests ?? 0 });
    expect(m.heatmapScale(cells)).toBe(100);
    expect(m.heatmapScale(m.heatmapCells([]))).toBe(0);
    expect(m.heatmapScale(m.heatmapCells([
      { started_at: "2026-01-01T10:00:00+08:00", requests: 4, complete: true },
    ]))).toBe(4);
    expect(m.heatmapLevel(0, 100)).toBe(0);
    expect(m.heatmapLevel(100, 100)).toBe(m.HEAT_LEVELS);
    expect([1, 25, 50, 75, 100].every((value, index, all) =>
      index === 0 || m.heatmapLevel(value, 100) >= m.heatmapLevel(all[index - 1], 100))).toBe(true);
    expect(m.heatmapLevel(5, 0)).toBe(0);
    expect(m.HEAT_LEVELS).toBeGreaterThanOrEqual(2);
  });
});

describe("历史聚合", () => {
  const histPoints = [
    { started_at: "2026-01-01T00:00:00+08:00", ended_at: "2026-01-01T00:30:00+08:00", requests: 10, successes: 9, failures: 1, retries: 2, prompt_tokens: 60, completion_tokens: 40, total_tokens: 100, cached_tokens: 20, total_duration_ms: 1000, total_first_token_ms: 100, complete: true },
    { started_at: "2026-01-01T00:30:00+08:00", ended_at: "2026-01-01T01:00:00+08:00", requests: 30, successes: 30, failures: 0, retries: 0, prompt_tokens: 180, completion_tokens: 120, total_tokens: 300, cached_tokens: 60, total_duration_ms: 6000, total_first_token_ms: 300, complete: true },
    { started_at: "2026-01-02T14:00:00+08:00", ended_at: "2026-01-02T14:30:00+08:00", requests: 5, successes: 5, failures: 0, retries: 0, prompt_tokens: 30, completion_tokens: 20, total_tokens: 50, cached_tokens: 10, total_duration_ms: 500, total_first_token_ms: 50, complete: false },
  ];

  it("按北京日期并天、升序且标记残日", () => {
    const days = m.dailyUsage(histPoints);
    expect(days).toHaveLength(2);
    expect(days[0].requests).toBe(40);
    expect(days[0].total_tokens).toBe(400);
    expect(m.dailyUsage([{ started_at: "2026-01-01T16:30:00Z", requests: 1, complete: true }])[0].date).toBe("2026-01-02");
    expect(days[0].date < days[1].date).toBe(true);
    expect(days[1].partial).toBe(true);
    expect(days[0].partial).toBe(false);
    expect(m.dailyUsage([])).toHaveLength(0);
    expect(m.dailyUsage([undefined, {}])).toHaveLength(0);
  });

  it("24 个北京时间小时格，空小时补零", () => {
    const profile = m.hourlyProfile(histPoints);
    expect(profile).toHaveLength(24);
    expect(profile[0].value).toBe(40);
    expect(profile[14].value).toBe(5);
    expect(profile[8].value).toBe(0);
    expect(profile[8].buckets).toBe(0);
    expect(m.hourlyProfile(histPoints, { value: (point) => point.total_tokens ?? 0 })[0].value).toBe(400);
  });

  it("累计只累加可读桶，缺失桶不重置", () => {
    const cumulative = m.cumulativeSeries(m.series([
      { started_at: "2026-01-01T00:00:00+08:00", requests: 10, complete: true },
      { started_at: "2026-01-01T00:15:00+08:00", requests: null, complete: true },
      { started_at: "2026-01-01T00:30:00+08:00", requests: 30, complete: true },
    ], m.METRIC_MAP.requests, 900));
    expect(cumulative[0].value).toBe(10);
    expect(cumulative[2].value).toBe(40);
    expect(cumulative[1].value).toBe(10);
  });

  it("前后对半只统计已完结桶", () => {
    const compare = m.splitCompare(m.series([
      { started_at: "2026-01-01T00:00:00+08:00", requests: 10, complete: true },
      { started_at: "2026-01-01T00:15:00+08:00", requests: 20, complete: true },
      { started_at: "2026-01-01T00:30:00+08:00", requests: 60, complete: true },
      { started_at: "2026-01-01T00:45:00+08:00", requests: 60, complete: true },
    ], m.METRIC_MAP.requests, 900));
    expect(compare?.before).toBe(30);
    expect(compare?.after).toBe(120);
    expect(compare?.change).toBeCloseTo(3);
    expect(m.splitCompare(m.series([
      { started_at: "2026-01-01T00:00:00+08:00", requests: 1, complete: true },
    ], m.METRIC_MAP.requests, 900))).toBeNull();
  });
});

describe("桑基布局", () => {
  const FLOW = [
    { source_layer: 0, target_layer: 1, source: "teamA", target: "TASK_000001", requests: 3, total_tokens: 30 },
    { source_layer: 0, target_layer: 1, source: "teamA", target: "TASK_000002", requests: 1, total_tokens: 10 },
    { source_layer: 1, target_layer: 2, source: "TASK_000001", target: "gpt-4o", requests: 3, total_tokens: 30 },
    { source_layer: 1, target_layer: 2, source: "TASK_000002", target: "gpt-4o", requests: 1, total_tokens: 10 },
  ];

  it("节点厚度为流出合计，流带不溢出节点", () => {
    const layout = m.sankeyLayout(FLOW, { width: 900, height: 400 });
    expect(layout.columns).toHaveLength(3);
    expect(layout.edges).toHaveLength(4);
    const task1 = layout.columns[1].nodes.find((node) => node.name === "TASK_000001");
    const teamA = layout.columns[0].nodes.find((node) => node.name === "teamA");
    expect(task1 && task1.height).toBeGreaterThan(0);
    expect(teamA && teamA.height).toBeGreaterThan(task1?.height ?? 0);

    let spill = 0;
    for (const edge of layout.edges) {
      for (const [layer, name, y] of [
        [edge.sourceLayer, edge.sourceName, edge.source.y],
        [edge.targetLayer, edge.targetName, edge.target.y],
      ] as const) {
        const node = layout.columns[layer].nodes.find((item) => item.name === name);
        if (!node || y < node.y - 1e-6 || y + edge.thickness > node.y + node.height + 1e-6) spill += 1;
      }
    }
    expect(spill).toBe(0);
  });

  it("流带厚度与请求数严格成正比，同节点出边不重叠", () => {
    const layout = m.sankeyLayout(FLOW, { width: 900, height: 400 });
    const thickOf = (name: string) => layout.edges.find((edge) => edge.sourceName === name)?.thickness ?? 0;
    expect(thickOf("TASK_000001") / thickOf("TASK_000002")).toBeCloseTo(3, 6);
    const outs = layout.edges.filter((edge) => edge.sourceName === "teamA");
    expect(outs).toHaveLength(2);
    expect(outs[0].source.y + outs[0].thickness).toBeCloseTo(outs[1].source.y, 6);
  });

  it("路径闭合且坐标有限，锚点贴住节点边缘", () => {
    const layout = m.sankeyLayout(FLOW, { width: 900, height: 400 });
    const path = m.sankeyLinkPath(layout.edges[0]);
    expect(path.startsWith("M ")).toBe(true);
    expect(path.endsWith("Z")).toBe(true);
    expect(/NaN|undefined|null|Infinity/.test(path)).toBe(false);
    expect(layout.edges.every((edge) => !/NaN|undefined|null|Infinity/.test(m.sankeyLinkPath(edge)))).toBe(true);

    let badAnchor = 0;
    for (const edge of layout.edges) {
      const from = layout.columns[edge.sourceLayer].nodes.find((node) => node.name === edge.sourceName);
      const to = layout.columns[edge.targetLayer].nodes.find((node) => node.name === edge.targetName);
      if (!from || !to) { badAnchor += 1; continue; }
      if (!Number.isFinite(edge.source.x) || !Number.isFinite(edge.target.x)) { badAnchor += 1; continue; }
      if (Math.abs(edge.source.x - (from.x + layout.nodeWidth)) > 1e-6) badAnchor += 1;
      else if (Math.abs(edge.target.x - to.x) > 1e-6) badAnchor += 1;
    }
    expect(badAnchor).toBe(0);
  });

  it("标签宽度按字宽估算并保证截断后不溢出", () => {
    expect(m.textWidth("中文")).toBeGreaterThan(m.textWidth("ab"));
    expect(m.textWidth("abcd")).toBeCloseTo(4 * 0.56 * 10);
    expect(m.fitLabel("gpt-4o", 100)).toBe("gpt-4o");
    for (const [name, budget] of [["gpt-4o-2024-08-06", 60], ["claude-sonnet-4-20250514", 80], ["中文很长的模型名字", 50]] as const) {
      expect(m.textWidth(m.fitLabel(name, budget))).toBeLessThanOrEqual(budget);
    }
    expect(m.fitLabel("gpt-4o-2024-08-06", 40).endsWith("…")).toBe(true);
    expect(m.fitLabel("gpt-4o", 1).length).toBeGreaterThan(0);
    const mid = m.sankeyLabelBudget({ step: 300, nodeWidth: 14 });
    const last = m.sankeyLabelBudget({ step: 300, nodeWidth: 14, last: true });
    expect(Math.abs(last - mid / 2)).toBeLessThan(1e-9);
    expect(m.sankeyLabelBudget({ step: 0, nodeWidth: 14 })).toBeGreaterThan(0);
  });

  it("退化输入不抛异常", () => {
    expect(m.sankeyLayout([], {}).edges).toHaveLength(0);
    expect(m.sankeyLayers([])).toHaveLength(0);
    expect(m.sankeyLayout([
      { source_layer: 0, target_layer: 1, source: "a", target: "b", requests: 0 },
    ], {}).edges[0].thickness).toBe(0);
  });
});
