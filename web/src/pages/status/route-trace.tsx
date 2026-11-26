import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Panel } from "@/components/ui/Panel";
import { Sheet } from "@/components/ui/Sheet";
import { api, ApiError } from "@/lib/api";
import { formatDateTime, formatDuration } from "@/lib/format";
import type { RouteTraceEvent, RouteTraceResponse, RouteTraceTarget } from "@/lib/types";

const labels: Record<string, string> = {
  resolve: "模型解析", candidate: "候选筛选", selection: "目标选取", probe: "原生端点探测",
  attempt: "上游调用", fallback: "回退",
  request: "入站请求", primary: "主目标", prepare: "前置检查",
  direct: "直接调用", alias: "别名", task: "任务", unified: "统一模型",
  full: "完整权限", local: "本地鉴权", access_key: "访问密钥", workspace_inference: "工作空间推理密钥",
  not_found: "未找到", model_not_allowed: "模型不在允许范围内",
  missing_model: "未指定模型", malformed_input: "请求格式错误", local_rejection: "本地拒绝",
  disabled: "未启用", provider_not_allowed: "供应商不在允许范围内", quota_frozen: "额度冻结",
  excluded_after_attempt: "本次尝试后排除", cooldown: "冷却中", balance_limited: "余额不足",
  concurrency_limited: "达到并发上限", eligible_not_selected: "可用但未选中", selected: "已选中",
  specified: "指定 Key", only_first: "只选首项", priority: "按优先级", round_robin: "轮询", sticky: "粘滞选取",
  lowest_active_round_robin: "在最低并发候选中轮询", cooldown_soft_fallback: "冷却软回退（允许选取冷却中的候选）",
  supported: "支持", unsupported: "不支持", error: "探测失败", not_sent: "未发送",
  response: "已收到响应", retryable_http: "HTTP 状态允许重试", transport_error: "传输失败",
  first_byte_timeout: "首字节超时", client_canceled: "客户端取消", budget_exhausted: "上游调用预算已用尽",
  unavailable: "不可用", no_available_key: "没有可用 Key", retryable: "允许重试",
  endpoint_not_supported: "端点不支持", request_build_failed: "上游请求构建失败", connection_error: "连接失败", sent: "已发送",
  native_to_compatible: "原生协议降级到兼容协议", model_fallback: "切换备选模型",
  complete: "流正常结束", upstream_error: "流中出现上游错误", incomplete: "流未完整结束", not_stream: "非流式",
  running: "进行中", completed: "已完成", success: "成功", failed: "失败", canceled: "已取消",
};

const unknown = "未知";
const describe = (value: string | null | undefined) => value && Object.hasOwn(labels, value) ? labels[value] : unknown;
function describeStage(stage: string) {
  const [phase, step] = stage.split("/");
  const phaseLabel = phase === "fallback" ? "备选目标" : describe(phase);
  if (!step) return phaseLabel;
  const stepLabel = step === "preflight" ? "前置检查" : step === "regular" ? "常规选路" : describe(step);
  return `${phaseLabel} / ${stepLabel}`;
}

const httpStatus = (value: number | null | undefined) => value == null ? unknown : `HTTP ${value}`;
const duration = (value: number | null | undefined) => value == null ? unknown : formatDuration(value);

function targetText(target: RouteTraceTarget | null | undefined) {
  if (!target) return "未知或未发送";
  const fields = [
    target.model_id && `模型：${target.model_id}`,
    target.provider_id && `供应商：${target.provider_id}`,
    target.key_name && `Key 显示名：${target.key_name}`,
    target.upstream_model_id && `上游模型：${target.upstream_model_id}`,
  ];
  return fields.filter(Boolean).join(" · ") || unknown;
}

function Timeline({ events, emptyText }: { events: RouteTraceEvent[]; emptyText: string }) {
  if (!events.length) return <p className="text-meta text-ink-3">{emptyText}</p>;
  return (
    <ol className="space-y-3 border-l border-rule pl-3">
      {events.map((event) => {
        const headline = event.kind === "attempt"
          ? event.sent === false || event.result === "not_sent" ? "上游调用未发送"
            : event.attempt_no != null ? `第 ${event.attempt_no} 次上游发送` : "上游调用"
          : event.kind === "fallback" ? "回退" : describe(event.kind);
        const details = [
          event.stage && describeStage(event.stage),
          event.mode && describe(event.mode),
          event.reason && describe(event.reason),
          event.result && event.result !== event.reason && describe(event.result),
          event.protocol && `协议：${event.protocol}`,
          event.upstream_status != null && `上游 ${httpStatus(event.upstream_status)}`,
          event.duration_ms != null && `耗时 ${duration(event.duration_ms)}`,
        ].filter(Boolean);
        return (
          <li key={event.seq} className="space-y-1">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="font-mono text-meta tabular-nums text-ink-3">#{event.seq} · +{event.offset_ms} ms</span>
              <strong className="text-body font-medium text-ink">{headline}</strong>
              {details.length ? <span className="break-words text-meta text-ink-2">{details.join(" · ")}</span> : null}
            </div>
            {event.target ? <p className="break-words text-meta text-ink-3">{targetText(event.target)}</p> : null}
            {event.from || event.to ? (
              <p className="break-words text-meta text-ink-3">从 {targetText(event.from)} 切换到 {targetText(event.to)}</p>
            ) : null}
          </li>
        );
      })}
    </ol>
  );
}

function TraceDetails({ trace }: { trace: RouteTraceResponse }) {
  const running = trace.status === "running";
  const plan = trace.events.filter((event) => ["resolve", "candidate", "selection"].includes(event.kind));
  const execution = trace.events.filter((event) => ["attempt", "probe", "fallback"].includes(event.kind));
  const { request, final } = trace;
  return (
    <div className="space-y-5">
      {running ? <Banner tone="info" title="追踪进行中">目前只显示已观察到的事件。请求结束后可刷新查看最终结果。</Banner> : null}
      {trace.truncated ? <Banner tone="warn" title="追踪已截断">达到记录上限，省略了 {trace.omitted_events} 个事件。时间线不代表完整经过。</Banner> : null}
      <section className="space-y-2">
        <h3 className="text-body font-semibold text-ink">请求概览</h3>
        <div className="flex flex-wrap gap-2">
          <Badge tone={running ? "warn" : "neutral"}>{describe(trace.status)}</Badge>
          {trace.outcome ? <Badge tone={trace.outcome === "success" ? "good" : trace.outcome === "failed" ? "critical" : "neutral"}>{describe(trace.outcome)}</Badge> : null}
        </div>
        <dl className="grid gap-x-5 gap-y-1 break-words text-meta text-ink-2 sm:grid-cols-2">
          <div><dt className="inline text-ink-3">请求模型：</dt><dd className="inline">{request.model ?? unknown}</dd></div>
          <div><dt className="inline text-ink-3">路由类型：</dt><dd className="inline">{describe(request.route_kind)}</dd></div>
          <div><dt className="inline text-ink-3">凭据类型：</dt><dd className="inline">{describe(request.caller_type)}</dd></div>
          <div><dt className="inline text-ink-3">工作空间：</dt><dd className="inline">{request.workspace ?? unknown}</dd></div>
          <div><dt className="inline text-ink-3">请求类型：</dt><dd className="inline">{request.stream == null ? unknown : request.stream ? "流式" : "非流式"}</dd></div>
          <div><dt className="inline text-ink-3">开始时间：</dt><dd className="inline">{formatDateTime(trace.started_at)}</dd></div>
          <div><dt className="inline text-ink-3">完成时间：</dt><dd className="inline">{trace.finished_at ? formatDateTime(trace.finished_at) : unknown}</dd></div>
          <div><dt className="inline text-ink-3">请求耗时：</dt><dd className="inline">{duration(trace.duration_ms)}</dd></div>
        </dl>
      </section>
      <section className="space-y-2">
        <h3 className="text-body font-semibold text-ink">路由计划与候选筛选</h3>
        <Timeline events={plan} emptyText="未观察到解析、筛选或选取事件。" />
      </section>
      <section className="space-y-2">
        <h3 className="text-body font-semibold text-ink">上游发送时间线</h3>
        <Timeline events={execution} emptyText="未观察到上游发送、探测或回退事件。" />
      </section>
      <section className="space-y-2 border-t border-rule pt-4">
        <h3 className="text-body font-semibold text-ink">最终 HTTP 与流状态</h3>
        <dl className="space-y-1 break-words text-meta text-ink-2">
          <div><dt className="inline text-ink-3">下游已提交状态：</dt><dd className="inline">{httpStatus(final.http_status)}</dd></div>
          <div><dt className="inline text-ink-3">最终上游状态：</dt><dd className="inline">{httpStatus(final.upstream_status)}</dd></div>
          <div><dt className="inline text-ink-3">最终上游目标：</dt><dd className="inline">{targetText(final.target)}</dd></div>
          <div><dt className="inline text-ink-3">流终态：</dt><dd className="inline">{describe(final.stream_end)}</dd></div>
        </dl>
      </section>
    </div>
  );
}

function RouteTraceDrawer({ requestId, onClose }: { requestId: string; onClose: () => void }) {
  const query = useQuery({
    queryKey: ["route-trace", requestId],
    queryFn: () => api.routeTrace(requestId),
    retry: false, gcTime: 0, refetchOnWindowFocus: false,
  });
  const status = query.error instanceof ApiError ? query.error.status : null;
  return (
    <Sheet
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title="单请求选路解释"
      subtitle={<span className="break-all font-mono">{requestId}</span>}
      width="lg"
      footer={<Button onClick={() => void query.refetch()} loading={query.isFetching}>刷新追踪</Button>}
    >
      {query.isPending ? <p role="status" className="text-body text-ink-3">正在读取追踪…</p> : null}
      {query.isError ? (
        status === 404 ? <Empty title="记录不存在、已过期、未保存或无权访问" />
          : <Banner tone="warn" title={status === 503 ? "追踪暂不可用" : status === 422 ? "请求 ID 格式无效" : "追踪读取失败"}>请检查请求 ID，或稍后刷新重试。</Banner>
      ) : null}
      {!query.isError && query.data ? (
        Array.isArray(query.data.events) ? <TraceDetails trace={query.data} /> : <Empty title="无权查看完整选路解释" />
      ) : null}
    </Sheet>
  );
}

export function RouteTraceLookup() {
  const [draftId, setDraftId] = useState("");
  const [requestId, setRequestId] = useState<string | null>(null);
  const [invalid, setInvalid] = useState(false);
  return (
    <Panel title="按请求 ID 查看选路" description="从代理响应头 X-AMKR-Request-ID 复制 ID。记录最多保留设置的时长（上限 168 小时），可能提前淘汰或未保存。">
      <form className="flex max-w-xl flex-col gap-2 sm:flex-row" noValidate onSubmit={(event) => {
        event.preventDefault();
        const id = draftId.trim();
        if (!/^rt_[0-9a-f]{32}$/.test(id)) {
          setInvalid(true);
          return;
        }
        setRequestId(id);
      }}>
        <Field label="请求 ID" error={invalid ? "请求 ID 须以 rt_ 开头，后接 32 位小写十六进制字符。" : undefined}>
          <Input value={draftId} placeholder="rt_…" autoComplete="off" spellCheck={false} onChange={(event) => {
            setDraftId(event.target.value);
            setInvalid(false);
          }} />
        </Field>
        <Button className="self-start sm:mt-5" type="submit" variant="primary">查询</Button>
      </form>
      {requestId ? <RouteTraceDrawer requestId={requestId} onClose={() => setRequestId(null)} /> : null}
    </Panel>
  );
}
