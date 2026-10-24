// 任务路由：把「模型 + 固定采样参数」打包成一个可直接当 model 传的任务名。
//
// 任务名只在**工作空间**内唯一，因此界面始终工作在一个具体空间里（默认空间即「不带
// X-AMKR-Workspace 头」的那个）。切换空间时必须清掉编辑态与旧任务列表：编辑器里的任务
// 属于原来的空间，留着它再点保存会打到新空间去，甚至因为重名而覆盖同名任务。

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Pencil, Plus, RefreshCw, Search, ShieldCheck, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText, shortRevision } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import { readStorage, writeStorage } from "@/lib/storage";
import type { Task, WorkspaceCreateResponse, WorkspaceSummary } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Chip } from "@/components/ui/Chip";
import { CopyChip } from "@/components/ui/CopyChip";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { Input } from "@/components/ui/Input";
import { ListPane, ListPaneItem, SplitView } from "@/components/ui/SplitView";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Tooltip } from "@/components/ui/Tooltip";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { useSecretDialog, type SecretItem } from "@/components/ui/SecretDialog";
import { TaskEditor } from "./TaskEditor";
import { nextTaskName, summarizeParams } from "./draft";
import {
  NewWorkspaceDialog,
  RenameWorkspaceDialog,
  WorkspaceModelsSheet,
  panelEmbedURL,
} from "./WorkspaceDialogs";
import { setParams, useRoute } from "@/app/router";

const DEFAULT_WORKSPACE = "default";
// 记住用户选的空间：刷新或切页回来时不必重新选。任务路由是团队各自维护的。
const WORKSPACE_STORAGE = "amkr.workspace";

function storedWorkspace(): string {
  return readStorage(WORKSPACE_STORAGE) || DEFAULT_WORKSPACE;
}

function rememberWorkspace(workspace: string): void {
  writeStorage(WORKSPACE_STORAGE, workspace);
}

// 操作列固定在表格右侧：横向滚动时仍能看到编辑/删除，避免窄屏上只能靠
// 悬停出现的按钮被挤出可视区。底色跟随面板与行 hover 状态。
function TableStickyFix() {
  return (
    <style>{`
      .task-table thead tr > th:last-child,
      .task-table tbody td[data-row-action] {
        position: sticky;
        right: 0;
        border-left: 1px solid var(--color-rule);
      }
      .task-table thead tr > th:last-child {
        z-index: 12;
        background: var(--color-sunken);
      }
      .task-table tbody td[data-row-action] {
        z-index: 4;
        background: var(--color-panel);
      }
      .task-table tbody tr:hover td[data-row-action],
      .task-table tbody tr:focus-within td[data-row-action] {
        background: var(--color-hover);
      }
      /* 窄屏没有 hover，操作列必须一直露出来；行点击仍是主要入口。 */
      @media (max-width: 1100px) {
        .task-table tbody td[data-row-action] > div {
          opacity: 1;
        }
      }
    `}</style>
  );
}

function countOf(entry: WorkspaceSummary | undefined, tasks: Task[]): number {
  return entry?.task_count ?? tasks.length;
}

interface HeaderActionProps {
  label: string;
  onClick: () => void;
  disabled?: boolean;
  reason?: string;
  icon?: ReactNode;
  danger?: boolean;
}

function HeaderAction({ label, onClick, disabled = false, reason, icon, danger = false }: HeaderActionProps) {
  const button = (
    <Button
      size="sm"
      variant={danger ? "danger-ghost" : "ghost"}
      disabled={disabled}
      leadingIcon={icon}
      onClick={onClick}
    >
      {label}
    </Button>
  );
  // 禁用按钮不接收指针事件，Radix 的 Tooltip 触发不了；包一层可聚焦的 span 让原因仍可读。
  if (disabled && reason) {
    return (
      <Tooltip content={reason}>
        <span tabIndex={0} className="inline-flex rounded-control">
          {button}
        </span>
      </Tooltip>
    );
  }
  return button;
}

export default function TasksPage() {
  const route = useRoute();
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const showSecret = useSecretDialog();

  const routeWorkspace = route.params.get("ws");
  const [workspace, setWorkspace] = useState(() => routeWorkspace || storedWorkspace());
  // workspacePinned 表示当前空间是用户自己选的，不是从 localStorage 恢复的。只有恢复
  // 来的名字才需要「它还在不在」的校验：用户输入的新名字天然不在目录里。
  const pinned = useRef(Boolean(routeWorkspace));
  const [editing, setEditing] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [detailOpen, setDetailOpen] = useState(Boolean(routeWorkspace));
  const [modelsOpen, setModelsOpen] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [newOpen, setNewOpen] = useState(false);
  const [rotating, setRotating] = useState(false);

  const workspacesQuery = useQuery({
    queryKey: queryKeys.workspaces(),
    queryFn: api.workspaces,
  });
  // default 固定在最上：它是「不带 X-AMKR-Workspace 头」的兜底空间，不能因为服务端
  // 目录顺序变化而沉到列表中间。
  const workspaceList = useMemo(() => {
    if (!workspacesQuery.data) return undefined;
    return [...workspacesQuery.data.workspaces].sort((a, b) => {
      if (a.name === DEFAULT_WORKSPACE) return -1;
      if (b.name === DEFAULT_WORKSPACE) return 1;
      return 0;
    });
  }, [workspacesQuery.data]);
  // 目录要先于任务取：任务列表按空间过滤，若恢复出来的空间已经不存在，得先把选择纠正
  // 回一个真实存在的空间，否则会拿一个空名字去查任务。
  const effectiveWorkspace = useMemo(() => {
    if (!workspaceList) return workspace;
    if (pinned.current) return workspace;
    if (workspaceList.some((item) => item.name === workspace)) return workspace;
    return workspaceList.some((item) => item.name === DEFAULT_WORKSPACE)
      ? DEFAULT_WORKSPACE
      : (workspaceList[0]?.name ?? DEFAULT_WORKSPACE);
  }, [workspaceList, workspace]);

  useEffect(() => {
    if (effectiveWorkspace !== workspace) {
      setWorkspace(effectiveWorkspace);
      rememberWorkspace(effectiveWorkspace);
    }
  }, [effectiveWorkspace, workspace]);

  // 浏览器前进/后退换掉 ?ws= 时跟着走；自己调用 setParams 时值相同，不会循环。
  useEffect(() => {
    if (routeWorkspace && routeWorkspace !== workspace) {
      pinned.current = true;
      setWorkspace(routeWorkspace);
      setEditing(null);
      rememberWorkspace(routeWorkspace);
    }
  }, [routeWorkspace, workspace]);

  const tasksQuery = useQuery({
    queryKey: queryKeys.tasks(effectiveWorkspace),
    queryFn: () => api.tasks(effectiveWorkspace),
    enabled: workspacesQuery.isSuccess,
  });
  const modelsQuery = useQuery({ queryKey: queryKeys.models(), queryFn: api.models });
  const healthQuery = useQuery({ queryKey: queryKeys.health(), queryFn: api.health });

  const tasks = tasksQuery.data?.tasks ?? [];
  const models = modelsQuery.data?.models ?? [];
  const revision = tasksQuery.data?.config_revision ?? modelsQuery.data?.config_revision ?? "";
  const entry = workspaceList?.find((item) => item.name === effectiveWorkspace);
  const actionable = Boolean(entry) && effectiveWorkspace !== DEFAULT_WORKSPACE;
  const taskCount = countOf(entry, tasks);

  const visibleTasks = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    if (!keyword) return tasks;
    return tasks.filter((task) =>
      [task.name, task.display_name, task.model, task.fallback_model]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword)),
    );
  }, [tasks, search]);

  function switchWorkspace(next: string) {
    setDetailOpen(true);
    if (next === effectiveWorkspace) return;
    pinned.current = true;
    setWorkspace(next);
    rememberWorkspace(next);
    setEditing(null);
    setSearch("");
    // URL 里的 ?ws= 与选择保持一致，链接可以分享给同事。
    setParams((params) => {
      params.set("ws", next);
      return params;
    });
  }

  async function refreshAll() {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: queryKeys.tasks(effectiveWorkspace) }),
      queryClient.invalidateQueries({ queryKey: queryKeys.workspaces() }),
      queryClient.invalidateQueries({ queryKey: queryKeys.models() }),
    ]);
  }

  async function deleteTask(task: Task) {
    const ok = await confirm({
      title: "删除任务路由",
      message: `删除任务 ${task.name}？调用方再传这个名字会被当作未配置的模型。`,
      confirmLabel: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteTask(revision, effectiveWorkspace, task.name);
      toast.success("任务路由已删除。");
      setEditing(null);
      await refreshAll();
    } catch (caught) {
      toast.error(errorText(caught));
      if (caught instanceof ApiError && caught.status === 409) await refreshAll();
    }
  }

  async function deleteWorkspace() {
    if (!actionable) return;
    const ok = await confirm({
      title: "删除工作空间",
      // 把任务数写进确认文案：删空间会连带删掉里面的任务，这是不可逆的。
      message: taskCount
        ? `删除工作空间 ${effectiveWorkspace}？其中 ${taskCount} 个任务会一起被删除。`
        : `删除工作空间 ${effectiveWorkspace}？`,
      confirmLabel: "删除",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteWorkspace(revision, effectiveWorkspace);
      toast.success("工作空间已删除。");
      // 删完之后空间不存在了，切回默认空间，至少保证用户看到的是一个真实存在的空间。
      pinned.current = true;
      setWorkspace(DEFAULT_WORKSPACE);
      rememberWorkspace(DEFAULT_WORKSPACE);
      setEditing(null);
      setParams((params) => {
        params.set("ws", DEFAULT_WORKSPACE);
        return params;
      });
      await refreshAll();
    } catch (caught) {
      toast.error(errorText(caught));
      if (caught instanceof ApiError && caught.status === 409) await refreshAll();
    }
  }

  async function rotateInferenceKey() {
    if (!actionable || rotating) return;
    const ok = await confirm({
      title: "轮换推理 key",
      message: `将换掉工作空间「${effectiveWorkspace}」的推理 key，旧 key 会立刻失效。请准备把新 key 更新到各个项目的环境变量里。`,
      confirmLabel: "轮换",
      danger: true,
    });
    if (!ok) return;
    setRotating(true);
    try {
      const result = await api.rotateInferenceKey(revision, effectiveWorkspace);
      await queryClient.invalidateQueries({ queryKey: queryKeys.workspaces() });
      if (result.inference_key) {
        await showSecret({
          title: "推理 key 已轮换",
          warning: "旧 key 已经失效。新 key 只显示这一次，请立即更新到各个项目的环境变量里。",
          items: [
            { label: "工作空间", value: effectiveWorkspace },
            { label: "推理 key", value: result.inference_key },
          ],
        });
      }
    } catch (caught) {
      toast.error(`轮换失败：${errorText(caught)}`);
      if (caught instanceof ApiError && caught.status === 409) await refreshAll();
    } finally {
      setRotating(false);
    }
  }

  async function handleWorkspaceCreated(created: WorkspaceCreateResponse) {
    setNewOpen(false);
    pinned.current = true;
    setWorkspace(created.name);
    rememberWorkspace(created.name);
    setEditing(null);
    setParams((params) => {
      params.set("ws", created.name);
      return params;
    });
    await queryClient.invalidateQueries({ queryKey: queryKeys.workspaces() });

    // 明文 key 只在这条 201 响应里出现；关掉就只剩轮换或重建这一条路。
    const panelKey = created.api_key || "";
    const inferenceKey = created.inference_key || "";
    if (!panelKey && !inferenceKey) return;
    const embed = panelKey ? panelEmbedURL(panelKey) : "";
    const snippet = embed
      ? `<iframe src="${embed}" width="100%" height="720" style="border:0" title="AMKR 工作空间面板"></iframe>`
      : "";
    const items: SecretItem[] = [{ label: "工作空间", value: created.name }];
    if (panelKey) items.push({ label: "面板 key", value: panelKey, hint: "给嵌入工作空间面板的应用：只能读写这一个空间。" });
    if (inferenceKey) items.push({ label: "推理 key", value: inferenceKey, hint: "给项目做 /v1 调用：空间由这把 key 决定。" });
    if (embed) items.push({ label: "面板嵌入地址", value: embed });
    if (snippet) items.push({ label: "iframe 片段", value: snippet });
    await showSecret({
      title: "工作空间已创建",
      warning: "这两把 key 只显示这一次。请立即复制并妥善保存；关闭后无法再查看，"
        + "只能轮换推理 key，或删除该工作空间后重建面板 key。",
      items,
      extra: (
        <div className="space-y-2 border-t border-rule pt-3 text-meta leading-5 text-ink-3">
          <p className="font-medium text-ink-2">两把 key 的分工</p>
          <p>面板 key —— 给嵌入工作空间面板的应用：它只能读写这一个空间的任务与用量，支持被 iframe 直接嵌入（key 放在 fragment 里，不进 Referer 与访问日志）。</p>
          <p>推理 key —— 给各个项目做 /v1 调用：把它配到项目的环境变量里作为 Authorization: Bearer。空间由这把 key 决定，调用方无法用 X-AMKR-Workspace 头换到别的空间。</p>
        </div>
      ),
    });
  }

  async function handleRenamed(next: string) {
    setRenameOpen(false);
    pinned.current = true;
    setWorkspace(next);
    rememberWorkspace(next);
    setEditing(null);
    setParams((params) => {
      params.set("ws", next);
      return params;
    });
    await refreshAll();
  }

  const columns: DataTableColumn<Task>[] = [
    {
      key: "display",
      header: <span className="whitespace-nowrap">显示名</span>,
      width: 110,
      cell: (task) => task.display_name
        ? <span className="font-medium text-ink">{task.display_name}</span>
        : <span className="text-ink-3">未设置</span>,
      sortValue: (task) => task.display_name || task.name,
    },
    {
      key: "name",
      header: <span className="whitespace-nowrap">任务名</span>,
      width: 140,
      cell: (task) => <CopyChip text={task.name} className="h-6" />,
      sortValue: (task) => task.name,
    },
    {
      key: "models",
      header: <span className="whitespace-nowrap">首选 → 备选</span>,
      width: 210,
      cell: (task) => (
        <div className="flex min-w-0 items-center gap-1.5 text-dense">
          <span className={task.model ? "truncate font-mono text-ink" : "truncate text-ink-3"}>
            {task.model || "尚未指定"}
          </span>
          {task.fallback_model ? (
            <>
              <ArrowRight aria-hidden className="size-3 shrink-0 text-ink-3" />
              <span className="truncate font-mono text-ink-2">{task.fallback_model}</span>
            </>
          ) : (
            <span className="shrink-0 text-meta text-ink-3">无备选</span>
          )}
        </div>
      ),
      sortValue: (task) => task.model || "",
    },
    {
      key: "params",
      header: <span className="whitespace-nowrap">固定参数</span>,
      width: 220,
      cell: (task) => {
        const params = summarizeParams(task.params);
        if (params.length === 0) return <span className="text-meta text-ink-3">全部透传</span>;
        return (
          <div className="flex max-w-[420px] flex-wrap gap-1">
            {params.map((item) => (
              <Chip key={item.label} className="font-mono">
                {item.label} {item.value}
              </Chip>
            ))}
          </div>
        );
      },
    },
  ];

  const defaultEmpty = effectiveWorkspace === DEFAULT_WORKSPACE;

  return (
    <div className="flex flex-col gap-3">
      <TableStickyFix />
      <PageHeader
        title="任务路由"
        description="把任务名当作模型名调用，自动使用该任务固定的模型与采样参数。"
        actions={(
          <Button variant="secondary" leadingIcon={<Plus aria-hidden className="size-4" />} onClick={() => setEditing("__new__")}>
            新建任务
          </Button>
        )}
      />

      {workspacesQuery.error ? (
        <Banner tone="critical">
          无法读取工作空间目录：{errorText(workspacesQuery.error)}
        </Banner>
      ) : null}

      <div className="h-[calc(100vh-13rem)] min-h-[520px] overflow-hidden rounded-panel border border-rule bg-panel">
        <SplitView
          listWidth={248}
          selected={detailOpen || Boolean(editing) || modelsOpen || renameOpen}
          onBack={() => {
            setDetailOpen(false);
            setEditing(null);
            setModelsOpen(false);
            setRenameOpen(false);
          }}
          list={(
            <ListPane
              title="工作空间"
              empty={workspacesQuery.isPending ? "正在读取…" : undefined}
              footer={(
                <Button
                  variant="ghost"
                  className="w-full justify-start"
                  leadingIcon={<Plus aria-hidden className="size-4" />}
                  disabled={!revision}
                  onClick={() => setNewOpen(true)}
                >
                  新建工作空间
                </Button>
              )}
            >
              {(workspaceList ?? []).map((item) => (
                <ListPaneItem
                  key={item.name}
                  selected={item.name === effectiveWorkspace}
                  onClick={() => switchWorkspace(item.name)}
                  meta={`${item.task_count ?? 0} 个任务`}
                >
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate">{item.name}</span>
                    {item.name === DEFAULT_WORKSPACE ? <Badge tone="neutral">默认</Badge> : null}
                  </span>
                </ListPaneItem>
              ))}
              {/* 当前空间不在目录里：刚被删空/改名，或本地记着一个早已不存在的名字。补一项，
                  否则下拉与页面上的空列表对不上。 */}
              {entry ? null : (
                <ListPaneItem selected onClick={() => undefined} meta="未写入配置">
                  <span className="truncate">{effectiveWorkspace}</span>
                </ListPaneItem>
              )}
            </ListPane>
          )}
          detail={(
            <div className="flex h-full min-h-0 flex-col">
              <div className="flex flex-wrap items-start justify-between gap-3 border-b border-rule px-4 py-3">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <h2 className="truncate text-[20px] font-semibold leading-tight text-ink">{effectiveWorkspace}</h2>
                    {effectiveWorkspace === DEFAULT_WORKSPACE ? <Badge tone="neutral">默认</Badge> : null}
                    {revision ? <Badge tone="neutral">版本 {shortRevision(revision)}</Badge> : null}
                  </div>
                  <p className="mt-1 max-w-2xl text-meta leading-5 text-ink-3">
                    {effectiveWorkspace === DEFAULT_WORKSPACE
                      ? "调用方不带 X-AMKR-Workspace 头时命中这个空间；它没有作用域凭据，因此不能改名、删除或配置模型清单。"
                      : `调用方带 X-AMKR-Workspace: ${effectiveWorkspace} 头时命中这个空间；发给项目的推理 key 也只能访问这里。`}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-1">
                  <HeaderAction
                    label="模型授权"
                    icon={<ShieldCheck aria-hidden className="size-4" />}
                    disabled={!actionable}
                    reason={effectiveWorkspace === DEFAULT_WORKSPACE
                      ? "默认空间不能配置模型清单（它没有作用域凭据）"
                      : "这个空间还没有写进配置，先在它里面建一个任务"}
                    onClick={() => setModelsOpen(true)}
                  />
                  <HeaderAction
                    label="轮换推理 key"
                    icon={<RefreshCw aria-hidden className="size-4" />}
                    disabled={!actionable || rotating}
                    reason={effectiveWorkspace === DEFAULT_WORKSPACE
                      ? "默认空间没有推理 key"
                      : "这个空间还没有写进配置"}
                    onClick={() => void rotateInferenceKey()}
                  />
                  <HeaderAction
                    label="改名"
                    icon={<Pencil aria-hidden className="size-4" />}
                    disabled={!actionable}
                    reason={effectiveWorkspace === DEFAULT_WORKSPACE
                      ? "默认工作空间不能改名"
                      : "这个空间还没有写进配置"}
                    onClick={() => setRenameOpen(true)}
                  />
                  <HeaderAction
                    label="删除"
                    icon={<Trash2 aria-hidden className="size-4" />}
                    danger
                    disabled={!actionable}
                    reason={effectiveWorkspace === DEFAULT_WORKSPACE
                      ? "默认工作空间不能删除"
                      : "这个空间还没有写进配置"}
                    onClick={() => void deleteWorkspace()}
                  />
                </div>
              </div>

              <div className="min-h-0 flex-1 overflow-y-auto p-4">
                {tasksQuery.error ? (
                  <Banner tone="critical" className="mb-3">
                    无法读取或写入任务路由：{errorText(tasksQuery.error)}
                  </Banner>
                ) : null}
                {!models.length && !tasks.length && !tasksQuery.isPending ? (
                  <Banner tone="info" className="mb-3">
                    尚未配置可用模型。请先在供应商页添加 Key 并绑定服务模型；也可以先建一个尚未指定模型的任务占位。
                  </Banner>
                ) : null}

                <Panel
                  title="任务"
                  description="任务名就是调用方传的 model；显示名称只用于在这个页面上辨认任务。"
                  // 同访问密钥页：筛选控件进 toolbar，避免窄屏被 shrink-0 的操作区挤成竖排。
                  toolbar={(
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone="neutral">{tasks.length} 个</Badge>
                      <Input
                        value={search}
                        onChange={(event) => setSearch(event.target.value)}
                        placeholder="搜索任务"
                        aria-label="搜索任务"
                        size="sm"
                        className="w-52 max-w-full"
                        leadingIcon={<Search aria-hidden className="size-3.5" />}
                      />
                    </div>
                  )}
                  padding={false}
                  busy={tasksQuery.isFetching && !tasksQuery.isPending}
                  className="min-w-0 overflow-hidden"
                >
                  <DataTable
                    aria-label="任务列表"
                    columns={columns}
                    rows={visibleTasks}
                    rowKey={(task) => task.name}
                    onRowClick={(task) => setEditing(task.name)}
                    className="task-table"
                    dense
                    loading={tasksQuery.isPending}
                    loadingRows={4}
                    emptyState={(
                      <Empty
                        title={defaultEmpty ? "尚未配置任务路由。" : `工作空间 ${effectiveWorkspace} 还没有任务。`}
                        description={defaultEmpty
                          ? '新建一个任务后，调用方传 model: "TASK_XXXXXX" 即可命中。'
                          : `在这个空间里新建第一个任务后，它才会写进配置。调用方带 X-AMKR-Workspace: ${effectiveWorkspace} 即可命中其中的任务。`}
                        action={(
                          <Button variant="secondary" onClick={() => setEditing("__new__")}>
                            新建任务
                          </Button>
                        )}
                      />
                    )}
                    rowActions={(task) => (
                      <>
                        <Button size="sm" variant="secondary" onClick={() => setEditing(task.name)}>编辑</Button>
                        <Button size="sm" variant="danger-ghost" onClick={() => void deleteTask(task)}>删除</Button>
                      </>
                    )}
                  />
                </Panel>
              </div>
            </div>
          )}
        />
      </div>

      {editing === "__new__" ? (
        <TaskEditor
          key="__new__"
          task={null}
          isNew
          workspace={effectiveWorkspace}
          models={models}
          suggestedName={nextTaskName(tasks)}
          baseUrl={typeof healthQuery.data?.base_url === "string" ? healthQuery.data.base_url : undefined}
          revision={revision}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null);
            await refreshAll();
          }}
          onRefresh={refreshAll}
        />
      ) : null}
      {editing && editing !== "__new__" ? (() => {
        const task = tasks.find((item) => item.name === editing);
        if (!task) return null;
        return (
          <TaskEditor
            key={task.name}
            task={task}
            isNew={false}
            workspace={effectiveWorkspace}
            models={models}
            suggestedName={task.name}
            baseUrl={typeof healthQuery.data?.base_url === "string" ? healthQuery.data.base_url : undefined}
            revision={revision}
            onClose={() => setEditing(null)}
            onSaved={async () => {
              setEditing(null);
              await refreshAll();
            }}
            onRefresh={refreshAll}
          />
        );
      })() : null}

      {modelsOpen && entry ? (
        <WorkspaceModelsSheet
          workspace={effectiveWorkspace}
          entry={entry}
          models={models}
          revision={revision}
          onClose={() => setModelsOpen(false)}
          onSaved={async () => {
            setModelsOpen(false);
            await refreshAll();
          }}
          onRefresh={refreshAll}
        />
      ) : null}

      {renameOpen ? (
        <RenameWorkspaceDialog
          workspace={effectiveWorkspace}
          taskCount={taskCount}
          revision={revision}
          onClose={() => setRenameOpen(false)}
          onRenamed={handleRenamed}
          onRefresh={refreshAll}
        />
      ) : null}

      {newOpen ? (
        <NewWorkspaceDialog
          revision={revision}
          onClose={() => setNewOpen(false)}
          onCreated={handleWorkspaceCreated}
          onRefresh={refreshAll}
        />
      ) : null}
    </div>
  );
}
