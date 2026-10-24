// /v1/chat/completions 的 SSE 解析：两个分片累积成全文，[DONE] 只结束流。
//
// 这里用真实 ReadableStream 走 api.chatCompletions，验证请求地址、鉴权头、
// 增量回调与最终结果，而不是只测一个纯解析函数。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import type { ChatStreamChunk } from "@/lib/types";

interface Call {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: Record<string, unknown>;
}

let calls: Call[] = [];

function sseResponse(events: string[]): Response {
  const encoder = new TextEncoder();
  const text = events.map((event) => `data: ${event}`).join("\n\n") + "\n\n";
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(text));
      controller.close();
    },
  });
  return new Response(stream, { status: 200, headers: { "Content-Type": "text/event-stream" } });
}

beforeEach(() => {
  calls = [];
  localStorage.clear();
  localStorage.setItem("amkr.apiKey", "test-key");
  window.history.replaceState(null, "", "/ui/index.html");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("chatCompletions SSE", () => {
  it("累积 data 分片，跳过 [DONE]，并返回最终内容", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({
        url: String(input),
        method: (init?.method || "GET").toUpperCase(),
        headers: (init?.headers || {}) as Record<string, string>,
        body: JSON.parse(String(init?.body)) as Record<string, unknown>,
      });
      return sseResponse([
        '{"choices":[{"delta":{"content":"你"}}]}',
        '{"choices":[{"delta":{"content":"好"}}]}',
        "[DONE]",
      ]);
    }));

    const chunks: ChatStreamChunk[] = [];
    const result = await api.chatCompletions({
      body: { model: "m", messages: [], stream: true },
      onChunk: (value) => chunks.push(value),
    });

    expect(calls[0]?.url.endsWith("/v1/chat/completions")).toBe(true);
    expect(calls[0]?.method).toBe("POST");
    expect(calls[0]?.headers.Authorization).toBe("Bearer test-key");
    expect(calls[0]?.body).toMatchObject({ model: "m", stream: true });

    expect(chunks).toHaveLength(2);
    expect(chunks[0]).toMatchObject({ content: "你", deltaContent: "你" });
    expect(chunks[1]).toMatchObject({ content: "你好", deltaContent: "好" });
    expect(result.message.content).toBe("你好");
    expect(result.aborted).toBe(false);
  });
});
