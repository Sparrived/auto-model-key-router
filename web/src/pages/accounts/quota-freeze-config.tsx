// 账号资源页的额度冻结规则编辑：5h / 7d 提前冻结底限与模型白名单。
//
// 配置写回 router-config.json，复用 PUT /api/settings；白名单只豁免 threshold 冻结，
// 硬性耗尽仍会挡住所有模型。模型清单从 /api/models 读取，避免手输不存在的模型名。

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type { ComboboxOption } from "@/components/ui/Combobox";
import { Combobox } from "@/components/ui/Combobox";
import { Banner } from "@/components/ui/Banner";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";

interface Draft {
  fiveHour: string;
  sevenDay: string;
  whitelist: string[];
}

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}

function percentText(value: number | undefined): string {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return "0";
  return String(Math.round(number * 1000) / 10);
}

function parsePercent(value: string): number | null {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0 || number > 100) return null;
  return Math.round(number * 10) / 1000;
}

function sameList(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((item, index) => item === b[index]);
}

export function QuotaFreezeConfigSheet({ open, onOpenChange, onSaved }: Props) {
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
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [fiveHourError, setFiveHourError] = useState("");
  const [sevenDayError, setSevenDayError] = useState("");

  useEffect(() => {
    if (!open || draft || !settingsQuery.data?.settings) return;
    const settings = settingsQuery.data.settings;
    const next: Draft = {
      fiveHour: percentText(settings.quota_freeze_threshold_5h),
      sevenDay: percentText(settings.quota_freeze_threshold_7d),
      whitelist: [...(settings.quota_freeze_threshold_whitelist ?? [])],
    };
    setInitial(next);
    setDraft(next);
  }, [draft, open, settingsQuery.data]);

  const options = useMemo(() => {
    const byValue = new Map<string, ComboboxOption>();
    for (const model of modelsQuery.data?.models ?? []) {
      const aliases = (model.aliases ?? []).filter(Boolean);
      byValue.set(model.id, {
        value: model.id,
        label: model.id,
        group: "已配置模型",
        note: aliases.length ? `别名：${aliases.join("、")}` : undefined,
        keywords: aliases,
      });
      // 旧配置可能直接写着别名；把它也列出来，避免编辑面板把它误标成「不在模型列表」。
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
    // 旧配置可能写着别名或已从模型列表消失的名字；保留为可移除的当前项，避免打开
    // 配置面板时把一条用户没改过的白名单悄悄丢掉。
    for (const name of draft?.whitelist ?? []) {
      if (byValue.has(name)) continue;
      byValue.set(name, {
        value: name,
        label: name,
        group: "当前白名单",
        note: "不在当前模型列表",
      });
    }
    return [...byValue.values()];
  }, [draft?.whitelist, modelsQuery.data?.models]);

  const dirty = Boolean(initial && draft && (
    initial.fiveHour !== draft.fiveHour
    || initial.sevenDay !== draft.sevenDay
    || !sameList(initial.whitelist, draft.whitelist)
  ));
  const ready = Boolean(initial && draft);

  async function save() {
    if (!initial || !draft || saving) return;
    const fiveHour = parsePercent(draft.fiveHour);
    const sevenDay = parsePercent(draft.sevenDay);
    const nextFiveError = fiveHour === null ? "5h 底限必须是 0 到 100 之间的百分比。" : "";
    const nextSevenError = sevenDay === null ? "7d 底限必须是 0 到 100 之间的百分比。" : "";
    setFiveHourError(nextFiveError);
    setSevenDayError(nextSevenError);
    if (nextFiveError || nextSevenError) return;
    setSaveError("");

    const patch: Record<string, unknown> = {};
    if (fiveHour !== parsePercent(initial.fiveHour)) patch.quota_freeze_threshold_5h = fiveHour;
    if (sevenDay !== parsePercent(initial.sevenDay)) patch.quota_freeze_threshold_7d = sevenDay;
    if (!sameList(initial.whitelist, draft.whitelist)) patch.quota_freeze_threshold_whitelist = draft.whitelist;
    if (!Object.keys(patch).length) {
      onSaved();
      return;
    }
    const revision = settingsQuery.data?.config_revision;
    if (!revision) {
      setSaveError("没有读到配置版本，请关闭后重新打开。");
      return;
    }

    setSaving(true);
    try {
      const response = await api.updateSettings(revision, patch);
      client.setQueryData(queryKeys.settings(), response);
      toast.success("额度冻结规则已保存。");
      await client.invalidateQueries({ queryKey: queryKeys.cpaAccounts() });
      await client.invalidateQueries({ queryKey: queryKeys.quotaFreezes() });
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

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title="额度冻结规则"
      subtitle="设置低于额度的哪个比例时提前冻结 Key；白名单模型可继续使用，直到额度完全耗尽。"
      width="lg"
      dirty={dirty}
      onSave={ready ? save : undefined}
      saveLabel="保存规则"
      saving={saving}
    >
      {settingsQuery.isPending ? <Skeleton variant="table" className="h-40" /> : settingsQuery.error ? (
        <Banner tone="critical" title="读取冻结规则失败">{errorText(settingsQuery.error)}</Banner>
      ) : !draft ? <Skeleton variant="table" className="h-40" /> : (
        <div className="space-y-5">
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="5h 提前冻结底限"
              hint="剩余比例低于此值时提前冻结；0 表示关闭。"
              error={fiveHourError}
            >
              <Input
                type="number"
                min={0}
                max={100}
                step={0.1}
                inputMode="decimal"
                trailingIcon={<span className="text-meta text-ink-3">%</span>}
                value={draft.fiveHour}
                onChange={(event) => {
                  setFiveHourError("");
                  setDraft((current) => current ? { ...current, fiveHour: event.target.value } : current);
                }}
              />
            </Field>
            <Field
              label="7d 提前冻结底限"
              hint="剩余比例低于此值时提前冻结；0 表示关闭。"
              error={sevenDayError}
            >
              <Input
                type="number"
                min={0}
                max={100}
                step={0.1}
                inputMode="decimal"
                trailingIcon={<span className="text-meta text-ink-3">%</span>}
                value={draft.sevenDay}
                onChange={(event) => {
                  setSevenDayError("");
                  setDraft((current) => current ? { ...current, sevenDay: event.target.value } : current);
                }}
              />
            </Field>
          </div>

          <Field
            label="提前冻结白名单"
            hint="白名单只豁免提前冻结；额度硬性耗尽后，所有模型仍会立即停用。可多选。"
          >
            <Combobox
              multiple
              options={options}
              value={draft.whitelist}
              onValueChange={(value) => {
                setDraft((current) => current
                  ? { ...current, whitelist: Array.isArray(value) ? value : [] }
                  : current);
              }}
              placeholder={modelsQuery.isPending ? "正在读取模型…" : "选择允许继续使用的模型"}
              aria-label="提前冻结白名单"
            />
          </Field>
          {modelsQuery.error ? (
            <Banner tone="warn" title="模型列表读取失败">
              {errorText(modelsQuery.error)} 现有白名单仍保留，但暂时不能从列表里新增模型。
            </Banner>
          ) : null}
          {saveError ? <Banner tone="critical" title="保存冻结规则失败">{saveError}</Banner> : null}
          <p className="text-meta leading-5 text-ink-3">
            保存会热重载配置，不重启服务。提前冻结的 Key 会在触发窗口的刷新时间到达后自动解冻；
            手工解冻、硬性耗尽与后台巡检间隔仍按现有规则执行。
          </p>
        </div>
      )}
    </Sheet>
  );
}
