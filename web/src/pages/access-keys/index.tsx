// 访问密钥：分发给外部使用者的受限推理凭据。
//
// 明文 key 只在本页的**两个动作**后出现：新建与轮换。之后列表只显示指纹；要看旧 key
// 只能翻配置文件。凭据随列表一起发出去，等于每次打开这一页都重新泄漏一遍。

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BarChart3, Info, KeyRound, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText, formatCount, formatRelative } from "@/lib/format";
import { ProviderIcon } from "@/lib/brand-icons";
import { queryKeys } from "@/lib/query-keys";
import type { AccessKeyEntry, RouteEntry } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Chip } from "@/components/ui/Chip";
import { CopyChip } from "@/components/ui/CopyChip";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { Input } from "@/components/ui/Input";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Switch } from "@/components/ui/Switch";
import { Tooltip } from "@/components/ui/Tooltip";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { useSecretDialog } from "@/components/ui/SecretDialog";
import { navigate } from "@/app/router";
import { KeyEditor } from "./KeyEditor";
import {
  buildModelOptions,
  buildModelProviderIndex,
  groupModelsByProvider,
} from "./scope";

// 操作列固定在表格右侧：横向滚动时仍能看到编辑/轮换/删除，避免窄屏上
// 只能靠悬停出现的按钮被挤出可视区。底色跟随面板与行 hover 状态。
function TableStickyFix() {
  return (
    <style>{`
      .ak-table thead tr > th:last-child,
      .ak-table tbody td[data-row-action] {
        position: sticky;
        right: 0;
        border-left: 1px solid var(--color-rule);
      }
      .ak-table thead tr > th:last-child {
        z-index: 12;
        background: var(--color-sunken);
      }
      .ak-table tbody td[data-row-action] {
        z-index: 4;
        background: var(--color-panel);
      }
      .ak-table tbody tr:hover td[data-row-action],
      .ak-table tbody tr:focus-within td[data-row-action] {
        background: var(--color-hover);
      }
      /* 窄屏没有 hover，操作列必须一直露出来；行点击仍是主要入口。 */
      @media (max-width: 1100px) {
        .ak-table tbody td[data-row-action] > div {
          opacity: 1;
        }
      }
    `}</style>
  );
}

// 上一次调用时间按北京时间显示全称；相对时间只用于「刚刚发生过」的短提示。
function fullBeijing(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
  }).format(date);
}

function LastUsed({ value }: { value: string | null | undefined }) {
  // 三种状态必须区分开：字段缺失是「后端没给」，null 是「确实没有调用记录」。
  // 把后者显示成 0 或「-」会让人以为指标丢了。
  if (value === undefined) return <span className="whitespace-nowrap text-meta text-ink-3">后端未提供</span>;
  if (value === null) return <span className="whitespace-nowrap text-meta text-ink-3">暂无调用记录</span>;
  return (
    <span className="whitespace-nowrap text-meta text-ink-2" title={`北京时间 · ${fullBeijing(value)}`}>
      {formatRelative(value)}
    </span>
  );
}

function ScopeCell({ list, kind }: { list: string[] | null | undefined; kind: "provider" | "model" }) {
  // null 与 undefined 都表示「服务端没有下发这份清单」= 不限制；空数组才是全部禁止。
  if (list === undefined || list === null) return <Badge tone="neutral">不限制</Badge>;
  if (list.length === 0) return <Badge tone="critical">全部禁止</Badge>;
  // 表格里只露前两项；完整清单放进 Tooltip，避免长清单把行动作列顶到屏幕外。
  const shown = list.slice(0, 2);
  const cell = (
    <div
      className="flex max-w-full flex-wrap gap-1"
      tabIndex={list.length > shown.length ? 0 : undefined}
      aria-label={list.length > shown.length ? `${kind === "provider" ? "供应商" : "模型"}范围：${list.join("、")}` : undefined}
    >
      {shown.map((value) => (
        <Chip
          key={value}
          icon={kind === "provider" ? <ProviderIcon id={value} baseUrl={null} size={12} /> : undefined}
          className={kind === "model" ? "max-w-32 font-mono" : "max-w-28"}
        >
          {value}
        </Chip>
      ))}
      {list.length > shown.length ? (
        <span onClick={(event) => event.stopPropagation()} className="inline-flex">
          <Chip>+{list.length - shown.length}</Chip>
        </span>
      ) : null}
    </div>
  );
  if (list.length <= shown.length) return cell;
  return (
    <Tooltip
      content={(
        <ul className="space-y-0.5">
          {list.map((value) => <li key={value} className="break-all font-mono">{value}</li>)}
        </ul>
      )}
    >
      {cell}
    </Tooltip>
  );
}

export default function AccessKeysPage() {
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const showSecret = useSecretDialog();

  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<"all" | "enabled" | "disabled">("all");
  const [editing, setEditing] = useState<AccessKeyEntry | "__new__" | null>(null);
  // 行内启停是乐观更新：开关先动，失败再把覆盖值撤掉。pending 防止连点把两个请求
  // 乱序发出去——后发先至会让界面停在与服务端相反的状态。
  const [overrides, setOverrides] = useState<Record<string, boolean>>({});
  const [pending, setPending] = useState<string[]>([]);

  const keysQuery = useQuery({ queryKey: queryKeys.accessKeys(), queryFn: api.accessKeys });
  const providersQuery = useQuery({ queryKey: queryKeys.providers(), queryFn: api.providers });
  const modelsQuery = useQuery({ queryKey: queryKeys.models(), queryFn: api.models });
  const routesQuery = useQuery({
    queryKey: queryKeys.routes(),
    queryFn: () => api.routes().catch(() => ({ routes: [] as RouteEntry[] })),
  });
  const dimensionsQuery = useQuery({
    queryKey: queryKeys.metricDimensions({ hours: 24 }),
    queryFn: () => api.metricDimensions({ hours: 24 }),
  });

  const keys = keysQuery.data?.access_keys ?? [];
  const providers = providersQuery.data?.providers ?? [];
  const models = modelsQuery.data?.models ?? [];
  const routes = routesQuery.data?.routes ?? [];
  const revision = keysQuery.data?.config_revision ?? "";

  const index = useMemo(
    () => buildModelProviderIndex(routes, models, providers),
    [routes, models, providers],
  );
  const modelOptions = useMemo(() => buildModelOptions(models), [models]);
  const modelGroups = useMemo(
    () => groupModelsByProvider(modelOptions, index.providerToModels, providers.map((provider) => provider.id)),
    [modelOptions, index, providers],
  );

  // 24 小时请求数取不到时显示「—」而不是 0：没读到数字和确实是 0 是两件事。
  const requestsById = useMemo(() => {
    const map = new Map<string, number>();
    for (const row of dimensionsQuery.data?.access_keys ?? []) map.set(row.id, row.requests);
    return map;
  }, [dimensionsQuery.data]);

  const visible = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    return keys.filter((key) => {
      if (status === "enabled" && key.enabled === false) return false;
      if (status === "disabled" && key.enabled !== false) return false;
      if (!keyword) return true;
      return [key.name, key.id, key.key_fingerprint]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword));
    });
  }, [keys, search, status]);

  function enabledOf(key: AccessKeyEntry): boolean {
    return overrides[key.id] ?? key.enabled !== false;
  }

  async function toggleEnabled(key: AccessKeyEntry, next: boolean) {
    if (pending.includes(key.id)) return;
    setOverrides((current) => ({ ...current, [key.id]: next }));
    setPending((current) => [...current, key.id]);
    try {
      await api.updateAccessKey(revision, key.id, {
        name: key.name,
        enabled: next,
        // 启停也要提交完整的两份清单：接口要求 name/enabled/providers/models 一起给，
        // 漏传会变成 422；三态取当前值，未配置的清单要显式写 null 才是「不限制」。
        providers: key.providers == null ? null : [...key.providers].sort(),
        models: key.models == null ? null : [...key.models].sort(),
      });
      toast.success(next ? `已启用「${key.name}」。` : `已停用「${key.name}」。`);
      await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
    } catch (caught) {
      // 回滚：撤掉覆盖值就回到服务端的最新值（可能也已经不是点击前的值了）。
      setOverrides((current) => {
        const copy = { ...current };
        delete copy[key.id];
        return copy;
      });
      toast.error(`操作失败：${errorText(caught)}`);
      if (caught instanceof ApiError && caught.status === 409) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
      }
    } finally {
      setPending((current) => current.filter((id) => id !== key.id));
      setOverrides((current) => {
        const copy = { ...current };
        delete copy[key.id];
        return copy;
      });
    }
  }

  async function rotate(key: AccessKeyEntry) {
    const ok = await confirm({
      title: "轮换访问密钥",
      message: `将换掉「${key.name}」的密钥，旧密钥立刻失效。请准备把新密钥交给使用者。`,
      confirmLabel: "轮换",
      danger: true,
    });
    if (!ok) return;
    try {
      const result = await api.rotateAccessKey(revision, key.id);
      await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
      if (result.key) {
        await showSecret({
          title: "访问密钥已轮换",
          warning: "旧密钥已经立刻失效。新密钥只显示这一次，请现在交给使用者。",
          items: [
            { label: "访问密钥", value: result.key },
            { label: "名称", value: result.name || key.name },
            { label: "ID", value: key.id },
          ],
        });
      }
    } catch (caught) {
      toast.error(`轮换失败：${errorText(caught)}`);
      if (caught instanceof ApiError && caught.status === 409) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
      }
    }
  }

  async function remove(key: AccessKeyEntry) {
    const ok = await confirm({
      title: "删除访问密钥",
      message: `将删除「${key.name}」，使用它的调用方会立刻收到 401。此操作不可撤销。`,
      confirmLabel: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteAccessKey(revision, key.id);
      toast.success("访问密钥已删除。");
      setEditing(null);
      await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
    } catch (caught) {
      toast.error(`删除失败：${errorText(caught)}`);
      if (caught instanceof ApiError && caught.status === 409) {
        await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
      }
    }
  }

  const columns: DataTableColumn<AccessKeyEntry>[] = [
    {
      key: "name",
      header: "名称",
      width: 220,
      cell: (key) => (
        <div className="min-w-0 space-y-0.5">
          <p className="truncate font-medium text-ink">{key.name}</p>
          <p className="truncate font-mono text-[11px] leading-3 text-ink-3">{key.id}</p>
          {key.key_fingerprint ? (
            <CopyChip text={key.key_fingerprint} className="h-5 px-1.5 text-[11px]" />
          ) : (
            <span className="text-meta text-ink-3">无指纹</span>
          )}
        </div>
      ),
      sortValue: (key) => key.name,
    },
    {
      key: "enabled",
      header: <span className="whitespace-nowrap">启用</span>,
      align: "center",
      width: 60,
      cell: (key) => (
        <Switch
          checked={enabledOf(key)}
          onCheckedChange={(next) => void toggleEnabled(key, next)}
          aria-label={`${enabledOf(key) ? "停用" : "启用"} ${key.name}`}
          disabled={!revision || pending.includes(key.id)}
        />
      ),
      sortValue: (key) => (enabledOf(key) ? 0 : 1),
    },
    {
      key: "providers",
      header: <span className="whitespace-nowrap">供应商范围</span>,
      width: 140,
      cell: (key) => <ScopeCell list={key.providers} kind="provider" />,
    },
    {
      key: "models",
      header: <span className="whitespace-nowrap">模型范围</span>,
      width: 175,
      cell: (key) => <ScopeCell list={key.models} kind="model" />,
    },
    {
      key: "requests",
      header: <span className="whitespace-nowrap">24 小时请求数</span>,
      align: "right",
      width: 94,
      tabular: true,
      cell: (key) => {
        const value = requestsById.get(key.id);
        return value === undefined
          ? <span className="text-ink-3">—</span>
          : <span>{formatCount(value)}</span>;
      },
      sortValue: (key) => requestsById.get(key.id) ?? null,
    },
    {
      key: "last_used",
      header: <span className="whitespace-nowrap">上一次调用</span>,
      width: 104,
      cell: (key) => <LastUsed value={key.last_used_at} />,
      sortValue: (key) => key.last_used_at ?? null,
    },
  ];

  return (
    <div className="flex flex-col gap-3">
      <TableStickyFix />
      <PageHeader
        title="访问密钥"
        description={(
          <span className="inline-flex flex-wrap items-center gap-1">
            分发给外部使用者的受限凭据：每把密钥各自限定能用的供应商与模型。
            <Tooltip content={(
              <ul className="list-disc space-y-1 pl-4">
                <li>明文密钥只在创建与轮换时显示一次；轮换后旧密钥立刻失效。</li>
                <li>未配置清单 = 不限制，空清单 = 全部禁止；上一次调用按北京时间显示最近一次上游尝试（含失败）。</li>
              </ul>
            )}>
              <button
                type="button"
                aria-label="说明"
                className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-focus"
              >
                <Info aria-hidden className="size-3.5" />
              </button>
            </Tooltip>
          </span>
        )}
        actions={(
          <Button
            variant="secondary"
            leadingIcon={<Plus aria-hidden className="size-4" />}
            disabled={!revision}
            onClick={() => setEditing("__new__")}
          >
            新建访问密钥
          </Button>
        )}
      />

      {keysQuery.error ? (
        <Banner tone="critical">无法读取访问密钥：{errorText(keysQuery.error)}</Banner>
      ) : null}

      <Panel
        title="密钥列表"
        description="启用开关会立刻影响调用方；修改清单在编辑抽屉里完成。"
        // 筛选控件放到 toolbar 而不是 actions：Panel 的 actions 是 shrink-0，
        // 窄屏上会把标题挤成竖排，表格也会把整页撑出横向滚动。
        toolbar={(
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone="neutral">{keys.length} 把</Badge>
            <Segmented
              aria-label="按启用状态筛选"
              value={status}
              onValueChange={(value) => setStatus(value as typeof status)}
              options={[
                { value: "all", label: "全部" },
                { value: "enabled", label: "启用" },
                { value: "disabled", label: "停用" },
              ]}
            />
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索名称或 ID"
              aria-label="搜索访问密钥"
              size="sm"
              className="w-52 max-w-full"
              leadingIcon={<Search aria-hidden className="size-3.5" />}
            />
          </div>
        )}
        padding={false}
        busy={keysQuery.isFetching && !keysQuery.isPending}
        className="min-w-0 overflow-hidden"
      >
        <DataTable
          aria-label="访问密钥列表"
          columns={columns}
          rows={visible}
          rowKey={(key) => key.id}
          onRowClick={(key) => setEditing(key)}
          className="ak-table"
          dense
          loading={keysQuery.isPending}
          loadingRows={4}
          emptyState={(
            <Empty
              icon={<KeyRound aria-hidden />}
              title={keys.length ? "没有符合条件的访问密钥。" : "还没有访问密钥。"}
              description={keys.length
                ? "换一个筛选条件或搜索词试试。"
                : "新建一把并把它交给使用者，它只能调用你在这把密钥上列出的供应商与模型。"}
              action={keys.length ? undefined : (
                <Button variant="secondary" disabled={!revision} onClick={() => setEditing("__new__")}>
                  新建访问密钥
                </Button>
              )}
            />
          )}
          rowActions={(key) => (
            <>
              <Button
                size="sm"
                variant="secondary"
                aria-label="编辑"
                onClick={() => setEditing(key)}
              >
                编辑
              </Button>
              <IconButton
                size="sm"
                aria-label="查看用量"
                tooltip="查看用量"
                onClick={() => navigate("activity", { ak: key.id })}
              >
                <BarChart3 aria-hidden className="size-3.5" />
              </IconButton>
              <IconButton
                size="sm"
                aria-label="轮换"
                tooltip="轮换"
                onClick={() => void rotate(key)}
              >
                <RefreshCw aria-hidden className="size-3.5" />
              </IconButton>
              <IconButton
                size="sm"
                variant="danger-ghost"
                aria-label="删除"
                tooltip="删除"
                onClick={() => void remove(key)}
              >
                <Trash2 aria-hidden className="size-3.5" />
              </IconButton>
            </>
          )}
        />
      </Panel>

      {editing !== null ? (
        <KeyEditor
          key={editing === "__new__" ? "__new__" : editing.id}
          accessKey={editing === "__new__" ? null : editing}
          isNew={editing === "__new__"}
          revision={revision}
          providers={providers}
          index={index}
          modelOptions={modelOptions}
          modelGroups={modelGroups}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
          }}
          onRefresh={async () => {
            await queryClient.invalidateQueries({ queryKey: queryKeys.accessKeys() });
          }}
        />
      ) : null}
    </div>
  );
}
