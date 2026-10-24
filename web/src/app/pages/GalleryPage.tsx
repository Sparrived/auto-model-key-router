import { useState } from "react";
import {
  Construction,
  Copy,
  ExternalLink,
  MoreHorizontal,
  Plus,
  RefreshCw,
  Search,
  SlidersHorizontal,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Chip } from "@/components/ui/Chip";
import { Combobox } from "@/components/ui/Combobox";
import { CopyChip } from "@/components/ui/CopyChip";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Dialog } from "@/components/ui/Dialog";
import { DropdownMenu, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator } from "@/components/ui/DropdownMenu";
import { Empty } from "@/components/ui/Empty";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Kbd } from "@/components/ui/Kbd";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Popover } from "@/components/ui/Popover";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sheet } from "@/components/ui/Sheet";
import { Skeleton } from "@/components/ui/Skeleton";
import { Spinner } from "@/components/ui/Spinner";
import { SplitView, ListPane, ListPaneItem } from "@/components/ui/SplitView";
import { StatusDot } from "@/components/ui/StatusDot";
import { Switch } from "@/components/ui/Switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/Tabs";
import { Textarea } from "@/components/ui/Textarea";
import { TokenInput } from "@/components/ui/TokenInput";
import { Tooltip } from "@/components/ui/Tooltip";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { useSecretDialog } from "@/components/ui/SecretDialog";
import { GalleryChartSections, GalleryFilterSection } from "@/components/charts/GalleryChartSections";

interface DemoRow {
  id: string;
  model: string;
  provider: string;
  requests: number;
  tokens: number;
  latency: string;
  status: "good" | "warn" | "critical";
}

const DEMO_ROWS: DemoRow[] = [
  { id: "r1", model: "gpt-4o", provider: "openai", requests: 12480, tokens: 8_320_000, latency: "812ms", status: "good" },
  { id: "r2", model: "claude-sonnet-4", provider: "anthropic", requests: 8320, tokens: 5_940_000, latency: "1.2s", status: "good" },
  { id: "r3", model: "gemini-2.5-pro", provider: "google", requests: 2140, tokens: 1_120_000, latency: "2.4s", status: "warn" },
  { id: "r4", model: "deepseek-chat", provider: "deepseek", requests: 960, tokens: 480_000, latency: "3.8s", status: "critical" },
];

const COMBOBOX_OPTIONS = [
  { value: "openai", label: "OpenAI", group: "供应商", note: "12 条路由" },
  { value: "anthropic", label: "Anthropic", group: "供应商", note: "8 条路由" },
  { value: "google", label: "Google", group: "供应商", note: "3 条路由" },
  { value: "gpt-4o", label: "gpt-4o", group: "模型" },
  { value: "claude-sonnet-4", label: "claude-sonnet-4", group: "模型" },
];

function Section({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <div>
        <h2 className="text-[15px] font-semibold text-ink">{title}</h2>
        {description ? <p className="mt-0.5 text-meta text-ink-3">{description}</p> : null}
      </div>
      {children}
    </section>
  );
}

export default function GalleryPage() {
  const confirm = useConfirm();
  const impactConfirm = useImpactConfirm();
  const showSecret = useSecretDialog();

  const [segment, setSegment] = useState("24h");
  const [tokens, setTokens] = useState(["gpt-4o", "claude-sonnet-4"]);
  const [switchOn, setSwitchOn] = useState(true);
  const [checked, setChecked] = useState(false);
  const [tab, setTab] = useState("overview");
  const [single, setSingle] = useState("openai");
  const [multiple, setMultiple] = useState<string[]>(["openai", "anthropic"]);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  const [sheetName, setSheetName] = useState("初始值");
  const [selectedRow, setSelectedRow] = useState("r1");
  const selectedDemo = DEMO_ROWS.find((row) => row.id === selectedRow) ?? DEMO_ROWS[0];
  const selectedStatusLabel = selectedDemo.status === "good" ? "正常" : selectedDemo.status === "warn" ? "波动" : "异常";

  const columns: DataTableColumn<DemoRow>[] = [
    {
      key: "model",
      header: "模型",
      cell: (row) => <span className="font-medium text-ink">{row.model}</span>,
      sortValue: (row) => row.model,
    },
    {
      key: "provider",
      header: "供应商",
      cell: (row) => <span className="text-ink-3">{row.provider}</span>,
      sortValue: (row) => row.provider,
    },
    {
      key: "requests",
      header: "请求数",
      align: "right",
      tabular: true,
      cell: (row) => row.requests.toLocaleString("zh-CN"),
      sortValue: (row) => row.requests,
    },
    {
      key: "tokens",
      header: "Token",
      align: "right",
      tabular: true,
      cell: (row) => `${(row.tokens / 1_000_000).toFixed(1)}M`,
      sortValue: (row) => row.tokens,
    },
    {
      key: "status",
      header: "状态",
      align: "center",
      cell: (row) => (
        <Badge tone={row.status} dot>
          {row.status === "good" ? "正常" : row.status === "warn" ? "波动" : "异常"}
        </Badge>
      ),
      sortValue: (row) => row.status,
    },
  ];

  async function askConfirm() {
    const ok = await confirm({
      title: "删除这条路由？",
      message: "删除后，指向该路由的请求会立即失败。此操作无法撤销。",
      confirmLabel: "删除路由",
      danger: true,
    });
    toast[ok ? "success" : "message"](ok ? "已确认删除" : "已取消");
  }

  async function askImpact() {
    const ok = await impactConfirm({
      title: "删除模型 gpt-4o？",
      message: "该模型仍被 2 个访问 Key 与 1 个工作区引用。",
      impacted: true,
      confirmLabel: "继续删除",
      impact: {
        removed_models: ["gpt-4o"],
        access_keys: [{ id: "ak_1", name: "本地调试", models: ["gpt-4o"] }],
        workspaces: [{ name: "default", models: ["gpt-4o"] }],
      },
    });
    toast[ok ? "success" : "message"](ok ? "已确认" : "已取消");
  }

  async function askSecret() {
    await showSecret({
      title: "访问 Key 已创建",
      items: [
        { label: "名称", value: "本地调试" },
        { label: "密钥", value: "amkr_sk_demo_7f3a9c2e", hint: "只显示这一次，请立即保存。" },
      ],
    });
    toast.success("凭据已确认保存");
  }

  return (
    <div className="mx-auto max-w-[1180px] space-y-6 pb-16">
      <PageHeader
        title="组件陈列"
        description="仅开发环境可见。所有组件共用同一套 tokens、焦点环与键盘行为。"
        actions={<Badge tone="accent">DEV ONLY</Badge>}
      />

      <GalleryFilterSection />

      <Section title="按钮" description="主操作每屏只有一个 primary；危险操作必须显式写动词。">
        <Panel>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" leadingIcon={<Plus size={15} />}>新建路由</Button>
            <Button variant="secondary" leadingIcon={<RefreshCw size={15} />}>刷新</Button>
            <Button variant="ghost">取消</Button>
            <Button variant="danger" leadingIcon={<Trash2 size={15} />}>删除</Button>
            <Button variant="danger-ghost">清空</Button>
            <Button variant="primary" loading>保存中</Button>
            <Button disabled>不可用</Button>
            <Button size="sm" variant="secondary">小号按钮</Button>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-rule pt-3">
            <IconButton aria-label="添加" variant="secondary"><Plus size={16} /></IconButton>
            <IconButton aria-label="刷新"><RefreshCw size={16} /></IconButton>
            <IconButton aria-label="更多操作" variant="ghost"><MoreHorizontal size={16} /></IconButton>
            <Tooltip content="复制到剪贴板">
              <Button variant="secondary" size="sm" leadingIcon={<Copy size={14} />}>悬停看提示</Button>
            </Tooltip>
            <Kbd>⌘</Kbd><Kbd>K</Kbd>
          </div>
        </Panel>
      </Section>

      <Section title="表单" description="标签、错误与帮助文本由 Field 统一编排；控件自己只负责状态。">
        <div className="grid gap-3 lg:grid-cols-2">
          <Panel title="输入控件">
            <div className="space-y-3">
              <Field label="供应商名称" required hint="用于在路由列表里识别来源。">
                <Input placeholder="例如 openai" />
              </Field>
              <Field label="请求路径" error="必须以 /v1 开头。">
                <Input defaultValue="v1/chat" leadingIcon={<Search size={15} />} />
              </Field>
              <Field label="备注">
                <Textarea placeholder="可选" />
              </Field>
              <Field label="默认模型">
                <Select
                  placeholder="选择模型"
                  options={[
                    { value: "gpt-4o", label: "gpt-4o" },
                    { value: "claude-sonnet-4", label: "claude-sonnet-4" },
                    { value: "gemini-2.5-pro", label: "gemini-2.5-pro" },
                  ]}
                />
              </Field>
            </div>
          </Panel>

          <Panel title="选择与切换">
            <div className="space-y-4">
              <Field label="单个供应商" hint="Combobox 支持搜索、分组和键盘选择。">
                <Combobox
                  options={COMBOBOX_OPTIONS}
                  value={single}
                  onValueChange={(value) => setSingle(value as string)}
                  aria-label="单个供应商"
                />
              </Field>
              <Field label="多个模型">
                <Combobox
                  multiple
                  options={COMBOBOX_OPTIONS}
                  value={multiple}
                  onValueChange={(value) => setMultiple(value as string[])}
                  placeholder="选择一个或多个模型"
                  aria-label="多个模型"
                />
              </Field>
              <div className="flex flex-wrap items-center gap-4">
                <Switch checked={switchOn} onCheckedChange={setSwitchOn} label="启用重试" description="失败后最多重试 2 次。" />
                <Checkbox checked={checked} onCheckedChange={setChecked} label="记住本次选择" />
              </div>
              <div>
                <div className="mb-1.5 text-meta font-medium text-ink-2">时间窗口</div>
                <Segmented
                  aria-label="时间窗口"
                  value={segment}
                  onValueChange={setSegment}
                  options={[
                    { value: "1h", label: "1 小时" },
                    { value: "24h", label: "24 小时" },
                    { value: "7d", label: "7 天" },
                    { value: "30d", label: "30 天", disabled: true },
                  ]}
                />
              </div>
              <Field label="模型白名单" hint="回车或逗号分隔；Backspace 删除最后一个。">
                <TokenInput value={tokens} onChange={setTokens} placeholder="输入模型名" />
              </Field>
            </div>
          </Panel>
        </div>
      </Section>

      <Section title="状态与反馈" description="颜色只承载数据含义；文字与图标始终同时在场。">
        <Panel>
          <div className="flex flex-wrap items-center gap-2">
            <Badge>默认</Badge>
            <Badge tone="good" dot>正常</Badge>
            <Badge tone="warn" dot>波动</Badge>
            <Badge tone="serious" dot>降级</Badge>
            <Badge tone="critical" dot>异常</Badge>
            <Badge tone="accent">当前</Badge>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-5 border-t border-rule pt-3">
            <StatusDot tone="good" label="运行中" pulse />
            <StatusDot tone="warn" label="正在连接" />
            <StatusDot tone="critical" label="服务未连接" />
            <StatusDot tone="neutral" label="未启用" />
            <Spinner />
            <Spinner size="md" />
          </div>
          <div className="mt-4 grid gap-3 border-t border-rule pt-3 md:grid-cols-2">
            <div className="space-y-1.5">
              <div className="text-meta text-ink-3">请求成功率 · 98.4%</div>
              <ProgressBar value={98.4} tone="good" showValue />
              <ProgressBar value={72} tone="warn" label="配额使用" showValue />
              <ProgressBar value={18} tone="critical" label="剩余预算" showValue />
            </div>
            <div className="space-y-2">
              <Banner tone="info" title="这是一条信息提示">用于解释当前页面的计算口径。</Banner>
              <Banner tone="warn" title="配置尚未生效" action={<Button size="sm" variant="secondary">重试</Button>}>
                保存后需要重启服务。
              </Banner>
              <Banner tone="critical" title="连接失败" onDismiss={() => toast.message("已关闭提示")}>
                无法连接 AMKR 服务。
              </Banner>
            </div>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-rule pt-3">
            <Chip onRemove={() => toast.message("已移除")}>gpt-4o</Chip>
            <Chip tone="accent" onRemove={() => toast.message("已移除")}>claude-sonnet-4</Chip>
            <Chip tone="good">已同步</Chip>
            <Chip tone="critical">已禁用</Chip>
            <CopyChip text="amkr_sk_demo_7f3a9c2e" label="密钥" />
          </div>
        </Panel>
      </Section>

      <Section title="指标与面板" description="监控页全站只允许一个 44px hero 读数。">
        <MetricStrip>
          <MetricCell hero label="总请求" value="23,904" unit="次" delta={{ direction: "up", value: 0.124, polarity: "neutral" }} hint="较上一周期" />
          <MetricCell label="成功率" value="98.4%" tone="good" delta={{ direction: "up", value: 0.004, polarity: "direct" }} />
          <MetricCell label="平均延迟" value="842" unit="ms" delta={{ direction: "down", value: 0.081, polarity: "inverse" }} />
          <MetricCell label="活跃 Key" value="12" unit="个" hint="1 个即将过期" />
        </MetricStrip>
        <div className="grid gap-3 lg:grid-cols-2">
          <Panel title="刷新中的面板" description="busy 只说明正在取数，不遮断操作。" busy actions={<Button size="sm" variant="ghost">查看</Button>}>
            <div className="space-y-2">
              <Skeleton />
              <Skeleton />
              <Skeleton className="w-2/3" />
            </div>
          </Panel>
          <Panel title="空状态" padding={false}>
            <Empty
              icon={<Construction />}
              title="还没有路由"
              description="添加第一个供应商后，这里会显示路由的健康状况。"
              action={<Button variant="primary" leadingIcon={<Plus size={15} />}>添加供应商</Button>}
            />
          </Panel>
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <Skeleton variant="chart" />
          <Skeleton variant="table" />
          <Skeleton variant="block" />
        </div>
      </Section>

      <Section title="标签页与浮层" description="浮层均使用 Radix：Esc 关闭、焦点约束、关闭后回到触发器。">
        <Panel>
          <Tabs value={tab} onValueChange={setTab}>
            <TabsList aria-label="示例标签页">
              <TabsTrigger value="overview">概览</TabsTrigger>
              <TabsTrigger value="routes">路由</TabsTrigger>
              <TabsTrigger value="settings">设置</TabsTrigger>
            </TabsList>
            <TabsContent value="overview" className="text-dense text-ink-2">概览内容：这里是标签页的正文区域。</TabsContent>
            <TabsContent value="routes" className="text-dense text-ink-2">路由内容：表格或列表可以放在这里。</TabsContent>
            <TabsContent value="settings" className="text-dense text-ink-2">设置内容：表单可以放在这里。</TabsContent>
          </Tabs>
          <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-rule pt-3">
            <Popover trigger={<Button variant="secondary">打开浮层</Button>}>
              <div className="space-y-2">
                <div className="text-dense font-medium text-ink">筛选口径</div>
                <p className="text-meta text-ink-3">浮层适合承载轻量说明和临时筛选，不打断当前页面。</p>
                <Button size="sm" variant="primary" onClick={() => toast.success("已应用")}>应用</Button>
              </div>
            </Popover>
            <DropdownMenu
              trigger={<Button variant="secondary" leadingIcon={<SlidersHorizontal size={15} />}>更多操作</Button>}
            >
              <DropdownMenuLabel>路由操作</DropdownMenuLabel>
              <DropdownMenuItem icon={<ExternalLink size={15} />} shortcut="↵" onSelect={() => toast.message("打开详情")}>
                查看详情
              </DropdownMenuItem>
              <DropdownMenuItem icon={<Copy size={15} />} shortcut="⌘C" onSelect={() => toast.success("已复制")}>
                复制模型名
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem danger icon={<Trash2 size={15} />} onSelect={() => toast.error("已删除")}>
                删除路由
              </DropdownMenuItem>
            </DropdownMenu>
            <Button variant="secondary" onClick={() => setDialogOpen(true)}>打开对话框</Button>
            <Button variant="secondary" onClick={() => void askConfirm()}>命令式确认</Button>
            <Button variant="secondary" onClick={() => void askImpact()}>影响预演确认</Button>
            <Button variant="secondary" onClick={() => void askSecret()}>一次性凭据</Button>
            <Button variant="secondary" onClick={() => setSheetOpen(true)}>打开抽屉</Button>
          </div>
        </Panel>
      </Section>

      <Section title="列表与表格" description="桌面两栏；窄屏按选中项切换列表/详情。表格支持键盘排序。">
        <Panel padding={false} className="overflow-hidden">
          <div className="h-[320px]">
            <SplitView
              selected={Boolean(selectedRow)}
              listWidth={240}
              onBack={() => setSelectedRow("")}
              list={
                <ListPane
                  title="路由"
                  search={{ value: "", onChange: () => undefined, placeholder: "搜索路由" }}
                  footer={<Button size="sm" variant="secondary" className="w-full">新增路由</Button>}
                >
                  <ListPaneItem selected={selectedRow === "r1"} onClick={() => setSelectedRow("r1")} meta="12k">gpt-4o</ListPaneItem>
                  <ListPaneItem selected={selectedRow === "r2"} onClick={() => setSelectedRow("r2")} meta="8k">claude-sonnet-4</ListPaneItem>
                  <ListPaneItem selected={selectedRow === "r3"} onClick={() => setSelectedRow("r3")} meta="2k">gemini-2.5-pro</ListPaneItem>
                </ListPane>
              }
              detail={
                <div className="space-y-3 p-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <h3 className="text-title font-semibold text-ink">{selectedDemo.model}</h3>
                      <p className="text-meta text-ink-3">{selectedDemo.provider} / 主路由</p>
                    </div>
                    <Badge tone={selectedDemo.status === "critical" ? "critical" : selectedDemo.status === "warn" ? "warn" : "good"} dot>{selectedStatusLabel}</Badge>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="rounded-control bg-sunken p-3">
                      <div className="text-meta text-ink-3">今日请求</div>
                      <div className="mt-1 text-title font-semibold tabular-nums text-ink">{selectedDemo.requests.toLocaleString("zh-CN")}</div>
                    </div>
                    <div className="rounded-control bg-sunken p-3">
                      <div className="text-meta text-ink-3">平均延迟</div>
                      <div className="mt-1 text-title font-semibold tabular-nums text-ink">{selectedDemo.latency}</div>
                    </div>
                  </div>
                  <TablePreview rows={DEMO_ROWS} />
                </div>
              }
            />
          </div>
        </Panel>
      </Section>

      <Section title="数据表" description="客户端排序；空状态、加载骨架、行操作与行点击都内置。">
        <Panel padding={false}>
          <DataTable
            caption="模型用量"
            aria-label="模型用量"
            columns={columns}
            rows={DEMO_ROWS}
            rowKey={(row) => row.id}
            onRowClick={(row) => toast.message(`打开 ${row.model}`)}
            rowActions={(row) => (
              <DropdownMenu trigger={<IconButton aria-label={`${row.model} 的操作`} tooltip={null}><MoreHorizontal size={16} /></IconButton>}>
                <DropdownMenuItem onSelect={() => toast.message(`编辑 ${row.model}`)}>编辑</DropdownMenuItem>
                <DropdownMenuItem danger onSelect={() => toast.message(`删除 ${row.model}`)}>删除</DropdownMenuItem>
              </DropdownMenu>
            )}
          />
        </Panel>
      </Section>

      <GalleryChartSections />

      <Dialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title="添加供应商"
        description="凭据只会发送到本机 AMKR 服务。"
        footer={
          <>
            <Button variant="secondary" onClick={() => setDialogOpen(false)}>取消</Button>
            <Button variant="primary" onClick={() => { setDialogOpen(false); toast.success("已保存"); }}>保存供应商</Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="名称" required><Input placeholder="例如 openai" /></Field>
          <Field label="Base URL"><Input placeholder="https://api.openai.com/v1" /></Field>
        </div>
      </Dialog>

      <Sheet
        open={sheetOpen}
        onOpenChange={setSheetOpen}
        title="编辑路由"
        subtitle="抽屉适合较长的上下文表单。"
        dirty={sheetName !== "初始值"}
        onSave={() => { setSheetOpen(false); toast.success("路由已保存"); }}
      >
        <div className="space-y-3">
          <Field label="路由名称"><Input value={sheetName} onChange={(event) => setSheetName(event.target.value)} /></Field>
          <Field label="目标模型"><Input defaultValue="gpt-4o" /></Field>
          <Banner tone="info">修改会立即影响正在使用该路由的请求。</Banner>
        </div>
      </Sheet>

      <div className="flex items-center gap-2 rounded-panel border border-dashed border-rule px-4 py-3 text-meta text-ink-3">
        <TriangleAlert className="size-4 shrink-0" />
        这些组件在 T1b 交付后会被真实页面直接复用；陈列页只在开发构建中注册。
        <Badge tone="neutral" className="ml-auto">最后更新 · T1b</Badge>
      </div>
    </div>
  );
}

function TablePreview({ rows }: { rows: DemoRow[] }) {
  return (
    <div className="overflow-hidden rounded-control border border-rule">
      <table className="w-full text-meta">
        <thead className="bg-sunken text-ink-3">
          <tr>
            <th className="px-2 py-1.5 text-left font-medium">模型</th>
            <th className="px-2 py-1.5 text-right font-medium">请求</th>
            <th className="px-2 py-1.5 text-right font-medium">Token</th>
          </tr>
        </thead>
        <tbody>
          {rows.slice(0, 3).map((row) => (
            <tr key={row.id} className="border-t border-rule">
              <td className="px-2 py-1.5 text-ink-2">{row.model}</td>
              <td className="px-2 py-1.5 text-right tabular-nums text-ink-2">{row.requests.toLocaleString("zh-CN")}</td>
              <td className="px-2 py-1.5 text-right tabular-nums text-ink-2">{(row.tokens / 1_000_000).toFixed(1)}M</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
