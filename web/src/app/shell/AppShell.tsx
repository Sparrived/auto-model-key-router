import { Suspense, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
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
        <main id="content" tabIndex={-1} className="mx-auto w-full max-w-[1600px] flex-1 px-4 py-4 md:px-6 md:py-6">
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

      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} health={health} />
    </div>
  );
}
