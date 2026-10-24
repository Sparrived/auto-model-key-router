import { useEffect, useRef, useState } from "react";
import { Info, Radar } from "lucide-react";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { Provider, ProbeResponse } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Panel } from "@/components/ui/Panel";

interface ProbeResult {
  key_name?: string;
  endpoint?: string;
  models?: string[];
  latency_ms?: number | null;
  error?: string | null;
  [key: string]: unknown;
}

const STATUS_LABELS: Record<string, string> = {
  pending: "排队中",
  running: "正在探测",
  complete: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

// sanitizeEndpoint 只保留 origin + pathname：探测结果里的 endpoint 可能带查询串，
// 那张表里读不出比路径更多的东西，而整串 URL 会把列撑得很宽。
function sanitizeEndpoint(endpoint: unknown): string {
  if (!endpoint) return "端点地址不可用";
  try {
    const url = new URL(String(endpoint));
    return `${url.origin}${url.pathname}`;
  } catch {
    return "端点地址不可用";
  }
}

const TERMINAL = new Set(["complete", "failed", "cancelled"]);

export function ProbePanel({ provider }: { provider: Provider }) {
  const [timeout, setTimeoutText] = useState("15");
  const [probe, setProbe] = useState<ProbeResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);
  const timer = useRef<number | null>(null);

  useEffect(() => () => {
    // 离开面板即作废这一代探测：迟到的轮询结果不能再写进已卸载的组件。
    generation.current += 1;
    if (timer.current !== null) window.clearTimeout(timer.current);
  }, []);

  async function poll(probeId: string, currentGeneration: number) {
    if (generation.current !== currentGeneration) return;
    try {
      const data = await api.getProbe(probeId);
      if (generation.current !== currentGeneration) return;
      setProbe((previous) => ({ ...previous, ...data }));
      if (data.status && TERMINAL.has(data.status)) {
        setBusy(false);
        return;
      }
      timer.current = window.setTimeout(() => void poll(probeId, currentGeneration), 750);
    } catch (failure) {
      // 轮询失败即停止，但不自动取消服务端探测。
      setError(errorText(failure));
      setBusy(false);
    }
  }

  async function start() {
    const seconds = Number(timeout);
    if (!Number.isFinite(seconds) || seconds <= 0 || seconds > 120) {
      setError("探测超时必须在 0 到 120 秒之间。");
      return;
    }
    if (timer.current !== null) window.clearTimeout(timer.current);
    generation.current += 1;
    const currentGeneration = generation.current;
    setBusy(true);
    setError(null);
    setProbe({ probe_id: undefined, status: "pending", results: [] });
    try {
      const created = await api.startProbe(provider.id, [], seconds);
      if (generation.current !== currentGeneration) {
        if (created.probe_id) void api.cancelProbe(created.probe_id).catch(() => undefined);
        return;
      }
      setProbe((previous) => ({ ...previous, ...created }));
      if (created.probe_id) void poll(created.probe_id, currentGeneration);
    } catch (failure) {
      setBusy(false);
      setError(errorText(failure));
    }
  }

  async function cancel() {
    if (!probe?.probe_id) return;
    try {
      const data = await api.cancelProbe(probe.probe_id);
      setProbe((previous) => ({ ...previous, ...data }));
      if (data.status && TERMINAL.has(data.status)) {
        setBusy(false);
        if (timer.current !== null) window.clearTimeout(timer.current);
      }
    } catch (failure) {
      setError(errorText(failure));
    }
  }

  const results = (probe?.results || []) as ProbeResult[];
  const status = probe?.status || "";
  const statusTone = status === "complete" ? "good" : status === "failed" ? "critical" : status === "cancelled" ? "warn" : "neutral";

  return (
    <Panel
      title="批量探测"
      description="一次探测全部 Key 的模型列表与各端点可用性；结果只做展示，不会写回 Key 的能力缓存。"
      actions={probe ? <Badge tone={statusTone} dot>{STATUS_LABELS[status] || status}</Badge> : <Badge tone="neutral">未运行</Badge>}
    >
      <div className="flex flex-wrap items-end gap-3">
        <Field label="探测超时（秒）" className="w-32">
          <Input
            type="number"
            min="0.1"
            max="120"
            step="0.1"
            value={timeout}
            onChange={(event) => setTimeoutText(event.target.value)}
          />
        </Field>
        <Button variant="secondary" leadingIcon={<Radar aria-hidden className="size-4" />} loading={busy} onClick={() => void start()}>
          探测全部 Key
        </Button>
        {busy && probe?.probe_id ? (
          <Button variant="danger-ghost" onClick={() => void cancel()}>取消探测</Button>
        ) : null}
        {busy ? <span className="text-meta text-ink-3">已完成 {results.length} / {provider.keys.length} 个 Key</span> : null}
      </div>

      {error ? <Banner tone="critical" className="mt-3">探测失败: {error}</Banner> : null}

      {results.length ? (
        <div className="mt-4 overflow-x-auto">
          <table className="w-full min-w-[720px] border-collapse text-dense">
            <thead>
              <tr className="border-b border-rule text-left text-meta text-ink-3">
                <th className="px-2 py-2 font-medium">Key</th>
                <th className="px-2 py-2 font-medium">端点</th>
                <th className="px-2 py-2 font-medium">模型</th>
                <th className="px-2 py-2 font-medium">延迟</th>
                <th className="px-2 py-2 font-medium">错误</th>
              </tr>
            </thead>
            <tbody>
              {results.map((row, index) => (
                <tr key={`${row.key_name || "key"}-${index}`} className="border-b border-rule last:border-b-0">
                  <td className="px-2 py-2 font-mono text-ink">{row.key_name || "-"}</td>
                  <td className="px-2 py-2 font-mono text-meta text-ink-2">{sanitizeEndpoint(row.endpoint)}</td>
                  <td className="px-2 py-2 text-ink-2">{(row.models || []).join(", ") || "未发现模型"}</td>
                  <td className="px-2 py-2 tabular-nums text-ink-2">
                    {row.latency_ms === null || row.latency_ms === undefined ? "延迟未知" : `${row.latency_ms} ms`}
                  </td>
                  <td className="px-2 py-2 text-critical-ink">{row.error || "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      <p className="mt-3 flex items-start gap-1.5 text-meta text-ink-3">
        <Info aria-hidden className="mt-0.5 size-3.5 shrink-0" />
        探测全部 Key 会串行访问每个 Key 的上游；超时可随时取消，已经发出的请求不会回滚。
      </p>
    </Panel>
  );
}
