// 状态监控口径回归：从 legacy 的 webui_status_probe.mjs（status-math 部分）搬过来。
//
// 颜色是最容易被悄悄改坏的东西——阈值挪 5 分界面上看不出异常，只有逐格断言才能发现。

import { describe, expect, it } from "vitest";
import * as m from "./status-math";
import type { StatusCell } from "./status-math";

const BUCKET_STARTS = [
  "2026-07-14T00:00:00+08:00",
  "2026-07-14T01:00:00+08:00",
  "2026-07-14T02:00:00+08:00",
  "2026-07-14T03:00:00+08:00",
  "2026-07-14T04:00:00+08:00",
];

const statCell = (stats: Partial<StatusCell>): StatusCell => ({
  started_at: BUCKET_STARTS[0],
  ended_at: BUCKET_STARTS[1],
  complete: true,
  ...stats,
});

describe("常量", () => {
  it("样本门槛、阈值、锚点与权重", () => {
    expect(m.MIN_HEALTH_SAMPLES).toBe(3);
    expect(m.HEALTH_THRESHOLDS).toEqual({ good: 80, warn: 50 });
    expect([m.TTFT_GOOD_MS, m.TTFT_BAD_MS]).toEqual([1500, 8000]);
    expect(m.CACHE_FULL_RATE).toBe(0.3);
    expect(m.OVERALL_WEIGHTS).toEqual({ success: 0.6, ttft: 0.2, cache: 0.2 });
    expect(m.GROUP_BY_MODES.map((item) => item.id)).toEqual(["provider", "provider_model", "provider_key"]);
    expect(m.HEALTH_MODES.map((item) => item.id)).toEqual(["overall", "success", "ttft", "cache"]);
  });
});

describe("样本门槛与分档", () => {
  it("少于 3 次一律 unknown，0 次是 no-traffic", () => {
    const two = statCell({ requests: 2, successes: 2, total_first_token_ms: 3000, prompt_tokens: 100, cached_tokens: 50 });
    expect(m.healthBand(two, "overall")).toBe("unknown");
    expect(m.healthBand(two, "success")).toBe("unknown");
    expect(m.healthScore(two, "success")).toBeNull();
    const three = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 0 });
    expect(m.healthBand(three, "overall")).toBe("good");
    expect(m.healthBand(statCell({ requests: 0 }), "overall")).toBe("no-traffic");
    expect(m.healthScore(statCell({ requests: 0 }), "overall")).toBeNull();
  });

  it("成功率 100/50/33 对应 good/warn/bad", () => {
    expect(m.healthBand(statCell({ requests: 3, successes: 3 }), "success")).toBe("good");
    expect(m.healthBand(statCell({ requests: 4, successes: 2 }), "success")).toBe("warn");
    expect(m.healthBand(statCell({ requests: 3, successes: 1 }), "success")).toBe("bad");
  });

  it("综合分边界 80/79/50/49", () => {
    const score80 = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 0 });
    expect(m.healthScore(score80, "overall")).toBe(80);
    expect(m.healthBand(score80, "overall")).toBe("good");
    const score79 = statCell({ requests: 3, successes: 3, total_first_token_ms: 5475, prompt_tokens: 100, cached_tokens: 0 });
    expect(m.healthScore(score79, "overall")).toBe(79);
    expect(m.healthBand(score79, "overall")).toBe("warn");
    const score50 = statCell({ requests: 4, successes: 2, total_first_token_ms: 6000, prompt_tokens: 100, cached_tokens: 0 });
    expect(m.healthScore(score50, "overall")).toBe(50);
    expect(m.healthBand(score50, "overall")).toBe("warn");
    const score49 = statCell({ requests: 4, successes: 2, total_first_token_ms: 7300 });
    expect(m.healthScore(score49, "overall")).toBe(49);
    expect(m.healthBand(score49, "overall")).toBe("bad");
  });

  it("缺读数按 0 计入，三项满分才是 100", () => {
    expect(m.healthScore(statCell({ requests: 3, successes: 3 }), "overall")).toBe(60);
    const full = statCell({ requests: 3, successes: 3, total_first_token_ms: 4500, prompt_tokens: 100, cached_tokens: 30 });
    expect(m.healthScore(full, "overall")).toBe(100);
  });
});

describe("单项评分", () => {
  it("首字锚点线性插值并封顶", () => {
    expect(m.healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 1500 }), "ttft")).toBe(100);
    expect(m.healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 8000 }), "ttft")).toBe(0);
    expect(m.healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 4750 }), "ttft")).toBe(50);
    expect(m.healthScore(statCell({ requests: 3, total_first_token_ms: 3 * 20000 }), "ttft")).toBe(0);
    expect(m.healthBand(statCell({ requests: 3, successes: 3 }), "ttft")).toBe("unknown");
    expect(m.healthBand(statCell({ requests: 3, total_first_token_ms: 3 * 10000 }), "ttft")).toBe("bad");
  });

  it("缓存率 30% 满分、15% 半程、0% 零分", () => {
    expect(m.healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 30 }), "cache")).toBe(100);
    expect(m.healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 15 }), "cache")).toBe(50);
    expect(m.healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 0 }), "cache")).toBe(0);
    expect(m.healthScore(statCell({ requests: 3, prompt_tokens: 100, cached_tokens: 80 }), "cache")).toBe(100);
    expect(m.healthBand(statCell({ requests: 3, successes: 3 }), "cache")).toBe("unknown");
    expect(m.healthScore(statCell({ requests: 3, prompt_tokens: 0, cached_tokens: 0 }), "cache")).toBeNull();
  });
});

describe("格子读数", () => {
  it("0/0 不是 0%，首字与缓存缺失返回 null", () => {
    expect(m.cellMetrics({ requests: 0 }).successRate).toBeNull();
    expect(m.cellMetrics({ requests: 3, prompt_tokens: 0 }).cacheRate).toBeNull();
    expect(m.cellMetrics({ requests: 3, total_first_token_ms: 0 }).avgTtft).toBeNull();
    expect(m.cellMetrics({ requests: 4, total_first_token_ms: 2000 }).avgTtft).toBe(500);
    expect(m.cellMetrics({ requests: 5, successes: 2 }).failures).toBe(3);
    expect(m.cellMetrics({ requests: -1, successes: -5 }).requests).toBe(0);
  });

  it("逐格相加且传播 complete=false", () => {
    const aggregate = m.aggregateCells([
      statCell({ requests: 2, successes: 1, failures: 1, prompt_tokens: 10, cached_tokens: 5 }),
      { ...statCell({ requests: 1, successes: 1, failures: 0, prompt_tokens: 20, cached_tokens: 0 }), complete: false },
    ]);
    expect(aggregate.requests).toBe(3);
    expect(aggregate.successes).toBe(2);
    expect(aggregate.prompt_tokens).toBe(30);
    expect(aggregate.complete).toBe(false);
    expect(m.aggregateCells([]).requests).toBe(0);
  });
});

describe("行标签与文案", () => {
  it("三种身份与未归属", () => {
    expect(m.rowLabel({ provider_id: "prov-a" }, "provider").label).toBe("prov-a");
    expect(m.rowLabel({ provider_id: null }, "provider").label).toBe("未归属");
    expect(m.rowLabel({ provider_id: "prov-a", upstream_model_id: "model-x" }, "provider_model").label).toBe("prov-a / model-x");
    expect(m.rowLabel({ provider_id: "prov-a", upstream_model_id: null }, "provider_model").label).toBe("prov-a / 未归属");
    expect(m.rowLabel({ provider_id: "prov-a", key_name: "key-main" }, "provider_key").label).toBe("prov-a / key-main");
  });

  it("分档文案与 tooltip 写出全部数值", () => {
    expect(m.HEALTH_BAND_LABELS).toEqual({
      good: "健康", warn: "注意", bad: "异常", unknown: "样本不足", "no-traffic": "无流量",
    });
    expect(m.healthBandLabel("warn")).toBe("注意");
    const tip = m.formatHealthTooltip(statCell({
      requests: 3, successes: 2, failures: 1,
      prompt_tokens: 100, cached_tokens: 40,
      total_first_token_ms: 3000, total_duration_ms: 500,
    }), "overall");
    expect(tip).toContain("请求 3 次");
    expect(tip).toContain("成功率 66.7%");
    expect(tip).toContain("平均首字 1.00s");
    expect(tip).toContain("缓存率 40.0%");
    expect(tip).toContain("着色口径 综合");
    expect(tip).toContain("健康分 80");
    expect(tip).toContain("时段 ");
    const partial = m.formatHealthTooltip(
      { ...statCell({ requests: 3, successes: 3, total_first_token_ms: 4500 }), complete: false }, "success");
    expect(partial).toContain("累加中，读数偏低");
    expect(partial).toContain("着色口径 错误率");
    expect(m.formatHealthTooltip(statCell({ requests: 3 }), "ttft")).toContain("平均首字 无读数");
  });
});

describe("错误归因", () => {
  it("只按失败状态码分类，不被成功 200/302 吞掉", () => {
    const reasons = m.errorReasons({ failures: 15, status_codes: { "200": 1000, "302": 8 } }, {
      "200": 2, "401": 1, "403": 1, "429": 2,
      "400": 1, "404": 1, "413": 1, "422": 1,
      "500": 1, "502": 1, "418": 1, none: 2,
    });
    expect(reasons.rows.map((row) => row.id)).toEqual(
      ["auth", "rate", "request", "upstream", "other4xx", "response", "no-status"]);
    expect(reasons.rows.map((row) => row.label)).toEqual(
      ["认证失败", "限流", "请求错误", "上游故障", "其它 4xx", "响应异常 / 2xx 失败", "未收到上游状态码"]);
    expect(reasons.rows.map((row) => row.count)).toEqual([2, 2, 4, 2, 1, 2, 2]);
    expect(reasons.failures).toBe(15);
    expect(reasons.coded).toBe(13);
    expect(reasons.noStatus).toBe(2);
    expect(reasons.rows[0].ratio).toBeCloseTo(2 / 15);
    expect(reasons.available).toBe(true);
  });

  it("旧数据不臆造关联，3xx 失败计入未分类", () => {
    expect(m.errorReasons({ failures: 3, status_codes: { "200": 99 } }).available).toBe(false);
    expect(m.errorReasons({ failures: 3 }).noStatus).toBe(0);
    expect(m.errorReasons({ failures: 1 }, { "302": 1 }).unclassified).toBe(1);
    expect(m.errorReasons({ failures: 1 }, { none: -2 }).noStatus).toBe(0);
    expect(m.errorReasons({}, {}).rows.map((row) => row.count)).toEqual([0, 0, 0, 0, 0, 0, 0]);
    expect(m.errorReasons({}, {}).rows[0].ratio).toBeNull();
    expect(m.ERROR_REASON_ROWS.map((row) => row.id)).toEqual(
      ["auth", "rate", "request", "upstream", "other4xx", "response", "no-status"]);
  });
});
