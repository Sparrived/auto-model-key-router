import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal, Pencil, Radar, Trash2, Unlock } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { errorText, formatRelative } from "@/lib/format";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import {
  freezeExpiresAtText,
  freezeLookupKey,
  freezeReasonText,
  freezeResetAtText,
  freezeResetCountdown,
  freezeTriggerText,
} from "@/lib/quota-freeze";
import type { Provider, ProviderKey, ProvidersResponse, QuotaFreeze } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { DropdownMenu, DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/DropdownMenu";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Tooltip } from "@/components/ui/Tooltip";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { CONCURRENCY_INVALID, CONCURRENCY_SUM_HINT, concurrencyLimit, concurrencyText } from "./concurrency";

interface KeysTableProps {
  provider: Provider;
  revision: string;
  boundModels: Map<string, string[]>;
  onRefresh: () => Promise<ProvidersResponse>;
  onOpenModels: (key: ProviderKey) => void;
  /** 「供应商 + Key → 冻结记录」索引；缺省表示没有冻结数据（测试与旧调用方兼容）。 */
  freezes?: Map<string, QuotaFreeze>;
  /** 页面级 useNow 的当前时刻；冻结 tooltip 与行内倒计时都按它重算。 */
  now: number;
  /** 解除一把 Key 的冻结；由页面负责调接口、刷新与提示。 */
  onReleaseFreeze?: (freeze: QuotaFreeze) => void;
}

function capabilityBadge(key: ProviderKey) {
  const capabilities = key.capabilities;
  if (!capabilities) return <Badge tone="neutral">未探测</Badge>;
  const models = capabilities.models || [];
  if (models.length) return <Badge tone="good">{models.length} 个模型</Badge>;
  const errors = Object.values(capabilities.errors || {}).filter(Boolean);
  if (errors.length) return <Badge tone="warn">探测有错误</Badge>;
  return <Badge tone="neutral">未发现模型</Badge>;
}

function concurrencyBadge(value: number | null | undefined) {
  const limit = Number(value);
  const limited = Number.isFinite(limit) && limit > 0;
  // **必须把"不限制"写出来**——一个空白徽标读不出任何意思，而"没设上限"正是用户
  // 扫这一行时最想知道的事之一。
  return <Badge tone={limited ? "good" : "neutral"}>{limited ? `并发 ${limit}` : "并发不限"}</Badge>;
}

// 冻结 tooltip：原因 + 自动解冻倒计时与绝对时刻。expires_at 是内置计时器的解冻点；
// 只有旧数据缺 expires_at 时才退回 reset_at 的窗口刷新倒计时。
function FreezeTooltip({ freeze, now }: { freeze: QuotaFreeze; now: number }) {
  const countdown = freezeResetCountdown(freeze, now);
  const resetAt = freezeResetAtText(freeze);
  const expiresAt = freezeExpiresAtText(freeze);
  return (
    <div className="space-y-0.5">
      <div>{freezeReasonText(freeze)}</div>
      {countdown ? (
        <div>{countdown === "正在自动解冻…" ? countdown : `${expiresAt ? "自动解冻倒计时" : "窗口重置倒计时"}：${countdown}`}</div>
      ) : null}
      {expiresAt ? <div>自动解冻时刻：{expiresAt}</div> : null}
      {resetAt ? <div>窗口刷新：{resetAt}</div> : null}
      <div>冻结不会改动启用开关</div>
    </div>
  );
}

export function KeysTable({ provider, revision, boundModels, onRefresh, onOpenModels, freezes, now, onReleaseFreeze }: KeysTableProps) {
  const client = useQueryClient();
  const impactConfirm = useImpactConfirm();
  const [editing, setEditing] = useState<string | null>(null);
  const [probeState, setProbeState] = useState<Record<string, "idle" | "pending" | "success" | "error">>({});
  const timers = useRef<number[]>([]);

  useEffect(() => () => {
    for (const timer of timers.current) window.clearTimeout(timer);
    timers.current = [];
  }, []);

  async function toggle(key: ProviderKey, enabled: boolean) {
    const keyQuery = queryKeys.providers();
    await client.cancelQueries({ queryKey: keyQuery });
    const previous = client.getQueryData<ProvidersResponse>(keyQuery);
    client.setQueryData<ProvidersResponse>(keyQuery, (data) => data ? {
      ...data,
      providers: data.providers.map((item) => item.id !== provider.id ? item : {
        ...item,
        keys: item.keys.map((candidate) => candidate.name === key.name ? { ...candidate, enabled } : candidate),
      }),
    } : data);
    try {
      await api.updateProviderKey(revision, provider.id, key.name, { enabled });
      await onRefresh();
    } catch (failure) {
      if (previous) client.setQueryData(keyQuery, previous);
      toast.error(errorText(failure));
      if (failure instanceof ApiError && failure.isConflict) void onRefresh();
    }
  }

  async function probeOne(key: ProviderKey) {
    setProbeState((current) => ({ ...current, [key.name]: "pending" }));
    try {
      await api.probeKey(revision, provider.id, key.name);
      const data = await onRefresh();
      setProbeState((current) => ({ ...current, [key.name]: "success" }));
      const fresh = data.providers.find((item) => item.id === provider.id)?.keys.find((item) => item.name === key.name);
      const count = fresh?.capabilities?.models?.length || 0;
      toast(count ? `Key ${key.name} 探测成功，发现 ${count} 个模型。` : `Key ${key.name} 探测完成（未发现模型）。`);
      timers.current.push(window.setTimeout(() => {
        setProbeState((current) => ({ ...current, [key.name]: "idle" }));
      }, 2400));
    } catch (failure) {
      setProbeState((current) => ({ ...current, [key.name]: "error" }));
      toast.error(`Key ${key.name} 探测失败: ${errorText(failure)}`);
    }
  }

  async function removeKey(key: ProviderKey) {
    try {
      const { confirmed } = await writeWithImpactConfirm({
        title: "删除 Key",
        message: `删除 Key ${key.name}？`,
        confirmLabel: "删除",
        alwaysConfirm: true,
        preview: async () => (await api.deleteProviderKey(revision, provider.id, key.name, { dryRun: true })) as unknown as Impact,
        commit: () => api.deleteProviderKey(revision, provider.id, key.name),
        confirm: impactConfirm,
      });
      if (!confirmed) return;
      await onRefresh();
      toast("Key 已删除。");
    } catch (failure) {
      toast.error(errorText(failure));
    }
  }

  const keys = provider.keys || [];
  if (!keys.length) {
    return (
      <div className="px-4 py-8 text-center text-meta text-ink-3">
        尚无 Key。添加 Key 并绑定服务模型后，模型才会出现在模型路由里。
      </div>
    );
  }

  return (
    <div className="w-full min-w-0 overflow-x-auto">
      <table className="w-full table-fixed border-collapse text-dense">
        <colgroup>
          <col className="w-[32%]" />
          <col className="w-[9%]" />
          <col className="w-[24%]" />
          <col className="w-[16%]" />
          <col className="w-[12%]" />
          <col className="w-[7%]" />
        </colgroup>
        <thead>
          <tr className="border-b border-rule text-left text-meta text-ink-3">
            <th className="px-3 py-2 font-medium">Key / 指纹</th>
            <th className="px-1 py-2 font-medium">启用</th>
            <th className="px-2 py-2 font-medium">服务模型</th>
            <th className="px-2 py-2 font-medium">探测</th>
            <th className="px-2 py-2 font-medium">并发</th>
            <th className="sticky right-0 z-20 border-l border-rule bg-panel px-1 py-2 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => {
            const bound = boundModels.get(`${provider.id}|${key.name}`) || [];
            // 冻结是选路侧的临时状态，与配置里的 enabled 无关：开关照旧绑定 enabled。
            const freeze = freezes?.get(freezeLookupKey(provider.id, key.name));
            return editing === key.name ? (
              <KeyEditorRow
                key={`${key.name}-edit`}
                provider={provider}
                providerKey={key}
                revision={revision}
                onCancel={() => setEditing(null)}
                onRefresh={onRefresh}
                onSaved={async () => {
                  await onRefresh();
                  setEditing(null);
                  toast("Key 已更新。");
                }}
              />
            ) : (
              <tr
                key={key.name}
                tabIndex={0}
                onClick={() => setEditing(key.name)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    setEditing(key.name);
                  }
                }}
                className="group cursor-pointer border-b border-rule last:border-b-0 hover:bg-hover focus-visible:bg-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent"
              >
                <td className="min-w-0 px-3 py-2.5 align-top">
                  <div className="flex min-w-0 flex-col gap-1">
                    <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="truncate font-mono text-dense font-medium text-ink">{key.name}</span>
                      {freeze ? (
                        <Tooltip content={<FreezeTooltip freeze={freeze} now={now} />}>
                          <span
                            tabIndex={0}
                            className="inline-flex"
                            aria-label={`Key ${key.name} 的额度冻结详情`}
                            onClick={(event) => event.stopPropagation()}
                            onKeyDown={(event) => event.stopPropagation()}
                          >
                            <Badge tone={freeze.trigger === "threshold" ? "warn" : "critical"} dot>
                              {freezeTriggerText(freeze)}
                            </Badge>
                          </span>
                        </Tooltip>
                      ) : null}
                    </div>
                    <span
                      className="min-w-0 max-w-full"
                      onClick={(event) => event.stopPropagation()}
                      onKeyDown={(event) => event.stopPropagation()}
                    >
                      <CopyChip
                        text={key.api_key_fingerprint || "-"}
                        label="指纹"
                        className="h-5 w-full max-w-full px-1.5 text-[11px]"
                      />
                    </span>
                  </div>
                </td>
                <td className="px-1 py-2.5 align-top">
                  <span
                    className="inline-flex"
                    onClick={(event) => event.stopPropagation()}
                    onKeyDown={(event) => event.stopPropagation()}
                  >
                    <Switch
                      checked={Boolean(key.enabled)}
                      onCheckedChange={(checked) => void toggle(key, checked)}
                      aria-label={`启用 Key ${key.name}`}
                    />
                  </span>
                </td>
                <td className="min-w-0 px-2 py-2.5 align-top">
                  <div className="flex min-w-0 flex-wrap items-center gap-1">
                    {bound.slice(0, 2).map((model) => (
                      <button
                        key={model}
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          onOpenModels(key);
                        }}
                        onKeyDown={(event) => event.stopPropagation()}
                        title={model}
                        className="inline-flex h-6 max-w-32 items-center rounded-chip border border-rule bg-sunken px-2 font-mono text-meta text-ink-2 hover:bg-hover hover:text-ink"
                      >
                        <span className="truncate">{model}</span>
                      </button>
                    ))}
                    {bound.length > 2 ? (
                      <Tooltip
                        content={(
                          <div className="flex max-h-48 max-w-56 flex-col gap-0.5 overflow-y-auto">
                            {bound.map((model) => <span key={model} className="truncate font-mono">{model}</span>)}
                          </div>
                        )}
                      >
                        <button
                          type="button"
                          aria-label={`查看全部 ${bound.length} 个服务模型`}
                          onClick={(event) => {
                            event.stopPropagation();
                            onOpenModels(key);
                          }}
                          onKeyDown={(event) => event.stopPropagation()}
                          className="inline-flex h-6 items-center rounded-chip border border-rule bg-sunken px-2 text-meta text-ink-3 hover:bg-hover hover:text-ink"
                        >
                          +{bound.length - 2}
                        </button>
                      </Tooltip>
                    ) : null}
                    {!bound.length ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={(event) => {
                          event.stopPropagation();
                          onOpenModels(key);
                        }}
                        onKeyDown={(event) => event.stopPropagation()}
                      >
                        绑定模型
                      </Button>
                    ) : null}
                  </div>
                </td>
                <td className="px-2 py-2.5 align-top">
                  <div
                    className="flex flex-col items-start gap-1"
                    onClick={(event) => event.stopPropagation()}
                    onKeyDown={(event) => event.stopPropagation()}
                  >
                    {capabilityBadge(key)}
                    <span className="text-meta text-ink-3">
                      {key.capabilities?.checked_at ? formatRelative(key.capabilities.checked_at) : "尚未探测"}
                    </span>
                    {Object.entries(key.capabilities?.errors || {}).filter(([, message]) => message).length ? (
                      <span className="max-w-36 truncate text-meta text-warn-ink" title={
                        Object.entries(key.capabilities?.errors || {}).map(([mode, message]) => `${mode}: ${message}`).join("\n")
                      }>
                        {Object.values(key.capabilities?.errors || {}).filter(Boolean).join("；")}
                      </span>
                    ) : null}
                    <Button
                      size="sm"
                      variant="secondary"
                      loading={probeState[key.name] === "pending"}
                      leadingIcon={probeState[key.name] === "pending" ? undefined : <Radar aria-hidden className="size-3.5" />}
                      onClick={(event) => {
                        event.stopPropagation();
                        void probeOne(key);
                      }}
                      onKeyDown={(event) => event.stopPropagation()}
                    >
                      {probeState[key.name] === "pending" ? "探测中" : probeState[key.name] === "success" ? "已探测" : "探测"}
                    </Button>
                    {probeState[key.name] === "error" ? <span className="text-meta text-critical-ink">探测失败</span> : null}
                  </div>
                </td>
                <td className="px-2 py-2.5 align-top">{concurrencyBadge(key.max_concurrency)}</td>
                <td
                  className="sticky right-0 z-10 border-l border-rule bg-panel px-1 py-2.5 align-top group-hover:bg-hover"
                  onClick={(event) => event.stopPropagation()}
                  onKeyDown={(event) => event.stopPropagation()}
                >
                  <DropdownMenu
                    trigger={(
                      <IconButton aria-label={`Key ${key.name} 的操作`} size="sm">
                        <MoreHorizontal aria-hidden className="size-4" />
                      </IconButton>
                    )}
                  >
                    <DropdownMenuItem icon={<Pencil />} onSelect={() => setEditing(key.name)}>编辑</DropdownMenuItem>
                    {freeze ? (
                      <DropdownMenuItem icon={<Unlock />} onSelect={() => onReleaseFreeze?.(freeze)}>解除冻结</DropdownMenuItem>
                    ) : null}
                    <DropdownMenuSeparator />
                    <DropdownMenuItem danger icon={<Trash2 />} onSelect={() => void removeKey(key)}>删除 Key</DropdownMenuItem>
                  </DropdownMenu>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

interface KeyEditorRowProps {
  provider: Provider;
  providerKey: ProviderKey;
  revision: string;
  onCancel: () => void;
  onRefresh: () => Promise<ProvidersResponse>;
  onSaved: () => Promise<void>;
}

function KeyEditorRow({ provider, providerKey, revision, onCancel, onRefresh, onSaved }: KeyEditorRowProps) {
  const [name, setName] = useState(providerKey.name);
  const [secret, setSecret] = useState("");
  const [concurrency, setConcurrency] = useState(concurrencyText(providerKey.max_concurrency));
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const save = async () => {
    const nextName = name.trim();
    if (!nextName) {
      setError("Key 名称不能为空。");
      return;
    }
    const maxConcurrency = concurrencyLimit(concurrency);
    if (maxConcurrency === undefined) {
      setError(CONCURRENCY_INVALID);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const patch: { name: string; enabled?: boolean; max_concurrency: number | null; api_key?: string } = {
        name: nextName,
        enabled: providerKey.enabled,
        max_concurrency: maxConcurrency,
      };
      if (secret.trim()) patch.api_key = secret.trim();
      await api.updateProviderKey(revision, provider.id, providerKey.name, patch);
      await onSaved();
    } catch (failure) {
      setError(`操作失败: ${errorText(failure)}`);
      if (failure instanceof ApiError && failure.isConflict) await onRefresh().catch(() => undefined);
    } finally {
      setSaving(false);
    }
  };

  return (
    <tr className="border-b border-rule bg-sunken/60">
      <td colSpan={6} className="px-4 py-3">
        <div className="space-y-3">
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Key 名称">
              <Input value={name} onChange={(event) => setName(event.target.value)} />
            </Field>
            <Field label="替换 API Key" hint="留空保持不变。">
              <Input
                type="password"
                autoComplete="new-password"
                value={secret}
                onChange={(event) => setSecret(event.target.value)}
                placeholder="留空保持不变"
              />
            </Field>
            <Field label="并发上限" hint={`留空即清除该 Key 的上限（不限制）。${CONCURRENCY_SUM_HINT}`}>
              <Input
                type="number"
                min="1"
                step="1"
                value={concurrency}
                onChange={(event) => setConcurrency(event.target.value)}
                placeholder="留空不限制"
              />
            </Field>
          </div>
          {error ? <Banner tone="critical">{error}</Banner> : null}
          <div className="flex items-center gap-2">
            <Button size="sm" variant="primary" loading={saving} onClick={() => void save()}>保存</Button>
            <Button size="sm" variant="ghost" onClick={onCancel}>取消</Button>
          </div>
        </div>
      </td>
    </tr>
  );
}
