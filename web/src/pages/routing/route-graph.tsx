import { ArrowRight, GripVertical, MoreHorizontal } from "lucide-react";
import { PLAN_FAMILIES } from "@/lib/endpoint-plans";
import type { ModelSummaryResponse, Provider, RouteEntry } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { IconButton } from "@/components/ui/Button";
import { Chip } from "@/components/ui/Chip";
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/DropdownMenu";
import { ProviderIcon } from "@/lib/brand-icons";
import { modeLabel } from "./shared";

interface RouteGraphProps {
  route: RouteEntry;
  models: ModelSummaryResponse[];
  providers: Provider[];
  onMoveTarget?: (index: number, targetCount: number) => void;
}

// 路由图是**只读**的：改名、排序、删目标都收在编辑抽屉里。
//
// 目标是有序序列，编号就是调用时实际尝试的先后，所以必须把数字画在卡片上——只靠上下
// 排列表示顺序，用户截个图发给别人就只剩下一堆没有先后的名字。
export function RouteGraph({ route, models, providers, onMoveTarget }: RouteGraphProps) {
  const targets = route.targets || [];
  return (
    <div className="space-y-5">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-stretch">
        <div className="flex min-w-0 flex-col justify-center gap-2 rounded-panel border border-rule bg-panel p-4 lg:w-56">
          <span className="text-meta text-ink-3">对外名称</span>
          <span className="break-all font-mono text-title font-semibold text-ink">{route.id}</span>
          {route.aliases?.length ? (
            <div className="flex flex-wrap gap-1">
              {route.aliases.map((alias) => <Chip key={alias} tone="neutral">{alias}</Chip>)}
            </div>
          ) : (
            <span className="text-meta text-ink-3">无别名</span>
          )}
          <span className="text-meta text-ink-3">ID 与别名都出现在 /v1/models</span>
        </div>

        <div className="hidden items-center justify-center text-ink-3 lg:flex" aria-hidden>
          <ArrowRight className="size-4" />
        </div>

        <div className="flex flex-col justify-center gap-2 rounded-panel border border-rule bg-panel p-4 lg:w-40">
          <span className="text-meta text-ink-3">路由模式</span>
          <Badge tone="neutral">{modeLabel(route.routing_mode)}</Badge>
          <span className="text-meta leading-5 text-ink-3">
            {route.routing_mode === "priority" ? "按上方顺序，前面的 Key 失败再试下一个。" : null}
            {route.routing_mode === "only_first" ? "只使用第一条可用目标。" : null}
            {route.routing_mode === "round_robin" ? "请求在目标之间轮转。" : null}
            {!route.routing_mode ? "默认策略：按服务端默认的轮询处理。" : null}
          </span>
        </div>

        <div className="hidden items-center justify-center text-ink-3 lg:flex" aria-hidden>
          <ArrowRight className="size-4" />
        </div>

        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-meta text-ink-3">轮询目标（按优先级）</span>
            <span className="text-meta text-ink-3">{targets.length} 个</span>
          </div>
          {targets.length ? targets.map((target, index) => (
            <div key={`${target.provider}|${target.key}|${target.upstream_model}|${index}`} className="flex items-center gap-2 rounded-control border border-rule bg-panel px-2.5 py-2">
              <span className="grid size-6 shrink-0 place-items-center rounded-pill bg-ink text-[11px] font-medium tabular-nums text-panel">
                {index + 1}
              </span>
              <ProviderIcon
                id={target.provider}
                baseUrl={providers.find((provider) => provider.id === target.provider)?.base_url || null}
                size={16}
              />
              <div className="min-w-0 flex-1">
                <div className="truncate font-mono text-dense text-ink">{target.provider} / {target.key}</div>
                <div className="truncate font-mono text-meta text-ink-3">
                  上游模型 {target.upstream_model || route.id}
                  {target.upstream_model ? null : "（留空 = 路由名）"}
                </div>
              </div>
              {onMoveTarget ? (
                <DropdownMenu
                  trigger={<IconButton aria-label={`目标 ${index + 1} 的操作`} size="sm"><MoreHorizontal aria-hidden className="size-4" /></IconButton>}
                >
                  <DropdownMenuItem onSelect={() => onMoveTarget(index, targets.length)}>移到其它路由</DropdownMenuItem>
                </DropdownMenu>
              ) : (
                <GripVertical aria-hidden className="size-4 text-ink-3" />
              )}
            </div>
          )) : (
            <div className="rounded-control border border-dashed border-rule px-3 py-4 text-center text-meta text-ink-3">
              没有目标。这个路由会在保存时被删除。
            </div>
          )}
        </div>
      </div>

      <div className="space-y-2">
        <div className="flex items-baseline justify-between gap-3">
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
                    <span className="font-mono text-meta text-ink">{model}</span>
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
