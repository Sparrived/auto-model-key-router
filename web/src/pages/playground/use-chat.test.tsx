// 对话状态机：流式增量累积与「停止」后的状态复位。
//
// 停止必须立刻把 UI 复位，而不是等上游 Promise 收尾；上游若忽略 abort，
// 之后的迟到分片也不能把 streaming 重新点亮。

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ChatResult, ChatStreamChunk } from "@/lib/types";
import { useChatSession, type ChatRunner, type ChatSendOptions } from "./use-chat";

const OPTIONS: ChatSendOptions = {
  model: "demo-model",
  content: "你好",
  temperature: 0.7,
  topP: 1,
  stream: true,
};

function chunk(content: string, reasoning = ""): ChatStreamChunk {
  return {
    content,
    reasoning,
    deltaContent: content.slice(-1),
    deltaReasoning: reasoning.slice(-1),
    usage: null,
    raw: null,
  };
}

function result(content: string): ChatResult {
  return {
    message: { role: "assistant", content, reasoning_content: "" },
    usage: null,
    duration: 12,
    ttft: 3,
    raw: null,
  };
}

afterEach(cleanup);

describe("useChatSession 流式状态", () => {
  it("累积两次 onChunk 的全文，结束后 streaming 为 false", async () => {
    const runner: ChatRunner = async ({ onChunk }) => {
      onChunk?.(chunk("Hel"));
      onChunk?.(chunk("Hello"));
      return result("Hello");
    };
    const { result: hook } = renderHook(() => useChatSession(runner));

    await act(async () => {
      await hook.current.send(OPTIONS);
    });

    const assistant = hook.current.messages.at(-1);
    expect(assistant?.role).toBe("assistant");
    expect(assistant?.content).toBe("Hello");
    expect(assistant?.streaming).toBe(false);
    expect(hook.current.streaming).toBe(false);
  });

  it("stop 立即复位，且迟到分片不能复活 streaming", async () => {
    let emit: ((value: ChatStreamChunk) => void) | null = null;
    let receivedSignal: AbortSignal | undefined;
    const runner: ChatRunner = ({ signal, onChunk }) => new Promise<ChatResult>((_resolve, reject) => {
      receivedSignal = signal;
      emit = onChunk || null;
      onChunk?.(chunk("部分"));
      signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
    });
    const { result: hook } = renderHook(() => useChatSession(runner));

    let sending: Promise<void> | undefined;
    act(() => {
      sending = hook.current.send(OPTIONS);
    });
    await waitFor(() => expect(hook.current.streaming).toBe(true));

    act(() => hook.current.stop());
    expect(hook.current.streaming).toBe(false);
    expect(hook.current.messages.at(-1)?.streaming).toBe(false);
    expect(receivedSignal?.aborted).toBe(true);

    // 上游忽略 abort 时的迟到分片只能改内容，不能把状态改回「流式中」。
    act(() => emit?.(chunk("迟到的内容")));
    expect(hook.current.streaming).toBe(false);
    expect(hook.current.messages.at(-1)?.streaming).toBe(false);

    await act(async () => {
      await sending;
    });
    expect(hook.current.streaming).toBe(false);
    expect(hook.current.messages.at(-1)?.streaming).toBe(false);
    expect(hook.current.messages.at(-1)?.error).toBeNull();
  });

  it("stop 后立即重发，旧请求的 finally 不能清掉新 controller", async () => {
    const signals: AbortSignal[] = [];
    let runs = 0;
    const runner: ChatRunner = ({ signal, onChunk }) => {
      runs += 1;
      if (signal) signals.push(signal);
      onChunk?.(chunk(runs === 1 ? "旧请求" : "新请求"));
      return new Promise<ChatResult>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      });
    };
    const { result: hook } = renderHook(() => useChatSession(runner));

    let first!: Promise<void>;
    act(() => {
      first = hook.current.send(OPTIONS);
    });
    await waitFor(() => expect(hook.current.streaming).toBe(true));

    let second!: Promise<void>;
    act(() => {
      // 同一帧内先 stop 再重发：旧请求的 finally 会排在后面执行。
      hook.current.stop();
      second = hook.current.send(OPTIONS);
    });
    await waitFor(() => expect(hook.current.streaming).toBe(true));
    expect(signals).toHaveLength(2);

    // 让第一个请求因 abort 收尾，触发它的 finally。
    await act(async () => {
      await first;
    });

    // 旧请求收尾后，新请求的 controller 与流式状态都必须还在。
    expect(signals[1]?.aborted).toBe(false);
    expect(hook.current.streaming).toBe(true);

    act(() => hook.current.stop());
    expect(signals[1]?.aborted).toBe(true);
    expect(hook.current.streaming).toBe(false);
    expect(hook.current.messages.at(-1)?.streaming).toBe(false);

    await act(async () => {
      await second;
    });
    expect(hook.current.streaming).toBe(false);
  });
});
