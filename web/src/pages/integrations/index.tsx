// 集成：把本机 CLI 客户端接到 AMKR 的统一入口。
//
// 每个代理工具独立一张面板：读取它的目标配置路径、当前是否已被接管、备份是否可回退，
// 并把 AMKR 的入口地址与本地鉴权状态放在同一处。单个 Agent 读取失败只影响自己的面板，
// 与 legacy integrations.js 的隔离口径一致。

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type { HealthResponse, IntegrationEntry, SettingsData } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Badge, type BadgeTone } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Spinner } from "@/components/ui/Spinner";
import { Skeleton } from "@/components/ui/Skeleton";
import { useConfirm } from "@/components/ui/ConfirmProvider";

const AGENTS = [
  { id: "claude-code", name: "Claude Code" },
  { id: "codex", name: "Codex" },
  { id: "pi-agent", name: "Pi Agent" },
] as const;

const PREVIEW: Record<string, string[]> = {
  "claude-code": [
    "env.ANTHROPIC_BASE_URL",
    "env.ANTHROPIC_AUTH_TOKEN",
    "清理旧版写入的 env.ANTHROPIC_MODEL 等单模型覆盖",
    "modelPicker.options（全部可用模型）",
  ],
  codex: [
    "model_provider",
    "清理旧版写入的 model / review_model / model_reasoning_effort",
    "model_catalog_json（全部可用模型）",
    "model_providers.OpenAI",
    "auth.json",
  ],
  "pi-agent": [
    "providers.amkr",
    "providers.amkr.baseUrl",
    "providers.amkr.apiKey",
    "providers.amkr.models（全部可用模型）",
  ],
};

function statusBadge(item: IntegrationEntry | null): { text: string; tone: BadgeTone } {
  if (!item) return { text: "正在读取", tone: "neutral" };
  if (item.error) return { text: "操作失败", tone: "critical" };
  if (item.current_is_applied) return { text: "已接管", tone: "good" };
  if (item.backup_available) return { text: "配置已变更 · 可回退", tone: "warn" };
  if (item.target_exists) return { text: "检测到配置", tone: "neutral" };
  return { text: "未找到配置", tone: "neutral" };
}

function routerOrigin(settings: SettingsData | undefined): string | null {
  if (!settings?.host || !settings?.port) return null;
  let host = String(settings.host);
  if (["0.0.0.0", "::", "[::]"].includes(host)) host = "127.0.0.1";
  if (host.includes(":") && !host.startsWith("[")) host = `[${host}]`;
  return `http://${host}:${settings.port}`;
}

export default function IntegrationsPage() {
  const confirm = useConfirm();
  const [busy, setBusy] = useState<string | null>(null);
  const [errorByAgent, setErrorByAgent] = useState<Record<string, string | null>>({});
  const [refreshing, setRefreshing] = useState(false);

  const integrationsQuery = useQuery({
    queryKey: queryKeys.integrations(),
    queryFn: api.integrations,
    retry: false,
  });
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings(),
    queryFn: api.settings,
    retry: false,
  });
  const healthQuery = useQuery({
    queryKey: queryKeys.health(),
    queryFn: api.health,
    retry: false,
  });

  const items = integrationsQuery.data?.integrations ?? integrationsQuery.data?.agents ?? [];
  const itemByAgent = useMemo(() => {
    const map = new Map<string, IntegrationEntry>();
    for (const item of items as IntegrationEntry[]) {
      if (item.agent) map.set(item.agent, item);
    }
    return map;
  }, [items]);

  const health = healthQuery.data as HealthResponse | undefined;
  const settings = settingsQuery.data?.settings;
  const baseUrl = routerOrigin(settings);
  const connected = Boolean(health?.status === "ok" && !healthQuery.error);
  const authEnabled = health?.local_auth_enabled === true;
  const opsDisabled = health?.ops_enabled === false;
  const unavailable = !connected || opsDisabled || Boolean(integrationsQuery.error) || Boolean(settingsQuery.error);
  const loading = integrationsQuery.isPending || settingsQuery.isPending || healthQuery.isPending;

  async function refresh() {
    if (refreshing || busy) return;
    setRefreshing(true);
    try {
      await Promise.all([
        integrationsQuery.refetch(),
        settingsQuery.refetch(),
        healthQuery.refetch(),
      ]);
    } catch {
      // 查询自身的 error 会由上面的 Banner 呈现；刷新按钮只需退出忙碌态。
    } finally {
      setRefreshing(false);
    }
  }

  async function apply(agent: string, name: string) {
    const ok = await confirm({
      title: `接管 ${name} 配置`,
      message: `将写入 ${name} 的配置文件，并备份原配置以便回退。确定继续吗？`,
      confirmLabel: "应用",
    });
    if (!ok) return;
    setBusy(agent);
    setErrorByAgent((current) => ({ ...current, [agent]: null }));
    try {
      await api.applyIntegration(agent);
      await integrationsQuery.refetch();
      toast.success(`${name} 已接管配置。`);
    } catch (error) {
      setErrorByAgent((current) => ({ ...current, [agent]: errorText(error) }));
      toast.error(errorText(error));
    } finally {
      setBusy(null);
    }
  }

  async function rollback(agent: string, name: string) {
    const ok = await confirm({
      title: `回退 ${name} 配置`,
      message: `从备份恢复 ${name} 的原配置？当前接管内容会被替换。`,
      confirmLabel: "回退",
      danger: true,
    });
    if (!ok) return;
    setBusy(agent);
    setErrorByAgent((current) => ({ ...current, [agent]: null }));
    try {
      await api.rollbackIntegration(agent);
      await integrationsQuery.refetch();
      toast.success("已回退到原配置。");
    } catch (error) {
      setErrorByAgent((current) => ({ ...current, [agent]: errorText(error) }));
      toast.error(errorText(error));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title="集成"
        description="集成：让本机的 CLI 客户端通过 AMKR 统一入口发请求；每个工具独立接管与回退。"
        actions={(
          <Button size="sm" variant="secondary" loading={refreshing} onClick={() => void refresh()}>
            刷新状态
          </Button>
        )}
      />

      {integrationsQuery.error ? (
        <Banner tone="critical" title="读取集成状态失败">{errorText(integrationsQuery.error)}</Banner>
      ) : null}
      {settingsQuery.error ? (
        <Banner tone="critical" title="读取运行设置失败">{errorText(settingsQuery.error)}</Banner>
      ) : null}
      {!healthQuery.isPending && !connected ? (
        <Banner tone="warn" title="尚未连接到 AMKR 服务">无法应用集成；请先确认服务正在运行。</Banner>
      ) : !healthQuery.isPending && opsDisabled ? (
        <Banner tone="warn" title="运维接口已关闭">当前服务以 `--no-ops` 运行，无法应用或回退集成。</Banner>
      ) : !healthQuery.isPending && !authEnabled ? (
        <Banner tone="warn" title="本地鉴权未启用">集成需要本地鉴权 Key 才能写入客户端配置。</Banner>
      ) : null}

      {loading ? (
        <div className="space-y-3">
          {AGENTS.map((agent) => <Skeleton key={agent.id} variant="block" className="h-52" />)}
        </div>
      ) : (
        AGENTS.map((agent) => {
          const item = itemByAgent.get(agent.id) ?? null;
          const status = statusBadge(item);
          const disabled = unavailable || !authEnabled || Boolean(busy);
          return (
            <Panel
              key={agent.id}
              title={item?.display_name || agent.name}
              description={item?.target_path ? "配置检测结果与接管操作" : "尚未检测到客户端配置路径。"}
              actions={(
                <div className="flex items-center gap-2">
                  <Badge tone={status.tone}>{status.text}</Badge>
                  {busy === agent.id ? <Spinner className="size-4" /> : null}
                </div>
              )}
              busy={busy === agent.id}
            >
              <div className="space-y-4">
                <div className="grid gap-3 rounded-control border border-rule bg-sunken/40 p-3 md:grid-cols-2">
                  <div className="min-w-0">
                    <div className="text-meta text-ink-3">AMKR 入口地址</div>
                    <div className="mt-1 flex flex-wrap items-center gap-2">
                      {baseUrl ? <CopyChip text={baseUrl} label="复制入口地址" /> : <span className="text-meta text-ink-3">运行设置未提供监听地址</span>}
                      <Badge tone={authEnabled ? "good" : "warn"}>{authEnabled ? "本地鉴权已启用" : "本地鉴权未启用"}</Badge>
                    </div>
                  </div>
                  <div className="min-w-0">
                    <div className="text-meta text-ink-3">配置路径</div>
                    <div className="mt-1 flex flex-wrap items-center gap-2">
                      {item?.target_path ? <CopyChip text={item.target_path} /> : <span className="text-meta text-ink-3">未检测到配置文件</span>}
                    </div>
                  </div>
                </div>

                {errorByAgent[agent.id] ? <Banner tone="critical">{errorByAgent[agent.id]}</Banner> : null}
                {item?.error ? <Banner tone="critical">{item.error}</Banner> : null}

                <p className="text-meta text-ink-3">
                  写入 AMKR 当前可用的全部模型（至少有一把启用 Key 的模型与别名，含已配置的 unified-model），在客户端自己的模型选择器里切换。
                </p>

                <details className="rounded-control border border-rule bg-panel">
                  <summary className="cursor-pointer px-3 py-2 text-meta text-ink-2">
                    变更字段 · {PREVIEW[agent.id]?.length ?? 0} 项
                  </summary>
                  <div className="space-y-1 border-t border-rule px-3 py-2">
                    {(PREVIEW[agent.id] ?? []).map((name) => (
                      <div key={name} className="font-mono text-[11px] text-ink-3">{name}</div>
                    ))}
                  </div>
                </details>

                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    variant="primary"
                    loading={busy === agent.id}
                    disabled={disabled}
                    onClick={() => void apply(agent.id, item?.display_name || agent.name)}
                  >
                    应用
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={disabled || !item?.backup_available}
                    onClick={() => void rollback(agent.id, item?.display_name || agent.name)}
                  >
                    回退
                  </Button>
                  {!item?.target_exists && !item?.current_is_applied ? (
                    <span className="text-meta text-ink-3">目标配置尚未找到，应用后会创建。</span>
                  ) : null}
                </div>
              </div>
            </Panel>
          );
        })
      )}
    </div>
  );
}
