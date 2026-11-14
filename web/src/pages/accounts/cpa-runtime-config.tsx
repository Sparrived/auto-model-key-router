import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type { CPARuntimeConfigResponse } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";
import { Switch } from "@/components/ui/Switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/Tabs";
import { Textarea } from "@/components/ui/Textarea";

interface Props {
  instance: { id: string; label?: string };
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

interface Draft {
  debug: boolean;
  requestRetry: string;
  strategy: string;
  clientAPIKeys: string;
}

const STRATEGIES = [
  { value: "round-robin", label: "轮询（round-robin）" },
  { value: "fill-first", label: "优先填满（fill-first）" },
  { value: "weighted-round-robin", label: "加权轮询（weighted-round-robin）" },
];

function makeDraft(config: CPARuntimeConfigResponse): Draft {
  return {
    debug: config.debug ?? false,
    requestRetry: String(config.request_retry ?? 0),
    strategy: config.strategy || "round-robin",
    clientAPIKeys: (config.client_api_keys ?? []).join("\n"),
  };
}

function parseKeys(value: string): string[] | null {
  const keys = value.split(/\r?\n/).map((key) => key.trim()).filter(Boolean);
  return new Set(keys).size === keys.length ? keys : null;
}

export function CPARuntimeConfigSheet({ instance, open, onOpenChange }: Props) {
  const confirm = useConfirm();
  const client = useQueryClient();
  const query = useQuery({
    queryKey: queryKeys.cpaRuntimeConfig(instance.id),
    queryFn: () => api.cpaRuntimeConfig(instance.id),
    enabled: open,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [initial, setInitial] = useState<Draft | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [conflicted, setConflicted] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [retryError, setRetryError] = useState("");
  const [keysError, setKeysError] = useState("");

  useEffect(() => {
    if (!open || draft || !query.data) return;
    const next = makeDraft(query.data);
    setInitial(next);
    setDraft(next);
  }, [draft, open, query.data]);

  const dirty = Boolean(initial && draft && JSON.stringify(initial) !== JSON.stringify(draft));
  const strategyOptions = useMemo(() => {
    if (!draft || STRATEGIES.some((option) => option.value === draft.strategy)) return STRATEGIES;
    return [...STRATEGIES, { value: draft.strategy, label: "当前值（" + draft.strategy + "）" }];
  }, [draft]);

  function update(patch: Partial<Draft>) {
    setDraft((current) => current ? { ...current, ...patch } : current);
    setSaveError("");
  }

  async function save() {
    if (!initial || !draft || !query.data || saving || conflicted) return;
    const retries = Number(draft.requestRetry);
    const retryInvalid = !draft.requestRetry.trim() || !Number.isInteger(retries) || retries < 0;
    const keys = parseKeys(draft.clientAPIKeys);
    setRetryError(retryInvalid ? "请求重试次数必须是大于或等于 0 的整数。" : "");
    setKeysError(keys === null ? "客户端密钥不能重复。" : "");
    if (retryInvalid || keys === null) return;
    if (!query.data.etag) {
      setSaveError("CPA 没有返回配置 ETag，无法安全保存。请确认实例已升级到支持 v8 配置管理的版本。");
      return;
    }

    const patch: { etag: string; debug?: boolean; request_retry?: number; strategy?: string; client_api_keys?: string[] } = {
      etag: query.data.etag,
    };
    if (draft.debug !== initial.debug) patch.debug = draft.debug;
    if (draft.requestRetry !== initial.requestRetry) patch.request_retry = retries;
    if (draft.strategy !== initial.strategy) patch.strategy = draft.strategy;
    if (draft.clientAPIKeys !== initial.clientAPIKeys) patch.client_api_keys = keys;
    if (Object.keys(patch).length === 1) {
      onOpenChange(false);
      return;
    }
    if (patch.client_api_keys) {
      const accepted = await confirm({
        title: "替换 CPA 客户端密钥列表",
        message: "保存会完整替换该实例的客户端 API Key 列表。被移除的 Key 将立即无法调用 CPA；AMKR 的本地访问密钥不受影响。",
        confirmLabel: "替换密钥列表",
        danger: true,
      });
      if (!accepted) return;
    }

    setSaving(true);
    setSaveError("");
    try {
      const response = await api.patchCPARuntimeConfig(instance.id, patch);
      client.setQueryData(queryKeys.cpaRuntimeConfig(instance.id), response);
      toast.success("CPA 实例配置已保存。");
      onOpenChange(false);
    } catch (error) {
      setSaveError(error instanceof ApiError && error.isConflict
        ? "配置已被其它操作更新。草稿仍保留；请关闭后重新打开，核对最新配置再保存。"
        : errorText(error));
      if (error instanceof ApiError && error.isConflict) {
        // 新 ETag 只用于展示最新快照，旧草稿必须经用户重新打开核对后才可再写。
        setConflicted(true);
        await query.refetch();
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title={(instance.label || instance.id) + " · 实例配置"}
      subtitle="直接修改 CPA 的持久化运行配置。保存使用 v8 配置接口与 ETag，不改写 AMKR 的实例连接信息。"
      width="lg"
      dirty={dirty}
      onSave={initial && draft && !conflicted ? save : undefined}
      saveLabel="保存到 CPA"
      saving={saving}
    >
      {query.isPending ? <Skeleton variant="table" className="h-48" /> : query.error ? (
        <div className="space-y-3">
          <Banner tone="critical" title="读取 CPA 配置失败">{errorText(query.error)}</Banner>
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>重试</Button>
        </div>
      ) : !draft ? <Skeleton variant="table" className="h-48" /> : (
        <Tabs defaultValue="runtime">
          <TabsList aria-label="CPA 配置分区">
            <TabsTrigger value="runtime">运行与路由</TabsTrigger>
            <TabsTrigger value="client-keys">客户端密钥</TabsTrigger>
          </TabsList>
          <TabsContent value="runtime" className="space-y-5 border border-rule bg-panel p-4">
            <Switch
              label="调试日志"
              description="对应 CPA 的 observability.logs.debug；只影响该实例的日志详细程度。"
              checked={draft.debug}
              disabled={saving}
              onCheckedChange={(debug) => update({ debug })}
            />
            <div className="grid gap-3 sm:grid-cols-2">
              <Field
                label="额外请求重试次数"
                hint="0 表示关闭额外重试；对应 routing.retry.request-retry。未配置时以 0 作为编辑起点，未修改则不会写入。"
                error={retryError}
              >
                <Input
                  type="number"
                  min={0}
                  step={1}
                  inputMode="numeric"
                  value={draft.requestRetry}
                  disabled={saving}
                  onChange={(event) => {
                    setRetryError("");
                    update({ requestRetry: event.target.value });
                  }}
                />
              </Field>
              <Field label="账号路由策略" hint="对应 routing.strategy；切换策略会改变 CPA 选择账号的顺序。">
                <Select
                  aria-label="账号路由策略"
                  options={strategyOptions}
                  value={draft.strategy}
                  disabled={saving}
                  onChange={(event) => update({ strategy: event.target.value })}
                />
              </Field>
            </div>
            <Banner tone="info">
              这里只开放经过核对的配置字段，不提供任意配置树或凭据内容透传。保存成功后重新读取 CPA，页面展示的是对端实际配置。
            </Banner>
          </TabsContent>
          <TabsContent value="client-keys" className="space-y-3 border border-rule bg-panel p-4">
            <Field
              label="CPA 客户端 API Key"
              hint="每行一把。对应 access.api-keys；这是客户端调用 CPA 的凭据，不是 CPA 管理密钥，也不是上游供应商 Key。"
              error={keysError}
            >
              <Textarea
                className="min-h-44 font-mono text-meta"
                value={draft.clientAPIKeys}
                disabled={saving}
                spellCheck={false}
                autoComplete="off"
                onChange={(event) => {
                  setKeysError("");
                  update({ clientAPIKeys: event.target.value });
                }}
              />
            </Field>
            <Banner tone="warn">保存这一分区会替换整份列表；留空会移除全部 CPA 客户端密钥，并需再次确认。</Banner>
          </TabsContent>
          {saveError ? <Banner tone="critical" title="保存 CPA 配置失败">{saveError}</Banner> : null}
        </Tabs>
      )}
    </Sheet>
  );
}
