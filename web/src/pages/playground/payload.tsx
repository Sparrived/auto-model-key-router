import { Copy } from "lucide-react";
import { toast } from "sonner";
import { copyText, errorText } from "@/lib/format";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Empty } from "@/components/ui/Empty";
import type { ChatCompletionRequest } from "@/lib/types";
import type { PlaygroundMessage } from "./use-chat";

export interface InspectPayload {
  title: string;
  request: unknown;
  response: unknown;
  duration?: number;
  usage?: { total_tokens?: number; [key: string]: unknown } | null;
}

function stringify(value: unknown): string {
  try { return JSON.stringify(value ?? {}, null, 2); } catch { return String(value); }
}

async function copy(value: string, message: string) {
  try {
    await copyText(value);
    toast.success(message);
  } catch (failure) {
    toast.error(errorText(failure));
  }
}

// 对话的载荷要跟着每次请求走：请求体来自消息自带的 rawPayload，响应则要分两路。
// 非流式直接展示服务端 JSON；流式只把最后一个 chunk 交给上游解析，单看它会丢掉
// 已经拼接好的全文，所以这里补上最终内容、usage 和最后一个 chunk。
export function chatInspectPayload(message: PlaygroundMessage): InspectPayload {
  const request = (message.rawPayload ?? {}) as ChatCompletionRequest;
  const stream = request.stream === true;
  const model = typeof request.model === "string" ? request.model : "";
  return {
    title: `文本对话详情${model ? ` · ${model}` : ""}`,
    request,
    response: stream ? {
      stream: true,
      content: message.content,
      reasoning_content: message.reasoning || null,
      usage: message.usage ?? null,
      last_chunk: message.rawResponse ?? null,
    } : (message.rawResponse ?? null),
    duration: message.duration,
    usage: message.usage,
  };
}

export function PayloadPanel({ inspect }: { inspect: InspectPayload | null }) {
  if (!inspect) {
    return <Empty title="还没有请求载荷" description="发送一条消息或生成一张图片后，这里会显示入站请求与原始响应。" />;
  }
  const request = stringify(inspect.request);
  const response = stringify(inspect.response);
  return (
    <div className="min-w-0 space-y-3">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone="neutral">{inspect.title}</Badge>
        {inspect.duration ? <Badge tone="neutral">耗时 {inspect.duration}ms</Badge> : null}
        {inspect.usage?.total_tokens ? <Badge tone="neutral">{inspect.usage.total_tokens} tokens</Badge> : null}
      </div>
      <section className="min-w-0">
        <div className="mb-1 flex items-center justify-between gap-2">
          <h4 className="text-meta font-medium text-ink-2">入站请求数据</h4>
          <Button size="sm" variant="ghost" leadingIcon={<Copy aria-hidden className="size-3" />} onClick={() => void copy(request, "已复制请求 JSON")}>复制</Button>
        </div>
        <pre className="max-h-64 overflow-auto rounded-control border border-rule bg-sunken p-2.5 font-mono text-[11px] leading-5 text-ink-2">{request}</pre>
      </section>
      <section className="min-w-0">
        <div className="mb-1 flex items-center justify-between gap-2">
          <h4 className="text-meta font-medium text-ink-2">原始响应数据</h4>
          <Button size="sm" variant="ghost" leadingIcon={<Copy aria-hidden className="size-3" />} onClick={() => void copy(response, "已复制响应 JSON")}>复制</Button>
        </div>
        <pre className="max-h-64 overflow-auto rounded-control border border-rule bg-sunken p-2.5 font-mono text-[11px] leading-5 text-ink-2">{response}</pre>
      </section>
    </div>
  );
}
