import { useEffect, useMemo, useState } from "react";
import { LogOut, RefreshCw } from "lucide-react";
import {
  ALL_HISTORY,
  USAGE_RANGES,
  formatPercentValue,
  rangeSpec,
} from "@/lib/chart-math";
import { errorText, formatCompact, formatCount, formatDateTime, formatDuration } from "@/lib/format";
import { formatCost, formatCostExact, normalizeCatalog, sumCost, type PriceIndex } from "@/lib/pricing";
import type { MetricStats } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Skeleton } from "@/components/ui/Skeleton";
import { RankBars } from "@/components/charts/RankBars";
import {
  createGuestApi,
  guestCredential,
  saveGuestCredential,
  GuestApiError,
  type GuestRecentRequest,
  type GuestUsageResponse,
} from "./api";

const DIMENSION_LABELS: Record<string, string> = {
  model_id: "模型",
  provider_id: "供应商",
  upstream_model_id: "上游模型",
};

function statsOf(usage: GuestUsageResponse | null): MetricStats | null {
  return usage?.stats || null;
}

function rangeLabel(selection: string | number, usage: GuestUsageResponse | null): string {
  if (selection !== ALL_HISTORY) return rangeSpec(selection).label;
  const from = usage?.window?.from;
  return from ? `全部历史（自 ${formatDateTime(from)}）` : "全部历史";
}

function keyTail(key: string): string {
  return key.length > 8 ? `…${key.slice(-6)}` : "已保存";
}

function statusBadge(row: GuestRecentRequest) {
  const code = row.status_code;
  if (code === null || code === undefined) return <Badge tone="neutral">无响应</Badge>;
  return <Badge tone={row.success ? "good" : "critical"}>{code}</Badge>;
}

function KeyForm({ onSubmit }: { onSubmit: (key: string) => void }) {
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  function submit() {
    const key = value.trim();
    if (!key) {
      setError("请填写访问密钥。");
      return;
    }
    setError(null);
    onSubmit(key);
  }
  return (
    <div className="mx-auto w-full max-w-xl">
      <Panel title="使用访问密钥登录" description="查看这把密钥自己的用量。">
        <div className="space-y-3">
          <p className="text-meta leading-5 text-ink-3">
            密钥由 AMKR 管理员在「访问密钥」页创建并分发给你；它决定了你能调用哪些供应商与模型。
          </p>
          <Field label="访问密钥" error={error}>
            <Input
              type="password"
              autoComplete="off"
              aria-label="访问密钥"
              placeholder="amkr_ak_…"
              value={value}
              onChange={(event) => setValue(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") submit();
              }}
            />
          </Field>
          <Button variant="primary" onClick={submit}>查看看板</Button>
        </div>
      </Panel>
    </div>
  );
}

function Kpi({ label, value, hint }: { label: string; value: string; hint: string }) {
  return (
    <div className="min-w-0 rounded-panel border border-rule bg-panel px-3 py-3">
      <div className="text-meta text-ink-3">{label}</div>
      <div className="mt-1 text-[24px] font-semibold leading-none tabular-nums text-ink">{value}</div>
      <div className="mt-1 break-words text-meta leading-4 text-ink-3">{hint}</div>
    </div>
  );
}

function dimensionRows(usage: GuestUsageResponse | null, dimension: string) {
  const grouped = usage?.dimensions?.[dimension] || {};
  return Object.entries(grouped)
    .map(([name, stats]) => ({ name, value: Number(stats.requests) || 0 }))
    .filter((row) => row.value > 0)
    .sort((left, right) => right.value - left.value);
}

export function GuestPage() {
  const [key, setKey] = useState(() => guestCredential());
  const [usage, setUsage] = useState<GuestUsageResponse | null>(null);
  const [selection, setSelection] = useState<string | number>(24);
  const [loading, setLoading] = useState(Boolean(key));
  const [unauthorized, setUnauthorized] = useState(false);
  const [disabled, setDisabled] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [at, setAt] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [prices, setPrices] = useState<PriceIndex | null>(null);
  const [pricingError, setPricingError] = useState<string | null>(null);
  const [pricingLoading, setPricingLoading] = useState(false);

  const api = useMemo(() => key ? createGuestApi(key) : null, [key]);

  useEffect(() => {
    if (!api) return;
    let cancelled = false;
    const current = rangeSpec(selection);
    setLoading(true);
    setError(null);
    api.usage({ hours: current.hours, allHistory: selection === ALL_HISTORY })
      .then((next) => {
        if (cancelled) return;
        setUsage(next);
        setUnauthorized(false);
        setDisabled(false);
        setError(null);
        setAt(new Date().toISOString());
      })
      .catch((failure: unknown) => {
        if (cancelled) return;
        setUsage(null);
        if (failure instanceof GuestApiError && failure.status === 401) {
          setUnauthorized(true);
          setDisabled(false);
          setError(null);
        } else if (failure instanceof GuestApiError && failure.status === 403) {
          setDisabled(true);
          setUnauthorized(false);
          setError(null);
        } else {
          setError(errorText(failure));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [api, selection, reload]);

  useEffect(() => {
    if (!api) return;
    let cancelled = false;
    setPricingLoading(true);
    api.pricing()
      .then((payload) => {
        if (cancelled) return;
        setPrices(normalizeCatalog(payload));
        setPricingError(null);
      })
      .catch((failure) => {
        if (cancelled) return;
        setPrices(null);
        setPricingError(errorText(failure));
      })
      .finally(() => {
        if (!cancelled) setPricingLoading(false);
      });
    return () => { cancelled = true; };
  }, [api]);

  function acceptKey(value: string) {
    saveGuestCredential(value);
    setKey(value);
    setUsage(null);
    setUnauthorized(false);
    setDisabled(false);
    setError(null);
    setLoading(true);
    setReload((current) => current + 1);
  }

  function logout() {
    saveGuestCredential("");
    setKey("");
    setUsage(null);
    setUnauthorized(false);
    setDisabled(false);
    setError(null);
  }

  const stats = statsOf(usage);
  const range = rangeLabel(selection, usage);
  const name = key ? (usage?.access_key_name || "访问密钥") : "访问密钥看板";
  const grouped = usage?.dimensions?.upstream_model_id;
  const cost = grouped && prices ? sumCost(prices, grouped) : null;
  const costRows = grouped && prices
    ? Object.entries(grouped)
      .map(([model, modelStats]) => {
        const amount = sumCost(prices, { [model]: modelStats });
        return { name: model, value: amount.total, priced: amount.priced > 0 };
      })
      .filter((row) => row.priced)
      .sort((left, right) => right.value - left.value)
    : [];

  return (
    <main className="min-h-dvh bg-paper px-3 py-4 text-ink [&_h3]:text-ink sm:px-5 lg:px-6">
      <div className="mx-auto flex w-full max-w-[1280px] flex-col gap-3">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="min-w-0">
            <div className="text-meta font-medium uppercase tracking-wide text-ink-3">访客看板</div>
            <h1 className="mt-0.5 flex min-w-0 flex-wrap items-baseline gap-2 text-page font-semibold">
              <span className="break-all font-mono">{name}</span>
              {key ? <span className="shrink-0 rounded-chip bg-sunken px-1.5 py-0.5 text-meta font-normal text-ink-3">{keyTail(key)}</span> : null}
            </h1>
            {at ? <p className="mt-1 text-meta text-ink-3">更新于 {formatDateTime(at)}</p> : null}
          </div>
          <div className="flex max-w-full flex-wrap items-center gap-2">
            {key ? (
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
                <Button size="sm" variant="ghost" leadingIcon={<RefreshCw aria-hidden className="size-3.5" />} onClick={() => setReload((current) => current + 1)}>刷新</Button>
                <Button size="sm" variant="ghost" leadingIcon={<LogOut aria-hidden className="size-3.5" />} onClick={logout}>退出</Button>
              </>
            ) : null}
          </div>
        </header>

        {!key ? <KeyForm onSubmit={acceptKey} /> : null}

        {key && unauthorized ? (
          <div className="space-y-3">
            <Banner tone="critical">这把访问密钥无效（可能已被删除或轮换）。请在下方重新输入密钥。</Banner>
            <KeyForm onSubmit={acceptKey} />
          </div>
        ) : null}

        {key && disabled ? (
          <Banner tone="warn" title="访问密钥已停用">
            这把访问密钥已被停用。请联系 AMKR 管理员在「访问密钥」页重新启用。这把密钥当前不能查看或调用任何资源。
          </Banner>
        ) : null}

        {key && !unauthorized && !disabled && error ? (
          <Banner tone="critical" title="无法读取看板数据" action={<Button size="sm" variant="secondary" onClick={() => setReload((current) => current + 1)}>重试</Button>}>
            {error}
          </Banner>
        ) : null}

        {key && !unauthorized && !disabled && loading ? (
          <div className="space-y-3"><Skeleton variant="block" /><Skeleton variant="chart" /></div>
        ) : null}

        {key && !unauthorized && !disabled && !loading && usage ? (
          <>
            <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
              <Kpi label="请求" value={formatCount(stats?.requests || 0)} hint={range} />
              <Kpi
                label="成功率"
                value={stats?.requests ? formatPercentValue((stats.successes || 0) / stats.requests) : "-"}
                hint={`${formatCount(stats?.successes || 0)} 成功 · ${formatCount(stats?.failures || 0)} 失败`}
              />
              <Kpi label="Token" value={formatCompact(stats?.total_tokens || 0)} hint={`缓存 ${formatCompact(stats?.cached_tokens || 0)} Token`} />
              <Kpi label="平均耗时" value={formatDuration(Number(stats?.avg_duration_ms) || 0)} hint="每次请求" />
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              {(["model_id", "provider_id"] as const).map((dimension) => (
                <RankBars
                  key={dimension}
                  title={DIMENSION_LABELS[dimension]}
                  ariaLabel={DIMENSION_LABELS[dimension]}
                  items={dimensionRows(usage, dimension).slice(0, 8).map((row) => ({ id: row.name, label: row.name, value: row.value }))}
                  colorScope={`guest:${dimension}`}
                  format={(value) => `${formatCount(value)} 次`}
                  emptyText="窗口内没有请求。"
                />
              ))}
            </div>

            <RankBars
              title={DIMENSION_LABELS.upstream_model_id}
              ariaLabel={DIMENSION_LABELS.upstream_model_id}
              items={dimensionRows(usage, "upstream_model_id").slice(0, 8).map((row) => ({ id: row.name, label: row.name, value: row.value }))}
              colorScope="guest:upstream_model_id"
              format={(value) => `${formatCount(value)} 次`}
              emptyText="窗口内没有可归因的上游模型。"
            />

            <Panel title={`花费估算（按${DIMENSION_LABELS.upstream_model_id}）`} padding={false}>
              {!grouped || !cost ? (
                <Empty
                  title="暂时无法估算花费。"
                  description={pricingLoading ? "正在读取价格目录…" : (pricingError || "价格目录不可用")}
                />
              ) : costRows.length ? (
                <>
                  <div className="p-3">
                    <RankBars
                      embedded
                      className="pt-8"
                      ariaLabel="花费估算"
                      items={costRows.slice(0, 8).map((row) => ({ id: row.name, label: row.name, value: row.value }))}
                      colorScope="guest:upstream_model_id"
                      format={(value) => formatCost(value)}
                      emptyText="窗口内没有能匹配到单价的请求。"
                    />
                  </div>
                  <div className="border-t border-rule px-4 py-3">
                    <div className="flex items-center justify-between gap-3 text-body">
                      <span>合计（估算）</span>
                      <strong title={formatCostExact(cost.total)}>{formatCost(cost.total)}</strong>
                    </div>
                    <p className="mt-1 text-meta text-ink-3">
                      {cost.priced < cost.total_count
                        ? `${cost.total_count} 个上游模型里有 ${cost.priced} 个匹配到单价，其余未计入。`
                        : ""}
                      金额是与 models.dev 公开单价的估算，不是账单。
                    </p>
                  </div>
                </>
              ) : (
                <Empty title="窗口内没有能匹配到单价的请求。" description="价格按上游模型名匹配 models.dev 目录；匹配不上时该条不计入金额。" />
              )}
            </Panel>

            <RecentCalls usage={usage} range={range} />
            <p className="text-meta leading-5 text-ink-3">
              这里只显示这把访问密钥自己的流量。可用范围由管理员配置的供应商与模型清单决定；要调整请联系管理员。
            </p>
          </>
        ) : null}
      </div>
    </main>
  );
}

function RecentCalls({ usage, range }: { usage: GuestUsageResponse; range: string }) {
  const rows = usage.recent_requests || [];
  return (
    <Panel
      title="最近调用"
      actions={<><Badge tone="neutral">{rows.length} 条</Badge><Badge tone="neutral">{range}</Badge></>}
      padding={false}
    >
      {rows.length ? (
        <div className="divide-y divide-rule">
          {rows.map((row, index) => (
            <div key={`${row.created_at || "row"}-${index}`} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 text-meta">
              <span className="w-28 shrink-0 tabular-nums text-ink-3">{formatDateTime(row.created_at)}</span>
              <span className="min-w-0 flex-1 truncate font-mono text-ink" title={`本地路由名 ${row.model_id || ""}`}>{row.upstream_model_id || row.model_id || "—"}</span>
              <span className="min-w-0 truncate font-mono text-ink-3">{row.provider_id || "—"}</span>
              <span className="flex shrink-0 items-center gap-1">{statusBadge(row)}{row.retried ? <Badge tone="warn">重试</Badge> : null}</span>
              <span className="w-14 shrink-0 text-right tabular-nums text-ink-2">{formatCompact(row.total_tokens || 0)}</span>
              <span className="w-14 shrink-0 text-right tabular-nums text-ink-3">{formatDuration(row.duration_ms || 0)}</span>
            </div>
          ))}
        </div>
      ) : (
        <Empty title="窗口内没有调用记录。" />
      )}
      {rows.length >= 50 ? <p className="border-t border-rule px-4 py-3 text-meta text-ink-3">只显示最近 50 条；更早的调用请缩短时间窗口查看。</p> : null}
    </Panel>
  );
}
