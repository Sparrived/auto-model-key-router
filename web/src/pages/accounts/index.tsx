// 账号资源：CPA 实例的账号与订阅剩余额度看板。
//
// 口径（与 legacy accounts.js 一致）：
// - 浏览器不直接访问 CPA，所有读数走 /api/cpa-accounts 的服务端扇出；
// - 进页只取账号读数，管理密钥只在打开「管理实例」时读取；
// - 只读、不轮询；刷新由用户或冻结到期触发，一次刷新只发一次账号请求；
// - 额度显示**剩余**比例，≤5% 用 critical，≤20% 用 warn；
// - 单个实例读取失败只影响自己的面板，搜索/筛选对所有实例统一生效；
// - 额度冻结是后台的临时状态，不写回配置的 enabled 开关（见 docs/design/quota-freeze.md）。

import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText, formatCount, formatDateTime, formatPercent } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import {
  freezeLookupKey,
  freezeReasonText,
  freezeResetCountdown,
  freezeTriggerText,
  indexQuotaFreezes,
  invalidateFreezeViews,
  useDeadlineRefresh,
  useNow,
} from "@/lib/quota-freeze";
import { cn } from "@/lib/cn";
import type {
  CPAAccount,
  CPAAccountInstance,
  CPAAccountsResponse,
  CPAInstance,
  CPAResetCreditsInfo,
  CPAResetCreditsResponse,
  CPASubscription,
  CPAQuotaWindow,
  QuotaFreeze,
  QuotaGuardStatus,
} from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";
import { Tooltip } from "@/components/ui/Tooltip";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { Meter } from "@/components/charts";

type StatusFilter = "all" | "usable" | "cooling" | "alert";

interface InstanceRow {
  id: string;
  label: string;
  base_url: string;
  management_key: string;
}

const STATUS_FILTERS: Array<{ value: StatusFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "usable", label: "可用" },
  { value: "cooling", label: "停用/冷却" },
  { value: "alert", label: "额度告警" },
];

function clampPercent(value: unknown): number {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.max(0, Math.min(1, number));
}

function lowestRemaining(account: { windows?: CPAQuotaWindow[] }): number | null {
  const values = (account.windows ?? []).map((window) => clampPercent(window.remaining));
  return values.length ? Math.min(...values) : null;
}

function quotaTone(remaining: number): "critical" | "warn" | "good" {
  if (remaining <= 0.05) return "critical";
  if (remaining <= 0.2) return "warn";
  return "good";
}

function formatMetric(metric: { value?: number | string; unit?: string }): string {
  const value = Number(metric.value);
  const text = Number.isFinite(value) ? String(Math.round(value * 100) / 100) : String(metric.value ?? "");
  return [text, metric.unit].filter(Boolean).join(" ");
}

function countdown(window: CPAQuotaWindow, offsetMs: number, now: number): string {
  if (!window.reset_at) return "";
  const at = Date.parse(window.reset_at);
  if (!Number.isFinite(at)) return "";
  const remaining = at - (Number(offsetMs) || 0) - now;
  if (remaining <= 0) return "即将重置";
  const minutes = Math.round(remaining / 60000);
  if (minutes < 60) return `${minutes} 分钟后重置`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时 ${minutes % 60} 分钟后重置`;
  return `${Math.floor(hours / 24)} 天 ${hours % 24} 小时后重置`;
}

function statusBadge(account: CPAAccount): { text: string; tone: "good" | "warn" | "neutral" | "critical" } {
  if (account.disabled) return { text: "已停用", tone: "neutral" };
  if (account.unavailable) return { text: "冷却中", tone: "warn" };
  if (account.status && account.status !== "active") return { text: account.status, tone: "neutral" };
  return { text: "可用", tone: "good" };
}

function quotaTitle(window: CPAQuotaWindow, offsetMs: number, now: number): string {
  return [
    window.source === "passive" ? "CPA 采集" : "现场查询",
    window.status === "rejected" ? "已用尽" : null,
    countdown(window, offsetMs, now),
    window.description || null,
  ].filter(Boolean).join("\n");
}

function quotaHint(account: CPAAccount): string {
  if (!account.quota_error) {
    return account.supports_quota ? "对端没有可读的额度" : "上游未提供额度信号";
  }
  if (account.quota_error.includes("501")) return "对端没有额度提供者（未装额度插件，也没配 quota_probe）";
  return account.quota_error;
}

// 页头说明里的检测节奏：配置关掉整套机制（interval_seconds=0）时要说「已关闭」，
// 不能写成「每 0 秒」；字段缺省（旧后端）时退回「定时」而不是报错。
//
// 关闭档的文案必须与后端一致：interval=0 时后台巡检、上游响应头信号、页面即时评估
// **都不产生新冻结**（internal/api/quota_guard.go），已有冻结仍在选路里生效到到期或
// 手工解冻（docs/USAGE.md 4.2）。所以关闭档不能复用「进页会立即评估一次」这句。
function thresholdDescription(value: number | undefined): string {
  const ratio = Number(value);
  if (!Number.isFinite(ratio) || ratio <= 0) return "未启用";
  return `${Math.round(ratio * 1000) / 10}%`;
}

function guardDescription(guard?: QuotaGuardStatus): string {
  const thresholds = `提前冻结底限：5h ${thresholdDescription(guard?.threshold_5h)}、7d ${thresholdDescription(guard?.threshold_7d)}。`;
  if (guard?.enabled === false) {
    return `后台自动检测已在配置里关闭；进页也不会产生新冻结，已有冻结仍展示、仍参与选路，直到到期或手工解冻。${thresholds}`;
  }
  const tail = "进页会立即评估一次；命中硬性额度或低于 5h/7d 底限时对应 Key 会被自动冻结（不写回配置的启用开关），冻结按内置计时器自动解冻。";
  const seconds = Number(guard?.interval_seconds);
  const every = Number.isFinite(seconds) && seconds > 0 ? `每 ${seconds} 秒` : "定时";
  return `后台${every}自动检测；${tail}${thresholds}`;
}

function normalizeInstances(value: unknown): InstanceRow[] {
  if (Array.isArray(value)) {
    return value.map((item) => {
      const entry = (item && typeof item === "object" ? item : {}) as Partial<CPAInstance> & { id?: string };
      return {
        id: String(entry.id ?? ""),
        label: String(entry.label ?? ""),
        base_url: String(entry.base_url ?? ""),
        management_key: String(entry.management_key ?? ""),
      };
    });
  }
  if (value && typeof value === "object") {
    return Object.entries(value as Record<string, Partial<CPAInstance>>).map(([id, item]) => ({
      id,
      label: String(item?.label ?? ""),
      base_url: String(item?.base_url ?? ""),
      management_key: String(item?.management_key ?? ""),
    }));
  }
  return [];
}

function nearestExpiry(info: CPAResetCreditsInfo): string {
  const values = (info.credits ?? [])
    .map((credit) => (credit.expires_at ? Date.parse(credit.expires_at) : NaN))
    .filter((value) => Number.isFinite(value));
  if (!values.length) return "无到期时间";
  return `最近 ${formatDateTime(new Date(Math.min(...values)).toISOString())} 过期`;
}

function resetInfoFor(
  data: CPAResetCreditsResponse | null,
  instance: CPAAccountInstance,
  account: CPAAccount,
): CPAResetCreditsInfo | null {
  if (!data) return null;
  const instanceEntry = (data.instances ?? []).find((entry) =>
    entry.id === instance.id || (entry.label && entry.label === instance.label));
  if (!instanceEntry) return null;
  const authIndex = account.auth_index === undefined || account.auth_index === null
    ? "" : String(account.auth_index);
  const accountEntry = (instanceEntry.accounts ?? []).find((entry) => {
    if (String(entry.auth_index ?? "") !== authIndex) return false;
    if (account.provider && entry.provider && entry.provider !== account.provider) return false;
    return true;
  });
  return accountEntry?.reset_credits ?? null;
}

function QuotaSection({ account, now }: { account: CPAAccount; now: number }) {
  const windows = account.windows ?? [];
  const offset = Number(account.server_time_offset_ms) || 0;
  const groups: Array<{ name: string; windows: CPAQuotaWindow[] }> = [];
  for (const window of windows) {
    const name = (window.group ?? "").trim();
    const last = groups[groups.length - 1];
    if (last && last.name === name) last.windows.push(window);
    else groups.push({ name, windows: [window] });
  }
  return (
    <div className="space-y-2">
      <div className="text-meta font-medium text-ink-2">剩余额度</div>
      {!windows.length ? (
        <p className="rounded-control bg-sunken px-2 py-1.5 text-meta text-ink-3">{quotaHint(account)}</p>
      ) : groups.map((group, groupIndex) => {
        const minimum = Math.min(...group.windows.map((window) => clampPercent(window.remaining)));
        return (
          <div key={`${group.name}-${groupIndex}`} className="space-y-1.5">
            {group.name ? (
              <div className="flex items-center justify-between gap-2">
                <span className="text-[11px] text-ink-3">{group.name}</span>
                <Badge tone={minimum <= 0.05 ? "critical" : minimum <= 0.2 ? "warn" : "good"}>
                  {minimum <= 0.05 ? "额度耗尽" : minimum <= 0.2 ? `告警 ${Math.round(minimum * 100)}%` : `充裕 ${Math.round(minimum * 100)}%`}
                </Badge>
              </div>
            ) : null}
            <div className="grid gap-2 sm:grid-cols-2">
              {group.windows.map((window, index) => {
                const remaining = clampPercent(window.remaining);
                const tone = quotaTone(remaining);
                const label = window.label || window.window || "额度";
                return (
                  <div key={`${window.key ?? label}-${index}`} data-tone={tone} title={quotaTitle(window, offset, now)}>
                    <Meter
                      value={remaining}
                      label={`${label} 剩余额度`}
                      hint={countdown(window, offset, now) || (window.source === "passive" ? "CPA 采集" : "现场查询")}
                      tone={tone}
                      ariaLabel={`${label} 剩余额度`}
                      format={(value) => `${Math.floor(value * 100 + 1e-9)}%`}
                    />
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
      {account.quota_error && windows.length ? (
        <p className="rounded-control bg-sunken px-2 py-1.5 text-meta text-ink-3">{quotaHint(account)}</p>
      ) : null}
      {(account.summary ?? []).length ? (
        <div className="flex flex-wrap gap-1">
          {(account.summary ?? []).map((metric, index) => (
            <Badge key={`${metric.key ?? metric.label ?? index}`} tone="neutral">
              {metric.label || metric.key} {formatMetric(metric)}
            </Badge>
          ))}
        </div>
      ) : null}
      {account.model_quotas && Object.keys(account.model_quotas).length ? (
        <details className="text-meta text-ink-3">
          <summary className="cursor-pointer">逐模型额度 · {Object.keys(account.model_quotas).length} 个模型</summary>
          <div className="mt-1 space-y-2">
            {Object.entries(account.model_quotas).map(([model, value]) => (
              <div key={model}>
                <div className="font-mono text-[11px]">{model}</div>
                <div className="mt-1 grid gap-2 sm:grid-cols-2">
                  {(value.windows ?? []).map((window, index) => {
                    const remaining = clampPercent(window.remaining);
                    return (
                      <div key={`${model}-${window.key ?? index}`} title={quotaTitle(window, 0, now)}>
                        <Meter
                          value={remaining}
                          label={`${model} ${window.label || window.window || "额度"}`}
                          tone={quotaTone(remaining)}
                          format={(v) => `${Math.floor(v * 100 + 1e-9)}%`}
                        />
                      </div>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        </details>
      ) : null}
    </div>
  );
}

function RecentBars({ buckets }: { buckets: CPAAccount["recent_requests"] }) {
  const list = buckets ?? [];
  if (!list.length) return null;
  // 不筛掉空桶：十个时间片必须等距，否则一次静默的间隔会让相邻柱看起来“更密”。
  const peak = Math.max(...list.map((bucket) => (Number(bucket.success) || 0) + (Number(bucket.failed) || 0)), 1);
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-[11px] text-ink-3">
        <span>最近调用</span>
        <span>{list.length} 个 10 分钟桶</span>
      </div>
      <div className="flex h-5 items-end gap-1" aria-label="最近调用分布">
        {list.map((bucket, index) => {
          const success = Number(bucket.success) || 0;
          const failed = Number(bucket.failed) || 0;
          const height = Math.max(3, Math.round(((success + failed) / peak) * 18));
          return (
            <div
              key={`${bucket.time ?? index}-${index}`}
              className={cn("w-1.5 rounded-[2px]", failed ? "bg-critical" : success ? "bg-accent" : "bg-rule-strong")}
              style={{ height }}
              title={`${bucket.time ?? `第 ${index + 1} 个桶`}：成功 ${success} / 失败 ${failed}`}
            />
          );
        })}
      </div>
    </div>
  );
}

interface SubscriptionCardProps {
  entry: CPASubscription;
  /** 页面级 useNow 的当前时刻，让倒计时随时间前进。 */
  now: number;
  /** 解冻处理器；null 表示当前不可解冻（只显示冻结信息）。 */
  releasingFreeze: ((freeze: QuotaFreeze) => void) | null;
  /** 正在解冻的 Key（freezeLookupKey 结果），用于只给被点的那把按钮上 loading。 */
  releasingKey: string | null;
}

function SubscriptionCard({ entry, now, releasingFreeze, releasingKey }: SubscriptionCardProps) {
  const windows = entry.windows ?? [];
  const freeze = entry.freeze;
  const freezeId = freeze ? freezeLookupKey(freeze.provider_id, freeze.key_name) : "";
  const canRelease = Boolean(freeze?.provider_id && freeze?.key_name && releasingFreeze);
  const freezeCountdown = freeze ? freezeResetCountdown(freeze, now) : "";
  const threshold = freeze?.trigger === "threshold";
  return (
    <Panel
      title={entry.vendor || entry.kind || entry.id || "订阅"}
      description={[entry.base_url, entry.account ? `账号 ${entry.account}` : null, entry.plan || entry.tier_id ? `档位 ${[entry.plan, entry.tier_id].filter(Boolean).join(" / ")}` : null].filter(Boolean).join(" · ")}
      actions={(
        <div className="flex flex-wrap items-center gap-2">
          {freeze ? <Badge tone={threshold ? "warn" : "critical"} dot>{freezeTriggerText(freeze)}</Badge> : null}
          <Badge tone="neutral">{entry.key_name ? `${entry.provider_id} · ${entry.key_name}` : entry.provider_id || "订阅"}</Badge>
          {entry.plan || entry.tier_id ? <Badge tone="neutral">{[entry.plan, entry.tier_id].filter(Boolean).join(" / ")}</Badge> : null}
          {entry.ok && entry.observed_at ? <span className="text-[11px] text-ink-3">查询于 {formatDateTime(entry.observed_at)}</span> : null}
        </div>
      )}
    >
      {freeze ? (
        <Banner
          tone={threshold ? "warn" : "critical"}
          className="mb-2"
          title={threshold ? "此 Key 已被提前冻结" : "此 Key 已被自动冻结"}
          action={canRelease && freeze ? (
            <Button
              size="sm"
              variant="secondary"
              loading={releasingKey === freezeId}
              onClick={() => releasingFreeze?.(freeze)}
            >
              解除冻结
            </Button>
          ) : null}
        >
          <div className="space-y-0.5">
            <p>{freezeReasonText(freeze)}</p>
            {freezeCountdown ? <p>{freezeCountdown}</p> : null}
          </div>
        </Banner>
      ) : null}
      {entry.ok === false || entry.error ? <Banner tone="critical">{entry.error || "读取失败"}</Banner> : windows.length ? (
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
          {windows.map((window, index) => {
            const remaining = clampPercent(window.remaining);
            return (
              <div key={`${window.key ?? index}`} title={quotaTitle(window, 0, now)}>
                <Meter
                  value={remaining}
                  label={`${window.label || window.window || "额度"} 剩余额度`}
                  hint={countdown(window, 0, now) || "上游返回的剩余额度"}
                  tone={quotaTone(remaining)}
                  format={(value) => `${Math.floor(value * 100 + 1e-9)}%`}
                />
              </div>
            );
          })}
        </div>
      ) : <Banner tone="info">上游这次没回可读的额度窗口{entry.status ? `，状态 ${entry.status}` : ""}{entry.plan ? `，档位 ${entry.plan}` : ""}。</Banner>}
      {(entry.summary ?? []).length ? (
        <div className="mt-2 flex flex-wrap gap-1">
          {(entry.summary ?? []).map((metric, index) => <Badge key={`${metric.key ?? index}`} tone="neutral">{metric.label || metric.key} {formatMetric(metric)}</Badge>)}
        </div>
      ) : null}
      {entry.signals && Object.keys(entry.signals).length ? (
        <details className="mt-2 text-meta text-ink-3">
          <summary className="cursor-pointer">原始信号 · {Object.keys(entry.signals).length} 项</summary>
          <div className="mt-1 space-y-0.5 font-mono text-[11px]">
            {Object.entries(entry.signals).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => (
              <div key={name}>{name}: {String(value)}</div>
            ))}
          </div>
        </details>
      ) : null}
    </Panel>
  );
}

export default function AccountsPage() {
  const confirm = useConfirm();
  const accountsQuery = useQuery({
    queryKey: queryKeys.cpaAccounts(),
    queryFn: api.cpaAccounts,
    // 明确不轮询：每次刷新都是对 CPA 与上游的一次真实扇出。
    retry: false,
    refetchOnWindowFocus: false,
  });
  const client = useQueryClient();
  // 页面刻意不轮询：倒计时只重算本地时间；到最近一条 expires_at 才 refetch 一次。
  const now = useNow();
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const searchRef = useRef<HTMLInputElement>(null);
  // 正在解冻的 Key（freezeLookupKey 结果）；同一时刻只放行一次解冻请求。
  const [releasing, setReleasing] = useState<string | null>(null);
  const [resetData, setResetData] = useState<CPAResetCreditsResponse | null>(null);
  const [resetHidden, setResetHidden] = useState(false);
  const [resetLoading, setResetLoading] = useState(false);
  const [resetError, setResetError] = useState("");
  const [managing, setManaging] = useState(false);
  const [manageLoading, setManageLoading] = useState(false);
  const [manageSaving, setManageSaving] = useState(false);
  const [manageError, setManageError] = useState("");
  const [manageRevision, setManageRevision] = useState("");
  const [manageRows, setManageRows] = useState<InstanceRow[]>([]);

  const report = accountsQuery.data as CPAAccountsResponse | undefined;
  const instances = report?.instances ?? [];
  const subscriptions = report?.subscriptions ?? [];
  // 冻结列表随账号资源响应一起回来（顶层 freezes），不再单独请求一次；逐 Key 的徽标/明细
  // 按 provider_id + key_name 索引出来。顶层数组缺省（后端过渡版本）时退回逐条字段汇总，
  // 不让「有冻结徽标但计数为 0」这种自相矛盾的画面出现。
  const freezes = useMemo(() => {
    if (report?.freezes) return report.freezes;
    const fallback = new Map<string, QuotaFreeze>();
    for (const entry of report?.subscriptions ?? []) {
      if (entry.freeze) fallback.set(freezeLookupKey(entry.freeze.provider_id, entry.freeze.key_name), entry.freeze);
    }
    for (const instance of report?.instances ?? []) {
      for (const item of instance.frozen_keys ?? []) {
        fallback.set(freezeLookupKey(item.provider_id, item.key_name), {
          provider_id: item.provider_id,
          key_name: item.key_name,
        });
      }
    }
    return [...fallback.values()];
  }, [report]);
  const freezeIndex = useMemo(() => indexQuotaFreezes(freezes), [freezes]);
  // 顶层 freezes 已包含全部冻结；订阅条目里的 freeze 再合并一次，兼容顶层数组暂缺的
  // 过渡版本。expires_at 是唯一会触发自动刷新的时间，不能用 reset_at 代替。
  const freezeDeadlines = useMemo(() => {
    const list = [
      ...(report?.freezes ?? []),
      ...(report?.subscriptions ?? []).flatMap((entry) => entry.freeze ? [entry.freeze] : []),
    ];
    return list.map((freeze) => freeze.expires_at);
  }, [report?.freezes, report?.subscriptions]);
  useDeadlineRefresh(freezeDeadlines, () => {
    // 到点徽标必须自己消失：不依赖用户点刷新，也不改成全局轮询。
    void accountsQuery.refetch();
  });
  const guard = report?.guard;
  const accounts = useMemo(() => instances.flatMap((instance) => instance.accounts ?? []), [instances]);
  const readableInstances = instances.filter((instance) => instance.ok !== false);
  const usableAccounts = accounts.filter((account) => !account.disabled && !account.unavailable);
  const lowAccounts = accounts.filter((account) => {
    const lowest = lowestRemaining(account);
    return lowest !== null && lowest <= 0.2;
  });
  const lowSubscriptions = subscriptions.filter((entry) => {
    const lowest = lowestRemaining(entry);
    return lowest !== null && lowest <= 0.2;
  });
  const alertCount = lowAccounts.length + lowSubscriptions.length;
  const drainedCount = [...lowAccounts, ...lowSubscriptions].filter((entry) => {
    const lowest = lowestRemaining(entry);
    return lowest !== null && lowest <= 0.05;
  }).length;

  const filteredInstances = useMemo(() => {
    const query = keyword.trim().toLowerCase();
    return instances.map((instance) => ({
      ...instance,
      originalAccountCount: (instance.accounts ?? []).length,
      accounts: (instance.accounts ?? []).filter((account) => {
        if (statusFilter === "usable" && (account.disabled || account.unavailable)) return false;
        if (statusFilter === "cooling" && !account.disabled && !account.unavailable) return false;
        if (statusFilter === "alert") {
          const lowest = lowestRemaining(account);
          if (lowest === null || lowest > 0.2) return false;
        }
        if (!query) return true;
        return [account.label, account.name, account.email, account.provider, account.project_id, account.plan, account.tier_id]
          .filter(Boolean)
          .join(" ")
          .toLowerCase()
          .includes(query);
      }),
    }));
  }, [instances, keyword, statusFilter]);
  const shownAccounts = filteredInstances.reduce((sum, instance) => sum + instance.accounts.length, 0);

  async function fetchResetCredits() {
    try {
      setResetData(await api.cpaResetCredits());
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        setResetHidden(true);
        return;
      }
      setResetError(errorText(error));
      toast.error(`读取重置卡失败：${errorText(error)}`);
    }
  }

  async function loadResetCredits() {
    if (resetLoading) return;
    setResetLoading(true);
    setResetError("");
    try {
      await fetchResetCredits();
    } finally {
      setResetLoading(false);
    }
  }

  async function useResetCredit(instance: CPAAccountInstance, account: CPAAccount) {
    const name = account.label || account.name || "该账号";
    const ok = await confirm({
      title: "使用一张重置卡",
      message: `将为 ${name} 使用一张重置卡。该操作会真实改变账号状态，默认使用最快过期的一张；确定继续吗？`,
      confirmLabel: "使用一张",
      danger: true,
    });
    if (!ok) return;
    setResetLoading(true);
    try {
      await api.useCpaResetCredit({
        instance: instance.id,
        authIndex: String(account.auth_index ?? ""),
      });
      toast.success("重置卡已使用。");
      await Promise.all([fetchResetCredits(), accountsQuery.refetch()]);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        setResetHidden(true);
      } else {
        toast.error(`使用重置卡失败：${errorText(error)}`);
      }
    } finally {
      setResetLoading(false);
    }
  }

  async function refreshAccounts() {
    const restoreSearchFocus = Boolean(keyword.trim());
    await accountsQuery.refetch();
    // 刷新期间页面会重绘；搜索框有内容时把焦点还回去，避免用户继续输入时发现光标跑了。
    if (restoreSearchFocus) requestAnimationFrame(() => searchRef.current?.focus());
  }

  async function openManage() {
    setManaging(true);
    setManageLoading(true);
    setManageError("");
    try {
      const listed = await api.cpaInstances();
      setManageRevision(listed.config_revision ?? "");
      setManageRows(normalizeInstances(listed.instances));
    } catch (error) {
      setManageError(errorText(error));
    } finally {
      setManageLoading(false);
    }
  }

  async function saveManage() {
    if (manageSaving) return;
    const object: Record<string, { label: string; base_url: string; management_key: string }> = {};
    for (const row of manageRows) {
      const id = row.id.trim();
      if (!id) {
        setManageError("实例 ID 不能为空。");
        return;
      }
      if (object[id]) {
        setManageError(`实例 ID 重复：${id}`);
        return;
      }
      object[id] = {
        label: row.label.trim(),
        base_url: row.base_url.trim(),
        management_key: row.management_key.trim(),
      };
    }
    setManageSaving(true);
    setManageError("");
    try {
      await api.saveCPAInstances(manageRevision, object);
      setManaging(false);
      toast.success("实例已保存。");
      await accountsQuery.refetch();
    } catch (error) {
      setManageError(error instanceof ApiError && error.status === 409
        ? "配置已被其它改动更新，请重新打开本窗口后再保存。"
        : errorText(error));
    } finally {
      setManageSaving(false);
    }
  }

  // 手工解冻：DELETE 只解除本次冻结并附抑制窗口，不写配置；成功后账号资源与
  // 冻结列表两个查询一起失效（供应商页共用同一份冻结列表缓存）。
  async function releaseFreeze(freeze: Pick<QuotaFreeze, "provider_id" | "key_name">) {
    const id = freezeLookupKey(freeze.provider_id, freeze.key_name);
    if (releasing) return;
    setReleasing(id);
    try {
      const result = await api.releaseQuotaFreeze(freeze.provider_id, freeze.key_name);
      const suppressed = result.suppressed_until ? `，抑制至 ${formatDateTime(result.suppressed_until)}` : "";
      toast.success(`已解除 ${freeze.provider_id} · ${freeze.key_name} 的额度冻结${suppressed}。`);
      await invalidateFreezeViews(client);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        // 冻结已被别的路径解除（到期自动解冻/别处手工解冻）：刷新一下追上真实状态。
        toast("该 Key 当前没有冻结，已刷新列表。");
        await invalidateFreezeViews(client);
      } else {
        toast.error(`解除额度冻结失败：${errorText(error)}`);
      }
    } finally {
      setReleasing(null);
    }
  }

  const pageHead = (
    <PageHeader
      title="账号资源"
      description={`账号资源：CPA 实例的账号，以及 OpenCode Go、Command Code 这类订阅的剩余额度。读数由服务端代问，页面本身不轮询。${guardDescription(guard)}`}
      actions={(
        <>
          {report?.fetched_at ? <span className="hidden text-meta text-ink-3 sm:inline">读取于 {formatDateTime(report.fetched_at)}</span> : null}
          {accountsQuery.isFetching && report ? <span className="text-meta text-ink-3">正在刷新…</span> : null}
          <Button size="sm" variant="secondary" loading={accountsQuery.isFetching} onClick={() => void refreshAccounts()}>
            刷新
          </Button>
          <Button size="sm" onClick={() => void openManage()}>管理实例</Button>
        </>
      )}
    />
  );

  if (accountsQuery.isPending && !report) {
    return (
      <div className="flex min-w-0 flex-col gap-3">
        {pageHead}
        <Skeleton variant="table" className="h-32" />
      </div>
    );
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      {pageHead}

      {accountsQuery.error ? (
        <Banner tone="critical" title="读取账号资源失败">{errorText(accountsQuery.error)}</Banner>
      ) : null}
      {resetError ? <Banner tone="critical" title="读取重置卡失败">{resetError}</Banner> : null}

      <MetricStrip className="cols-5">
        <MetricCell
          className="stat"
          label="实例"
          value={formatCount(instances.length)}
          hint={readableInstances.length < instances.length ? `${instances.length - readableInstances.length} 个读取失败` : "全部可读"}
          tone={readableInstances.length < instances.length ? "critical" : "neutral"}
        />
        <MetricCell className="stat" label="订阅" value={formatCount(subscriptions.length)} hint={subscriptions.length ? `${subscriptions.filter((entry) => entry.ok).length} 条可读` : "未配置订阅端点"} />
        <MetricCell className="stat" label="账号" value={formatCount(accounts.length)} hint={`${usableAccounts.length} 可用 · ${accounts.length - usableAccounts.length} 停用/冷却`} />
        <MetricCell
          className="stat"
          label="额度告警"
          value={formatCount(alertCount)}
          hint={alertCount ? `其中 ${drainedCount} 个已用尽` : "都还有 20% 以上"}
          tone={drainedCount ? "critical" : alertCount ? "warn" : "neutral"}
        />
        <MetricCell
          className="stat"
          label="额度冻结"
          value={formatCount(freezes.length)}
          hint={freezes.length
            ? "对应 Key 已从选路里摘出"
            : guard?.enabled === false ? "后台自动检测已关闭" : "当前没有 Key 被冻结"}
          tone={freezes.length ? "critical" : "neutral"}
        />
      </MetricStrip>

      <div className="flex flex-wrap items-center gap-2 rounded-panel border border-rule bg-panel px-3 py-2">
        <Input
          ref={searchRef}
          type="search"
          size="sm"
          className="w-[240px]"
          placeholder="搜索账号、邮箱、供应商或项目…"
          aria-label="搜索账号、邮箱、供应商或项目"
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
        />
        <Segmented
          aria-label="筛选账号状态"
          value={statusFilter}
          onValueChange={(value) => setStatusFilter(value as StatusFilter)}
          options={STATUS_FILTERS.map((option) => ({
            ...option,
            label: option.value === "all" ? `${option.label} (${accounts.length})` : option.label,
          }))}
        />
        <span className="ml-auto text-meta text-ink-3">显示 {shownAccounts} / {accounts.length} 个账号</span>
      </div>

      {!instances.length && !subscriptions.length ? (
        <Panel>
          <Empty
            title="还没有账号资源"
            description="填一个 CLIProxyAPI 的地址与管理密钥，或者配一个 OpenCode Go / Command Code 供应商——订阅会按端点自动出现在这里。"
            action={<Button onClick={() => void openManage()}>添加实例</Button>}
          />
        </Panel>
      ) : (
        <>
          {subscriptions.map((entry) => (
            <SubscriptionCard
              key={entry.id || `${entry.provider_id}-${entry.key_name}`}
              entry={entry}
              now={now}
              releasingFreeze={releaseFreeze}
              releasingKey={releasing}
            />
          ))}

          {filteredInstances.map((instance) => {
            const totalCalls = (instance.accounts ?? []).reduce((sum, account) => sum + (Number(account.success) || 0) + (Number(account.failed) || 0), 0);
            const totalSuccess = (instance.accounts ?? []).reduce((sum, account) => sum + (Number(account.success) || 0), 0);
            const usable = (instance.accounts ?? []).filter((account) => !account.disabled && !account.unavailable).length;
            const low = (instance.accounts ?? []).filter((account) => {
              const lowest = lowestRemaining(account);
              return lowest !== null && lowest <= 0.2;
            }).length;
            const offset = (instance.accounts ?? []).find((account) => account.server_time_offset_ms !== undefined)?.server_time_offset_ms;
            // 实例级联动：全部启用账号都硬性耗尽或都低于底限时，指向该实例的每一把 Key 都会被冻结。
            const frozenKeys = instance.frozen_keys ?? [];
            const linkedProviders = instance.linked_providers ?? [];
            const allExhausted = Boolean(instance.all_accounts_exhausted);
            const allBelowFloor = Boolean(instance.all_accounts_below_floor);
            return (
              <Panel
                key={instance.id}
                title={instance.label || instance.id}
                description={instance.base_url || "未填写地址"}
                actions={(
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge tone={instance.ok === false ? "critical" : "neutral"}>
                      {instance.ok === false ? "读取失败" : `${instance.originalAccountCount ?? instance.accounts?.length ?? 0} 个账号`}
                    </Badge>
                    {linkedProviders.length ? (
                      <Tooltip
                        content={(
                          <div className="space-y-0.5">
                            <div>地址指向这个实例的供应商：</div>
                            {linkedProviders.map((id) => <div key={id} className="font-mono">{id}</div>)}
                          </div>
                        )}
                      >
                        <span tabIndex={0}><Badge tone="neutral">关联 {linkedProviders.length} 个供应商</Badge></span>
                      </Tooltip>
                    ) : null}
                    {instance.observed_at ? <span className="text-[11px] text-ink-3">CPA 观测于 {formatDateTime(instance.observed_at)}</span> : null}
                  </div>
                )}
                padding={false}
              >
                {instance.error ? (
                  <div className="p-4"><Banner tone="critical">{instance.error}</Banner></div>
                ) : (
                  <>
                    {allExhausted || allBelowFloor ? (
                      <div className="border-b border-rule px-4 py-3">
                        <Banner
                          tone={allExhausted ? "critical" : "warn"}
                          title={allExhausted
                            ? `实例内全部账号额度均已用尽，已冻结指向该实例的 ${frozenKeys.length} 把 Key`
                            : `实例内全部账号额度均已低于冻结底限，已冻结指向该实例的 ${frozenKeys.length} 把 Key`}
                        >
                          {frozenKeys.length ? (
                            <ul className="space-y-1.5">
                              {frozenKeys.map((item) => {
                                const id = freezeLookupKey(item.provider_id, item.key_name);
                                const detail = freezeIndex.get(id);
                                const freezeCountdown = detail ? freezeResetCountdown(detail, now) : "";
                                return (
                                  <li key={id} className="flex flex-wrap items-center justify-between gap-2">
                                    <span className="min-w-0 truncate font-mono text-[11px]" title={detail ? freezeReasonText(detail) : undefined}>
                                      {item.provider_id} · {item.key_name}
                                      {freezeCountdown ? <span className="ml-1.5 font-sans text-ink-3">（{freezeCountdown}）</span> : null}
                                    </span>
                                    <Button
                                      size="sm"
                                      variant="secondary"
                                      loading={releasing === id}
                                      onClick={() => void releaseFreeze({ provider_id: item.provider_id, key_name: item.key_name })}
                                    >
                                      解冻
                                    </Button>
                                  </li>
                                );
                              })}
                            </ul>
                          ) : <p>冻结明细还没回传，刷新一次即可看到逐把 Key。</p>}
                        </Banner>
                      </div>
                    ) : null}
                    <div className="grid gap-3 border-b border-rule bg-sunken/40 px-4 py-3 text-meta sm:grid-cols-2 xl:grid-cols-5">
                      <div className="min-w-0">
                        <div className="text-ink-3">实例端点</div>
                        <div className="mt-1"><CopyChip text={instance.base_url || "-"} label="点击复制 CPA 实例地址" /></div>
                      </div>
                      <div><div className="text-ink-3">账号可用率</div><div className="mt-1 text-ink">{usable} / {instance.accounts?.length ?? 0} 可用</div></div>
                      <div><div className="text-ink-3">调度总量</div><div className="mt-1 text-ink">{formatCount(totalCalls)} 次（{formatPercent(totalSuccess, totalCalls)} 成功）</div></div>
                      <div><div className="text-ink-3">额度健康</div><div className="mt-1 text-ink">{low ? `${low} 个账号告警` : "全部充足"}</div></div>
                      {offset !== undefined ? <div><div className="text-ink-3">时钟同步</div><div className="mt-1 text-ink">{offset}ms（{Math.abs(Number(offset)) < 2000 ? "同步良好" : "时差较大"}）</div></div> : null}
                    </div>
                    {(instance.originalAccountCount ?? instance.accounts?.length ?? 0) === 0 ? (
                      <div className="p-4"><Empty title="这个实例上还没有账号。" /></div>
                    ) : (instance.accounts ?? []).length === 0 ? (
                      <div className="p-4"><Empty title="没有找到匹配的账号。" /></div>
                    ) : (
                      <div className="account-cards-grid grid gap-3 p-4 md:grid-cols-2 xl:grid-cols-3">
                        {(instance.accounts ?? []).map((account, index) => {
                          const authIndex = account.auth_index === undefined || account.auth_index === null ? "" : String(account.auth_index);
                          const title = account.label || account.name || "未命名账号";
                          const total = (Number(account.success) || 0) + (Number(account.failed) || 0);
                          const status = statusBadge(account);
                          const resetInfo = resetInfoFor(resetData, instance, account);
                          const supported = ["codex", "claude"].includes(String(account.provider || "").toLowerCase());
                          return (
                            <article key={`${instance.id}-${authIndex || index}`} className="account-card flex min-w-0 flex-col gap-4 rounded-panel border border-rule bg-panel p-3">
                              <div className="flex items-start justify-between gap-2">
                                <div className="min-w-0">
                                  <h3 className="truncate text-body font-semibold text-ink">{title}</h3>
                                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                                    {authIndex ? <span className="rounded-chip bg-sunken px-1.5 py-0.5 font-mono text-[11px] text-ink-3">#{authIndex}</span> : null}
                                    <Badge tone="neutral">{account.provider || "未知"}</Badge>
                                    <span title={account.status_message || undefined}><Badge tone={status.tone}>{status.text}</Badge></span>
                                    {account.quota_exhausted ? (
                                      <span title="该账号至少有一个硬性耗尽的额度窗口；不是配置里的停用">
                                        <Badge tone="critical">额度用尽</Badge>
                                      </span>
                                    ) : null}
                                    {account.quota_below_floor ? (
                                      <span title="该账号至少有一个窗口的剩余额度低于配置的 5h/7d 底限；不是配置里的停用">
                                        <Badge tone="warn">低于底限</Badge>
                                      </span>
                                    ) : null}
                                  </div>
                                </div>
                              </div>

                              {/* 邮箱/名称/档位一行；授权与项目另起一行——project_id 可能是长串，
                                  和文件标识挤在同一行会把卡片撑破。长串在自己的徽标里截断，完整值在 title 里。 */}
                              <div className="flex flex-wrap items-center gap-1.5">
                                {account.email && account.email !== title ? <span className="text-meta text-ink-3">{account.email}</span> : null}
                                {account.name && account.name !== title ? <span className="text-meta text-ink-3">{account.name}</span> : null}
                                {account.plan ? <Badge tone="neutral">{account.plan}</Badge> : null}
                                {account.tier_id ? <Badge tone="neutral">{account.tier_id}</Badge> : null}
                                {account.account_type || account.project_id ? (
                                  <div className="flex w-full min-w-0 flex-wrap items-center gap-1.5">
                                    {account.account_type ? <Badge tone="neutral">授权 {account.account_type}</Badge> : null}
                                    {account.project_id ? (
                                      <Badge tone="neutral" className="min-w-0 max-w-full">
                                        <span className="truncate" title={`项目 ${account.project_id}`}>项目 {account.project_id}</span>
                                      </Badge>
                                    ) : null}
                                  </div>
                                ) : null}
                              </div>

                              <QuotaSection account={account} now={now} />

                              <div className="space-y-1">
                                <div className="flex items-center justify-between text-meta">
                                  <span className="text-ink-3">调用统计（{formatCount(total)} 次 · 成功率 {formatPercent(account.success, total)}）</span>
                                  <span className="text-ink-2">
                                    <span className="text-good">✓ {formatCount(account.success)} 成功</span>
                                    <span className="mx-1 text-ink-3">/</span>
                                    <span className={Number(account.failed) > 0 ? "text-critical-ink" : "text-ink-3"}>✗ {formatCount(account.failed)} 失败</span>
                                  </span>
                                </div>
                                <ProgressBar
                                  value={total ? (Number(account.success) || 0) / total * 100 : 0}
                                  tone={(Number(account.failed) || 0) > 0 ? "warn" : "good"}
                                  aria-label={`${title} 调用成功率`}
                                />
                                <RecentBars buckets={account.recent_requests} />
                              </div>

                              {account.signals && Object.keys(account.signals).length ? (
                                <details className="text-meta text-ink-3">
                                  <summary className="cursor-pointer">原始信号 · {Object.keys(account.signals).length} 项</summary>
                                  <div className="mt-1 space-y-0.5 font-mono text-[11px]">
                                    {Object.entries(account.signals).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => (
                                      <div key={name}>{name}: {String(value)}</div>
                                    ))}
                                  </div>
                                </details>
                              ) : null}

                              {supported && !resetHidden ? (
                                <div className="rounded-control border border-rule bg-sunken/40 p-2.5">
                                  <div className="flex flex-wrap items-center justify-between gap-2">
                                    <div className="min-w-0">
                                      <div className="text-[11px] text-ink-3">重置卡</div>
                                      {resetInfo ? (
                                        <div className="mt-0.5 text-meta text-ink-2">
                                          可用 {formatCount(resetInfo.available_count ?? 0)} 次重置（{formatCount(resetInfo.credits?.length ?? 0)} 张卡） · {nearestExpiry(resetInfo)}
                                          {resetInfo.redeemable_count === 0 ? <span className="text-ink-3">（当前不可兑换）</span> : null}
                                          {resetInfo.cooldown_until ? <span className="text-ink-3"> · 冷却至 {formatDateTime(resetInfo.cooldown_until)}</span> : null}
                                          {resetInfo.reason ? <span className="text-ink-3"> · {resetInfo.reason}</span> : null}
                                        </div>
                                      ) : (
                                        <div className="mt-0.5 text-meta text-ink-3">按需读取，避免频繁访问上游</div>
                                      )}
                                      {resetInfo?.error ? <div className="mt-0.5 text-[11px] text-critical-ink">{resetInfo.error}</div> : null}
                                    </div>
                                    {resetInfo ? (
                                      <Button
                                        size="sm"
                                        variant="danger-ghost"
                                        disabled={resetLoading || resetInfo.eligible === false || (resetInfo.redeemable_count ?? resetInfo.available_count ?? 0) <= 0}
                                        loading={resetLoading}
                                        onClick={() => void useResetCredit(instance, account)}
                                      >
                                        使用一张
                                      </Button>
                                    ) : (
                                      <Button size="sm" variant="secondary" loading={resetLoading} onClick={() => void loadResetCredits()}>
                                        查看重置卡
                                      </Button>
                                    )}
                                  </div>
                                </div>
                              ) : null}
                            </article>
                          );
                        })}
                      </div>
                    )}
                  </>
                )}
              </Panel>
            );
          })}
        </>
      )}

      <Sheet
        open={managing}
        onOpenChange={setManaging}
        title="管理 CPA 实例"
        subtitle="实例保存在 AMKR 配置文件的 cpa_instances 里；管理密钥是 CPA 管理面的密钥，不是模型调用 key。"
        width="lg"
        footer={(
          <>
            <Button variant="secondary" onClick={() => setManaging(false)}>取消</Button>
            <Button variant="primary" loading={manageSaving} disabled={manageLoading} onClick={() => void saveManage()}>保存</Button>
          </>
        )}
      >
        {manageLoading ? <Skeleton variant="table" /> : (
          <div className="space-y-3">
            {manageError ? <Banner tone="critical">{manageError}</Banner> : null}
            {!manageRows.length ? <Banner tone="info">还没有实例。填一个 CPA 的地址与管理密钥，保存后看板就会列出它的账号。</Banner> : null}
            {manageRows.map((row, index) => (
              <div key={`${row.id}-${index}`} className="instance-editor space-y-2 rounded-control border border-rule p-3">
                <div className="grid gap-2 sm:grid-cols-2">
                  <Field label="实例 ID"><Input value={row.id} placeholder="cpa-a" onChange={(event) => setManageRows((current) => current.map((item, i) => i === index ? { ...item, id: event.target.value } : item))} /></Field>
                  <Field label="名称"><Input value={row.label} placeholder="主力 CPA" onChange={(event) => setManageRows((current) => current.map((item, i) => i === index ? { ...item, label: event.target.value } : item))} /></Field>
                  <Field label="地址"><Input value={row.base_url} placeholder="http://127.0.0.1:8317" onChange={(event) => setManageRows((current) => current.map((item, i) => i === index ? { ...item, base_url: event.target.value } : item))} /></Field>
                  <Field label="管理密钥"><Input type="password" autoComplete="off" value={row.management_key} onChange={(event) => setManageRows((current) => current.map((item, i) => i === index ? { ...item, management_key: event.target.value } : item))} /></Field>
                </div>
                <Button size="sm" variant="ghost" onClick={() => setManageRows((current) => current.filter((_, i) => i !== index))}>移除</Button>
              </div>
            ))}
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setManageRows((current) => [...current, { id: "", label: "", base_url: "", management_key: "" }])}
            >
              添加实例
            </Button>
          </div>
        )}
      </Sheet>
    </div>
  );
}
