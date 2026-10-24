// URL 是筛选的唯一真相，编码/解码错一个字符就会静默换窗口或丢维度。
// 这里只测纯函数：往返、uk 的几种历史写法、范围上限与接口形状。

import { describe, expect, it } from "vitest";
import {
  DIMENSION_LABELS,
  RANGE_PRESETS,
  parseFilters,
  rangeLabel,
  rangeToQuery,
  toApiFilters,
  writeFilters,
  type MetricFilterState,
  type UpstreamKeyFilter,
} from "./filters";

function emptyState(overrides: Partial<MetricFilterState> = {}): MetricFilterState {
  return {
    range: "24h",
    provider: [],
    model: [],
    uk: [],
    ak: [],
    ws: [],
    caller: [],
    outcome: null,
    ...overrides,
  };
}

function ukState(value: string): UpstreamKeyFilter[] {
  const params = new URLSearchParams();
  params.set("uk", value);
  return parseFilters(params).uk;
}

describe("parseFilters", () => {
  it("接受重复参数与逗号分隔，并去重、忽略空串", () => {
    const params = new URLSearchParams();
    params.set("range", "7d");
    params.append("provider", "openai");
    params.append("provider", "anthropic,openai");
    params.append("model", "gpt-4o,deepseek-chat");
    params.append("ak", "ak-1");
    params.append("ws", "team-a");
    params.append("caller", "local");
    params.set("outcome", "failure");
    params.set("page", "activity");

    const state = parseFilters(params);
    expect(state.range).toBe("7d");
    expect(state.provider).toEqual(["openai", "anthropic"]);
    expect(state.model).toEqual(["gpt-4o", "deepseek-chat"]);
    expect(state.ak).toEqual(["ak-1"]);
    expect(state.ws).toEqual(["team-a"]);
    expect(state.caller).toEqual(["local"]);
    expect(state.outcome).toBe("failure");
  });

  it("outcome 只认 success/failure，其它值归为全部", () => {
    expect(parseFilters(new URLSearchParams("outcome=success")).outcome).toBe("success");
    expect(parseFilters(new URLSearchParams("outcome=failure")).outcome).toBe("failure");
    expect(parseFilters(new URLSearchParams("outcome=maybe")).outcome).toBeNull();
    expect(parseFilters(new URLSearchParams("provider=&model=,")).provider).toEqual([]);
  });

  it("uk 兼容数组对、对象、蛇形字段、单个数组与裸写", () => {
    expect(ukState('[["openai","main"]]')).toEqual([{ providerId: "openai", keyName: "main" }]);
    expect(ukState('[{"providerId":"openai","keyName":"main"}]')).toEqual([
      { providerId: "openai", keyName: "main" },
    ]);
    expect(ukState('[{"provider_id":"openai","key_name":"main"}]')).toEqual([
      { providerId: "openai", keyName: "main" },
    ]);
    expect(ukState('["openai","main"]')).toEqual([{ providerId: "openai", keyName: "main" }]);
    expect(ukState("openai,main")).toEqual([{ providerId: "openai", keyName: "main" }]);
    expect(ukState("openai,main,anthropic,backup")).toEqual([
      { providerId: "openai", keyName: "main" },
      { providerId: "anthropic", keyName: "backup" },
    ]);
    expect(ukState('["openai","main","anthropic","backup"]')).toEqual([
      { providerId: "openai", keyName: "main" },
      { providerId: "anthropic", keyName: "backup" },
    ]);
    expect(ukState('[["a","b"]],[["c","d"]]')).toEqual([
      { providerId: "a", keyName: "b" },
      { providerId: "c", keyName: "d" },
    ]);
  });

  it("uk 非法 JSON 忽略，多个 uk 参数合并去重", () => {
    expect(ukState("not-json")).toEqual([]);
    expect(ukState('[["a","b"')).toEqual([]);

    const params = new URLSearchParams();
    params.append("uk", '[["a","b"]]');
    params.append("uk", '[["c","d"],["a","b"]]');
    expect(parseFilters(params).uk).toEqual([
      { providerId: "a", keyName: "b" },
      { providerId: "c", keyName: "d" },
    ]);
  });
});

describe("时间窗", () => {
  it("预设与自定义都换算成小时", () => {
    expect(rangeToQuery("1h")).toEqual({ hours: 1 });
    expect(rangeToQuery("3d")).toEqual({ hours: 72 });
    expect(rangeToQuery("1y")).toEqual({ hours: 8760 });
    expect(rangeToQuery("12h")).toEqual({ hours: 12 });
    expect(rangeToQuery("2d")).toEqual({ hours: 48 });
    expect(rangeToQuery("365d")).toEqual({ hours: 8760 });
    expect(rangeToQuery("all")).toEqual({ allHistory: true });
  });

  it("非法或超过 8760 小时回退默认窗口", () => {
    expect(rangeToQuery("366d")).toEqual({ hours: 24 });
    expect(rangeToQuery("0h")).toEqual({ hours: 24 });
    expect(parseFilters(new URLSearchParams("range=366d"), "6h").range).toBe("6h");
    expect(parseFilters(new URLSearchParams("range=nope"), "6h").range).toBe("6h");
    expect(parseFilters(new URLSearchParams(""), "6h").range).toBe("6h");
    expect(parseFilters(new URLSearchParams("range=999999h")).range).toBe("24h");
  });

  it("标签覆盖预设、自定义、全部与非法值", () => {
    expect(RANGE_PRESETS.map((preset) => preset.value)).toEqual([
      "1h", "6h", "24h", "3d", "7d", "30d", "90d", "180d", "1y", "all",
    ]);
    expect(rangeLabel("24h")).toBe("最近 24 小时");
    expect(rangeLabel("all")).toBe("全部历史");
    expect(rangeLabel("12h")).toBe("最近 12 小时");
    expect(rangeLabel("2d")).toBe("最近 2 天");
    expect(rangeLabel("bogus")).toBe("最近 24 小时");
  });
});

describe("writeFilters", () => {
  it("往返不丢维度、不毁调用方参数，非筛选参数原样保留", () => {
    const original = new URLSearchParams(
      "page=activity&keep=1&range=1h&provider=a&provider=b&model=m&uk=%5B%5B%22p%22%2C%22k%22%5D%5D&ak=x&ws=w&caller=local&outcome=success",
    );
    const state = parseFilters(original);
    const written = writeFilters(original, state);

    expect(original.getAll("provider")).toEqual(["a", "b"]);
    expect(written.get("page")).toBe("activity");
    expect(written.get("keep")).toBe("1");
    expect(written.getAll("provider")).toEqual(["a", "b"]);
    expect(JSON.parse(written.get("uk") ?? "null")).toEqual([["p", "k"]]);
    expect(parseFilters(written)).toEqual(state);
  });

  it("清空维度与 outcome 时删除旧参数，只保留时间窗", () => {
    const original = new URLSearchParams(
      "page=activity&range=1h&provider=a&model=m&ak=x&ws=w&caller=local&uk=%5B%5B%22p%22%2C%22k%22%5D%5D&outcome=failure",
    );
    const written = writeFilters(original, {
      range: "6h",
      provider: [],
      model: [],
      uk: [],
      ak: [],
      ws: [],
      caller: [],
      outcome: null,
    });

    expect(written.get("range")).toBe("6h");
    expect(written.get("page")).toBe("activity");
    for (const key of ["provider", "model", "ak", "ws", "caller", "uk", "outcome"]) {
      expect(written.get(key), key).toBeNull();
    }
  });
});

describe("toApiFilters", () => {
  it("维度映射到接口字段，outcome 转 success 布尔", () => {
    const state = emptyState({
      provider: ["p1"],
      model: ["m1"],
      uk: [{ providerId: "p", keyName: "k" }],
      ak: ["a1"],
      ws: ["w1"],
      caller: ["local"],
      outcome: "failure",
    });
    expect(toApiFilters(state)).toEqual({
      providerIds: ["p1"],
      modelIds: ["m1"],
      upstreamKeys: [{ providerId: "p", keyName: "k" }],
      accessKeyIds: ["a1"],
      workspaces: ["w1"],
      callerTypes: ["local"],
      success: false,
    });
    expect(toApiFilters(emptyState()).success).toBeNull();
    expect(toApiFilters(emptyState({ outcome: "success" })).success).toBe(true);
  });

  it("维度标签齐全", () => {
    expect(Object.keys(DIMENSION_LABELS)).toEqual([
      "provider", "model", "uk", "ak", "ws", "caller", "outcome",
    ]);
  });
});
