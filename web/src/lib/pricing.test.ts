// 成本估算口径回归：从 legacy 的 webui_pricing_probe.mjs 搬过来。
//
// 这里钉住三件最容易出错的事：缓存 token 不能重复计费；拿不到单价必须回 null 而不是 0；
// 匹配要大小写不敏感、能穿透日期后缀与供应商前缀。

import { afterEach, describe, expect, it } from "vitest";
import * as m from "./pricing";

const document = {
  version: 1,
  source: "https://models.dev/api.json",
  updated_at: "2026-01-02T03:04:05Z",
  error: null,
  models: {
    "gpt-4o": { input: 2.5, output: 10, cache_read: 1.25 },
    "claude-sonnet-4-5": { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 },
    "no-cache-price": { input: 1, output: 2 },
    "bad-entry": { input: "x", output: 2 },
    "null-input": { input: null, output: 3 },
  },
};

afterEach(() => {
  m.__setIndexForTest(null);
  localStorage.clear();
});

describe("目录归一化", () => {
  it("保留有效条目并报告更新时间", () => {
    const index = m.normalizeCatalog(document);
    expect(index).not.toBeNull();
    expect(index?.models.size).toBe(3);
    expect(index?.models.has("bad-entry")).toBe(false);
    expect(index?.models.has("null-input")).toBe(false);
    expect(index?.updatedAt).toBe("2026-01-02T03:04:05Z");
    expect(index?.models.get("no-cache-price")?.cacheRead).toBeNull();
  });

  it("形状不符时返回 null，而不是空索引", () => {
    expect(m.normalizeCatalog(null)).toBeNull();
    expect(m.normalizeCatalog({ version: 1 })).toBeNull();
    expect(m.normalizeCatalog({ models: {} })).toBeNull();
    expect(m.normalizeCatalog({ models: "nope" })).toBeNull();
    expect(m.normalizeCatalog({ models: { a: { input: "x", output: "y" } } })).toBeNull();
  });
});

describe("单价匹配", () => {
  const index = m.normalizeCatalog(document);
  const dated = m.normalizeCatalog({ models: { "gpt-4o-mini": { input: 0.15, output: 0.6 } } });
  const datedOnly = m.normalizeCatalog({ models: { "claude-3-7-sonnet-20250219": { input: 3, output: 15 } } });

  it("大小写、首尾空白与供应商前缀都能命中", () => {
    expect(m.lookupPrice(index, "gpt-4o")?.input).toBe(2.5);
    expect(m.lookupPrice(index, "GPT-4O")?.input).toBe(2.5);
    expect(m.lookupPrice(index, "  gpt-4o  ")?.input).toBe(2.5);
    expect(m.lookupPrice(index, "openai/gpt-4o")?.input).toBe(2.5);
    expect(m.lookupPrice(index, "not-a-model")).toBeNull();
    expect(m.lookupPrice(index, "")).toBeNull();
    expect(m.lookupPrice(index, null)).toBeNull();
    expect(m.lookupPrice(null, "gpt-4o")).toBeNull();
  });

  it("剥离日期后缀，但不误伤普通名字", () => {
    expect(m.lookupPrice(dated, "gpt-4o-mini-2024-07-18")?.input).toBe(0.15);
    expect(m.lookupPrice(dated, "gpt-4o-mini-20240718")?.input).toBe(0.15);
    expect(m.lookupPrice(datedOnly, "claude-3-7-sonnet-20250219")?.input).toBe(3);
    expect(m.lookupPrice(dated, "gpt-4o-mini-latest")).toBeNull();
  });
});

describe("成本公式", () => {
  const index = m.normalizeCatalog(document);
  const sonnet = index?.models.get("claude-sonnet-4-5");
  const gpt4o = index?.models.get("gpt-4o");
  const usage = {
    prompt_tokens: 1_000_000,
    completion_tokens: 200_000,
    cached_tokens: 400_000,
    cache_read_input_tokens: 400_000,
    cache_creation_input_tokens: 100_000,
  };

  it("缓存读、缓存写与输出分别计价", () => {
    expect(m.estimateCost(sonnet ?? null, usage)).toBeCloseTo(4.995);
    const naive = (usage.prompt_tokens * (sonnet?.input ?? 0) + usage.completion_tokens * (sonnet?.output ?? 0)) / 1e6;
    expect(m.estimateCost(sonnet ?? null, usage)).toBeLessThan(naive);
    expect(m.estimateCost(sonnet ?? null, usage)).toBeCloseTo(
      (500000 * 3 + 400000 * 0.3 + 100000 * 3.75 + 200000 * 15) / 1e6,
    );
  });

  it("四项分解与合计出自同一处算术", () => {
    const parts = m.costParts(sonnet ?? null, usage);
    expect(parts?.input).toBeCloseTo(1.5);
    expect(parts?.cacheRead).toBeCloseTo(0.12);
    expect(parts?.cacheWrite).toBeCloseTo(0.375);
    expect(parts?.output).toBeCloseTo(3);
    expect(m.costParts(null, usage)).toBeNull();
    expect(m.costParts(sonnet ?? null, null)).toBeNull();
  });

  it("OpenAI 语义回退 cached_tokens，缺失缓存价回退输入价", () => {
    const openaiUsage = {
      prompt_tokens: 1_000_000,
      completion_tokens: 1_000_000,
      cached_tokens: 500_000,
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    };
    expect(m.estimateCost(gpt4o ?? null, openaiUsage)).toBeCloseTo(11.875);
    const noCache = index?.models.get("no-cache-price");
    expect(m.estimateCost(noCache ?? null, {
      prompt_tokens: 1_000_000, completion_tokens: 0, cached_tokens: 500_000,
    })).toBeCloseTo(1);
  });

  it("零用量是 0，负数/NaN/字符串按 0 处理", () => {
    const zero = m.costParts(gpt4o ?? null, {});
    expect(zero?.input).toBe(0);
    expect(zero?.output).toBe(0);
    expect(m.estimateCost(gpt4o ?? null, {})).toBe(0);
    expect(m.estimateCost(gpt4o ?? null, { prompt_tokens: -100, completion_tokens: -5 })).toBe(0);
    expect(m.estimateCost(gpt4o ?? null, { prompt_tokens: "abc", completion_tokens: NaN })).toBe(0);
    expect(m.estimateCost(gpt4o ?? null, {
      prompt_tokens: 10, cached_tokens: 1000, cache_read_input_tokens: 1000,
    })).toBeGreaterThanOrEqual(0);
    expect(m.estimateCost(null, usage)).toBeNull();
    expect(m.estimateCost(gpt4o ?? null, null)).toBeNull();
  });

  it("单请求成本只认 upstream_model_id", () => {
    const item = {
      model_id: "本地路由名",
      upstream_model_id: "gpt-4o",
      prompt_tokens: 1_000_000,
      completion_tokens: 1_000_000,
      cached_tokens: 500_000,
    };
    expect(m.requestCost(index, item)).toBeCloseTo(11.875);
    const localOnly = { model_id: "gpt-4o" };
    expect(m.requestCost(index, localOnly)).toBeNull();
    expect(m.requestCost(index, { ...item, upstream_model_id: "nope" })).toBeNull();
    expect(m.requestCost(index, null)).toBeNull();
  });

  it("聚合统计有价条目与总条目", () => {
    const entries = {
      "gpt-4o": { prompt_tokens: 1_000_000, completion_tokens: 0, cached_tokens: 0 },
      "not-priced": { prompt_tokens: 5_000_000, completion_tokens: 0, cached_tokens: 0 },
      "claude-sonnet-4-5": { prompt_tokens: 1_000_000, completion_tokens: 0, cached_tokens: 0, cache_read_input_tokens: 0 },
    };
    const summed = m.sumCost(index, entries);
    expect(summed.total).toBeCloseTo(5.5);
    expect(summed.priced).toBe(2);
    expect(summed.total_count).toBe(3);
    expect(m.sumCost(index, {}).total).toBe(0);
    expect(m.sumCost(index, null).total_count).toBe(0);
  });
});

describe("金额与单价格式", () => {
  it("空值与 NaN 显示 —，绝不显示 0", () => {
    expect(m.formatCost(null)).toBe("—");
    expect(m.formatCost(undefined)).toBe("—");
    expect(m.formatCost(NaN)).toBe("—");
    expect(m.formatCost(0)).toBe("$0");
    expect(m.formatCost(0.00001)).toBe("<$0.0001");
    expect(m.formatCost(0.001234)).toBe("$0.00123");
    expect(m.formatCost(0.5)).toBe("$0.5000");
    expect(m.formatCost(12.3456)).toBe("$12.35");
    expect(m.formatCost(12345.678)).toBe("$12,345.68");
    expect(m.formatCost(0.00001)).not.toMatch(/e/i);
    expect(m.formatCost(0.0000012)).not.toMatch(/e/i);
  });

  it("精确金额与单价按需保留精度", () => {
    expect(m.formatCostExact(null)).toBe("—");
    expect(m.formatCostExact(0.0001234).startsWith("$0.000123")).toBe(true);
    expect(m.formatPrice(null)).toBe("—");
    expect(m.formatPrice(3)).toBe("$3");
    expect(m.formatPrice(0.15)).toBe("$0.15");
    expect(m.formatPrice(0)).toBe("$0");
  });
});

describe("目录加载", () => {
  it("失败不抛异常，状态里留下错误文案", async () => {
    const missing = await m.loadPricing(async () => { throw new Error("HTTP 503"); });
    expect(missing === null || typeof missing === "object").toBe(true);
    expect(typeof m.pricingStatus().error).toBe("string");
  });

  it("成功后缓存，后续调用不再请求", async () => {
    const loaded = await m.loadPricing(async () => document);
    expect(loaded?.models.size).toBe(3);
    let calls = 0;
    await m.loadPricing(async () => { calls += 1; return document; });
    expect(calls).toBe(0);
    expect(m.currentIndex()).toBe(loaded);
  });
});
