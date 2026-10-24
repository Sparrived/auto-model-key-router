// 试验场载荷面板的用户路径：发送后右侧必须自动出现请求体与响应。
// 流式响应不能只显示最后一个 chunk——页面还要展示拼接后的最终内容与 usage。

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConfirmProvider } from "@/components/ui/ConfirmProvider";
import PlaygroundPage from "./index";

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

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

function payloadPanels(): HTMLElement {
  const heading = screen.getByRole("heading", { name: "请求与响应载荷" });
  const panel = heading.closest("section");
  if (!panel) throw new Error("请求与响应载荷面板不存在");
  return panel;
}

function payloadPre(): NodeListOf<HTMLPreElement> {
  return payloadPanels().querySelectorAll("pre");
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("amkr.apiKey", "test-key");
  window.history.replaceState(null, "", "/ui/index.html");
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {};
    if (url.endsWith("/v1/models")) {
      return jsonResponse({ data: [{ id: "unified-model" }] });
    }
    if (url.endsWith("/api/models")) {
      return jsonResponse({ models: [{ id: "unified-model" }] });
    }
    if (url.includes("/ui/model-kinds.json")) {
      return jsonResponse({ catalog_available: false, models: {} });
    }
    if (url.endsWith("/api/workspaces")) {
      return jsonResponse({ workspaces: [] });
    }
    if (url.endsWith("/v1/chat/completions")) {
      if (body.stream) {
        return sseResponse([
          '{"choices":[{"delta":{"role":"assistant","content":"你"}}]}',
          '{"choices":[{"delta":{"content":"好呀"}}]}',
          '{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}',
          "[DONE]",
        ]);
      }
      return jsonResponse({
        choices: [{ message: { role: "assistant", content: "非流式回复" } }],
        usage: { prompt_tokens: 2, completion_tokens: 3, total_tokens: 5 },
      });
    }
    if (url.endsWith("/v1/images/generations")) {
      return jsonResponse({ created: 1, data: [{ url: "https://example.test/generated.png" }] });
    }
    throw new Error(`unexpected fetch: ${url}`);
  }));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("试验场载荷面板", () => {
  it("流式发送后显示请求体和拼接后的最终内容、last chunk 与 usage", async () => {
    const user = userEvent.setup();
    render(<ConfirmProvider><PlaygroundPage /></ConfirmProvider>);

    const input = await screen.findByLabelText("消息输入");
    expect(screen.getByText("还没有请求载荷")).toBeInTheDocument();

    await user.type(input, "你好");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("你好呀");

    await waitFor(() => {
      expect(screen.queryByText("还没有请求载荷")).not.toBeInTheDocument();
      const pre = payloadPre();
      expect(pre).toHaveLength(2);
      expect(pre[0].textContent).toContain('"model": "unified-model"');
      expect(pre[0].textContent).toContain('"stream": true');
      expect(pre[0].textContent).toContain('"content": "你好"');
      expect(pre[1].textContent).toContain('"content": "你好呀"');
      expect(pre[1].textContent).toContain('"last_chunk"');
      expect(pre[1].textContent).toContain('"total_tokens": 7');
    });
  });

  it("非流式发送后显示请求体和原始响应 JSON", async () => {
    const user = userEvent.setup();
    render(<ConfirmProvider><PlaygroundPage /></ConfirmProvider>);

    const input = await screen.findByLabelText("消息输入");
    const streamSwitch = screen.getByRole("switch", { name: /流式输出/ });
    expect(streamSwitch).toBeChecked();
    await user.click(streamSwitch);
    expect(streamSwitch).not.toBeChecked();

    await user.type(input, "非流式问题");
    await user.click(screen.getByRole("button", { name: "发送" }));
    await screen.findByText("非流式回复");

    await waitFor(() => {
      const pre = payloadPre();
      expect(pre[0].textContent).toContain('"stream": false');
      expect(pre[0].textContent).toContain('"content": "非流式问题"');
      expect(pre[1].textContent).toContain('"非流式回复"');
      expect(pre[1].textContent).toContain('"total_tokens": 5');
    });
  });

  it("图像生成后显示请求体和生成响应", async () => {
    const user = userEvent.setup();
    render(<ConfirmProvider><PlaygroundPage /></ConfirmProvider>);

    await screen.findByLabelText("消息输入");
    await user.click(screen.getByRole("radio", { name: "图像生成" }));
    const prompt = await screen.findByLabelText(/图像提示词/);
    await user.type(prompt, "一只测试猫");
    await user.click(screen.getByRole("button", { name: "立即生成图片" }));

    await waitFor(() => {
      const pre = payloadPre();
      expect(pre).toHaveLength(2);
      expect(pre[0].textContent).toContain('"model": "unified-model"');
      expect(pre[0].textContent).toContain('"prompt": "一只测试猫"');
      expect(pre[1].textContent).toContain("https://example.test/generated.png");
    });
  });
});
