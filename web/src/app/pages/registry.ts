import { lazy, type ComponentType, type LazyExoticComponent } from "react";
import {
  Activity,
  BarChart3,
  CircleDollarSign,
  FlaskConical,
  Gauge,
  KeyRound,
  Layers,
  LayoutDashboard,
  ListChecks,
  Plug,
  Route,
  ScrollText,
  Server,
  Settings,
  type LucideIcon,
} from "lucide-react";

export interface PageDef {
  id: string;
  title: string;
  group: string;
  icon: LucideIcon;
  /** 监控页之间切换时保留同一组筛选参数。 */
  monitoring?: boolean;
  component: LazyExoticComponent<ComponentType>;
}

export interface PageGroup {
  label: string;
  items: PageDef[];
}

const OverviewPage = lazy(() => import("./OverviewPage"));
const StatusPage = lazy(() => import("./StatusPage"));
const ActivityPage = lazy(() => import("./ActivityPage"));
const CostPage = lazy(() => import("./CostPage"));
const WorkspacesPage = lazy(() => import("./WorkspacesPage"));
const AccountsPage = lazy(() => import("./AccountsPage"));
const LogsPage = lazy(() => import("./LogsPage"));
const ProvidersPage = lazy(() => import("./ProvidersPage"));
const RoutingPage = lazy(() => import("./RoutingPage"));
const TasksPage = lazy(() => import("./TasksPage"));
const AccessKeysPage = lazy(() => import("./AccessKeysPage"));
const IntegrationsPage = lazy(() => import("./IntegrationsPage"));
const PlaygroundPage = lazy(() => import("./PlaygroundPage"));
const SettingsPage = lazy(() => import("./SettingsPage"));

export const PAGE_GROUPS: PageGroup[] = [
  {
    label: "监控",
    items: [
      { id: "overview", title: "概览", group: "监控", icon: LayoutDashboard, monitoring: true, component: OverviewPage },
      { id: "status", title: "状态监控", group: "监控", icon: Gauge, monitoring: true, component: StatusPage },
      { id: "activity", title: "用量统计", group: "监控", icon: Activity, monitoring: true, component: ActivityPage },
      { id: "cost", title: "成本", group: "监控", icon: CircleDollarSign, monitoring: true, component: CostPage },
      { id: "workspaces", title: "工作空间", group: "监控", icon: Layers, monitoring: true, component: WorkspacesPage },
      { id: "accounts", title: "账号资源", group: "监控", icon: BarChart3, monitoring: true, component: AccountsPage },
      { id: "logs", title: "服务日志", group: "监控", icon: ScrollText, monitoring: true, component: LogsPage },
    ],
  },
  {
    label: "配置",
    items: [
      { id: "providers", title: "供应商", group: "配置", icon: Server, component: ProvidersPage },
      { id: "routing", title: "模型路由", group: "配置", icon: Route, component: RoutingPage },
      { id: "tasks", title: "任务路由", group: "配置", icon: ListChecks, component: TasksPage },
      { id: "access-keys", title: "访问密钥", group: "配置", icon: KeyRound, component: AccessKeysPage },
      { id: "integrations", title: "集成", group: "配置", icon: Plug, component: IntegrationsPage },
    ],
  },
  {
    label: "体验",
    items: [
      { id: "playground", title: "试验场", group: "体验", icon: FlaskConical, component: PlaygroundPage },
    ],
  },
  {
    label: "系统",
    items: [
      { id: "settings", title: "设置", group: "系统", icon: Settings, component: SettingsPage },
    ],
  },
];

const galleryPage: PageDef | null = import.meta.env.DEV
  ? {
      id: "__gallery",
      title: "组件陈列",
      group: "开发",
      icon: FlaskConical,
      component: lazy(() => import("./GalleryPage")),
    }
  : null;

export const ALL_PAGES: PageDef[] = [
  ...PAGE_GROUPS.flatMap((group) => group.items),
  ...(galleryPage ? [galleryPage] : []),
];

export function pageById(id: string): PageDef {
  return ALL_PAGES.find((page) => page.id === id) ?? PAGE_GROUPS[0].items[0];
}
