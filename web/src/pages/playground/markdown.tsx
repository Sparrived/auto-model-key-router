import { useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { copyText } from "@/lib/format";

function inline(text: string): ReactNode[] {
  const parts = text.split(/(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*)/g);
  return parts.filter(Boolean).map((part, index) => {
    if (part.startsWith("`") && part.endsWith("`")) {
      return <code key={index} className="rounded-[3px] bg-sunken px-1 py-0.5 font-mono text-[0.92em] text-ink">{part.slice(1, -1)}</code>;
    }
    if (part.startsWith("**") && part.endsWith("**")) {
      return <strong key={index}>{part.slice(2, -2)}</strong>;
    }
    if (part.startsWith("*") && part.endsWith("*")) {
      return <em key={index}>{part.slice(1, -1)}</em>;
    }
    return <span key={index}>{part}</span>;
  });
}

function CodeBlock({ code, lang }: { code: string; lang: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await copyText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
      toast.success("代码已复制");
    } catch {
      toast.error("复制失败");
    }
  }
  return (
    <div className="my-2 overflow-hidden rounded-control border border-rule bg-sunken">
      <div className="flex items-center justify-between border-b border-rule px-2.5 py-1.5">
        <span className="font-mono text-[11px] text-ink-3">{lang || "plaintext"}</span>
        <button type="button" className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-ink-2 hover:bg-hover hover:text-ink" onClick={() => void copy()}>
          {copied ? <Check aria-hidden className="size-3" /> : <Copy aria-hidden className="size-3" />}
          {copied ? "已复制" : "复制"}
        </button>
      </div>
      <pre className="max-w-full overflow-x-auto p-3 text-[12px] leading-5 text-ink"><code>{code}</code></pre>
    </div>
  );
}

function flushText(lines: string[], key: number): ReactNode {
  if (!lines.length) return null;
  const blocks = lines.join("\n").split(/\n\s*\n/);
  return (
    <div key={key} className="space-y-2">
      {blocks.map((block, blockIndex) => {
        const text = block.trim();
        if (!text) return null;
        if (text.startsWith("### ")) return <h4 key={blockIndex} className="mt-2 font-semibold text-ink">{inline(text.slice(4))}</h4>;
        if (text.startsWith("## ")) return <h3 key={blockIndex} className="mt-2 font-semibold text-ink">{inline(text.slice(3))}</h3>;
        if (text.startsWith("# ")) return <h2 key={blockIndex} className="mt-2 font-semibold text-ink">{inline(text.slice(2))}</h2>;
        if (text.startsWith("> ")) {
          return <blockquote key={blockIndex} className="border-l-2 border-rule-strong pl-3 text-ink-2">{inline(text.split("\n").map((line) => line.replace(/^>\s?/, "")).join("\n"))}</blockquote>;
        }
        if (/^\s*[-*]\s/.test(text)) {
          return <ul key={blockIndex} className="list-disc space-y-0.5 pl-5">{text.split("\n").map((line, index) => <li key={index}>{inline(line.replace(/^\s*[-*]\s/, ""))}</li>)}</ul>;
        }
        if (/^\s*\d+\.\s/.test(text)) {
          return <ol key={blockIndex} className="list-decimal space-y-0.5 pl-5">{text.split("\n").map((line, index) => <li key={index}>{inline(line.replace(/^\s*\d+\.\s/, ""))}</li>)}</ol>;
        }
        return <p key={blockIndex} className="whitespace-pre-wrap break-words">{inline(text)}</p>;
      })}
    </div>
  );
}

/** 轻量、安全的 Markdown：只生成 React 节点，不使用 dangerouslySetInnerHTML。 */
export function Markdown({ text }: { text: string }) {
  if (!text) return <span className="text-ink-3">(空回复)</span>;
  const output: ReactNode[] = [];
  let textLines: string[] = [];
  let codeLines: string[] = [];
  let codeLang = "";
  let inCode = false;
  const lines = text.split("\n");
  let keySeed = 0;

  function pushText() {
    const block = flushText(textLines, keySeed++);
    if (block) output.push(block);
    textLines = [];
  }

  for (const line of lines) {
    if (line.trim().startsWith("```")) {
      if (!inCode) {
        pushText();
        inCode = true;
        codeLang = line.trim().slice(3).trim();
        codeLines = [];
      } else {
        output.push(<CodeBlock key={keySeed++} code={codeLines.join("\n")} lang={codeLang} />);
        inCode = false;
        codeLines = [];
      }
      continue;
    }
    if (inCode) codeLines.push(line);
    else textLines.push(line);
  }
  if (inCode && codeLines.length) output.push(<CodeBlock key={keySeed++} code={codeLines.join("\n")} lang={codeLang} />);
  pushText();
  return <div className="min-w-0 space-y-2 break-words text-body leading-6 text-ink-2">{output}</div>;
}
