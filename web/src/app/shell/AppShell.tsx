import { Suspense, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FlaskConical, Gauge, LayoutDashboard, Menu, Route } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { queryKeys } from "@/lib/query-keys";
import type { HealthResponse, MetricsSnapshot } from "@/lib/types";
import { PagePlaceholderFallback } from "../PageFallback";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import { CommandPalette } from "../command-palette";
import { ErrorBoundary } from "../ErrorBoundary";
import { pageById } from "../pages/registry";
import { navigate, useRoute } from "../router";
import { versionCompatible } from "../auth";
import { Banner } from "@/components/ui/Banner";
import { Empty } from "@/components/ui/Empty";

const G_PAGES: Record<string, string> = {
  o: "overview",
  s: "status",
  u: "activity",
  c: "cost",
  p: "providers",
  r: "routing",
  t: "tasks",
  k: "access-keys",
};

const MOBILE_QUICK_TABS = [
  { id: "overview", label: "概览", icon: LayoutDashboard },
  { id: "routing", label: "路由", icon: Route },
  { id: "status", label: "状态", icon: Gauge },
  { id: "playground", label: "试验场", icon: FlaskConical },
] as const;

function isEditable(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName.toLowerCase();
  return target.isContentEditable || tag === "input" || tag === "textarea" || tag === "select";
}

export function AppShell() {
  const route = useRoute();
  const page = pageById(route.page);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const gPrefixAt = useRef(0);

  const healthQuery = useQuery({
    queryKey: queryKeys.health(),
    queryFn: () => api.health(),
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
  });
  const metricsQuery = useQuery({
    queryKey: queryKeys.metrics(1),
    queryFn: () => api.metrics(1),
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
  });

  const health = healthQuery.data as HealthResponse | undefined;
  const metrics = metricsQuery.data as MetricsSnapshot | undefined;
  const compatible = versionCompatible(health?.version);
  const PageComponent = page.component;
  const isOverview = page.id === "overview";

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setPaletteOpen((open) => !open);
        return;
      }
      if (isEditable(event.target) || event.metaKey || event.ctrlKey || event.altKey) return;

      if (event.key === "g") {
        gPrefixAt.current = Date.now();
        return;
      }
      if (Date.now() - gPrefixAt.current < 1_500) {
        const target = G_PAGES[event.key.toLowerCase()];
        gPrefixAt.current = 0;
        if (target) {
          event.preventDefault();
          navigate(target);
        }
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  return (
    <div className="flex min-h-screen bg-paper">
      <Sidebar
        activePage={page.id}
        version={health?.version}
        mobileOpen={mobileOpen}
        onMobileOpenChange={setMobileOpen}
        returnFocusRef={menuButtonRef}
      />

      <div className="flex min-w-0 flex-1 flex-col" inert={mobileOpen || undefined}>
        <Topbar
          title={page.title}
          health={health}
          healthError={healthQuery.error}
          healthLoading={healthQuery.isLoading}
          metrics={metrics}
          onMenu={() => setMobileOpen(true)}
          menuButtonRef={menuButtonRef}
          onOpenPalette={() => setPaletteOpen(true)}
        />
        <main
          id="content"
          tabIndex={-1}
          className={cn(
            "mx-auto w-full flex-1 pb-20 md:pb-5",
            isOverview ? "max-w-[1920px] px-3 py-3" : "max-w-[1600px] px-4 py-4 md:px-6 md:py-5",
          )}
        >
          {!compatible && page.id !== "settings" ? (
            <div className="space-y-3">
              <Banner tone="warn">
                当前 AMKR {health?.version || "未知版本"}，WebUI 至少需要 4.0.0。升级前仅开放设置页。
              </Banner>
              <Empty
                title="后端版本不兼容"
                description="请先升级 AMKR 服务，或前往设置页查看当前版本信息。"
              />
            </div>
          ) : (
            <ErrorBoundary key={page.id} title={page.title}>
              <Suspense fallback={<PagePlaceholderFallback />}>
                <PageComponent />
              </Suspense>
            </ErrorBoundary>
          )}
        </main>
      </div>

      {/* 手机端底部快捷导航 */}
      <nav
        aria-label="移动端快捷导航"
        className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-5 border-t border-rule bg-panel pb-[env(safe-area-inset-bottom)] md:hidden"
      >
        {MOBILE_QUICK_TABS.map((tab) => {
          const Icon = tab.icon;
          const active = page.id === tab.id;
          return (
            <button
              key={tab.id}
              type="button"
              aria-current={active ? "page" : undefined}
              onClick={() => navigate(tab.id)}
              className={cn(
                "flex flex-col items-center justify-center gap-0.5 py-2 text-[10px] transition-colors",
                active ? "font-semibold text-accent" : "text-ink-3 hover:text-ink",
              )}
            >
              <Icon className="size-4" aria-hidden />
              <span>{tab.label}</span>
            </button>
          );
        })}
        <button
          type="button"
          aria-label="打开完整菜单"
          onClick={() => setMobileOpen(true)}
          className="flex flex-col items-center justify-center gap-0.5 py-2 text-[10px] text-ink-3 transition-colors hover:text-ink"
        >
          <Menu className="size-4" aria-hidden />
          <span>全部</span>
        </button>
      </nav>

      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} health={health} />
    </div>
  );
}
