import { ArrowDown, ArrowRight, ArrowUp, ArrowUpToLine, Copy, ExternalLink, GripVertical, MoreHorizontal, MoveRight, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { PLAN_FAMILIES } from "@/lib/endpoint-plans";
import { copyText } from "@/lib/format";
import { ProviderIcon } from "@/lib/brand-icons";
import {
  freezeExpiresAtText,
  freezeLookupKey,
  freezeReasonText,
  freezeResetAtText,
  freezeResetCountdown,
  freezeTriggerText,
} from "@/lib/quota-freeze";
import type { ModelSummaryResponse, Provider, QuotaFreeze, RouteEntry } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { IconButton } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { DropdownMenu, DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/DropdownMenu";
import { Tooltip } from "@/components/ui/Tooltip";
import { MODES, findProviderKey, normalizeMode } from "./shared";

interface RouteGraphProps {
  route: RouteEntry;
  models: ModelSummaryResponse[];
  providers: Provider[];
  freezes?: Map<string, QuotaFreeze>;
  now?: number;
  busy?: boolean;
  onChangeMode?: (mode: string) => void;
  onReorderTarget?: (from: number, to: number) => void;
  onRemoveTarget?: (index: number) => void;
  onMoveTarget?: (index: number, targetCount: number) => void;
  onOpenProvider?: (providerId: string) => void;
  onSelectRoute?: (routeId: string) => void;
}

function FreezeTooltip({ freeze, now }: { freeze: QuotaFreeze; now: number }) {
  const countdown = freezeResetCountdown(freeze, now);
  const resetAt = freezeResetAtText(freeze);
  const expiresAt = freezeExpiresAtText(freeze);
  return (
    <div className="space-y-0.5">
      <div>{freezeReasonText(freeze)}</div>
      {countdown ? (
        <div>{countdown === "正在自动解冻…" ? countdown : `${expiresAt ? "自动解冻倒计时" : "窗口重置倒计时"}：${countdown}`}</div>
      ) : null}
      {expiresAt ? <div>自动解冻时刻：{expiresAt}</div> : null}
      {resetAt ? <div>窗口刷新：{resetAt}</div> : null}
    </div>
  );
}

// 路由图展示「对外名称 → 路由模式 → 有序目标 → 端点计划」；支持高频单步快捷操作
// （切模式、调序、迁移、移除、跳供应商），完整多字段编辑仍收在右侧编辑抽屉。
export function RouteGraph({
  route,
  models,
  providers,
  freezes,
  now = Date.now(),
  busy = false,
  onChangeMode,
  onReorderTarget,
  onRemoveTarget,
  onMoveTarget,
  onOpenProvider,
  onSelectRoute,
}: RouteGraphProps) {
  const targets = route.targets || [];
  const activeMode = normalizeMode(route.routing_mode);

  const targetSectionTitle = activeMode === "priority"
    ? "03 / 目标（按优先级）"
    : activeMode === "only_first"
      ? "03 / 目标（仅命中首条）"
      : "03 / 目标（轮询顺序）";

  async function copyUpstream(text: string) {
    try {
      await copyText(text);
      toast.success("已复制上游模型名");
    } catch {
      toast.error("复制失败");
    }
  }

  return (
    <div className="space-y-4 p-3.5">
      <div className="amkr-dot-matrix flex flex-col gap-3 rounded-control border border-rule p-3.5 lg:flex-row lg:items-stretch">
        <div className="relative flex min-w-0 flex-col justify-center gap-2 rounded-[5px] border border-rule bg-raised p-3.5 shadow-[var(--shadow-node)] lg:w-56">
          <span aria-hidden className="pointer-events-none absolute -right-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] border-rule-strong bg-panel lg:block" />
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-3">01 / 对外名称</span>
          <div className="flex items-start gap-1.5">
            <span className="min-w-0 flex-1 break-all font-mono text-title font-semibold text-ink">{route.id}</span>
            <IconButton
              size="sm"
              aria-label={`复制模型名 ${route.id}`}
              tooltip="复制对外模型名"
              onClick={async () => {
                try {
                  await copyText(route.id);
                  toast.success("已复制模型名");
                } catch {
                  toast.error("复制失败");
                }
              }}
            >
              <Copy aria-hidden className="size-3.5" />
            </IconButton>
          </div>
          {route.aliases?.length ? (
            <div className="flex flex-wrap gap-1">
              {route.aliases.map((alias) => <CopyChip key={alias} text={alias} />)}
            </div>
          ) : (
            <span className="text-meta text-ink-3">无别名</span>
          )}
          <span className="text-[11px] text-ink-3">ID 与别名都出现在 /v1/models</span>
        </div>

        <div className="hidden items-center justify-center text-ink-3 lg:flex" aria-hidden>
          <ArrowRight className="size-4" />
        </div>

        <div className="relative flex flex-col justify-center gap-2 rounded-[5px] border border-rule bg-raised p-3.5 shadow-[var(--shadow-node)] lg:w-48">
          <span aria-hidden className="pointer-events-none absolute -left-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] border-rule-strong bg-panel lg:block" />
          <span aria-hidden className="pointer-events-none absolute -right-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] border-rule-strong bg-panel lg:block" />
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-3">02 / 路由模式</span>
          {onChangeMode ? (
            <div role="group" aria-label="快捷切换路由模式" className="grid grid-cols-3 gap-1 rounded-control border border-rule bg-sunken p-0.5">
              {MODES.map((option) => {
                const selected = activeMode === option.value;
                return (
                  <button
                    key={option.value}
                    type="button"
                    disabled={busy}
                    aria-pressed={selected}
                    onClick={() => {
                      if (!selected) onChangeMode(option.value);
                    }}
                    className={[
                      "rounded-[4px] px-1.5 py-1 text-[11px] font-medium transition-colors disabled:opacity-50",
                      selected
                        ? "bg-accent text-on-accent shadow-[var(--shadow-node)]"
                        : "text-ink-2 hover:bg-hover hover:text-ink",
                    ].join(" ")}
                  >
                    {option.label}
                  </button>
                );
              })}
            </div>
          ) : (
            <Badge tone="accent">{MODES.find((m) => m.value === activeMode)?.label || "轮询"}</Badge>
          )}
          <span className="text-[11px] leading-5 text-ink-3">
            {activeMode === "priority" ? "按编号顺序尝试，前面的 Key 失败或不可用时再试下一条。" : null}
            {activeMode === "only_first" ? "只使用第一条可用目标，其余目标仅作备用。" : null}
            {activeMode === "round_robin" ? "请求在可用目标之间依次轮转。" : null}
          </span>
        </div>

        <div className="hidden items-center justify-center text-ink-3 lg:flex" aria-hidden>
          <ArrowRight className="size-4" />
        </div>

        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex items-center justify-between px-0.5">
            <span className="font-mono text-[10px] uppercase tracking-wider text-ink-3">{targetSectionTitle}</span>
            <span className="font-mono text-[10px] text-ink-3">{targets.length} TARGETS</span>
          </div>
          {targets.length ? targets.map((target, index) => {
            const found = findProviderKey(providers, target.provider, target.key);
            const missing = Boolean(providers.length > 0 && !found);
            const disabled = Boolean(found && found.key.enabled === false);
            const freeze = freezes?.get(freezeLookupKey(target.provider, target.key));
            const maxConcurrency = found?.key.max_concurrency;
            const standby = activeMode === "only_first" && index > 0;
            const dimmed = missing || disabled || standby;
            const effectiveUpstream = target.upstream_model || route.id;
            const hasMenu = Boolean(onMoveTarget || onReorderTarget || onRemoveTarget || onOpenProvider);

            return (
              <div
                key={`${target.provider}|${target.key}|${target.upstream_model}|${index}`}
                className={[
                  "relative flex items-center gap-2 rounded-[5px] border bg-raised px-2.5 py-2 shadow-[var(--shadow-node)] transition-opacity",
                  freeze ? "border-critical/50" : (disabled || missing) ? "border-warn/50" : "border-rule",
                  dimmed ? "opacity-70" : "",
                ].join(" ")}
              >
                <span aria-hidden className="pointer-events-none absolute -left-[3px] top-1/2 hidden size-[5px] -translate-y-1/2 rounded-pill border-[1.5px] border-rule-strong bg-panel lg:block" />
                <span
                  className={
                    index === 0 && !disabled && !missing && !freeze
                      ? "grid size-5 shrink-0 place-items-center rounded-chip bg-accent font-mono text-[11px] font-semibold tabular-nums text-on-accent"
                      : "grid size-5 shrink-0 place-items-center rounded-chip border border-rule-strong font-mono text-[11px] font-medium tabular-nums text-ink-3"
                  }
                >
                  {index + 1}
                </span>
                <ProviderIcon
                  id={target.provider}
                  baseUrl={found?.provider.base_url || providers.find((provider) => provider.id === target.provider)?.base_url || null}
                  size={16}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    {onOpenProvider ? (
                      <button
                        type="button"
                        onClick={() => onOpenProvider(target.provider)}
                        title={`在供应商页查看 ${target.provider} / ${target.key}`}
                        className="truncate font-mono text-dense text-ink underline-offset-2 hover:text-accent hover:underline"
                      >
                        {target.provider} / {target.key}
                      </button>
                    ) : (
                      <span className="truncate font-mono text-dense text-ink">{target.provider} / {target.key}</span>
                    )}
                    {missing ? <Badge tone="warn">Key 不存在</Badge> : null}
                    {disabled ? <Badge tone="warn">已禁用</Badge> : null}
                    {freeze ? (
                      <Tooltip content={<FreezeTooltip freeze={freeze} now={now} />}>
                        <span className="inline-flex">
                          <Badge tone="critical">{freezeTriggerText(freeze)}</Badge>
                        </span>
                      </Tooltip>
                    ) : null}
                    {typeof maxConcurrency === "number" && maxConcurrency > 0 ? (
                      <Badge tone="neutral">并发 {maxConcurrency}</Badge>
                    ) : null}
                    {standby ? <Badge tone="neutral">仅备用</Badge> : null}
                  </div>
                  <div className="truncate font-mono text-meta text-ink-3">
                    上游模型 {effectiveUpstream}
                    {target.upstream_model ? null : "（留空 = 路由名）"}
                  </div>
                </div>
                {hasMenu ? (
                  <DropdownMenu
                    align="end"
                    trigger={
                      <IconButton aria-label={`目标 ${index + 1} 的操作`} size="sm" disabled={busy}>
                        <MoreHorizontal aria-hidden className="size-4" />
                      </IconButton>
                    }
                  >
                    {onReorderTarget && targets.length > 1 ? (
                      <>
                        {index > 0 ? (
                          <DropdownMenuItem icon={<ArrowUpToLine />} onSelect={() => onReorderTarget(index, 0)}>
                            移到首位
                          </DropdownMenuItem>
                        ) : null}
                        <DropdownMenuItem icon={<ArrowUp />} disabled={index === 0} onSelect={() => onReorderTarget(index, index - 1)}>
                          上移一位
                        </DropdownMenuItem>
                        <DropdownMenuItem icon={<ArrowDown />} disabled={index === targets.length - 1} onSelect={() => onReorderTarget(index, index + 1)}>
                          下移一位
                        </DropdownMenuItem>
                        <DropdownMenuSeparator />
                      </>
                    ) : null}
                    {onMoveTarget ? (
                      <DropdownMenuItem icon={<MoveRight />} onSelect={() => onMoveTarget(index, targets.length)}>
                        移到其它路由
                      </DropdownMenuItem>
                    ) : null}
                    {onOpenProvider ? (
                      <DropdownMenuItem icon={<ExternalLink />} onSelect={() => onOpenProvider(target.provider)}>
                        在供应商页查看
                      </DropdownMenuItem>
                    ) : null}
                    <DropdownMenuItem icon={<Copy />} onSelect={() => void copyUpstream(effectiveUpstream)}>
                      复制上游模型名
                    </DropdownMenuItem>
                    {onRemoveTarget ? (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem danger icon={<Trash2 />} onSelect={() => onRemoveTarget(index)}>
                          {targets.length === 1 ? "移除目标并删除路由" : "移除此目标"}
                        </DropdownMenuItem>
                      </>
                    ) : null}
                  </DropdownMenu>
                ) : (
                  <GripVertical aria-hidden className="size-4 text-ink-3" />
                )}
              </div>
            );
          }) : (
            <div className="rounded-control border border-dashed border-rule px-3 py-4 text-center text-meta text-ink-3">
              没有目标。这个路由会在保存时被删除。
            </div>
          )}
        </div>
      </div>

      <div className="space-y-2">
        <div className="flex flex-wrap items-baseline justify-between gap-3">
          <h3 className="text-dense font-medium text-ink">端点计划</h3>
          <span className="text-meta text-ink-3">直呼这个名称时，配过的端点改走指定模型；没配的仍用上面的目标</span>
        </div>
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {PLAN_FAMILIES.map((family) => {
            const primary = route.endpoints?.[family.id]?.primary;
            const model = primary?.model || "";
            const known = model ? models.some((item) => item.id === model) : false;
            return (
              <div
                key={family.id}
                className={model ? "rounded-control border border-rule bg-panel px-3 py-2" : "rounded-control border border-dashed border-rule px-3 py-2"}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className={model ? "shrink-0 whitespace-nowrap text-dense font-medium text-ink" : "shrink-0 whitespace-nowrap text-dense text-ink-3"}>{family.label}</span>
                  <span className="min-w-0 truncate font-mono text-[11px] text-ink-3" title={family.paths}>{family.paths.split("、")[0].split(" 与 ")[0]}</span>
                </div>
                {model ? (
                  <div className="mt-1 flex flex-wrap items-center gap-1.5">
                    {known && onSelectRoute && model !== route.id ? (
                      <button
                        type="button"
                        onClick={() => onSelectRoute(model)}
                        className="font-mono text-meta text-ink underline-offset-2 hover:text-accent hover:underline"
                      >
                        {model}
                      </button>
                    ) : (
                      <span className="font-mono text-meta text-ink">{model}</span>
                    )}
                    {!known ? <Badge tone="warn">不在模型列表</Badge> : null}
                    <span className="text-meta text-ink-3">{primary?.key ? `固定 Key ${primary.key}` : "自动路由"}</span>
                  </div>
                ) : (
                  <div className="mt-1 text-meta text-ink-3">用本路由的目标</div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
