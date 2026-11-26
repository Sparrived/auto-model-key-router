import { useEffect, useState, type RefObject } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Copy, Menu, Moon, Monitor, RefreshCw, Search, Sun } from "lucide-react";
import type { HealthResponse, MetricsSnapshot } from "@/lib/types";
import { apiBase } from "@/lib/api";
import { copyText, formatCompact, formatCount } from "@/lib/format";
import {
  ACCENT_OPTIONS,
  cycleTheme,
  normalizeAccentPreference,
  onThemeChange,
  setAccentPreference,
  themeInfo,
  THEME_OPTIONS,
  type ThemeInfo,
} from "@/lib/theme";
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
  if (error) return { label: "未连接", tone: "critical" as const };
  if (!health) return { label: "连接中", tone: "neutral" as const };
  if (health.status && health.status !== "ok") return { label: "未运行", tone: "warn" as const };
  if (loading) return { label: "连接中", tone: "neutral" as const };
  return { label: "运行中", tone: "good" as const };
}

const toneDot: Record<string, string> = {
  good: "bg-good",
  warn: "bg-warn",
  serious: "bg-serious",
  critical: "bg-critical",
  neutral: "bg-neutral",
};

const PALETTE_DOTS = ACCENT_OPTIONS.filter((option) => option.id !== "ink");

function resolveApiEndpoint(health: HealthResponse | undefined): string {
  const base = typeof health?.base_url === "string" ? health.base_url.trim() : "";
  if (base) return base;
  if (typeof window !== "undefined") return `${window.location.origin}${apiBase()}`;
  return "";
}

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

  const activeAccent = normalizeAccentPreference(theme.accent);
  const endpoint = resolveApiEndpoint(health);
  const endpointShort = endpoint.replace(/^https?:\/\//, "");
  const hasMetrics = Boolean(metrics?.total);

  return (
    <header className="sticky top-0 z-30 flex h-12 shrink-0 items-center gap-2.5 border-b border-rule bg-panel px-3 md:px-4">
      <button
        ref={menuButtonRef}
        type="button"
        aria-label="打开主导航"
        className="grid size-7 shrink-0 place-items-center rounded-control border border-rule-strong bg-raised text-ink-2 hover:bg-hover hover:text-ink md:hidden"
        onClick={onMenu}
      >
        <Menu className="size-4" aria-hidden />
      </button>

      <div className="flex min-w-0 items-center gap-2.5">
        <h1 className="min-w-0 truncate text-page font-semibold tracking-tight text-ink">{title}</h1>
        {endpoint ? (
          <button
            type="button"
            aria-label="复制入口地址"
            title={`点击复制统一入口：${endpoint}`}
            onClick={() => {
              void copyText(endpoint)
                .then(() => toast.success(`已复制入口地址 ${endpoint}`))
                .catch(() => toast.error("复制失败，请手动复制"));
            }}
            className="hidden h-6 max-w-[220px] items-center gap-1.5 rounded-control border border-rule bg-sunken px-2 font-mono text-[11px] text-ink-2 transition-colors hover:border-rule-strong hover:text-ink lg:inline-flex"
          >
            <span aria-hidden className={cn("size-1.5 shrink-0 rounded-pill", toneDot[service.tone])} />
            <span className="truncate">{endpointShort}</span>
            <Copy aria-hidden className="size-3 shrink-0 text-ink-3" />
          </button>
        ) : null}
      </div>

      {/* 中央遥测读数槽（冷静 tabular-nums，无装饰动效） */}
      <div
        className="ml-auto hidden h-7 items-center gap-2 rounded-control border border-rule bg-sunken px-2 text-[11px] lg:flex"
        aria-live="polite"
      >
        <span className="inline-flex items-center gap-1.5 text-ink-2">
          <span aria-hidden className={cn("size-1.5 shrink-0 rounded-pill", toneDot[service.tone])} />
          <span>{service.label}</span>
        </span>
        {hasMetrics ? (
          <>
            <span className="h-3 w-px bg-rule" aria-hidden />
            <span className="text-ink-3">
              RPM <strong className="font-mono font-semibold tabular-nums text-ink">{formatCount(metrics?.current_rpm ?? 0)}</strong>
            </span>
            <span className="h-3 w-px bg-rule" aria-hidden />
            <span className="text-ink-3">
              TPM <strong className="font-mono font-semibold tabular-nums text-ink">{formatCompact(metrics?.current_tpm ?? 0)}</strong>
            </span>
            <span className="h-3 w-px bg-rule" aria-hidden />
            <span className="text-ink-3">
              在途 <strong className="font-mono font-semibold tabular-nums text-ink">{formatCount(metrics?.active_requests ?? 0)}</strong>
            </span>
          </>
        ) : null}
      </div>

      <div className="ml-auto flex shrink-0 items-center gap-1.5 lg:ml-0">
        <div className="flex items-center gap-1.5 rounded-control border border-rule bg-sunken px-2 py-1 lg:hidden" aria-live="polite">
          <span aria-hidden className={cn("size-1.5 shrink-0 rounded-pill", toneDot[service.tone])} />
          <span className="text-[11px] text-ink-2">{service.label}</span>
        </div>

        <button
          type="button"
          aria-label="打开命令面板"
          title="打开命令面板"
          className="hidden h-7 items-center gap-1.5 rounded-control border border-rule-strong bg-raised px-2 text-meta text-ink-2 transition-colors hover:bg-hover hover:text-ink sm:flex"
          onClick={onOpenPalette}
        >
          <Search className="size-3.5 text-ink-3" aria-hidden />
          <span>搜索</span>
          <Kbd>⌘K</Kbd>
        </button>

        {/* 工程配色切换（signal / mineral / pine） */}
        <div
          role="group"
          aria-label="工程配色预设"
          className="hidden h-7 items-center gap-1.5 rounded-control border border-rule bg-sunken px-2 sm:flex"
        >
          {PALETTE_DOTS.map((preset) => {
            const active = activeAccent === preset.id;
            return (
              <button
                key={preset.id}
                type="button"
                aria-label={`配色：${preset.label}`}
                aria-pressed={active}
                title={preset.label}
                onClick={() => {
                  setAccentPreference(preset.id);
                  toast.success(`已切换配色：${preset.label}`);
                }}
                style={{ backgroundColor: preset.swatch }}
                className={cn(
                  "size-3 rounded-pill border transition-transform hover:scale-110",
                  active ? "border-ink ring-1 ring-ink/35" : "border-transparent opacity-80 hover:opacity-100",
                )}
              />
            );
          })}
        </div>

        <button
          type="button"
          aria-label={`${themeLabel}，点击切换`}
          title={`${themeLabel}，点击切换`}
          className="grid size-7 place-items-center rounded-control border border-rule-strong bg-raised text-ink-2 transition-colors hover:bg-hover hover:text-ink"
          onClick={() => {
            const next = cycleTheme();
            const option = THEME_OPTIONS.find((item) => item.id === next) ?? THEME_OPTIONS[0];
            toast.success(`主题已切换为${option.label}`);
          }}
        >
          <ThemeIcon className="size-3.5" aria-hidden />
        </button>

        <button
          type="button"
          aria-label="立即刷新"
          title="立即刷新状态与指标"
          className="grid size-7 place-items-center rounded-control border border-rule-strong bg-raised text-ink-2 transition-colors hover:bg-hover hover:text-ink"
          onClick={() => {
            void client.invalidateQueries();
            toast.success("已刷新");
          }}
        >
          <RefreshCw className="size-3.5" aria-hidden />
        </button>
      </div>
    </header>
  );
}
