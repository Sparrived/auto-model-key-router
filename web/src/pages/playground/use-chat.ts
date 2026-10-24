import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import type {
  ChatCompletionRequest,
  ChatResult,
  ChatStreamChunk,
  ChatUsage,
} from "@/lib/types";

export interface PlaygroundMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  reasoning?: string;
  images?: string[];
  duration?: number;
  ttft?: number | null;
  usage?: ChatUsage | null;
  rawPayload?: unknown;
  rawResponse?: unknown;
  error?: string | null;
  streaming?: boolean;
}

export interface ChatSendOptions {
  model: string;
  content: string;
  images?: string[];
  workspace?: string;
  systemPrompt?: string;
  temperature: number;
  topP: number;
  maxTokens?: string;
  stream: boolean;
}

export interface ChatRunner {
  (options: {
    body: ChatCompletionRequest;
    workspace?: string;
    signal?: AbortSignal;
    onChunk?: (chunk: ChatStreamChunk) => void;
  }): Promise<ChatResult>;
}

function id(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`;
}

function frame(callback: () => void): number {
  if (typeof requestAnimationFrame === "function") return requestAnimationFrame(callback);
  return window.setTimeout(callback, 16);
}

function cancelFrame(handle: number | null): void {
  if (handle === null) return;
  if (typeof cancelAnimationFrame === "function") cancelAnimationFrame(handle);
  else window.clearTimeout(handle);
}

/** 把消息历史折成 OpenAI 兼容的 messages；排除失败/空白助手消息。 */
export function buildMessageHistory(messages: PlaygroundMessage[], systemPrompt: string): Array<Record<string, unknown>> {
  const result: Array<Record<string, unknown>> = [];
  if (systemPrompt.trim()) result.push({ role: "system", content: systemPrompt.trim() });
  for (const message of messages) {
    if (message.role === "user") {
      if (message.images?.length) {
        const parts: Array<Record<string, unknown>> = [];
        if (message.content) parts.push({ type: "text", text: message.content });
        for (const url of message.images) parts.push({ type: "image_url", image_url: { url } });
        result.push({ role: "user", content: parts });
      } else {
        result.push({ role: "user", content: message.content });
      }
    } else if (!message.error && !message.streaming && message.content) {
      result.push({ role: "assistant", content: message.content });
    }
  }
  return result;
}

/**
 * 流式对话状态机。
 *
 * 流块只改 ref 里的当前助手消息，并用 rAF 合并成一次刷新——每个 token 都 setState
 * 会在长回答时把主线程压垮。停止时先立即把 UI 复位，再等上游的 Promise 收尾；
 * 上游即使忽略 abort（或 abort 抛出）也不会把「停止中」卡成永久状态。
 */
export function useChatSession(run: ChatRunner) {
  const messagesRef = useRef<PlaygroundMessage[]>([]);
  const [, rerender] = useReducer((value: number) => value + 1, 0);
  const [streaming, setStreaming] = useState(false);
  const controllerRef = useRef<AbortController | null>(null);
  const currentRef = useRef<PlaygroundMessage | null>(null);
  const frameRef = useRef<number | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      cancelFrame(frameRef.current);
      controllerRef.current?.abort();
    };
  }, []);

  const flush = useCallback(() => {
    frameRef.current = null;
    if (mounted.current) rerender();
  }, []);

  const schedule = useCallback(() => {
    if (frameRef.current !== null) return;
    frameRef.current = frame(flush);
  }, [flush]);

  const stop = useCallback(() => {
    controllerRef.current?.abort();
    controllerRef.current = null;
    if (currentRef.current) currentRef.current.streaming = false;
    cancelFrame(frameRef.current);
    frameRef.current = null;
    setStreaming(false);
    rerender();
  }, []);

  const clear = useCallback(() => {
    if (controllerRef.current) stop();
    messagesRef.current = [];
    currentRef.current = null;
    rerender();
  }, [stop]);

  const send = useCallback(async (options: ChatSendOptions): Promise<void> => {
    if (controllerRef.current) return;
    const content = options.content.trim();
    const images = options.images || [];
    if (!content && !images.length) return;

    const user: PlaygroundMessage = { id: id("user"), role: "user", content, images };
    const assistant: PlaygroundMessage = {
      id: id("asst"), role: "assistant", content: "", reasoning: "", streaming: true,
      duration: 0, ttft: null, usage: null, rawPayload: null, rawResponse: null, error: null,
    };
    const history = buildMessageHistory(messagesRef.current, options.systemPrompt || "");
    const payload: ChatCompletionRequest = {
      model: options.model,
      messages: [...history, { role: "user", content: content || " " }] as ChatCompletionRequest["messages"],
      stream: options.stream,
      temperature: options.temperature,
      top_p: options.topP,
    };
    const maxTokens = Number(options.maxTokens);
    if (options.maxTokens?.trim() && Number.isFinite(maxTokens) && maxTokens > 0) {
      payload.max_tokens = maxTokens;
    }
    if (images.length) {
      const last = payload.messages?.[payload.messages.length - 1];
      if (last) {
        const parts: Array<Record<string, unknown>> = [];
        if (content) parts.push({ type: "text", text: content });
        for (const url of images) parts.push({ type: "image_url", image_url: { url } });
        last.content = parts as unknown as string;
      }
    }
    assistant.rawPayload = payload;
    messagesRef.current = [...messagesRef.current, user, assistant];
    currentRef.current = assistant;
    const controller = new AbortController();
    controllerRef.current = controller;
    setStreaming(true);
    rerender();

    try {
      const result = await run({
        body: payload,
        workspace: options.workspace,
        signal: controller.signal,
        onChunk: (chunk) => {
          assistant.content = chunk.content;
          assistant.reasoning = chunk.reasoning || assistant.reasoning;
          assistant.usage = chunk.usage;
          assistant.rawResponse = chunk.raw;
          schedule();
        },
      });
      assistant.content = result.message.content;
      assistant.reasoning = result.message.reasoning_content || assistant.reasoning;
      assistant.duration = result.duration;
      assistant.ttft = result.ttft;
      assistant.usage = result.usage;
      assistant.rawResponse = result.raw;
      assistant.streaming = false;
    } catch (failure) {
      if (!controller.signal.aborted) {
        assistant.error = failure instanceof Error ? failure.message : String(failure);
      }
      assistant.streaming = false;
    } finally {
      // 只有仍是这次请求的 controller 才允许动共享的帧/流式状态。stop() 后立刻重发时，
      // 旧请求的 finally 不能取消新请求的刷新帧，也不能把新请求的 controller 抹掉。
      const isCurrent = controllerRef.current === controller;
      if (isCurrent) {
        cancelFrame(frameRef.current);
        frameRef.current = null;
        controllerRef.current = null;
        if (mounted.current) setStreaming(false);
      }
      if (currentRef.current === assistant) {
        assistant.streaming = false;
        currentRef.current = null;
      }
      if (mounted.current) rerender();
    }
  }, [run, schedule]);

  return {
    messages: messagesRef.current,
    streaming,
    send,
    stop,
    clear,
  };
}
