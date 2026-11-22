// 集成：把本机 CLI 客户端接到 AMKR 的统一入口。
//
// 每个代理工具独立一张面板：读取它的目标配置路径、当前是否已被接管、备份是否可回退，
// 并把 AMKR 的入口地址与本地鉴权状态放在同一处。单个 Agent 读取失败只影响自己的面板，
// 与 legacy integrations.js 的隔离口径一致。

import { useEffect, useMemo, useState } from "react";
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
import { Combobox } from "@/components/ui/Combobox";
import { Select } from "@/components/ui/Select";
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
    "model（选择默认模型时）",
  ],
  codex: [
    "model_provider",
    "model（选择默认模型时）",
    "model_reasoning_effort（选择推理强度时）",
    "model_catalog_json（全部可用模型）",
    "model_providers.OpenAI",
    "auth.json",
  ],
  "pi-agent": [
    "providers.amkr",
    "providers.amkr.baseUrl",
    "providers.amkr.apiKey",
    "providers.amkr.models（全部可用模型）",
    "settings.json 的默认模型 / 推理强度（选择时）",
  ],
};

const DRAFT_KEY = "amkr.integrations.draft";

function readDraft(): { models: Record<string, string>; efforts: Record<string, string> } {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(DRAFT_KEY) || "{}");
    if (!value || typeof value !== "object") return { models: {}, efforts: {} };
    const draft = value as { models?: unknown; efforts?: unknown };
    const clean = (entry: unknown) => Object.fromEntries(
      Object.entries(entry && typeof entry === "object" ? entry : {}).filter(
        ([key, value]) => AGENTS.some((agent) => agent.id === key) && typeof value === "string" && value.length <= 256,
      ),
    ) as Record<string, string>;
    return { models: clean(draft.models), efforts: clean(draft.efforts) };
  } catch {
    return { models: {}, efforts: {} };
  }
}

function reasoningOptions(agent: string) {
  const values = agent === "codex"
    ? ["minimal", "low", "medium", "high", "xhigh"]
    : ["off", "minimal", "low", "medium", "high", "xhigh"];
  return values.map((value) => ({ value, label: value }));
}

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
  const [draft] = useState(readDraft);
  const [modelByAgent, setModelByAgent] = useState<Record<string, string>>(draft.models);
  const [effortByAgent, setEffortByAgent] = useState<Record<string, string>>(draft.efforts);
  useEffect(() => {
    try {
      // 只保存模型和档位草稿，不保存凭据；sessionStorage 限于当前标签页。
      sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ models: modelByAgent, efforts: effortByAgent }));
    } catch {
      // 存储不可用不阻止接入，刷新后草稿会丢失。
    }
  }, [modelByAgent, effortByAgent]);

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
  const modelsQuery = useQuery({
    queryKey: ["integration-models"],
    queryFn: () => api.v1Models(),
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
  const modelNames = [...new Set((modelsQuery.data?.data ?? []).map((model) => model.id).filter(Boolean))];
  const modelOptions = modelNames.map((name) => ({ value: name, label: name }));
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
        modelsQuery.refetch(),
      ]);
    } catch {
      // 查询自身的 error 会由上面的 Banner 呈现；刷新按钮只需退出忙碌态。
    } finally {
      setRefreshing(false);
    }
  }

  async function apply(agent: string, name: string) {
    const model = modelByAgent[agent] || "";
    const effort = effortByAgent[agent] || "";
    const options = { ...(model ? { default_model: model } : {}), ...(effort ? { reasoning_effort: effort } : {}) };
    const extraFiles = agent === "codex"
      ? "还会写入同目录的 auth.json 和模型目录。"
      : agent === "pi-agent" && (model || effort) ? "还会写入同目录的 settings.json。" : "";
    const ok = await confirm({
      title: `接管 ${name} 配置`,
      message: `将写入 ${name} 的配置文件（${itemByAgent.get(agent)?.target_path || "目标路径未提供"}），并备份原配置以便回退。${extraFiles}自动接入使用具有完整管理权限的本地 Key。${model ? `新会话默认模型：${model}。` : "默认模型沿用现有行为。"}${effort ? `推理强度：${effort}。` : ""}确定继续吗？`,
      confirmLabel: "应用",
    });
    if (!ok) return;
    setBusy(agent);
    setErrorByAgent((current) => ({ ...current, [agent]: null }));
    try {
      await api.applyIntegration(agent, Object.keys(options).length ? options : undefined);
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
      message: `从备份恢复 ${name} 的原配置？接管后手工修改的当前配置也会被备份内容替换。`,
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

      <Panel title="手动接入" description="为其他应用创建受限的下游客户端 Access Key，再复制所需协议的地址。">
        <div className="space-y-3 text-body text-ink-2">
          <p>以下地址用于 AMKR 服务所在的机器；从其他机器连接时，请使用服务所在机器可达的地址，并先确认监听和网络安全配置。</p>
          {baseUrl ? (
            <div className="flex flex-wrap gap-2">
              <CopyChip text={`${baseUrl}/v1`} label="OpenAI Base URL" />
              <CopyChip text={baseUrl} label="Anthropic Base URL" />
            </div>
          ) : <p>无法确定入口地址，请检查运行设置。</p>}
          <p>API Key 使用创建时显示的下游访问密钥，示例占位符为 <code>YOUR_ACCESS_KEY</code>。Anthropic 请求使用 <code>x-api-key</code> 和 <code>anthropic-version</code> 头，并在请求体中填写已授权的模型名。不要在共享场所粘贴真实密钥。</p>
          <div className="flex flex-wrap items-center gap-2">
            <a href="#/access-keys" className="text-ink underline underline-offset-2">管理访问密钥</a>
            {baseUrl ? <CopyChip label="OpenAI 模型列表" text={`curl ${baseUrl}/v1/models -H "Authorization: Bearer YOUR_ACCESS_KEY"`} /> : null}
            {baseUrl ? <CopyChip label="Anthropic 请求地址" text={`${baseUrl}/v1/messages`} /> : null}
          </div>
        </div>
      </Panel>

      {modelsQuery.error ? (
        <Banner tone="warn" title="读取模型列表失败">{errorText(modelsQuery.error)}；可刷新状态后重试。</Banner>
      ) : !modelsQuery.isPending && modelNames.length === 0 ? (
        <Banner tone="warn" title="没有可用模型">请先在模型路由中配置至少一个启用的上游 Key，再刷新状态。</Banner>
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
                  自动接入将完整管理 Key 写入客户端配置；请勿把该配置文件分享给其他人。模型与推理强度选择仅影响新会话，客户端命令行或项目设置可能覆盖它们。
                </p>
                <div className="grid gap-3 md:grid-cols-2">
                  <div className="space-y-1">
                    <span className="text-meta text-ink-2">默认模型</span>
                    <Combobox
                      aria-label={`${agent.name} 默认模型`}
                      options={modelOptions}
                      value={modelByAgent[agent.id] || ""}
                      placeholder="沿用现有默认模型"
                      disabled={disabled || modelsQuery.isPending || Boolean(modelsQuery.error) || !modelNames.length}
                      onValueChange={(value) => setModelByAgent((current) => ({ ...current, [agent.id]: typeof value === "string" ? value : value[0] || "" }))}
                    />
                    {modelByAgent[agent.id] ? (
                      <Button size="sm" variant="ghost" onClick={() => setModelByAgent((current) => ({ ...current, [agent.id]: "" }))}>
                        清除所选模型
                      </Button>
                    ) : null}
                  </div>
                  {agent.id !== "claude-code" ? (
                    <label className="space-y-1">
                      <span className="text-meta text-ink-2">推理强度</span>
                      <Select
                        aria-label={`${agent.name} 推理强度`}
                        value={effortByAgent[agent.id] || ""}
                        onChange={(event) => setEffortByAgent((current) => ({ ...current, [agent.id]: event.target.value }))}
                        disabled={disabled}
                        placeholder="沿用现有设置"
                        options={reasoningOptions(agent.id)}
                      />
                    </label>
                  ) : null}
                </div>
                <p className="text-meta text-ink-3">可选强度取决于客户端版本和上游模型，AMKR 不保证每个模型都支持。</p>

                <details className="rounded-control border border-rule bg-panel">
                  <p className="px-3 pt-2 text-meta text-ink-3">以下为可能变更的字段清单，不是逐字差异预览。</p>
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
                    variant="secondary"
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
