// 调用示例：随表单实时更新，直接能复制去用。
//
// 鉴权写成 `<你的推理 key>` 占位——界面里没有任何一把真实推理 key 的明文（目录接口刻意
// 不返回它），这里填别的值只会让人以为那是能直接跑的密钥。非默认空间才带
// X-AMKR-Workspace：默认空间就是「不带这个头」命中的那个。

import { useState } from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import { apiBase } from "@/lib/api";
import { copyText } from "@/lib/format";
import { Button } from "@/components/ui/Button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/Tabs";
import type { TaskDraft } from "./draft";

export interface CallExampleProps {
  draft: TaskDraft;
  workspace: string;
  /** health.base_url；缺省时退回当前站点前缀。 */
  baseUrl?: string;
}

const PLACEHOLDER_KEY = "<你的推理 key>";

function endpoint(baseUrl: string): string {
  const base = (baseUrl || `${location.origin}${apiBase()}`).replace(/\/+$/, "");
  return `${base}/v1/chat/completions`;
}

function pythonBase(baseUrl: string): string {
  const base = (baseUrl || `${location.origin}${apiBase()}`).replace(/\/+$/, "");
  return `${base}/v1`;
}

export function CallExample({ draft, workspace, baseUrl }: CallExampleProps) {
  const [tab, setTab] = useState("curl");
  const taskName = draft.name.trim() || "TASK_XXXXXX";
  const nonDefault = workspace !== "default";
  const url = endpoint(baseUrl || "");

  const body = JSON.stringify({
    model: taskName,
    messages: [{ role: "user", content: "你好" }],
  });

  const curlLines = [
    `curl ${url} \\`,
    `  -H "Authorization: Bearer ${PLACEHOLDER_KEY}" \\`,
    `  -H "Content-Type: application/json" \\`,
  ];
  if (nonDefault) curlLines.push(`  -H "X-AMKR-Workspace: ${workspace}" \\`);
  curlLines.push(`  -d '${body}'`);
  const curl = curlLines.join("\n");

  const pythonLines = [
    "from openai import OpenAI",
    "",
    "client = OpenAI(",
    `    base_url="${pythonBase(baseUrl || "")}",`,
    `    api_key="${PLACEHOLDER_KEY}",`,
  ];
  if (nonDefault) {
    pythonLines.push(`    default_headers={"X-AMKR-Workspace": "${workspace}"},`);
  }
  pythonLines.push(
    ")",
    "",
    "resp = client.chat.completions.create(",
    `    model="${taskName}",`,
    '    messages=[{"role": "user", "content": "你好"}],',
    ")",
    "print(resp.choices[0].message.content)",
  );
  const python = pythonLines.join("\n");

  async function copy(value: string) {
    try {
      await copyText(value);
      toast.success("调用示例已复制。");
    } catch {
      toast.error("复制失败");
    }
  }

  return (
    <section className="rounded-panel border border-rule bg-panel">
      <div className="flex items-start justify-between gap-2 px-4 pt-4">
        <div>
          <h3 className="text-[16px] font-semibold text-ink">调用示例</h3>
          <p className="mt-1 text-meta text-ink-3">把任务名当作 model 传即可；示例随表单实时更新。</p>
        </div>
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList aria-label="调用示例语言">
            <TabsTrigger value="curl">curl</TabsTrigger>
            <TabsTrigger value="python">OpenAI Python SDK</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <Tabs value={tab} onValueChange={setTab}>
        <TabsContent value="curl" className="px-4 pb-4 pt-3">
          <div className="relative">
            <pre className="max-h-72 overflow-auto rounded-control bg-sunken p-3 pr-10 font-mono text-meta leading-5 text-ink-2">
              <code>{curl}</code>
            </pre>
            <Button
              variant="ghost"
              size="sm"
              className="absolute right-1 top-1"
              aria-label="复制 curl 示例"
              onClick={() => void copy(curl)}
            >
              <Copy aria-hidden className="size-3.5" />
            </Button>
          </div>
        </TabsContent>
        <TabsContent value="python" className="px-4 pb-4 pt-3">
          <div className="relative">
            <pre className="max-h-72 overflow-auto rounded-control bg-sunken p-3 pr-10 font-mono text-meta leading-5 text-ink-2">
              <code>{python}</code>
            </pre>
            <Button
              variant="ghost"
              size="sm"
              className="absolute right-1 top-1"
              aria-label="复制 Python 示例"
              onClick={() => void copy(python)}
            >
              <Copy aria-hidden className="size-3.5" />
            </Button>
          </div>
        </TabsContent>
      </Tabs>
      <p className="border-t border-rule px-4 py-2.5 text-meta text-ink-3">
        任务固定了的采样参数，调用方再传同名参数（含 reasoning_effort）会被直接拒绝；留空的参数照常透传。
      </p>
    </section>
  );
}
