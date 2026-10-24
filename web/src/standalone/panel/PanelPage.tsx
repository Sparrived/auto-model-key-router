import { useEffect, useMemo, useRef, useState } from "react";
import { Copy, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  ALL_HISTORY,
  USAGE_RANGES,
  formatCompactNumber,
  formatPercentValue,
  rangeSpec,
} from "@/lib/chart-math";
import { copyText, errorText, formatCompact, formatCount, formatDateTime, formatDuration } from "@/lib/format";
import type { MetricStats, WorkspaceUsageLink } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Skeleton } from "@/components/ui/Skeleton";
import { FlowMap } from "@/components/charts/FlowMap";
import { RankBars } from "@/components/charts/RankBars";
import {
  PanelApiError,
  createPanelApi,
  panelApiBase,
  panelCredential,
  type PanelCredential,
  type PanelTask,
  type PanelTaskPayload,
  type PanelUsage,
} from "./api";

const LAYER_LABELS: Record<string, string> = {
  workspace: "工作空间",
  requested_model_id: "任务/别名",
  model_id: "模型",
  provider_id: "供应商",
  key_name: "上游 Key",
  upstream_model_id: "上游模型",
};

type Metric = "requests" | "tokens";

interface Editing {
  task?: PanelTask;
  new?: boolean;
}

function statsOf(usage: PanelUsage | null): MetricStats | null {
  return usage?.workspaces?.[0]?.stats || (usage as { stats?: MetricStats } | null)?.stats || null;
}

function layerNames(usage: PanelUsage | null): string[] {
  return (usage?.layers || []).map(String);
}

function rangeLabel(selection: string | number, usage: PanelUsage | null): string {
  if (selection !== ALL_HISTORY) return rangeSpec(selection).label;
  const from = usage?.window?.from;
  return from ? `全部历史（自 ${formatDateTime(from)}）` : "全部历史";
}

function linksOf(usage: PanelUsage | null): WorkspaceUsageLink[] {
  return Array.isArray(usage?.links) ? usage.links : [];
}

function chainSegments(usage: PanelUsage | null) {
  const layers = layerNames(usage);
  const links = linksOf(usage);
  if (!links.length) return [];
  const sets = layers.map(() => new Set<string>());
  for (const link of links) {
    const source = Number(link.source_layer);
    const target = Number(link.target_layer);
    if (sets[source]) sets[source].add(link.source);
    if (sets[target]) sets[target].add(link.target);
  }
  return layers.slice(1).map((name, index) => ({
    label: LAYER_LABELS[name] || name,
    count: sets[index + 1]?.size || 0,
  })).filter((segment) => segment.count > 0);
}

function sumLinks(usage: PanelUsage | null, layer: string, side: "source" | "target"): Array<{ name: string; value: number }> {
  const layers = layerNames(usage);
  const index = layers.indexOf(layer);
  if (index < 0) return [];
  const totals = new Map<string, number>();
  for (const link of linksOf(usage)) {
    if (Number(link.source_layer) !== index || Number(link.target_layer) !== index + 1) continue;
    const key = String(link[side] || "");
    totals.set(key, (totals.get(key) || 0) + (Number(link.requests) || 0));
  }
  return [...totals.entries()]
    .map(([name, value]) => ({ name, value }))
    .filter((row) => row.name)
    .sort((left, right) => right.value - left.value);
}

function taskUsage(usage: PanelUsage | null) {
  return sumLinks(usage, "requested_model_id", "source");
}

function upstreamUsage(usage: PanelUsage | null) {
  const layers = layerNames(usage);
  const index = layers.indexOf("upstream_model_id");
  if (index <= 0) return [];
  const totals = new Map<string, number>();
  for (const link of linksOf(usage)) {
    if (Number(link.source_layer) !== index - 1 || Number(link.target_layer) !== index) continue;
    const key = String(link.target || "");
    totals.set(key, (totals.get(key) || 0) + (Number(link.requests) || 0));
  }
  return [...totals.entries()]
    .map(([name, value]) => ({ name, value }))
    .filter((row) => row.name)
    .sort((left, right) => right.value - left.value);
}

function Kpi({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="min-w-0 rounded-panel border border-rule bg-panel px-3 py-3">
      <div className="text-meta text-ink-3">{label}</div>
      <div className="mt-1 break-words text-[24px] font-semibold leading-none tabular-nums text-ink">{value}</div>
      <div className="mt-1 break-words text-meta leading-4 text-ink-3">{hint}</div>
    </div>
  );
}

function KeyForm({ onSubmit }: { onSubmit: (key: string) => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  function submit() {
    const key = value.trim();
    if (!key) {
      setError("请填写工作空间 key。");
      return;
    }
    setError(null);
    onSubmit(key);
  }
  return (
    <div className="mx-auto w-full max-w-xl">
      <Panel title="工作空间面板" description="需要一个工作空间 key。它只能读写这一个空间的任务与用量。">
        <div className="space-y-3">
          <Field label="工作空间 key" error={error}>
            <Input
              type="password"
              autoComplete="off"
              aria-label="工作空间 key"
              placeholder="amkr_ws_…"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") submit();
              }}
            />
          </Field>
          <Button variant="primary" onClick={submit}>打开面板</Button>
        </div>
      </Panel>
    </div>
  );
}

function TaskForm({
  editing, models, revision, onDone, onCancel,
}: {
  editing: Editing;
  models: string[];
  revision: string;
  onDone: (message: string) => Promise<void>;
  onCancel: () => void;
}) {
  const task = editing.task;
  const isNew = Boolean(editing.new);
  const [name, setName] = useState(task?.name || "");
  const [displayName, setDisplayName] = useState(task?.display_name || "");
  const [model, setModel] = useState(task?.model || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const api = useMemo(() => createPanelApi(panelCredential()), []);

  async function save() {
    const taskName = isNew ? name.trim() : task?.name;
    if (!taskName) {
      setError("请填写任务名。");
      return;
    }
    const payload: PanelTaskPayload = {
      display_name: displayName.trim() || null,
      model: model || null,
    };
    setBusy(true);
    setError(null);
    try {
      if (isNew) await api.createTask(revision, { name: taskName, ...payload });
      else await api.updateTask(revision, taskName, payload);
      await onDone(isNew ? "任务已创建。" : "任务已更新。");
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="m-3 rounded-control border border-rule bg-sunken/40 p-3">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Field label="任务名">
          <Input
            aria-label="任务名"
            value={name}
            disabled={!isNew}
            placeholder="例如 TASK_000001"
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label="显示名">
          <Input
            aria-label="显示名"
            value={displayName}
            placeholder="可选"
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </Field>
        <Field label="模型">
          <Select
            aria-label="模型"
            value={model}
            onChange={(event) => setModel(event.target.value)}
            options={[{ value: "", label: "（未指定）" }, ...[...new Set([...(task?.model ? [task.model] : []), ...models])].map((id) => ({ value: id, label: id }))]}
          />
        </Field>
      </div>
      {error ? <Banner tone="critical" className="mt-2">{error}</Banner> : null}
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Button variant="primary" loading={busy} onClick={() => void save()}>{isNew ? "创建任务" : "保存修改"}</Button>
        <Button variant="ghost" onClick={onCancel}>取消</Button>
        {!isNew ? <span className="text-meta text-ink-3">任务的参数（温度、停止词、推理强度等）请在 AMKR 管理界面调整，这里不会改动它们。</span> : null}
      </div>
    </div>
  );
}

export function PanelPage() {
  const [credential, setCredential] = useState<PanelCredential | null>(() => {
    const initial = panelCredential();
    return initial.key ? initial : null;
  });
  const [usage, setUsage] = useState<PanelUsage | null>(null);
  const [tasks, setTasks] = useState<PanelTask[]>([]);
  const [revision, setRevision] = useState("");
  const [selection, setSelection] = useState<string | number>(24);
  const [metric, setMetric] = useState<Metric>("requests");
  const [loading, setLoading] = useState(Boolean(credential));
  const [error, setError] = useState<string | null>(null);
  const [unauthorized, setUnauthorized] = useState(false);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [reload, setReload] = useState(0);
  const [at, setAt] = useState<string | null>(null);
  const alive = useRef(true);

  const api = useMemo(() => credential ? createPanelApi(credential) : null, [credential]);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useEffect(() => {
    if (!api) return;
    let cancelled = false;
    const current = rangeSpec(selection);
    setLoading(true);
    setError(null);
    Promise.all([
      api.usage({ hours: current.hours, allHistory: selection === ALL_HISTORY }),
      api.tasks(),
    ]).then(([nextUsage, nextTasks]) => {
      if (cancelled || !alive.current) return;
      setUsage(nextUsage);
      setTasks(nextTasks.tasks || []);
      setRevision(nextTasks.config_revision || "");
      setUnauthorized(false);
      setError(null);
      setAt(new Date().toISOString());
    }).catch((failure: unknown) => {
      if (cancelled || !alive.current) return;
      setUsage(null);
      if (failure instanceof PanelApiError && failure.status === 401) {
        setUnauthorized(true);
        setError(null);
      } else {
        setError(errorText(failure));
      }
    }).finally(() => {
      if (!cancelled && alive.current) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [api, selection, reload]);

  function acceptKey(key: string) {
    const url = new URL(window.location.href);
    const params = new URLSearchParams(url.hash.replace(/^#/, ""));
    params.set("k", key);
    url.hash = params.toString();
    window.history.replaceState(null, "", url);
    const next = panelCredential();
    setCredential(next);
    setUnauthorized(false);
    setError(null);
    setEditing(null);
  }

  function copyAddress() {
    if (!credential) return;
    const url = new URL(window.location.href);
    const params = new URLSearchParams(url.hash.replace(/^#/, ""));
    params.set("k", credential.key);
    if (credential.base && credential.base !== panelApiBase()) params.set("api", credential.base);
    url.hash = params.toString();
    void copyText(url.toString()).then(() => toast.success("面板地址已复制。")).catch((failure) => toast.error(errorText(failure)));
  }

  const stats = statsOf(usage);
  const workspace = usage?.workspace || usage?.workspaces?.[0]?.name || "工作空间";
  const models = usage?.models || [];
  const chain = chainSegments(usage);
  const range = rangeLabel(selection, usage);

  return (
    <main className="min-h-dvh bg-paper px-3 py-4 text-ink [&_h3]:text-ink sm:px-5 lg:px-6">
      <div className="mx-auto flex w-full max-w-[1280px] flex-col gap-3">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="min-w-0">
            <div className="text-meta font-medium uppercase tracking-wide text-ink-3">工作空间面板</div>
            <h1 className="mt-0.5 break-all font-mono text-page font-semibold">{workspace}</h1>
            {at ? <p className="mt-1 text-meta text-ink-3">更新于 {formatDateTime(at)}</p> : null}
          </div>
          <div className="flex max-w-full flex-wrap items-center gap-2">
            {credential ? (
              <>
              <div className="hidden max-w-full overflow-x-auto sm:block">
                <Segmented
                  aria-label="选择统计窗口"
                  options={USAGE_RANGES.map((item) => ({ value: String(item.hours), label: item.short }))}
                  value={String(selection)}
                  onValueChange={(value) => setSelection(value === ALL_HISTORY ? ALL_HISTORY : Number(value))}
                />
              </div>
              <Select
                className="w-28 sm:hidden"
                aria-label="选择统计窗口（移动端）"
                value={String(selection)}
                onChange={(event) => setSelection(event.target.value === ALL_HISTORY ? ALL_HISTORY : Number(event.target.value))}
                options={USAGE_RANGES.map((item) => ({ value: String(item.hours), label: item.short }))}
              />
              </>
            ) : null}
            <Button size="sm" variant="ghost" leadingIcon={<RefreshCw aria-hidden className="size-3.5" />} onClick={() => setReload((value) => value + 1)}>刷新</Button>
            <Button size="sm" variant="ghost" leadingIcon={<Copy aria-hidden className="size-3.5" />} disabled={!credential} onClick={copyAddress}>复制地址</Button>
          </div>
        </header>

        {!credential ? <KeyForm onSubmit={acceptKey} /> : null}

        {credential && unauthorized ? (
          <div className="space-y-3">
            <Banner tone="critical">这个 key 无效，或对应的空间已被删除。请在下方重新填写。</Banner>
            <KeyForm onSubmit={acceptKey} />
          </div>
        ) : null}

        {credential && !unauthorized && error ? (
          <Banner tone="critical" title="无法读取面板数据" action={<Button size="sm" variant="secondary" onClick={() => setReload((value) => value + 1)}>重试</Button>}>
            {error}
          </Banner>
        ) : null}

        {credential && !unauthorized && loading ? (
          <div className="space-y-3"><Skeleton variant="block" /><Skeleton variant="chart" /></div>
        ) : null}

        {credential && !unauthorized && !loading && usage ? (
          <>
            {chain.length ? (
              <div className="flex min-w-0 flex-wrap items-center gap-1.5 rounded-panel border border-rule bg-panel px-3 py-2 text-meta" role="group" aria-label="请求流经的层与各层节点数">
                <span className="inline-flex items-center gap-1"><span className="font-mono font-semibold text-ink">1</span><span className="text-ink-3">工作空间</span></span>
                {chain.map((segment) => (
                  <span key={segment.label} className="inline-flex items-center gap-1">
                    <span className="text-ink-3">→</span>
                    <span className="font-mono font-semibold text-ink">{formatCount(segment.count)}</span>
                    <span className="text-ink-3">{segment.label}</span>
                  </span>
                ))}
              </div>
            ) : null}

            <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
              <Kpi label="请求" value={formatCount(stats?.requests || 0)} hint={range} />
              <Kpi label="成功率" value={stats?.requests ? formatPercentValue((stats.successes || 0) / stats.requests) : "-"} hint={`${formatCount(stats?.successes || 0)} 成功 · ${formatCount(stats?.failures || 0)} 失败`} />
              <Kpi label="Token" value={formatCompact(stats?.total_tokens || 0)} hint={`缓存 ${formatCompact(stats?.cached_tokens || 0)} Token`} />
              <Kpi label="平均耗时" value={formatDuration(Number(stats?.avg_duration_ms) || 0)} hint="每次请求" />
            </div>

            <div>
              <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                <h2 className="text-[16px] font-semibold leading-tight text-ink">请求流向</h2>
                <Segmented
                  aria-label="选择流向宽度口径"
                  options={[{ value: "requests", label: "按请求数" }, { value: "tokens", label: "按 Token" }]}
                  value={metric}
                  onValueChange={(value) => setMetric(value as Metric)}
                />
              </div>
              <Panel padding={false}>
                <div className="max-w-full overflow-x-auto">
                <div className="min-w-[680px]">
                <FlowMap
                  embedded
                  links={linksOf(usage).map((link) => ({
                    ...link,
                    requests: metric === "tokens" ? Number(link.total_tokens) || 0 : Number(link.requests) || 0,
                  }))}
                  layerLabels={layerNames(usage).map((name) => LAYER_LABELS[name] || name)}
                  metric={metric}
                  format={(value) => metric === "tokens" ? formatCompactNumber(value) : formatCount(value)}
                  height={320}
                  ariaLabel="本工作空间的请求流向"
                  emptyText="窗口内没有可归因的请求。"
                />
                </div>
                </div>
              </Panel>
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              <RankBars
                title="任务用量"
                ariaLabel="任务用量"
                colorScope="panel:task"
                items={taskUsage(usage).map((row) => ({ id: row.name, label: row.name, value: row.value }))}
                format={(value) => `${formatCount(value)} 次`}
                emptyText="窗口内没有请求。"
              />
              <RankBars
                title="上游模型用量"
                ariaLabel="上游模型用量"
                colorScope="panel:upstream_model"
                items={upstreamUsage(usage).map((row) => ({ id: row.name, label: row.name, value: row.value }))}
                format={(value) => `${formatCount(value)} 次`}
                emptyText="窗口内没有可归因的上游模型。"
              />
            </div>

            <Panel
              title={`任务（${tasks.length}）`}
              actions={<Button size="sm" variant="primary" leadingIcon={<Plus aria-hidden className="size-3.5" />} onClick={() => setEditing({ new: true })}>新建任务</Button>}
              padding={false}
            >
              {editing ? (
                <TaskForm
                  key={editing.task?.name || "__new"}
                  editing={editing}
                  models={models}
                  revision={revision}
                  onCancel={() => setEditing(null)}
                  onDone={async (message) => {
                    setEditing(null);
                    setReload((value) => value + 1);
                    toast.success(message);
                  }}
                />
              ) : null}
              {tasks.length ? (
                <div className="divide-y divide-rule">
                  {tasks.map((task) => (
                    <div key={task.name} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
                      <span className="min-w-0 flex-1 break-all font-mono text-body text-ink">{task.name}</span>
                      <span className="min-w-0 truncate text-meta text-ink-2">{task.display_name || "—"}</span>
                      <span className="min-w-0 truncate font-mono text-meta text-ink-3">{task.model || "未指定"}</span>
                      <div className="flex shrink-0 items-center gap-1">
                        <Button size="sm" variant="ghost" disabled={editing !== null} onClick={() => setEditing({ task })}>编辑</Button>
                        <Button size="sm" variant="ghost" disabled={editing !== null} leadingIcon={<Trash2 aria-hidden className="size-3" />} onClick={() => void removeTask(task, revision, api, () => setReload((value) => value + 1))}>删除</Button>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <Empty title="这个工作空间还没有任务。" description="调用方要指定任务名（或模型别名）才能用到它。" />
              )}
            </Panel>
          </>
        ) : null}
      </div>
    </main>
  );
}

async function removeTask(task: PanelTask, revision: string, api: ReturnType<typeof createPanelApi> | null, done: () => void) {
  if (!api) return;
  if (!window.confirm(`删除任务「${task.name}」？使用它的调用方之后会收到 404。`)) return;
  try {
    await api.deleteTask(revision, task.name);
    done();
    toast.success("任务已删除。");
  } catch (failure) {
    toast.error(errorText(failure));
  }
}
