// 设置：把一个实例的运行参数、凭据、服务动作与迁移入口集中在一页。
//
// 运行设置采用「基线 + 草稿」：只有真正改过的字段才会提交；脏状态显示在固定底栏，
// 这样用户不用在长表单里来回找保存按钮。保存失败时草稿保留，便于原地修正。

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity, Copy, Download, ExternalLink, Play, RefreshCw, Shield, Square, Trash2, Upload,
} from "lucide-react";
import { toast } from "sonner";
import { api, ApiError, setKey } from "@/lib/api";
import { copyText, errorText, formatCount, formatDateTime } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import {
  ACCENT_OPTIONS, THEME_OPTIONS, getAccentPreference, getResolvedTheme, getThemePreference,
  setAccentPreference, setThemePreference, type ThemePreference,
} from "@/lib/theme";
import { cn } from "@/lib/cn";
import type {
  HealthResponse, MetricsSnapshot, ServiceActionResponse, SettingsData,
  SettingsResponse, ToolResponse, UpdateStatusResponse,
} from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Skeleton } from "@/components/ui/Skeleton";
import { Switch } from "@/components/ui/Switch";
import { Textarea } from "@/components/ui/Textarea";
import { useSecretDialog } from "@/components/ui/SecretDialog";

const SECTIONS = [
  { id: "runtime", label: "运行设置" },
  { id: "auth", label: "本地鉴权" },
  { id: "service", label: "服务控制" },
  { id: "transfer", label: "配置迁移" },
  { id: "update", label: "版本与更新" },
  { id: "appearance", label: "外观" },
  { id: "about", label: "关于" },
] as const;

type SectionId = (typeof SECTIONS)[number]["id"];

const RUNTIME_FIELDS = [
  { key: "host", label: "监听地址", type: "text", min: undefined },
  { key: "port", label: "端口", type: "number", min: 1 },
  { key: "request_timeout", label: "请求超时（秒）", type: "number", min: 0.1 },
  { key: "stream_first_byte_timeout", label: "首字节超时（秒）", type: "number", min: 0.1 },
  { key: "stream_idle_timeout", label: "流式空闲超时（秒）", type: "number", min: 0.1 },
  { key: "max_retries", label: "最大重试", type: "number", min: 0 },
] as const;

type RuntimeKey = (typeof RUNTIME_FIELDS)[number]["key"];
type RuntimeDraft = Record<RuntimeKey, string>;
type RuntimeErrors = Partial<Record<RuntimeKey, string>>;

const EMPTY_DRAFT: RuntimeDraft = {
  host: "",
  port: "",
  request_timeout: "",
  stream_first_byte_timeout: "",
  stream_idle_timeout: "",
  max_retries: "",
};

const SERVICE_GROUPS = [
  {
    title: "服务控制",
    actions: [
      { id: "start_amkr", label: "启动服务", icon: Play, confirm: undefined },
      { id: "stop_amkr", label: "停止服务", icon: Square, confirm: "停止正在运行的 AMKR 服务？正在进行中的请求会被中断。" },
      { id: "restart_amkr", label: "重启服务", icon: RefreshCw, confirm: "重启 AMKR 服务？界面会短暂断开。" },
      { id: "status_amkr", label: "查询任务", icon: Activity, confirm: undefined },
    ],
  },
  {
    title: "登录自启（当前用户）",
    actions: [
      { id: "install_user_amkr", label: "注册登录启动", icon: Shield, confirm: undefined },
      { id: "uninstall_amkr", label: "取消注册", icon: Trash2, confirm: "取消登录启动任务？正在运行的服务也会停止。", danger: true },
    ],
  },
  {
    title: "系统服务（需要管理员授权）",
    actions: [
      { id: "install_system_amkr", label: "注册开机服务", icon: Shield, confirm: "注册系统级开机服务？Windows 将请求管理员授权。" },
      { id: "start_system_amkr", label: "启动", icon: Play, confirm: undefined },
      { id: "stop_system_amkr", label: "停止", icon: Square, confirm: undefined },
      { id: "restart_system_amkr", label: "重启", icon: RefreshCw, confirm: undefined },
      { id: "uninstall_system_amkr", label: "取消系统服务", icon: Trash2, confirm: "取消系统级服务？Windows 将请求管理员授权。", danger: true },
    ],
  },
] as const;

function toDraft(settings: SettingsData | undefined): RuntimeDraft {
  return {
    host: String(settings?.host ?? ""),
    port: String(settings?.port ?? ""),
    request_timeout: String(settings?.request_timeout ?? ""),
    stream_first_byte_timeout: String(settings?.stream_first_byte_timeout ?? ""),
    stream_idle_timeout: String(settings?.stream_idle_timeout ?? ""),
    max_retries: String(settings?.max_retries ?? ""),
  };
}

function normalizeDraft(key: RuntimeKey, value: string): string {
  return key === "host" ? value.trim() : value.trim();
}

function validateRuntime(draft: RuntimeDraft): RuntimeErrors {
  const errors: RuntimeErrors = {};
  if (!draft.host.trim()) errors.host = "监听地址不能为空。";
  const port = Number(draft.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) errors.port = "端口必须是 1–65535 的整数。";
  for (const field of RUNTIME_FIELDS) {
    if (field.type !== "number" || field.key === "port") continue;
    const raw = draft[field.key].trim();
    const value = Number(raw);
    if (!raw || !Number.isFinite(value) || value < (field.min ?? 0)) errors[field.key] = "必须填写大于等于下限的数字。";
    if (field.key === "max_retries" && raw && !Number.isInteger(value)) errors[field.key] = "最大重试必须是整数。";
  }
  return errors;
}

function countEntries(value: unknown): number {
  if (Array.isArray(value)) return value.length;
  if (value && typeof value === "object") return Object.keys(value).length;
  return 0;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

// 顶层 tasks 与 workspaces.<name>.tasks 都属于配置里的任务；导入预览把两处一起数，
// 否则命名空间里的任务会被漏掉。
function countTasks(config: Record<string, unknown>): number {
  let total = countEntries(config.tasks);
  if (isPlainObject(config.workspaces)) {
    for (const space of Object.values(config.workspaces)) {
      if (isPlainObject(space)) total += countEntries(space.tasks);
    }
  }
  return total;
}

export default function SettingsPage() {
  const confirm = useConfirm();
  const showSecret = useSecretDialog();
  const queryClient = useQueryClient();
  const [active, setActive] = useState<SectionId>("runtime");

  const settingsQuery = useQuery({
    queryKey: queryKeys.settings(),
    queryFn: api.settings,
    retry: false,
  });
  const toolQuery = useQuery({
    queryKey: queryKeys.tool(),
    queryFn: api.tool,
    retry: false,
  });
  const updateStatusQuery = useQuery({
    queryKey: queryKeys.updateStatus(),
    queryFn: api.updateStatus,
    retry: false,
  });
  const healthQuery = useQuery({
    queryKey: queryKeys.health(),
    queryFn: api.health,
    retry: false,
  });
  const metricsQuery = useQuery({
    queryKey: queryKeys.metrics(1),
    queryFn: () => api.metrics(1),
    retry: false,
  });

  const settings = settingsQuery.data?.settings;
  const [draft, setDraft] = useState<RuntimeDraft>(EMPTY_DRAFT);
  const [baseline, setBaseline] = useState<RuntimeDraft>(EMPTY_DRAFT);
  const [errors, setErrors] = useState<RuntimeErrors>({});
  const [saving, setSaving] = useState(false);
  const [runtimeError, setRuntimeError] = useState("");
  const initialized = useRef(false);

  useEffect(() => {
    if (initialized.current || !settings) return;
    initialized.current = true;
    const next = toDraft(settings);
    setDraft(next);
    setBaseline(next);
  }, [settings]);

  const changedKeys = useMemo(() => RUNTIME_FIELDS
    .map((field) => field.key)
    .filter((key) => normalizeDraft(key, draft[key]) !== normalizeDraft(key, baseline[key])), [draft, baseline]);
  const dirtyCount = changedKeys.length;

  function updateDraft(key: RuntimeKey, value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
    setErrors((current) => ({ ...current, [key]: undefined }));
  }

  function discardDraft() {
    setDraft(baseline);
    setErrors({});
    setRuntimeError("");
    toast.success("已放弃未保存的更改。");
  }

  async function commitRuntime(hostValue: string) {
    const validation = validateRuntime(draft);
    if (Object.keys(validation).length) {
      setErrors(validation);
      return;
    }
    setSaving(true);
    setRuntimeError("");
    try {
      const payload: Record<string, unknown> = {};
      for (const key of changedKeys) {
        const value = normalizeDraft(key, draft[key]);
        payload[key] = key === "host" ? value : Number(value);
      }
      if (!changedKeys.length) return;
      const result = await api.updateSettings(settingsQuery.data?.config_revision ?? "", payload);
      const next = result.settings ? toDraft(result.settings) : { ...draft, host: hostValue };
      setDraft(next);
      setBaseline(next);
      queryClient.setQueryData<SettingsResponse>(queryKeys.settings(), (current) => ({
        ...(current ?? {}),
        ...result,
        settings: result.settings ?? current?.settings,
      }));
      toast.success("运行设置已保存。监听地址变更将在服务重启后生效。");
    } catch (error) {
      setRuntimeError(errorText(error));
    } finally {
      setSaving(false);
    }
  }

  async function saveRuntime() {
    const validation = validateRuntime(draft);
    if (Object.keys(validation).length) {
      setErrors(validation);
      return;
    }
    const hostValue = draft.host.trim();
    const loopback = new Set(["127.0.0.1", "localhost", "::1"]);
    // 只在监听地址真的发生变化时拦截；改端口/超时不应被已有非回环配置反复打断。
    if (changedKeys.includes("host") && !loopback.has(hostValue.toLowerCase())) {
      const ok = await confirm({
        title: "确认监听地址",
        message: "监听地址不是本机回环地址。远程客户端将可能访问 AMKR 管理 API，确定继续吗？",
        confirmLabel: "继续保存",
        danger: true,
      });
      if (!ok) return;
    }
    await commitRuntime(hostValue);
  }

  async function resetLocalKey() {
    const ok = await confirm({
      title: "重置本地鉴权 Key",
      message: "重置本地鉴权 Key？现有客户端需要改用新 Key。",
      confirmLabel: "重置",
      danger: true,
    });
    if (!ok) return;
    try {
      const result = await api.regenerateLocalKey(settingsQuery.data?.config_revision ?? "");
      if (result.local_api_key) {
        // 先更新本页凭据存储，再把明文交给一次性 SecretDialog；不把明文写进组件状态。
        setKey(result.local_api_key);
        await showSecret({
          title: "新的本地鉴权 Key",
          warning: "这是唯一一次显示新 Key。请立即更新所有客户端；关闭后无法再查看。",
          items: [
            { label: "本地鉴权 Key", value: result.local_api_key },
            ...(result.local_api_key_fingerprint ? [{ label: "指纹", value: result.local_api_key_fingerprint }] : []),
          ],
        });
      }
      queryClient.setQueryData<SettingsResponse>(queryKeys.settings(), (current) => {
        const nextSettings = result.settings ?? current?.settings;
        return {
          ...(current ?? {}),
          ...result,
          settings: nextSettings ? {
            ...nextSettings,
            ...(result.local_api_key ? { local_auth_enabled: true } : {}),
            ...(result.local_api_key_fingerprint ? { local_api_key_fingerprint: result.local_api_key_fingerprint } : {}),
          } : nextSettings,
        };
      });
      await settingsQuery.refetch();
      toast.success("本地鉴权 Key 已重置。");
    } catch (error) {
      toast.error(`重置本地鉴权 Key 失败：${errorText(error)}`);
    }
  }

  const [serviceBusy, setServiceBusy] = useState<string | null>(null);
  const [serviceOutput, setServiceOutput] = useState("");
  const [serviceError, setServiceError] = useState("");

  async function runServiceAction(action: { id: string; label: string; confirm?: string; danger?: boolean }) {
    if (serviceBusy) return;
    if (action.confirm) {
      const ok = await confirm({
        title: action.label,
        message: action.confirm,
        confirmLabel: action.label,
        danger: Boolean(action.danger),
      });
      if (!ok) return;
    }
    setServiceBusy(action.id);
    setServiceError("");
    setServiceOutput("");
    try {
      const result = await api.runService(action.id) as ServiceActionResponse;
      setServiceOutput(typeof result.text === "string" ? result.text : "（无输出）");
      await healthQuery.refetch();
    } catch (error) {
      setServiceError(errorText(error));
    } finally {
      setServiceBusy(null);
    }
  }

  const [transferText, setTransferText] = useState("");
  const [transferBusy, setTransferBusy] = useState<"export" | "import" | null>(null);
  const [transferError, setTransferError] = useState("");
  const [preview, setPreview] = useState<null | {
    config: Record<string, unknown>;
    providers: number;
    models: number;
    tasks: number;
    currentProviders: number;
    currentModels: number;
    currentTasks: number;
    accessKeys: number;
    bundleAccessKeys: number;
  }>(null);
  const [previewOpen, setPreviewOpen] = useState(false);

  async function exportConfig() {
    setTransferBusy("export");
    setTransferError("");
    try {
      const result = await api.exportConfig();
      const config = result.config ?? (result as unknown as Record<string, unknown>);
      setTransferText(JSON.stringify(config, null, 2));
      toast.success("已导出可迁移配置。");
    } catch (error) {
      setTransferError(errorText(error));
    } finally {
      setTransferBusy(null);
    }
  }

  async function copyExport() {
    if (!transferText.trim()) return;
    try {
      await copyText(transferText);
      toast.success("配置已复制。");
    } catch (error) {
      toast.error(`复制失败：${errorText(error)}`);
    }
  }

  function downloadExport() {
    if (!transferText.trim()) return;
    if (typeof URL.createObjectURL !== "function") {
      toast.error("当前环境不支持文件下载，请使用复制。");
      return;
    }
    const blob = new Blob([transferText], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = "amkr-config.json";
    link.click();
    URL.revokeObjectURL(url);
  }

  async function prepareImport() {
    setTransferError("");
    let parsed: unknown;
    try {
      parsed = JSON.parse(transferText);
    } catch {
      setTransferError("配置内容不是有效 JSON。");
      return;
    }
    if (!isPlainObject(parsed)) {
      setTransferError("配置内容必须是 JSON 对象。");
      return;
    }
    let currentProviders = 0;
    let currentModels = 0;
    let currentTasks = 0;
    let accessKeys = 0;
    try {
      const [providers, models, tasks, keys] = await Promise.all([
        api.providers(), api.models(), api.tasks(), api.accessKeys(),
      ]);
      currentProviders = providers.providers.length;
      currentModels = models.models.length;
      currentTasks = tasks.tasks?.length ?? 0;
      accessKeys = keys.access_keys.length;
    } catch {
      // 预览是辅助信息；计数接口不可用时仍允许用户带着包内数量确认导入。
    }
    setPreview({
      config: parsed,
      providers: countEntries(parsed.providers),
      models: countEntries(parsed.models),
      tasks: countTasks(parsed),
      currentProviders,
      currentModels,
      currentTasks,
      accessKeys,
      bundleAccessKeys: countEntries(parsed.access_keys),
    });
    setPreviewOpen(true);
  }

  async function importConfig() {
    if (!preview) return;
    setTransferBusy("import");
    setTransferError("");
    try {
      const providers = await api.providers();
      const result = await api.importConfig(providers.config_revision ?? settingsQuery.data?.config_revision ?? "", preview.config);
      if (result.imported === false) throw new Error("AMKR 未确认配置导入。");
      setPreviewOpen(false);
      setPreview(null);
      await queryClient.invalidateQueries();
      toast.success("配置已导入，AMKR 已热重载。");
    } catch (error) {
      setTransferError(error instanceof ApiError && error.status === 409
        ? "配置版本已变化，请重新导出/粘贴后再导入。"
        : errorText(error));
    } finally {
      setTransferBusy(null);
    }
  }

  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [updateResult, setUpdateResult] = useState<UpdateStatusResponse | null>(null);
  const [updating, setUpdating] = useState(false);
  const [webuiBusy, setWebuiBusy] = useState(false);
  const updateStatus = updateStatusQuery.data as UpdateStatusResponse | undefined;
  const tool = toolQuery.data as ToolResponse | undefined;

  async function checkUpdate() {
    setCheckingUpdate(true);
    try {
      const result = await api.checkUpdate();
      setUpdateResult(result);
      if (result.update_available) toast.success("发现新版本。");
      else toast.success("当前已是最新版本。");
      await queryClient.invalidateQueries({ queryKey: queryKeys.tool() });
    } catch (error) {
      toast.error(`检查更新失败：${errorText(error)}`);
    } finally {
      setCheckingUpdate(false);
    }
  }

  async function applyUpdate() {
    const latest = updateResult?.latest_version || tool?.latest_version || "新版本";
    const ok = await confirm({
      title: "立即更新",
      message: `将下载并安装 ${latest}，然后自动重启服务。更新期间界面会短暂断开，请稍后刷新。`,
      confirmLabel: "更新并重启",
      danger: true,
    });
    if (!ok) return;
    setUpdating(true);
    try {
      const result = await api.applyUpdate();
      toast.success(result?.message || "更新完成，服务正在重启。");
    } catch (error) {
      toast.error(errorText(error));
    } finally {
      setUpdating(false);
    }
  }

  async function toggleWebui(enabled: boolean) {
    setWebuiBusy(true);
    try {
      const result = await api.setWebui(enabled) as ToolResponse;
      queryClient.setQueryData<ToolResponse>(queryKeys.tool(), (current) => ({ ...(current ?? {}), ...result }));
      toast.success(result.enabled === result.webui_mounted
        ? (result.enabled ? "WebUI 已启用。" : "WebUI 已关闭。")
        : "WebUI 配置已保存，重启服务后生效。");
    } catch (error) {
      toast.error(errorText(error));
    } finally {
      setWebuiBusy(false);
    }
  }

  const [theme, setTheme] = useState<ThemePreference>(getThemePreference());
  const [accent, setAccent] = useState(getAccentPreference());
  const [customHex, setCustomHex] = useState(() => {
    const value = getAccentPreference();
    return value.startsWith("#") ? value : "#3b6ef5";
  });
  const resolved = getResolvedTheme(theme);
  const customSelected = accent.startsWith("#");

  function chooseTheme(next: string) {
    if (next === "system" || next === "light" || next === "dark") {
      setTheme(next);
      setThemePreference(next);
    }
  }

  function chooseAccent(next: string) {
    if (next === "custom") {
      setAccent(customHex);
      setAccentPreference(customHex);
      return;
    }
    setAccent(next);
    setAccentPreference(next);
  }

  function applyCustomHex(value: string) {
    setCustomHex(value);
    if (/^#[0-9a-fA-F]{6}$/.test(value)) {
      setAccent(value);
      setAccentPreference(value);
    }
  }

  const metrics = metricsQuery.data as MetricsSnapshot | undefined;
  const health = healthQuery.data as HealthResponse | undefined;

  return (
    <div className={cn("flex min-w-0 flex-col gap-3", dirtyCount > 0 && "pb-20")}>
      <PageHeader
        title="设置"
        description="设置：运行参数、本地鉴权、服务控制与配置迁移。"
        actions={health?.base_url ? <Badge tone="neutral">{health.base_url}</Badge> : null}
      />

      <div className="flex min-w-0 flex-col gap-3 md:flex-row md:items-start">
        <nav aria-label="设置分节" className="flex shrink-0 flex-wrap gap-1 rounded-panel border border-rule bg-panel p-1.5 md:w-40 md:flex-col">
          {SECTIONS.map((section) => (
            <button
              key={section.id}
              type="button"
              aria-current={active === section.id ? "page" : undefined}
              className={cn(
                "whitespace-nowrap rounded-control px-3 py-1.5 text-left text-meta transition-colors hover:bg-hover hover:text-ink",
                active === section.id ? "bg-selected font-medium text-ink" : "text-ink-2",
              )}
              onClick={() => {
                setActive(section.id);
                const target = document.getElementById(`settings-${section.id}`);
                if (target && typeof target.scrollIntoView === "function") target.scrollIntoView({ behavior: "smooth", block: "start" });
              }}
            >
              {section.label}
            </button>
          ))}
        </nav>

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          {settingsQuery.error ? <Banner tone="critical" title="读取设置失败">{errorText(settingsQuery.error)}</Banner> : null}
          {healthQuery.error ? <Banner tone="critical" title="读取健康状态失败">{errorText(healthQuery.error)}</Banner> : null}

          <section id="settings-runtime" className="scroll-mt-4">
            <Panel
              title="运行设置"
              description={settingsQuery.data?.config_revision ? `配置版本 ${String(settingsQuery.data.config_revision).slice(0, 8)}` : "读取中…"}
              actions={settingsQuery.isPending ? <Skeleton variant="text" className="w-16" /> : null}
            >
              {!settings ? (
                <Banner tone="warn">运行设置暂不可用。</Banner>
              ) : (
                <div className="space-y-4">
                  <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                    {RUNTIME_FIELDS.map((field) => (
                      <Field
                        key={field.key}
                        label={field.label}
                        required
                        error={errors[field.key]}
                        hint={field.type === "number" && field.key !== "port" ? "留空或输入数字；上限由服务端校验。" : undefined}
                      >
                        <Input
                          type={field.type}
                          min={field.type === "number" ? field.min : undefined}
                          max={field.key === "port" ? 65535 : undefined}
                          step={field.key === "max_retries" || field.key === "port" ? 1 : 0.1}
                          required
                          value={draft[field.key]}
                          onChange={(event) => updateDraft(field.key, event.target.value)}
                        />
                      </Field>
                    ))}
                  </div>
                  {runtimeError ? <Banner tone="critical">{runtimeError}</Banner> : null}
                  <p className="text-meta text-ink-3">监听地址变更将在服务重启后生效。只有发生变化的字段会提交；未保存时页面底部会出现保存条。</p>
                </div>
              )}
            </Panel>
          </section>

          <section id="settings-auth" className="scroll-mt-4">
            <Panel
              title="本地鉴权"
              description="本实例的入口凭据；重置后现有客户端需要改为新 Key。"
            >
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-control border border-rule bg-sunken/40 p-3">
                <div className="min-w-0">
                  <div className="text-meta text-ink-3">当前状态</div>
                  <div className="mt-1 flex flex-wrap items-center gap-2">
                    <Badge tone={settings?.local_auth_enabled ? "good" : "warn"}>{settings?.local_auth_enabled ? "已启用" : "未启用"}</Badge>
                    {settings?.local_api_key_fingerprint ? <span className="font-mono text-meta text-ink-2">{settings.local_api_key_fingerprint}</span> : null}
                  </div>
                </div>
                <Button variant="secondary" onClick={() => void resetLocalKey()}>重置本地鉴权 Key</Button>
              </div>
              <p className="mt-2 text-meta text-ink-3">新 Key 只会显示一次，请在关闭对话框前完成复制。</p>
            </Panel>
          </section>

          <section id="settings-service" className="scroll-mt-4">
            <Panel
              title="服务控制"
              description="这些动作在本机执行，需要服务进程具备相应权限。"
              actions={serviceBusy ? <Badge tone="neutral">正在执行 {serviceBusy}</Badge> : null}
              busy={Boolean(serviceBusy)}
            >
              <div className="space-y-4">
                {SERVICE_GROUPS.map((group) => (
                  <div key={group.title}>
                    <h3 className="mb-2 text-meta font-medium text-ink-2">{group.title}</h3>
                    <div className="flex flex-wrap gap-2">
                      {group.actions.map((action) => {
                        const Icon = action.icon;
                        return (
                          <Button
                            key={action.id}
                            size="sm"
                            variant={"danger" in action && action.danger ? "danger-ghost" : "secondary"}
                            leadingIcon={<Icon aria-hidden className="size-3.5" />}
                            loading={serviceBusy === action.id}
                            disabled={Boolean(serviceBusy)}
                            onClick={() => void runServiceAction(action)}
                          >
                            {action.label}
                          </Button>
                        );
                      })}
                    </div>
                  </div>
                ))}
                {serviceError ? <Banner tone="critical">{serviceError}</Banner> : null}
                {serviceOutput ? <pre className="max-h-56 overflow-auto rounded-control border border-rule bg-sunken p-3 font-mono text-[12px] whitespace-pre-wrap text-ink-2">{serviceOutput}</pre> : null}
              </div>
            </Panel>
          </section>

          <section id="settings-transfer" className="scroll-mt-4">
            <Panel
              title="配置迁移"
              description="导出整台实例的供应商、模型与任务；导入会合并配置，访问密钥不随配置迁移。"
              actions={transferBusy ? <Badge tone="neutral">{transferBusy === "export" ? "正在导出" : "正在导入"}</Badge> : null}
            >
              <div className="space-y-3">
                <Textarea
                  value={transferText}
                  onChange={(event) => setTransferText(event.target.value)}
                  placeholder="导出后在此显示，或粘贴可迁移配置以导入。"
                  className="min-h-40 font-mono text-[12px]"
                  aria-label="配置 JSON"
                />
                {transferError ? <Banner tone="critical">{transferError}</Banner> : null}
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant="secondary" loading={transferBusy === "export"} onClick={() => void exportConfig()}>导出</Button>
                  <Button size="sm" variant="secondary" disabled={!transferText.trim()} onClick={() => void copyExport()}>
                    <Copy aria-hidden className="size-3.5" />复制
                  </Button>
                  <Button size="sm" variant="secondary" disabled={!transferText.trim()} onClick={downloadExport}>
                    <Download aria-hidden className="size-3.5" />下载
                  </Button>
                  <Button size="sm" variant="secondary" disabled={!transferText.trim() || transferBusy !== null} onClick={() => void prepareImport()}>
                    <Upload aria-hidden className="size-3.5" />导入配置
                  </Button>
                </div>
              </div>
            </Panel>
          </section>

          <section id="settings-update" className="scroll-mt-4">
            <Panel
              title="版本与更新"
              description="检查 GitHub 上的新版本；自更新仅在服务端具备能力且确有更新时出现。"
              actions={tool?.update_available ? <Badge tone="warn">发现新版本</Badge> : tool ? <Badge tone="neutral">v{tool.version}</Badge> : null}
            >
              {toolQuery.error ? <Banner tone="critical">读取版本信息失败：{errorText(toolQuery.error)}</Banner> : null}
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                <div><div className="text-meta text-ink-3">当前版本</div><div className="mt-1 text-body text-ink">{tool?.version || "暂不可用"}</div></div>
                <div><div className="text-meta text-ink-3">最新版本</div><div className="mt-1 text-body text-ink">{tool?.latest_version || "暂不可用"}</div></div>
                <div><div className="text-meta text-ink-3">WebUI 资产</div><div className="mt-1 text-body text-ink">{tool?.webui_available ? "已安装" : "未随包安装"}</div></div>
                <div><div className="text-meta text-ink-3">WebUI 运行中</div><div className="mt-1 text-body text-ink">{tool?.webui_mounted ? "是" : "否"}</div></div>
              </div>
              {updateStatusQuery.error ? <Banner tone="critical">读取更新状态失败：{errorText(updateStatusQuery.error)}</Banner> : null}
              <div className="mt-4 flex flex-wrap items-center gap-2">
                <Button size="sm" variant="secondary" loading={checkingUpdate} onClick={() => void checkUpdate()}>检查更新</Button>
                {updateResult?.update_available && updateStatus?.available ? (
                  <Button size="sm" variant="danger" loading={updating} onClick={() => void applyUpdate()}>立即更新</Button>
                ) : null}
                {updateStatus && updateStatus.supported === false ? <span className="text-meta text-ink-3">当前构建不支持自更新。</span> : null}
                {updateResult?.release_url ? (
                  <a href={updateResult.release_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-meta text-accent hover:underline">
                    发布页面 <ExternalLink aria-hidden className="size-3" />
                  </a>
                ) : null}
                <div className="ml-auto">
                  <Switch
                    checked={Boolean(tool?.webui_enabled)}
                    onCheckedChange={(checked) => void toggleWebui(checked)}
                    disabled={webuiBusy || !tool}
                    label="WebUI 已启用"
                  />
                </div>
              </div>
              <p className="mt-2 text-meta text-ink-3">自更新：{updateStatus?.available ? "当前构建支持" : "当前构建不支持"}{updateStatus?.version ? ` · ${updateStatus.version}` : ""}</p>
            </Panel>
          </section>

          <section id="settings-appearance" className="scroll-mt-4">
            <Panel title="外观" description="主题与主色立即预览；选择保存在本浏览器。">
              <div className="space-y-4">
                <div>
                  <div className="mb-1.5 text-meta font-medium text-ink-2">主题</div>
                  <Segmented
                    aria-label="选择主题"
                    value={theme}
                    onValueChange={chooseTheme}
                    options={THEME_OPTIONS.map((option) => ({ value: option.id, label: option.label }))}
                  />
                  <p className="mt-1.5 text-meta text-ink-3">
                    {theme === "system" ? `当前系统设置为${resolved === "dark" ? "深色" : "浅色"}模式，界面已自动适配。` : theme === "dark" ? "低照度深色控制台配色。" : "明快清爽的浅色高对比度工作台。"}
                  </p>
                </div>
                <div>
                  <div className="mb-1.5 text-meta font-medium text-ink-2">主色</div>
                  <Segmented
                    aria-label="选择主色"
                    value={customSelected ? "custom" : accent}
                    onValueChange={chooseAccent}
                    options={[...ACCENT_OPTIONS.map((option) => ({ value: option.id, label: option.label })), { value: "custom", label: "自定义" }]}
                  />
                  <div className="mt-2 flex flex-wrap items-center gap-2">
                    <input
                      type="color"
                      aria-label="自定义主色"
                      value={cachedHex(accent, customHex)}
                      onChange={(event) => applyCustomHex(event.target.value)}
                      className="h-8 w-12 cursor-pointer rounded border border-rule-strong bg-panel p-1"
                    />
                    <Input
                      value={customHex}
                      aria-label="自定义主色 HEX"
                      className="w-32 font-mono"
                      onChange={(event) => applyCustomHex(event.target.value)}
                      placeholder="#3b6ef5"
                    />
                    {customSelected ? <Badge tone="good">已应用自定义色</Badge> : null}
                    {!customSelected ? <span className="text-meta text-ink-3">选择「自定义」后输入 #rrggbb。</span> : null}
                  </div>
                </div>
              </div>
            </Panel>
          </section>

          <section id="settings-about" className="scroll-mt-4">
            <Panel title="关于" description="运行状态、数据目录与计数口径。">
              {metricsQuery.error ? <Banner tone="critical">读取运行指标失败：{errorText(metricsQuery.error)}</Banner> : null}
              {metricsQuery.isPending ? <Skeleton variant="table" /> : (
                <div className="space-y-3">
                  <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                    <div><div className="text-meta text-ink-3">版本</div><div className="mt-1 text-body text-ink">{health?.version || tool?.version || "—"}</div></div>
                    <div><div className="text-meta text-ink-3">启动时间</div><div className="mt-1 text-body text-ink">{metrics?.started_at ? formatDateTime(metrics.started_at) : "—"}</div></div>
                    <div><div className="text-meta text-ink-3">数据库路径</div><div className="mt-1 break-all text-body text-ink">{metrics?.database_path || "—"}</div></div>
                    <div><div className="text-meta text-ink-3">窗口请求数</div><div className="mt-1 text-body text-ink">{metrics ? formatCount(metrics.total?.requests) : "—"}</div></div>
                  </div>
                  <div>
                    <div className="text-meta text-ink-3">计数口径</div>
                    <p className="mt-1 text-meta text-ink-2">{metrics?.count_semantics || "服务端未提供计数口径。"}</p>
                  </div>
                  <div className="flex flex-wrap gap-2 text-meta text-ink-3">
                    <Badge tone="neutral">查询窗口 1 小时</Badge>
                    {health?.commit ? <Badge tone="neutral">commit {String(health.commit).slice(0, 8)}</Badge> : null}
                    {health?.build_time ? <Badge tone="neutral">构建 {health.build_time}</Badge> : null}
                  </div>
                </div>
              )}
            </Panel>
          </section>
        </div>
      </div>

      {dirtyCount > 0 ? (
        <div
          role="status"
          aria-live="polite"
          className="fixed bottom-0 left-0 right-0 z-30 flex flex-wrap items-center gap-2 border-t border-rule bg-panel px-4 py-3 shadow-[0_-4px_16px_rgba(0,0,0,0.08)] md:left-56"
        >
          <span className="text-body text-ink">有 {dirtyCount} 项未保存的更改</span>
          <div className="ml-auto flex gap-2">
            <Button variant="secondary" size="sm" disabled={saving} onClick={discardDraft}>放弃</Button>
            <Button variant="primary" size="sm" loading={saving} onClick={() => void saveRuntime()}>保存运行设置</Button>
          </div>
        </div>
      ) : null}

      <Dialog
        open={previewOpen}
        onOpenChange={setPreviewOpen}
        title="确认导入配置"
        footer={(
          <>
            <Button variant="secondary" onClick={() => setPreviewOpen(false)}>取消</Button>
            <Button variant="primary" loading={transferBusy === "import"} onClick={() => void importConfig()}>导入</Button>
          </>
        )}
      >
        {preview ? (
          <div className="space-y-3 text-body text-ink-2">
            <p>导入将追加/合并供应商与路由配置，并保留本机设置。是否继续？</p>
            <div className="space-y-1 rounded-control border border-rule bg-sunken/40 p-3 text-meta">
              <div>供应商：当前 {preview.currentProviders} 个 → 包内 {preview.providers} 个（预计新增 {Math.max(0, preview.providers - preview.currentProviders)}）</div>
              <div>模型：当前 {preview.currentModels} 个 → 包内 {preview.models} 个（预计新增 {Math.max(0, preview.models - preview.currentModels)}）</div>
              <div>任务：当前 {preview.currentTasks} 个 → 包内 {preview.tasks} 个（预计新增 {Math.max(0, preview.tasks - preview.currentTasks)}）</div>
              <div>访问密钥：当前 {preview.accessKeys} 个 → 包内 {preview.bundleAccessKeys} 个（不随配置迁移）</div>
            </div>
            <p className="text-meta text-ink-3">实际变化取决于同名条目的合并结果；访问密钥需要在目标实例上重新创建。</p>
            {transferError ? <Banner tone="critical">{transferError}</Banner> : null}
          </div>
        ) : null}
      </Dialog>
    </div>
  );
}

function cachedHex(accent: string, fallback: string): string {
  return accent.startsWith("#") ? accent : fallback;
}
