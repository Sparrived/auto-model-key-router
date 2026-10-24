// 服务日志：把“最近发生了什么”做成可检索的排查工具，而不是用量看板的附属。
//
// 服务端只回最近 64 KiB；前端最多渲染最后 2000 行，避免超长日志在筛选时
// 每次重新创建几千个 DOM 节点。轮询由 React Query 的 refetchInterval 负责，
// 组件卸载时会自然停止，不在模块级保存定时器。

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Copy, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText, formatClockSeconds } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import { cn } from "@/lib/cn";
import type { LogsResponse } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Empty } from "@/components/ui/Empty";
import { Input } from "@/components/ui/Input";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Skeleton } from "@/components/ui/Skeleton";
import { Switch } from "@/components/ui/Switch";
import { copyText } from "@/lib/format";

const INTERVALS = [
  { value: "2000", label: "2 秒" },
  { value: "5000", label: "5 秒" },
  { value: "15000", label: "15 秒" },
  { value: "0", label: "暂停" },
] as const;

const LEVELS = [
  { value: "all", label: "全部" },
  { value: "warn", label: "警告以上" },
  { value: "error", label: "仅错误" },
] as const;

type Level = "error" | "warning" | "info" | "debug" | "none";

interface LineLevel {
  level: Level;
  label: string;
  tone: string;
}

function levelOf(line: string): LineLevel {
  const match = /\b(trace|debug|info|warn(?:ing)?|error|fatal|critical)\b/i.exec(line);
  if (!match) return { level: "none", label: "—", tone: "border-rule-strong text-ink-3" };
  const word = match[1].toLowerCase();
  if (/error|fatal|critical/.test(word)) return { level: "error", label: "错误", tone: "border-critical text-critical-ink" };
  if (word.startsWith("warn")) return { level: "warning", label: "警告", tone: "border-warn text-warn-ink" };
  if (word === "info") return { level: "info", label: "信息", tone: "border-rule-strong text-ink-2" };
  return { level: "debug", label: "调试", tone: "border-rule-strong text-ink-3" };
}

function highlight(line: string, query: string) {
  const keyword = query.trim();
  if (!keyword) return line;
  const lower = line.toLowerCase();
  const needle = keyword.toLowerCase();
  const parts: Array<string | { match: string; at: number }> = [];
  let cursor = 0;
  let at = lower.indexOf(needle);
  while (at >= 0) {
    if (at > cursor) parts.push(line.slice(cursor, at));
    parts.push({ match: line.slice(at, at + needle.length), at });
    cursor = at + needle.length;
    at = lower.indexOf(needle, cursor);
  }
  if (cursor < line.length) parts.push(line.slice(cursor));
  return parts.map((part, index) => typeof part === "string"
    ? part
    : <mark key={`${part.at}-${index}`} className="rounded-[2px] bg-warn-bg px-0.5 text-warn-ink">{part.match}</mark>);
}

export default function LogsPage() {
  const [interval, setInterval] = useState(2000);
  const [level, setLevel] = useState<(typeof LEVELS)[number]["value"]>("all");
  const [queryText, setQueryText] = useState("");
  const [follow, setFollow] = useState(true);
  const [wrap, setWrap] = useState(true);
  const scrollRef = useRef<HTMLDivElement>(null);

  const logsQuery = useQuery({
    queryKey: queryKeys.logs(),
    queryFn: api.logs,
    // 0 表示暂停；交给 React Query 管理，离开页面自动清理。
    refetchInterval: interval || false,
    refetchIntervalInBackground: false,
  });

  const data: LogsResponse | undefined = logsQuery.data;
  // 新接口回 text；lines 是旧版/测试替身的兼容形态，两者都接受。
  const text = typeof data?.text === "string"
    ? data.text
    : (data?.lines ?? []).join("\n");
  const allLines = useMemo(() => text.split(/\r?\n/).filter((line) => line.trim() !== ""), [text]);
  const visible = useMemo(() => {
    const keyword = queryText.trim().toLowerCase();
    return allLines.filter((line) => {
      const lineLevel = levelOf(line).level;
      if (level === "error" && lineLevel !== "error") return false;
      if (level === "warn" && lineLevel !== "error" && lineLevel !== "warning") return false;
      return !keyword || line.toLowerCase().includes(keyword);
    });
  }, [allLines, level, queryText]);

  // 只渲染最后 2000 行：日志是排查用的，用户要看的是最近发生了什么；保留脚注
  // 说明被截断，避免“明明搜到却看不见”的错觉。
  const rendered = visible.slice(-2000);
  const hidden = visible.length - rendered.length;

  useEffect(() => {
    if (!follow || !scrollRef.current) return;
    // 等 DOM 完成这一轮提交后再贴底。
    const frame = requestAnimationFrame(() => {
      if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [follow, text, level, queryText, wrap, rendered.length]);

  function onScroll() {
    const node = scrollRef.current;
    if (!node) return;
    const atBottom = node.scrollHeight - node.scrollTop - node.clientHeight <= 8;
    if (atBottom && !follow) setFollow(true);
    if (!atBottom && follow) setFollow(false);
  }

  function jumpToLatest() {
    setFollow(true);
    requestAnimationFrame(() => {
      if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    });
  }

  async function copyLine(line: string) {
    try {
      await copyText(line);
      toast.success("日志行已复制");
    } catch {
      toast.error("复制失败");
    }
  }

  const readError = typeof data?.error === "string" && data.error ? data.error : "";
  const queryError = logsQuery.error ? errorText(logsQuery.error) : "";
  const at = logsQuery.dataUpdatedAt ? new Date(logsQuery.dataUpdatedAt) : null;

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title="服务日志"
        description="服务日志：最近 64 KiB 运行日志，用于排查请求失败与上游异常。"
        actions={(
          <>
            {at ? <span className="hidden text-meta text-ink-3 sm:inline">拉取于 {formatClockSeconds(at)}</span> : null}
            <Button
              size="sm"
              variant="secondary"
              leadingIcon={<RefreshCw aria-hidden className="size-3.5" />}
              loading={logsQuery.isFetching}
              onClick={() => void logsQuery.refetch()}
            >
              立即刷新
            </Button>
          </>
        )}
      />

      {queryError ? <Banner tone="critical" title="无法读取服务日志">{queryError}</Banner> : null}
      {readError ? <Banner tone="critical" title="日志不可用">{readError}</Banner> : null}

      <Panel
        className="log-panel max-sm:[&>header>div]:flex-col max-sm:[&>header>div]:items-stretch max-sm:[&>header>div>div:last-child]:w-full max-sm:[&>header>div>div:last-child]:flex-wrap"
        title="日志"
        description="级别由每行中的单词识别；搜索与级别筛选只影响显示，不改变服务端读取的尾部范围。"
        padding={false}
        actions={(
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={interval ? "neutral" : "warn"}>{interval ? `${interval / 1000} 秒刷新` : "已暂停"}</Badge>
            {data?.truncated ? <Badge tone="warn">已截断</Badge> : null}
            <Segmented
              aria-label="选择刷新间隔"
              value={String(interval)}
              onValueChange={(value) => setInterval(Number(value))}
              options={INTERVALS.map((item) => ({ value: item.value, label: item.label }))}
            />
          </div>
        )}
        toolbar={(
          <div className="flex flex-wrap items-center gap-2">
            <Segmented
              aria-label="按级别过滤日志"
              value={level}
              onValueChange={(value) => setLevel(value as typeof level)}
              options={LEVELS.map((item) => ({ value: item.value, label: item.label }))}
            />
            <Input
              type="search"
              size="sm"
              className="w-[220px]"
              placeholder="搜索关键字…"
              aria-label="在日志中搜索"
              value={queryText}
              onChange={(event) => setQueryText(event.target.value)}
            />
            <span className="text-meta text-ink-3">显示 {rendered.length} / {allLines.length} 行（共 {allLines.length} 行）</span>
            <div className="ml-auto flex flex-wrap items-center gap-3">
              <Switch checked={wrap} onCheckedChange={setWrap} label="自动换行" />
              <Switch checked={follow} onCheckedChange={setFollow} label="跟随最新" />
            </div>
          </div>
        )}
      >
        {logsQuery.isPending && !data ? (
          <div className="p-4"><Skeleton variant="chart" className="h-80" /></div>
        ) : !text ? (
          <Empty
            title={readError ? "日志不可用" : "日志为空"}
            description={readError ? "请检查日志文件权限或路径。" : "服务暂未写入日志。"}
          />
        ) : visible.length === 0 ? (
          <Empty title="没有匹配的日志行" description="试试放宽级别或关键字。" />
        ) : (
          <div className="relative">
            <div
              ref={scrollRef}
              onScroll={onScroll}
              className={cn(
                "max-h-[min(62vh,680px)] overflow-auto bg-panel font-mono text-[12px] leading-[1.45]",
                wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre",
              )}
              tabIndex={0}
              role="log"
              aria-label="服务日志内容"
            >
              {rendered.map((line, index) => {
                const info = levelOf(line);
                return (
                  <div
                    key={`${index}-${line.slice(0, 24)}`}
                    className={cn("group relative flex min-w-0 items-start gap-2 border-l-2 px-3 pr-9 hover:bg-hover", info.tone)}
                  >
                    <span className="w-8 shrink-0 select-none text-right text-[10px] leading-[1.45] opacity-70">{info.label}</span>
                    <code className="min-w-0 flex-1 text-ink-2">{highlight(line, queryText)}</code>
                    <IconButton
                      aria-label="复制日志行"
                      tooltip="复制这一行"
                      size="sm"
                      className="absolute right-1.5 top-0 h-4 w-4 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                      onClick={() => void copyLine(line)}
                    >
                      <Copy aria-hidden className="size-3.5" />
                    </IconButton>
                  </div>
                );
              })}
            </div>
            {hidden > 0 ? (
              <div className="border-t border-rule px-3 py-2 text-meta text-ink-3">
                仅显示最近 2000 行，另有 {hidden} 行未渲染；缩小筛选范围可查看更早的匹配。
              </div>
            ) : null}
            {!follow ? (
              <div className="pointer-events-none sticky bottom-3 flex justify-center">
                <Button size="sm" variant="secondary" className="pointer-events-auto shadow-[var(--shadow-float)]" onClick={jumpToLatest}>
                  跳到最新
                </Button>
              </div>
            ) : null}
          </div>
        )}
      </Panel>
    </div>
  );
}
