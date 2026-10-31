// 单个账号的额度冻结策略：为 CPA 实例或订阅覆盖全局冻结底限与白名单。
//
// 账号策略保存在 settings.quota_freeze_policies 里；未配置的字段为 null/缺省时继承
// 全局默认。保存时整份数组替换，但只改当前 scope + target 的条目，其它账号策略原样保留。

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type { QuotaFreezePolicy } from "@/lib/types";
import type { ComboboxOption } from "@/components/ui/Combobox";
import { Combobox } from "@/components/ui/Combobox";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";
import { Switch } from "@/components/ui/Switch";

interface Draft {
  enabled: boolean;
  /** 空字符串表示继承全局；其它值是百分比。 */
  fiveHour: string;
  sevenDay: string;
  /** null 表示继承全局；[] 表示明确无白名单。 */
  whitelist: string[] | null;
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scope: QuotaFreezePolicy["scope"];
  target: string;
  title: string;
  onSaved: () => void;
}

function percentText(value: number | null | undefined): string {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return "0";
  return String(Math.round(number * 1000) / 10);
}

/** 空字符串 = 继承；undefined = 非法输入；数字 = 0..1 的显式覆盖值。 */
function parseOptionalPercent(value: string): number | null | undefined {
  const text = value.trim();
  if (!text) return null;
  const number = Number(text);
  if (!Number.isFinite(number) || number < 0 || number > 100) return undefined;
  return Math.round(number * 10) / 1000;
}

function sameList(a: string[] | null, b: string[] | null): boolean {
  if (a === null || b === null) return a === b;
  return a.length === b.length && a.every((item, index) => item === b[index]);
}

function findPolicy(
  policies: QuotaFreezePolicy[] | undefined,
  scope: QuotaFreezePolicy["scope"],
  target: string,
): QuotaFreezePolicy | undefined {
  return (policies ?? []).find((policy) => policy.scope === scope && policy.target === target);
}

function modelOptions(
  models: Array<{ id: string; aliases?: string[] }> | undefined,
  current: string[],
): ComboboxOption[] {
  const byValue = new Map<string, ComboboxOption>();
  for (const model of models ?? []) {
    const aliases = (model.aliases ?? []).filter(Boolean);
    byValue.set(model.id, {
      value: model.id,
      label: model.id,
      group: "已配置模型",
      note: aliases.length ? `别名：${aliases.join("、")}` : undefined,
      keywords: aliases,
    });
    // 旧配置允许直接写别名；继续列出来，避免编辑时把有效的别名误标成未知项。
    for (const alias of aliases) {
      if (byValue.has(alias)) continue;
      byValue.set(alias, {
        value: alias,
        label: alias,
        group: "模型别名",
        note: `别名 · ${model.id}`,
        keywords: [model.id],
      });
    }
  }
  // 继承全局名单或历史配置里的名字即使已不在模型列表，也保留为可移除项。
  for (const name of current) {
    if (byValue.has(name)) continue;
    byValue.set(name, {
      value: name,
      label: name,
      group: "当前白名单",
      note: "不在当前模型列表",
    });
  }
  return [...byValue.values()];
}

export function QuotaFreezePolicySheet({
  open,
  onOpenChange,
  scope,
  target,
  title,
  onSaved,
}: Props) {
  const client = useQueryClient();
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings(),
    queryFn: api.settings,
    enabled: open,
    retry: false,
  });
  const modelsQuery = useQuery({
    queryKey: queryKeys.models(),
    queryFn: api.models,
    enabled: open,
    retry: false,
  });

  const [initial, setInitial] = useState<Draft | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [hasPolicy, setHasPolicy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [fiveHourError, setFiveHourError] = useState("");
  const [sevenDayError, setSevenDayError] = useState("");

  // 关闭后清空快照；下次打开必须重新读取，不能在旧的 revision 上保存。
  useEffect(() => {
    if (open) return;
    setInitial(null);
    setDraft(null);
    setHasPolicy(false);
    setSaveError("");
    setFiveHourError("");
    setSevenDayError("");
  }, [open]);

  useEffect(() => {
    if (!open || draft || !settingsQuery.data?.settings) return;
    const settings = settingsQuery.data.settings;
    const policy = findPolicy(settings.quota_freeze_policies, scope, target);
    const next: Draft = {
      enabled: policy?.enabled !== false,
      fiveHour: policy?.threshold_5h == null ? "" : percentText(policy.threshold_5h),
      sevenDay: policy?.threshold_7d == null ? "" : percentText(policy.threshold_7d),
      whitelist: policy?.whitelist == null ? null : [...policy.whitelist],
    };
    setInitial(next);
    setDraft(next);
    setHasPolicy(Boolean(policy));
  }, [draft, open, scope, target, settingsQuery.data]);

  const settings = settingsQuery.data?.settings;
  const globalFiveHour = settings?.quota_freeze_threshold_5h ?? 0;
  const globalSevenDay = settings?.quota_freeze_threshold_7d ?? 0;
  const globalWhitelist = settings?.quota_freeze_threshold_whitelist ?? [];
  const visibleWhitelist = draft?.whitelist ?? globalWhitelist;
  const options = useMemo(
    () => modelOptions(modelsQuery.data?.models, visibleWhitelist),
    [modelsQuery.data?.models, visibleWhitelist],
  );

  const dirty = Boolean(initial && draft && (
    initial.enabled !== draft.enabled
    || initial.fiveHour !== draft.fiveHour
    || initial.sevenDay !== draft.sevenDay
    || !sameList(initial.whitelist, draft.whitelist)
  ));
  const ready = Boolean(initial && draft && settings);

  function updateDraft(patch: Partial<Draft>) {
    setDraft((current) => current ? { ...current, ...patch } : current);
  }

  async function persist(nextPolicies: QuotaFreezePolicy[], message: string) {
    const revision = settingsQuery.data?.config_revision;
    if (!revision) {
      setSaveError("没有读到配置版本，请关闭后重新打开。");
      return;
    }
    setSaveError("");
    setSaving(true);
    try {
      const response = await api.updateSettings(revision, { quota_freeze_policies: nextPolicies });
      client.setQueryData(queryKeys.settings(), response);
      toast.success(message);
      await Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.settings() }),
        client.invalidateQueries({ queryKey: queryKeys.cpaAccounts() }),
        client.invalidateQueries({ queryKey: queryKeys.quotaFreezes() }),
      ]);
      onSaved();
    } catch (error) {
      if (error instanceof ApiError && error.isConflict) {
        setSaveError("配置已被其它改动更新，请关闭后重新打开再试。");
        await client.invalidateQueries({ queryKey: queryKeys.settings() });
      } else {
        setSaveError(errorText(error));
      }
    } finally {
      setSaving(false);
    }
  }

  async function save() {
    if (!draft || !initial || saving) return;
    const fiveHour = parseOptionalPercent(draft.fiveHour);
    const sevenDay = parseOptionalPercent(draft.sevenDay);
    const nextFiveError = fiveHour === undefined ? "5h 底限必须是 0 到 100 之间的百分比，或留空继承全局。" : "";
    const nextSevenError = sevenDay === undefined ? "7d 底限必须是 0 到 100 之间的百分比，或留空继承全局。" : "";
    setFiveHourError(nextFiveError);
    setSevenDayError(nextSevenError);
    if (nextFiveError || nextSevenError) return;
    if (!dirty) {
      onSaved();
      return;
    }

    const bases = settingsQuery.data?.settings?.quota_freeze_policies ?? [];
    const currentPolicy = findPolicy(bases, scope, target);
    // 输入框按百分比只显示一位小数；如果用户没动这个字段，不能把 5.55% 四舍五入成
    // 5.6% 再写回。保留设置接口原值，只有用户实际改过的字段才用输入框解析值。
    const threshold5h = draft.fiveHour === initial.fiveHour
      ? currentPolicy?.threshold_5h ?? null
      : fiveHour ?? null;
    const threshold7d = draft.sevenDay === initial.sevenDay
      ? currentPolicy?.threshold_7d ?? null
      : sevenDay ?? null;
    const nextPolicy: QuotaFreezePolicy = {
      scope,
      target,
      enabled: draft.enabled,
      threshold_5h: threshold5h,
      threshold_7d: threshold7d,
      whitelist: draft.whitelist === null ? null : [...draft.whitelist],
    };
    const nextPolicies = bases.some((policy) => policy.scope === scope && policy.target === target)
      ? bases.map((policy) => (policy.scope === scope && policy.target === target ? nextPolicy : policy))
      : [...bases, nextPolicy];
    await persist(nextPolicies, "账号冻结策略已保存。");
  }

  async function restoreGlobal() {
    if (!hasPolicy || saving) return;
    const bases = settingsQuery.data?.settings?.quota_freeze_policies ?? [];
    await persist(
      bases.filter((policy) => !(policy.scope === scope && policy.target === target)),
      "已恢复全局默认冻结规则。",
    );
  }

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      subtitle="账号策略优先于全局默认；未配置的字段继续继承全局默认。"
      width="lg"
      dirty={dirty}
      onSave={ready ? save : undefined}
      saveLabel="保存策略"
      saving={saving}
    >
      {settingsQuery.isPending ? <Skeleton variant="table" className="h-40" /> : settingsQuery.error ? (
        <Banner tone="critical" title="读取冻结策略失败">{errorText(settingsQuery.error)}</Banner>
      ) : !draft ? <Skeleton variant="table" className="h-40" /> : (
        <div className="space-y-5">
          <div className="rounded-control border border-rule bg-sunken/60 p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="text-meta font-medium text-ink-2">
                {hasPolicy ? "当前为账号自定义策略" : "当前没有账号策略"}
              </div>
              {hasPolicy ? <Badge tone="accent">自定义</Badge> : <Badge tone="neutral">继承全局</Badge>}
            </div>
            <p className="mt-1 text-meta leading-5 text-ink-3">
              {`未配置字段继承全局默认（当前 5h ${percentText(globalFiveHour)}%、7d ${percentText(globalSevenDay)}%、白名单 ${globalWhitelist.length} 个）。`}
            </p>
          </div>

          <Switch
            checked={draft.enabled}
            onCheckedChange={(enabled) => updateDraft({ enabled })}
            label="启用自动冻结"
            description="关闭后该账号不参与自动冻结；已有自动冻结会被解除，手工解冻与额度展示不受影响。"
          />

          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="5h 提前冻结底限"
              hint={`留空继承全局默认（当前 ${percentText(globalFiveHour)}%）；0 表示关闭该窗口的提前冻结。`}
              error={fiveHourError}
            >
              <Input
                type="number"
                min={0}
                max={100}
                step={0.1}
                inputMode="decimal"
                placeholder={`继承全局（${percentText(globalFiveHour)}%）`}
                trailingIcon={<span className="text-meta text-ink-3">%</span>}
                value={draft.fiveHour}
                onChange={(event) => {
                  setFiveHourError("");
                  updateDraft({ fiveHour: event.target.value });
                }}
              />
            </Field>
            <Field
              label="7d 提前冻结底限"
              hint={`留空继承全局默认（当前 ${percentText(globalSevenDay)}%）；0 表示关闭该窗口的提前冻结。`}
              error={sevenDayError}
            >
              <Input
                type="number"
                min={0}
                max={100}
                step={0.1}
                inputMode="decimal"
                placeholder={`继承全局（${percentText(globalSevenDay)}%）`}
                trailingIcon={<span className="text-meta text-ink-3">%</span>}
                value={draft.sevenDay}
                onChange={(event) => {
                  setSevenDayError("");
                  updateDraft({ sevenDay: event.target.value });
                }}
              />
            </Field>
          </div>

          <Field
            label="提前冻结白名单"
            hint={draft.whitelist === null
              ? `当前继承全局白名单（${globalWhitelist.length} 个）；修改选择后改为本账号自定义。`
              : "本账号自定义白名单；清空表示明确不豁免任何模型。"}
          >
            <Combobox
              multiple
              options={options}
              value={visibleWhitelist}
              onValueChange={(value) => {
                updateDraft({ whitelist: Array.isArray(value) ? value : [] });
              }}
              placeholder={modelsQuery.isPending ? "正在读取模型…" : "选择允许继续使用的模型"}
              aria-label="提前冻结白名单"
            />
          </Field>
          {draft.whitelist !== null ? (
            <div className="flex justify-end">
              <Button
                size="sm"
                variant="secondary"
                disabled={saving}
                onClick={() => updateDraft({ whitelist: null })}
              >
                继承全局白名单
              </Button>
            </div>
          ) : null}

          {modelsQuery.error ? (
            <Banner tone="warn" title="模型列表读取失败">
              {errorText(modelsQuery.error)} 当前白名单仍保留，但暂时不能从列表里新增模型。
            </Banner>
          ) : null}
          {saveError ? <Banner tone="critical" title="保存账号冻结策略失败">{saveError}</Banner> : null}

          <div className="flex flex-wrap items-center justify-between gap-2 border-t border-rule pt-3">
            <p className="text-meta leading-5 text-ink-3">
              保存会热重载配置，不重启服务。账号策略只影响自动冻结；手工解冻与额度展示仍按现有规则执行。
            </p>
            {hasPolicy ? (
              <Button
                size="sm"
                variant="secondary"
                loading={saving}
                disabled={saving}
                onClick={() => void restoreGlobal()}
              >
                恢复全局默认
              </Button>
            ) : null}
          </div>
        </div>
      )}
    </Sheet>
  );
}
