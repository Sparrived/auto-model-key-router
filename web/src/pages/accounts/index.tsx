// 账号资源：CPA 实例的账号与订阅剩余额度看板。
//
// 口径（与 legacy accounts.js 一致）：
// - 浏览器不直接访问 CPA，所有读数走 /api/cpa-accounts 的服务端扇出；
// - 进页只取账号读数，管理密钥只在打开「管理实例」时读取；
// - 每 60 秒自动刷新一次，后台标签页暂停；手动刷新或冻结到期另外触发，一次刷新只发一次账号请求；
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
import { ChevronDown, MoreHorizontal } from "lucide-react";
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
  QuotaFreezePolicy,
} from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { DropdownMenu, DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/DropdownMenu";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { Meter } from "@/components/charts";
import { QuotaFreezeConfigSheet } from "./quota-freeze-config";
import { QuotaFreezePolicySheet } from "./quota-freeze-policy";
import { CPARuntimeConfigSheet } from "./cpa-runtime-config";
import { CPAAccountConfigSheet } from "./cpa-account-config";

const AUTO_REFRESH_MS = 60_000;
const FOCUS_REFRESH_STALE_MS = 30_000;

type StatusFilter = "all" | "usable" | "attention";

interface InstanceRow {
  id: string;
  label: string;
  base_url: string;
  management_key: string;
}

interface PolicyTarget {
  scope: QuotaFreezePolicy["scope"];
  target: string;
  title: string;
}

interface AccountConfigTarget {
  instance: CPAAccountInstance;
  account: CPAAccount;
}

function findAccountPolicy(
  policies: QuotaFreezePolicy[] | undefined,
  scope: QuotaFreezePolicy["scope"],
  target: string,
): QuotaFreezePolicy | undefined {
  return (policies ?? []).find((policy) => policy.scope === scope && policy.target === target);
}

function PolicyBadge({ policy, available }: { policy?: QuotaFreezePolicy; available: boolean }) {
  if (!available) return null;
  if (!policy) return <Badge tone="neutral">继承全局</Badge>;
  if (policy.enabled === false) return <Badge tone="warn">已关闭自动冻结</Badge>;
  return <Badge tone="accent">自定义</Badge>;
}

const STATUS_FILTERS: Array<{ value: StatusFilter; label: string }> = [
  { value: "all", label: "全部" },
  { value: "usable", label: "可用" },
  { value: "attention", label: "需处理" },
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

// 「需处理」同时覆盖停用、冷却与低额度三类事实；计数与筛选共用同一份判定，
// 避免两处条件漂移后出现「标签显示 3 个、筛出来 2 个」。
function needsAttention(account: CPAAccount): boolean {
  const lowest = lowestRemaining(account);
  return Boolean(account.disabled || account.unavailable || account.quota_exhausted || account.quota_below_floor)
    || (lowest !== null && lowest <= 0.2);
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

// 最近调用分布来自 CPA 随 auth-files 一起回的十分钟桶，不额外发请求；默认收在诊断信息里。
function RecentBars({ buckets }: { buckets: CPAAccount["recent_requests"] }) {
  const list = buckets ?? [];
  if (!list.length) return null;
  // 不筛掉空桶：时间片必须等距，否则一次静默的间隔会让相邻柱看起来更密。
  const peak = Math.max(...list.map((bucket) => (Number(bucket.success) || 0) + (Number(bucket.failed) || 0)), 1);
  return (
    <div className="space-y-1">
      <div className="font-sans">最近调用（{list.length} 个 10 分钟桶）</div>
      <div className="flex h-5 items-end gap-1" aria-label="最近调用分布">
        {list.map((bucket, index) => {
          const success = Number(bucket.success) || 0;
          const failed = Number(bucket.failed) || 0;
          const height = Math.max(3, Math.round(((success + failed) / peak) * 18));
          const tone = failed ? "bg-critical" : success ? "bg-accent" : "bg-rule-strong";
          return (
            <div
              key={`${bucket.time ?? index}-${index}`}
              className={`w-1.5 rounded-[2px] ${tone}`}
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
  onConfigurePolicy: (() => void) | null;
  policy?: QuotaFreezePolicy;
  policyAvailable: boolean;
}

function SubscriptionCard({
  entry,
  now,
  releasingFreeze,
  releasingKey,
  onConfigurePolicy,
  policy,
  policyAvailable,
}: SubscriptionCardProps) {
  const windows = entry.windows ?? [];
  const freeze = entry.freeze;
  const freezeId = freeze ? freezeLookupKey(freeze.provider_id, freeze.key_name) : "";
  const canRelease = Boolean(freeze?.provider_id && freeze?.key_name && releasingFreeze);
  const freezeCountdown = freeze ? freezeResetCountdown(freeze, now) : "";
  const threshold = freeze?.trigger === "threshold";
  return (
    <Panel
      title={entry.vendor || entry.kind || entry.id || "订阅"}
      description={[entry.key_name ? `${entry.provider_id} · ${entry.key_name}` : entry.provider_id, entry.account ? `账号 ${entry.account}` : null, entry.plan || entry.tier_id ? `档位 ${[entry.plan, entry.tier_id].filter(Boolean).join(" / ")}` : null].filter(Boolean).join(" · ")}
      actions={(
        <div className="flex flex-wrap items-center gap-2">
          {freeze ? <Badge tone={threshold ? "warn" : "critical"} dot>{freezeTriggerText(freeze)}</Badge> : null}
          <PolicyBadge policy={policy} available={policyAvailable} />
          <Button
            size="sm"
            variant="secondary"
            disabled={!onConfigurePolicy}
            onClick={() => onConfigurePolicy?.()}
          >
            冻结规则
          </Button>
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
    // 自动刷新只替换查询数据；后台标签页暂停，切回前台时补刷一次。
    // 每次刷新都是对 CPA 与上游的一次真实扇出：读数 30 秒内视为新鲜，来回切标签页
    // 不会连发请求。staleTime 只拦切回前台这一路，周期刷新、手动刷新与冻结到期不受影响。
    retry: false,
    staleTime: FOCUS_REFRESH_STALE_MS,
    refetchInterval: AUTO_REFRESH_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings(),
    queryFn: api.settings,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const client = useQueryClient();
  // 倒计时只重算本地时间；60 秒周期刷新之外，到最近一条 expires_at 还会额外 refetch 一次。
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
  const [configOpen, setConfigOpen] = useState(false);
  const [policyTarget, setPolicyTarget] = useState<PolicyTarget | null>(null);
  const [runtimeConfigTarget, setRuntimeConfigTarget] = useState<CPAAccountInstance | null>(null);
  const [accountConfigTarget, setAccountConfigTarget] = useState<AccountConfigTarget | null>(null);
  const [accountAction, setAccountAction] = useState("");
  const [manageLoading, setManageLoading] = useState(false);
  const [manageSaving, setManageSaving] = useState(false);
  const [manageError, setManageError] = useState("");
  const [manageRevision, setManageRevision] = useState("");
  const [manageRows, setManageRows] = useState<InstanceRow[]>([]);

  const report = accountsQuery.data as CPAAccountsResponse | undefined;
  const instances = report?.instances ?? [];
  const subscriptions = report?.subscriptions ?? [];
  const settings = settingsQuery.data?.settings;
  const policies = settings?.quota_freeze_policies;
  const policyAvailable = Boolean(settings);

  function openPolicy(scope: QuotaFreezePolicy["scope"], target: string, title: string) {
    if (!target) return;
    setPolicyTarget({ scope, target, title });
  }
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
  // 过渡版本。expires_at 是冻结到期额外补刷的时间，不能用 reset_at 代替。
  const freezeDeadlines = useMemo(() => {
    const list = [
      ...(report?.freezes ?? []),
      ...(report?.subscriptions ?? []).flatMap((entry) => entry.freeze ? [entry.freeze] : []),
    ];
    return list.map((freeze) => freeze.expires_at);
  }, [report?.freezes, report?.subscriptions]);
  useDeadlineRefresh(freezeDeadlines, () => {
    // 到点徽标必须自己消失：周期刷新之外，expires_at 到点再补一次 refetch。
    void accountsQuery.refetch();
  });
  const guard = report?.guard;
  const accounts = useMemo(() => instances.flatMap((instance) => instance.accounts ?? []), [instances]);
  const readableInstances = instances.filter((instance) => instance.ok !== false);
  const usableAccounts = accounts.filter((account) => !account.disabled && !account.unavailable);
  const attentionAccountCount = accounts.filter(needsAttention).length;
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
        if (statusFilter === "attention" && !needsAttention(account)) return false;
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
      // 删除实例会连带清理指向它的账号级冻结策略；同步刷新页面上的策略徽标与设置缓存。
      await Promise.all([
        accountsQuery.refetch(),
        client.invalidateQueries({ queryKey: queryKeys.settings() }),
      ]);
    } catch (error) {
      setManageError(error instanceof ApiError && error.status === 409
        ? "配置已被其它改动更新，请重新打开本窗口后再保存。"
        : errorText(error));
    } finally {
      setManageSaving(false);
    }
  }

  async function changeAccountStatus(instance: CPAAccountInstance, account: CPAAccount) {
    const authIndex = String(account.auth_index ?? "");
    if (!authIndex || !account.name || accountAction) return;
    const disabled = !account.disabled;
    if (disabled) {
      const accepted = await confirm({
        title: "停用 CPA 账号",
        message: "将停用 " + (account.label || account.name) + "。该账号会立即退出 CPA 自己的候选池；这不是 AMKR 的额度冻结，也不会修改 AMKR 的供应商 Key 配置。",
        confirmLabel: "停用账号",
        danger: true,
      });
      if (!accepted) return;
    }
    const actionId = instance.id + ":" + authIndex + ":status";
    setAccountAction(actionId);
    try {
      await api.setCPAAccountStatus(instance.id, authIndex, account.name, disabled);
      toast.success(disabled ? "CPA 账号已停用。" : "CPA 账号已启用。");
      await accountsQuery.refetch();
    } catch (error) {
      toast.error("修改 CPA 账号状态失败：" + errorText(error));
    } finally {
      setAccountAction("");
    }
  }

  async function resetAccountRouting(instance: CPAAccountInstance, account: CPAAccount) {
    const authIndex = String(account.auth_index ?? "");
    if (!authIndex || accountAction) return;
    const accepted = await confirm({
      title: "清除 CPA 冷却与路由状态",
      message: "将仅清除 " + (account.label || account.name || authIndex) + " 在 CPA 中的 quota/cooldown 路由状态。这不会解除 AMKR 额度冻结、不修改 AMKR 供应商 Key，也不会恢复上游真实额度；若上游仍限流，CPA 可能再次进入冷却。",
      confirmLabel: "清除冷却状态",
      danger: true,
    });
    if (!accepted) return;
    const actionId = instance.id + ":" + authIndex + ":reset";
    setAccountAction(actionId);
    try {
      await api.resetCPAAccountRouting(instance.id, authIndex);
      toast.success("CPA 冷却与路由状态已清除。");
      await accountsQuery.refetch();
    } catch (error) {
      toast.error("清除 CPA 冷却状态失败：" + errorText(error));
    } finally {
      setAccountAction("");
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
      description="集中查看账号状态与剩余额度，每 60 秒自动刷新；配置与冻结规则按需处理。"
      actions={(
        <>
          {accountsQuery.isFetching && report ? <span className="text-meta text-ink-3">正在刷新…</span> : null}
          <Button size="sm" variant="secondary" loading={accountsQuery.isFetching} onClick={() => void refreshAccounts()}>
            刷新
          </Button>
          <Button size="sm" variant="secondary" onClick={() => setConfigOpen(true)}>默认冻结规则</Button>
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

      <MetricStrip>
        <MetricCell
          className="stat"
          label="可用账号"
          value={`${usableAccounts.length} / ${accounts.length}`}
          hint={`${instances.length} 个实例 · ${subscriptions.length} 条订阅${readableInstances.length < instances.length ? ` · ${instances.length - readableInstances.length} 个实例读取失败` : ""}`}
          tone={readableInstances.length < instances.length ? "critical" : "neutral"}
        />
        <MetricCell
          className="stat"
          label="额度告警"
          value={formatCount(alertCount)}
          hint={alertCount ? `其中 ${drainedCount} 个已用尽` : "没有低于 20% 的额度窗口"}
          tone={drainedCount ? "critical" : alertCount ? "warn" : "neutral"}
        />
        <MetricCell
          className="stat"
          label="冻结 Key"
          value={formatCount(freezes.length)}
          hint={freezes.length
            ? "对应 Key 已退出 AMKR 选路"
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
            label: option.value === "all"
              ? `${option.label} (${accounts.length})`
              : option.value === "usable" ? `${option.label} (${usableAccounts.length})` : `${option.label} (${attentionAccountCount})`,
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
          {subscriptions.map((entry) => {
            const target = entry.provider_id && entry.key_name
              ? `${entry.provider_id}/${entry.key_name}`
              : "";
            const policy = target ? findAccountPolicy(policies, "subscription", target) : undefined;
            return (
              <SubscriptionCard
                key={entry.id || `${entry.provider_id}-${entry.key_name}`}
                entry={entry}
                now={now}
                releasingFreeze={releaseFreeze}
                releasingKey={releasing}
                onConfigurePolicy={target
                  ? () => openPolicy("subscription", target, `${entry.vendor || entry.id || target} · 冻结规则`)
                  : null}
                policy={policy}
                policyAvailable={policyAvailable}
              />
            );
          })}

          {filteredInstances.map((instance) => {
            const totalCalls = (instance.accounts ?? []).reduce((sum, account) => sum + (Number(account.success) || 0) + (Number(account.failed) || 0), 0);
            const totalSuccess = (instance.accounts ?? []).reduce((sum, account) => sum + (Number(account.success) || 0), 0);
            const usable = (instance.accounts ?? []).filter((account) => !account.disabled && !account.unavailable).length;
            const low = (instance.accounts ?? []).filter((account) => {
              const lowest = lowestRemaining(account);
              return lowest !== null && lowest <= 0.2;
            }).length;
            const offset = (instance.accounts ?? []).find((account) => account.server_time_offset_ms !== undefined)?.server_time_offset_ms;
            // 实例级联动：全部启用账号都硬性耗尽或都低于底限时，默认会冻结指向该实例的 Key；
            // 若该实例的账号策略关闭了自动冻结，则不展示这条“已冻结”结论。
            const frozenKeys = instance.frozen_keys ?? [];
            const linkedProviders = instance.linked_providers ?? [];
            const allExhausted = Boolean(instance.all_accounts_exhausted);
            const allBelowFloor = Boolean(instance.all_accounts_below_floor);
            const policy = findAccountPolicy(policies, "cpa_instance", instance.id);
            return (
              <Panel
                key={instance.id}
                title={instance.label || instance.id}
                description={`${usable} / ${instance.accounts?.length ?? 0} 个账号可用${low ? ` · ${low} 个额度告警` : " · 额度正常"}`}
                actions={(
                  <div className="flex items-center gap-2">
                    {instance.ok === false ? <Badge tone="critical">读取失败</Badge> : null}
                    <DropdownMenu
                      align="end"
                      trigger={<Button size="sm" variant="secondary" trailingIcon={<ChevronDown aria-hidden className="size-3.5" />}>实例操作</Button>}
                    >
                      <DropdownMenuItem onSelect={() => setRuntimeConfigTarget(instance)}>实例配置</DropdownMenuItem>
                      <DropdownMenuItem onSelect={() => openPolicy("cpa_instance", instance.id, `${instance.label || instance.id} · 冻结规则`)}>
                        冻结规则
                      </DropdownMenuItem>
                    </DropdownMenu>
                  </div>
                )}
                padding={false}
              >
                {instance.error ? (
                  <div className="p-4"><Banner tone="critical">{instance.error}</Banner></div>
                ) : (
                  <>
                    {policy?.enabled !== false && (allExhausted || allBelowFloor) ? (
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
                    <details className="border-b border-rule bg-sunken/30 px-4 py-2.5 text-meta">
                      <summary className="w-fit cursor-pointer text-ink-3">实例详情</summary>
                      <div className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                        <div className="min-w-0"><div className="text-ink-3">实例端点</div><div className="mt-1"><CopyChip text={instance.base_url || "-"} label="点击复制 CPA 实例地址" /></div></div>
                        <div><div className="text-ink-3">冻结策略</div><div className="mt-1"><PolicyBadge policy={policy} available={policyAvailable} /></div></div>
                        <div><div className="text-ink-3">关联供应商</div><div className="mt-1 text-ink">{linkedProviders.length ? `关联 ${linkedProviders.length} 个供应商（${linkedProviders.join("、")}）` : "未关联"}</div></div>
                        <div><div className="text-ink-3">调度统计</div><div className="mt-1 text-ink">{formatCount(totalCalls)} 次 · 成功率 {formatPercent(totalSuccess, totalCalls)}</div></div>
                        {offset !== undefined ? <div><div className="text-ink-3">时钟同步</div><div className="mt-1 text-ink">{offset}ms（{Math.abs(Number(offset)) < 2000 ? "同步良好" : "时差较大"}）</div></div> : null}
                        {instance.observed_at ? <div><div className="text-ink-3">CPA 观测时间</div><div className="mt-1 text-ink">{formatDateTime(instance.observed_at)}</div></div> : null}
                      </div>
                    </details>
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
                            <article key={`${instance.id}-${authIndex || index}`} className="account-card flex min-w-0 flex-col gap-3 rounded-panel border border-rule bg-panel p-3">
                              <div className="flex items-start justify-between gap-2">
                                <div className="min-w-0">
                                  <h3 className="truncate text-body font-semibold text-ink">{title}</h3>
                                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                                    <Badge tone="neutral">{account.provider || "未知"}</Badge>
                                    <span title={account.status_message || undefined}><Badge tone={status.tone}>{status.text}</Badge></span>
                                    {account.quota_exhausted ? <Badge tone="critical">额度用尽</Badge> : null}
                                    {account.quota_below_floor ? <Badge tone="warn">低于底限</Badge> : null}
                                  </div>
                                </div>
                                <DropdownMenu
                                  align="end"
                                  trigger={(
                                    <IconButton
                                      aria-label={`${title} 的账号操作`}
                                      size="sm"
                                      loading={accountAction.startsWith(`${instance.id}:${authIndex}:`)}
                                    >
                                      <MoreHorizontal aria-hidden className="size-4" />
                                    </IconButton>
                                  )}
                                >
                                  <DropdownMenuItem onSelect={() => setAccountConfigTarget({ instance, account })}>账号配置</DropdownMenuItem>
                                  <DropdownMenuSeparator />
                                  <DropdownMenuItem
                                    danger={!account.disabled}
                                    disabled={Boolean(accountAction) || !authIndex || !account.name}
                                    onSelect={() => void changeAccountStatus(instance, account)}
                                  >
                                    {account.disabled ? "启用账号" : "停用账号"}
                                  </DropdownMenuItem>
                                  <DropdownMenuItem disabled={Boolean(accountAction) || !authIndex} onSelect={() => void resetAccountRouting(instance, account)}>
                                    清除冷却
                                  </DropdownMenuItem>
                                </DropdownMenu>
                              </div>

                              {/* 邮箱/名称/档位一行；授权与项目另起一行——project_id 可能是长串，
                                  和文件标识挤在同一行会把卡片撑破。长串在自己的徽标里截断，完整值在 title 里。 */}
                              <div className="flex flex-wrap items-center gap-1.5">
                                {account.email && account.email !== title ? <span className="text-meta text-ink-3">{account.email}</span> : null}
                                {account.name && account.name !== title ? <span className="text-meta text-ink-3">{account.name}</span> : null}
                                {account.note ? <span className="w-full text-meta text-ink-3">{String(account.note)}</span> : null}
                                {account.plan ? <Badge tone="neutral">{account.plan}</Badge> : null}
                                {account.tier_id ? <Badge tone="neutral">{account.tier_id}</Badge> : null}
                              </div>

                              <QuotaSection account={account} now={now} />

                              <details className="text-meta text-ink-3">
                                <summary className="w-fit cursor-pointer">诊断信息</summary>
                                <div className="mt-2 space-y-1.5 rounded-control bg-sunken/50 p-2.5">
                                  {authIndex ? <div>凭据标识：<span className="font-mono">{authIndex}</span></div> : null}
                                  {account.account_type ? <div>授权类型：{account.account_type}</div> : null}
                                  {account.project_id ? <div className="break-all">项目：{account.project_id}</div> : null}
                                  <div>调用 {formatCount(total)} 次 · 成功率 {formatPercent(account.success, total)} · 成功 {formatCount(account.success)} / 失败 {formatCount(account.failed)}</div>
                                  <RecentBars buckets={account.recent_requests} />
                                  {account.signals && Object.keys(account.signals).length ? (
                                    <div className="space-y-0.5 font-mono text-[11px]">
                                      <div className="font-sans">原始信号（{Object.keys(account.signals).length} 项）</div>
                                      {Object.entries(account.signals).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => (
                                        <div key={name}>{name}: {String(value)}</div>
                                      ))}
                                    </div>
                                  ) : null}
                                </div>
                              </details>

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

      {runtimeConfigTarget ? (
        <CPARuntimeConfigSheet
          key={runtimeConfigTarget.id}
          instance={runtimeConfigTarget}
          open
          onOpenChange={(open) => {
            if (!open) setRuntimeConfigTarget(null);
          }}
        />
      ) : null}

      {accountConfigTarget ? (
        <CPAAccountConfigSheet
          key={accountConfigTarget.instance.id + ":" + String(accountConfigTarget.account.auth_index ?? "")}
          instance={accountConfigTarget.instance}
          account={accountConfigTarget.account}
          open
          onOpenChange={(open) => {
            if (!open) setAccountConfigTarget(null);
          }}
        />
      ) : null}

      {configOpen ? (
        <QuotaFreezeConfigSheet
          open={configOpen}
          onOpenChange={setConfigOpen}
          onSaved={() => setConfigOpen(false)}
        />
      ) : null}

      {policyTarget ? (
        <QuotaFreezePolicySheet
          key={`${policyTarget.scope}:${policyTarget.target}`}
          open
          onOpenChange={(open) => {
            if (!open) setPolicyTarget(null);
          }}
          scope={policyTarget.scope}
          target={policyTarget.target}
          title={policyTarget.title}
          onSaved={() => setPolicyTarget(null)}
        />
      ) : null}
    </div>
  );
}
