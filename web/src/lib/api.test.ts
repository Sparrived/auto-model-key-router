// API 层回归：URL、方法、请求体形状与鉴权钩子。
//
// 重点不是"能发请求"，而是那些一旦写错却**照样成功**的字段：
//   - updateProvider 省略 max_concurrency 会静默保留旧上限；
//   - updateProviderKey 把 null 过滤掉会让上限再也取消不了；
//   - createAccessKey 给不限制的字段补 null 会把"不限制"写成"禁止"；
//   - 多选筛选若用 set 会静默退化成单选。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, appendFilterParams, onUnauthorized, setKey } from "./api";

type FetchMock = ReturnType<typeof vi.fn>;

function mockFetch(payload: unknown = { config_revision: "rev-2" }, status = 200): FetchMock {
  const fn = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify(payload),
    json: async () => payload,
  }));
  vi.stubGlobal("fetch", fn);
  return fn;
}

function lastCall(fn: FetchMock): { url: string; init: RequestInit } {
  const [url, init] = fn.mock.calls.at(-1) as [string, RequestInit];
  return { url, init };
}

function bodyOf(init: RequestInit): Record<string, unknown> {
  return JSON.parse(String(init.body)) as Record<string, unknown>;
}

beforeEach(() => {
  localStorage.clear();
  setKey("test-key");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  onUnauthorized(null);
});

describe("供应商与 Key 写入", () => {
  it("updateProvider 总是发送 max_concurrency，null 表示清除上限", async () => {
    const fetchMock = mockFetch({ id: "openai", base_url: "https://api.openai.com", keys: [] });
    await api.updateProvider("rev-1", "openai", "openai", "https://api.openai.com", {}, null);
    const { url, init } = lastCall(fetchMock);
    expect(url).toBe("/api/providers/openai");
    expect(init.method).toBe("PUT");
    const body = bodyOf(init);
    expect(body).toEqual({
      config_revision: "rev-1",
      id: "openai",
      base_url: "https://api.openai.com",
      routes: {},
      max_concurrency: null,
    });
    expect(Object.prototype.hasOwnProperty.call(body, "max_concurrency")).toBe(true);

    await api.updateProvider("rev-1", "openai", "openai", "https://api.openai.com", { chat: "openai" }, 8);
    expect(bodyOf(lastCall(fetchMock).init).max_concurrency).toBe(8);
  });

  it("updateProviderKey 原样透传 max_concurrency: null", async () => {
    const fetchMock = mockFetch({ name: "main", enabled: false });
    await api.updateProviderKey("rev-1", "openai", "main", { max_concurrency: null, enabled: false });
    const { url, init } = lastCall(fetchMock);
    expect(url).toBe("/api/providers/openai/keys/main");
    expect(init.method).toBe("PUT");
    const body = bodyOf(init);
    expect(body).toEqual({ config_revision: "rev-1", max_concurrency: null, enabled: false });
    expect("max_concurrency" in body).toBe(true);
  });

  it("createProviderKey 不把 null 上限塞进请求体", async () => {
    const fetchMock = mockFetch({ name: "main" });
    await api.createProviderKey("rev-1", "openai", "main", "sk-test", null);
    const body = bodyOf(lastCall(fetchMock).init);
    expect(body).toEqual({ config_revision: "rev-1", name: "main", api_key: "sk-test" });
    expect("max_concurrency" in body).toBe(false);
  });
});

describe("访问密钥三态", () => {
  it("createAccessKey 在未指定清单时省略 providers/models", async () => {
    const fetchMock = mockFetch({ id: "ak1", key: "plain" });
    await api.createAccessKey("rev-1", "试用账号");
    const body = bodyOf(lastCall(fetchMock).init);
    expect(body).toEqual({ config_revision: "rev-1", name: "试用账号" });
    expect("providers" in body).toBe(false);
    expect("models" in body).toBe(false);
  });

  it("createAccessKey 保留显式空数组与 null 的差别", async () => {
    const fetchMock = mockFetch({ id: "ak1", key: "plain" });
    await api.createAccessKey("rev-1", "试用账号", { providers: [], models: null, enabled: false });
    const body = bodyOf(lastCall(fetchMock).init);
    expect(body.providers).toEqual([]);
    expect(body.models).toBeNull();
    expect(body.enabled).toBe(false);
  });

  it("updateAccessKey 只发调用方给了的键", async () => {
    const fetchMock = mockFetch({ id: "ak1" });
    await api.updateAccessKey("rev-1", "ak1", { name: "新名字", models: [] });
    const body = bodyOf(lastCall(fetchMock).init);
    expect(body).toEqual({ config_revision: "rev-1", name: "新名字", models: [] });
    expect("providers" in body).toBe(false);
  });
});

describe("统一模型", () => {
  it("updateUnified 只透传给了的键", async () => {
    const fetchMock = mockFetch({ default: { primary: { model: "gpt-4o" } } });
    await api.updateUnified("rev-1", {
      default: { primary: { model: "gpt-4o", key: "main" } },
      image: null,
      model: "legacy-model",
    });
    const body = bodyOf(lastCall(fetchMock).init);
    expect(body).toEqual({
      config_revision: "rev-1",
      default: { primary: { model: "gpt-4o", key: "main" } },
      image: null,
      model: "legacy-model",
    });
    for (const absent of ["embeddings", "speech", "transcriptions", "video", "rerank", "key", "image_model", "image_key"]) {
      expect(absent in body, absent).toBe(false);
    }
  });
});

describe("筛选参数", () => {
  it("维度内重复、维度间固定顺序，上游 Key 用 JSON 成对值", () => {
    const params = appendFilterParams(new URLSearchParams(), {
      providerIds: ["p1", "p2"],
      accessKeyIds: ["a1"],
      modelIds: ["m1"],
      upstreamKeys: [{ providerId: "p1", keyName: "k1" }],
      workspaces: ["w1"],
      callerTypes: ["local"],
      success: false,
    });
    expect(params.getAll("provider_id")).toEqual(["p1", "p2"]);
    expect(params.getAll("access_key_id")).toEqual(["a1"]);
    expect(params.getAll("model_id")).toEqual(["m1"]);
    expect(params.getAll("upstream_key")).toEqual([JSON.stringify(["p1", "k1"])]);
    expect(params.getAll("workspace")).toEqual(["w1"]);
    expect(params.getAll("caller_type")).toEqual(["local"]);
    expect(params.getAll("success")).toEqual(["false"]);

    const raw = params.toString();
    const order = ["provider_id", "access_key_id", "model_id", "upstream_key", "workspace", "caller_type", "success"];
    const positions = order.map((key) => raw.indexOf(`${key}=`));
    expect(positions).toEqual([...positions].sort((a, b) => a - b));
  });

  it("filteredMetrics 与 keyUsage 保留重复参数", async () => {
    const fetchMock = mockFetch({ snapshot: {}, series: {}, requests: null, window: {} });
    await api.filteredMetrics({
      providerIds: ["p1", "p2"], accessKeyIds: ["a1"], modelIds: ["m1"],
      upstreamKeys: [{ providerId: "p2", keyName: "main" }],
      workspaces: ["w1"], callerTypes: ["access_key"], success: true,
      hours: 6, bucketSeconds: 300, limit: 50,
    });
    const url = new URL(lastCall(fetchMock).url, "http://local");
    expect(url.pathname).toBe("/ui/filtered-metrics.json");
    expect(url.searchParams.getAll("provider_id")).toEqual(["p1", "p2"]);
    expect(url.searchParams.getAll("access_key_id")).toEqual(["a1"]);
    expect(url.searchParams.getAll("model_id")).toEqual(["m1"]);
    expect(url.searchParams.getAll("upstream_key")).toEqual([JSON.stringify(["p2", "main"])]);
    expect(url.searchParams.getAll("workspace")).toEqual(["w1"]);
    expect(url.searchParams.getAll("caller_type")).toEqual(["access_key"]);
    expect(url.searchParams.getAll("success")).toEqual(["true"]);
    expect(url.searchParams.get("hours")).toBe("6");
    expect(url.searchParams.get("bucket_seconds")).toBe("300");
    expect(url.searchParams.get("limit")).toBe("50");

    await api.keyUsage({ hours: 24, providerIds: ["p1", "p2"], accessKeyIds: ["a1"] });
    const keyUrl = new URL(lastCall(fetchMock).url, "http://local");
    expect(keyUrl.pathname).toBe("/ui/key-usage.json");
    expect(keyUrl.searchParams.getAll("provider_id")).toEqual(["p1", "p2"]);
    expect(keyUrl.searchParams.getAll("access_key_id")).toEqual(["a1"]);
  });

  it("metricDimensions 与 usageBreakdown 使用新接口", async () => {
    const fetchMock = mockFetch({ window: {}, providers: [] });
    await api.metricDimensions({ hours: 168 });
    let url = new URL(lastCall(fetchMock).url, "http://local");
    expect(url.pathname).toBe("/ui/metric-dimensions.json");
    expect(url.searchParams.get("hours")).toBe("168");

    await api.metricDimensions({ allHistory: true, hours: 24 });
    url = new URL(lastCall(fetchMock).url, "http://local");
    expect(url.searchParams.get("all_history")).toBe("true");
    expect(url.searchParams.has("hours")).toBe(false);

    await api.usageBreakdown({
      groupBy: "upstream_key", rankBy: "tokens", limit: 8, hours: 24,
      providerIds: ["p1"], upstreamKeys: [{ providerId: "p1", keyName: "k1" }],
    });
    url = new URL(lastCall(fetchMock).url, "http://local");
    expect(url.pathname).toBe("/ui/usage-breakdown.json");
    expect(url.searchParams.get("group_by")).toBe("upstream_key");
    expect(url.searchParams.get("rank_by")).toBe("tokens");
    expect(url.searchParams.get("limit")).toBe("8");
    expect(url.searchParams.getAll("provider_id")).toEqual(["p1"]);
    expect(url.searchParams.getAll("upstream_key")).toEqual([JSON.stringify(["p1", "k1"])]);
  });
});

describe("干跑与错误", () => {
  it("dry_run=1 走查询参数，请求体仍是同一份", async () => {
    const fetchMock = mockFetch({ config_revision: "rev-2" });
    await api.deleteProvider("rev-1", "openai", { dryRun: true });
    const { url, init } = lastCall(fetchMock);
    expect(url).toBe("/api/providers/openai?dry_run=1");
    expect(init.method).toBe("DELETE");
    expect(bodyOf(init)).toEqual({ config_revision: "rev-1" });
  });

  it("401 触发 onUnauthorized，并保留状态与 detail", async () => {
    mockFetch({ detail: "invalid api key" }, 401);
    let called = 0;
    onUnauthorized(() => { called += 1; });
    const failure = api.providers();
    await expect(failure).rejects.toBeInstanceOf(ApiError);
    await expect(failure).rejects.toMatchObject({ status: 401, detail: "invalid api key", isUnauthorized: true, isConflict: false });
    expect(called).toBe(1);
  });

  it("409 标记为版本冲突", async () => {
    mockFetch({ detail: "config revision mismatch" }, 409);
    try {
      await api.providers();
      throw new Error("应当抛错");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiError);
      expect((error as ApiError).isConflict).toBe(true);
    }
  });
});

describe("模型类型分块", () => {
  it("每 100 个名字一问并合并结果", async () => {
    const fetchMock = vi.fn(async (url: string) => {
      const names = new URL(url, "http://local").searchParams.getAll("model");
      return {
        ok: true,
        status: 200,
        text: async () => JSON.stringify({
          catalog_available: true,
          models: Object.fromEntries(names.map((name) => [name, { primary: "text" }])),
        }),
      };
    });
    vi.stubGlobal("fetch", fetchMock);
    const names = Array.from({ length: 250 }, (_, index) => `model-${index}`);
    const index = await api.modelKinds(names);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(Object.keys(index.models)).toHaveLength(250);
    expect(index.catalog_available).toBe(true);
  });
});

describe("试验场推理", () => {
  it("非流式对话补全返回消息、用量与耗时", async () => {
    const fetchMock = mockFetch({
      choices: [{ message: { role: "assistant", content: "你好", reasoning_content: "思考" } }],
      usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
    });
    const result = await api.chatCompletions({ body: { model: "gpt-4o", messages: [{ role: "user", content: "hi" }] } });
    expect(result.message).toEqual({ role: "assistant", content: "你好", reasoning_content: "思考" });
    expect(result.usage?.total_tokens).toBe(15);
    expect(result.ttft).toBeGreaterThanOrEqual(0);
    const { url, init } = lastCall(fetchMock);
    expect(url).toBe("/v1/chat/completions");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer test-key");
  });
});
