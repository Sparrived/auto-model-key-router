import { useEffect, useState, type RefObject } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Menu, Moon, Monitor, RefreshCw, Search, Sun } from "lucide-react";
import type { HealthResponse, MetricsSnapshot } from "@/lib/types";
import { formatCompact, formatCount } from "@/lib/format";
import { cycleTheme, onThemeChange, themeInfo, THEME_OPTIONS, type ThemeInfo } from "@/lib/theme";
import { cn } from "@/lib/cn";
import { Kbd } from "@/components/ui/Kbd";
import { toast } from "sonner";

interface TopbarProps {
  title: string;
  health?: HealthResponse;
  healthError?: unknown;
  healthLoading: boolean;
  metrics?: MetricsSnapshot;
  onMenu: () => void;
  menuButtonRef: RefObject<HTMLButtonElement | null>;
  onOpenPalette: () => void;
}

function serviceState(health: HealthResponse | undefined, error: unknown, loading: boolean) {
  if (error) return { label: "服务未连接", tone: "critical" as const };
  if (!health) return { label: "正在连接", tone: "neutral" as const };
  if (health.status && health.status !== "ok") return { label: "服务未运行", tone: "warn" as const };
  if (loading) return { label: "正在连接", tone: "neutral" as const };
  return { label: "运行中", tone: "good" as const };
}

const toneDot: Record<string, string> = {
  good: "bg-good",
  warn: "bg-warn",
  serious: "bg-serious",
  critical: "bg-critical",
  neutral: "bg-neutral",
};

export function Topbar({
  title,
  health,
  healthError,
  healthLoading,
  metrics,
  onMenu,
  menuButtonRef,
  onOpenPalette,
}: TopbarProps) {
  const client = useQueryClient();
  const [theme, setTheme] = useState<ThemeInfo>(themeInfo);
  useEffect(() => onThemeChange(setTheme), []);

  const service = serviceState(health, healthError, healthLoading);
  const preference = THEME_OPTIONS.find((option) => option.id === theme.preference) ?? THEME_OPTIONS[0];
  const ThemeIcon = theme.preference === "system" ? Monitor : theme.preference === "light" ? Sun : Moon;
  const themeLabel =
    theme.preference === "system"
      ? `主题：跟随系统（当前${theme.resolved === "dark" ? "深色" : "浅色"}）`
      : `主题：${preference.label}`;

  const hasMetrics = Boolean(metrics?.total);
  const live = hasMetrics
    ? [
        { label: "RPM", value: formatCount(metrics?.current_rpm ?? 0) },
        { label: "TPM", value: formatCompact(metrics?.current_tpm ?? 0) },
        { label: "进行中", value: formatCount(metrics?.active_requests ?? 0) },
      ]
    : null;

  return (
    <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-3 border-b border-rule bg-paper/95 px-4 backdrop-blur md:px-6">
      <button
        ref={menuButtonRef}
        type="button"
        aria-label="打开主导航"
        className="grid size-8 shrink-0 place-items-center rounded-control text-ink-2 hover:bg-hover hover:text-ink md:hidden"
        onClick={onMenu}
      >
        <Menu className="size-[18px]" aria-hidden />
      </button>

      <h1 className="min-w-0 truncate text-page font-semibold tracking-tight text-ink">{title}</h1>

      <div className="ml-auto flex min-w-0 items-center gap-2 sm:gap-3">
        <div className="hidden items-center gap-1.5 text-meta text-ink-2 sm:flex" aria-live="polite">
          <span aria-hidden className={cn("size-2 shrink-0 rounded-full", toneDot[service.tone])} />
          <span className="whitespace-nowrap">{service.label}</span>
          {live ? (
            <span className="ml-1 hidden items-center gap-2 border-l border-rule pl-2 text-[11px] text-ink-3 lg:flex">
              {live.map((item) => (
                <span key={item.label} className="whitespace-nowrap">
                  {item.label} <strong className="font-medium tabular-nums text-ink-2">{item.value}</strong>
                </span>
              ))}
            </span>
          ) : null}
        </div>

        <button
          type="button"
          aria-label="打开命令面板"
          title="打开命令面板"
          className="hidden h-8 items-center gap-1.5 rounded-control border border-rule bg-panel px-2.5 text-meta text-ink-3 hover:bg-hover hover:text-ink sm:flex"
          onClick={onOpenPalette}
        >
          <Search className="size-3.5" aria-hidden />
          <span>搜索</span>
          <Kbd>⌘K</Kbd>
        </button>

        <button
          type="button"
          aria-label={`${themeLabel}，点击切换`}
          title={`${themeLabel}，点击切换`}
          className="grid size-8 place-items-center rounded-control text-ink-2 hover:bg-hover hover:text-ink"
          onClick={() => {
            const next = cycleTheme();
            const option = THEME_OPTIONS.find((item) => item.id === next) ?? THEME_OPTIONS[0];
            toast.success(`主题已切换为${option.label}`);
          }}
        >
          <ThemeIcon className="size-[18px]" aria-hidden />
        </button>

        <button
          type="button"
          aria-label="立即刷新"
          title="立即刷新状态与指标"
          className="grid size-8 place-items-center rounded-control text-ink-2 hover:bg-hover hover:text-ink"
          onClick={() => {
            void client.invalidateQueries();
            toast.success("已刷新");
          }}
        >
          <RefreshCw className="size-[18px]" aria-hidden />
        </button>
      </div>
    </header>
  );
}
