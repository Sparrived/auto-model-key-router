// 流向图：把 workspace → task → model → provider 这类分层流量画成桑基图。
//
// 节点按来源着色；流带用来源色 15%–25% 透明度。悬停任一节点时，从它出发能到达
// 的全部上游/下游路径一起提亮，其余路径降到 6%，从而看清“这一支流量去了哪里”。
// 每层超过 8 个节点时，尾部合并为「其他」——超过这个数量，标签和流带都已不可读。

import { useMemo, useState, type ReactNode } from "react";
import {
  fitLabel,
  sankeyLabelBudget,
  sankeyLayout,
  sankeyLinkPath,
  sankeyLayers,
  type FlowLink,
  type SankeyEdge,
  type SankeyNode,
} from "@/lib/chart-math";
import { useSeriesColors } from "@/lib/series-colors";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { ChartTable } from "./chart-utils";
import { useChartSize } from "./useChartSize";

export interface FlowMapProps {
  links: FlowLink[];
  layerLabels: string[];
  metric: "requests" | "tokens";
  /** 仅这一层的节点使用分类色；其余层与流带保持中性灰。默认 0。 */
  colorLayer?: number;
  /** 分类色作用域；同一实体的颜色跨图表稳定。未传时按该层标签隔离。 */
  colorScope?: string;
  format: (value: number) => string;
  onNodeClick?: (node: { layer: number; name: string; value: number }) => void;
  height?: number;
  ariaLabel?: string;
  busy?: boolean;
  error?: ReactNode;
  emptyText?: string;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

const OTHER = "其他";
const MAX_NODES_PER_LAYER = 8;
const PAD = { top: 22, right: 16, bottom: 8, left: 16 };
const X_OFFSET = 56;

type WeightedLink = FlowLink & { weight: number };

function linkWeight(link: FlowLink, metric: "requests" | "tokens"): number {
  const value = metric === "tokens" ? link.total_tokens : link.requests;
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(0, number) : 0;
}

/** 超过上限的层按 weight 合并尾部；同一对节点之间的多条边再合并。 */
function foldLinks(links: WeightedLink[]): WeightedLink[] {
  if (!links.length) return [];
  const layers = sankeyLayers(links);
  const aliases = new Map<number, Map<string, string>>();
  layers.forEach((nodes, layerIndex) => {
    const sorted = [...nodes].sort((a, b) => b.value - a.value || a.name.localeCompare(b.name, "zh-CN"));
    if (sorted.length <= MAX_NODES_PER_LAYER) return;
    const keep = new Set(sorted.slice(0, MAX_NODES_PER_LAYER - 1).map((node) => node.name));
    const map = new Map<string, string>();
    for (const node of sorted) if (!keep.has(node.name)) map.set(node.name, OTHER);
    aliases.set(layerIndex, map);
  });
  const merged = new Map<string, WeightedLink>();
  for (const link of links) {
    const sourceLayer = Number(link.source_layer);
    const targetLayer = Number(link.target_layer);
    const source = aliases.get(sourceLayer)?.get(link.source) ?? link.source;
    const target = aliases.get(targetLayer)?.get(link.target) ?? link.target;
    const key = `${sourceLayer}\u0000${targetLayer}\u0000${source}\u0000${target}`;
    const existing = merged.get(key);
    if (existing) {
      existing.weight += link.weight;
      existing.requests = Number(existing.requests) + Number(link.requests || 0);
      existing.total_tokens = Number(existing.total_tokens || 0) + Number(link.total_tokens || 0);
    } else {
      merged.set(key, { ...link, source, target, weight: link.weight });
    }
  }
  return [...merged.values()].filter((link) => link.weight > 0);
}

export function FlowMap({
  links,
  layerLabels,
  metric,
  colorLayer = 0,
  colorScope,
  format,
  onNodeClick,
  height = 360,
  ariaLabel = "流向图",
  busy = false,
  error,
  emptyText = "当前时间窗口内没有流向数据",
  embedded = false,
  className,
}: FlowMapProps) {
  const { ref, width } = useChartSize();
  const [active, setActive] = useState<string | null>(null);
  const [hover, setHover] = useState<{ x: number; y: number; title: string; value: string } | null>(null);

  const weighted = useMemo(
    () => links.map((link) => ({ ...link, weight: linkWeight(link, metric) })).filter((link) => link.weight > 0),
    [links, metric],
  );
  const folded = useMemo(() => foldLinks(weighted), [weighted]);
  const layoutWidth = Math.max(260, width - X_OFFSET * 2);
  const layoutHeight = Math.max(120, height - PAD.top - PAD.bottom);
  const layoutLinks = useMemo(
    () => folded.map((link) => ({ ...link, requests: link.weight })),
    [folded],
  );
  const layout = useMemo(
    () => sankeyLayout(layoutLinks, { width: layoutWidth, height: layoutHeight, nodeWidth: 14, gap: 11 }),
    [layoutLinks, layoutWidth, layoutHeight],
  );

  const nodeKeys = useMemo(
    // 只给着色层分配分类色槽位：若把其它层也算进来，前 8 个槽位会被首层占满，
    // 真正要着色的第二层反而全落到 --series-other 的灰色。
    () => layout.columns.flatMap((column, layer) =>
      layer === colorLayer ? column.nodes.map((node) => node.name) : [],
    ),
    [layout.columns, colorLayer],
  );
  // 作用域按维度隔离：workspace / model 等维度的槽位互不挤占。未显式传
  // colorScope 时用层标签兜底，避免同页多个 FlowMap 共用 "flow-map" 占满 8 色。
  const color = useSeriesColors(
    colorScope ?? `flow:${layerLabels[colorLayer] ?? `第 ${colorLayer + 1} 层`}`,
    nodeKeys,
  );
  const adjacency = useMemo(() => {
    const map = new Map<string, Set<string>>();
    const add = (a: string, b: string) => {
      const set = map.get(a) ?? new Set<string>();
      set.add(b);
      map.set(a, set);
    };
    for (const edge of layout.edges) {
      const source = `${edge.sourceLayer}:${edge.sourceName}`;
      const target = `${edge.targetLayer}:${edge.targetName}`;
      add(source, target);
      add(target, source);
    }
    return map;
  }, [layout.edges]);

  const highlighted = useMemo(() => {
    if (!active) return null;
    const seen = new Set<string>([active]);
    const queue = [active];
    while (queue.length) {
      const current = queue.shift();
      if (!current) continue;
      for (const next of adjacency.get(current) ?? []) {
        if (!seen.has(next)) {
          seen.add(next);
          queue.push(next);
        }
      }
    }
    return seen;
  }, [active, adjacency]);

  function nodeKey(layer: number, name: string) {
    return `${layer}:${name}`;
  }

  function handleNodeEnter(event: React.PointerEvent<SVGRectElement>, layer: number, node: SankeyNode) {
    const parent = ref.current?.getBoundingClientRect();
    const rect = event.currentTarget.getBoundingClientRect();
    if (!parent) return;
    setActive(nodeKey(layer, node.name));
    setHover({
      x: rect.left - parent.left + rect.width / 2,
      y: rect.top - parent.top - 4,
      title: `${layerLabels[layer] ?? `第 ${layer + 1} 层`} · ${node.name}`,
      value: format(node.value),
    });
  }

  const empty = !folded.length;
  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      defaultView={width > 0 && width < 480 ? "table" : "chart"}
      embedded={embedded}
      busy={busy}
      error={error}
      empty={empty}
      emptyText={emptyText}
      legend={<span className="text-[11px] text-ink-3">仅第 {colorLayer + 1} 层节点使用分类色；流带与其它层为中性灰；悬停节点可追踪上下游路径。</span>}
      table={<FlowTable links={weighted} metric={metric} format={format} layerLabels={layerLabels} />}
    >
      <div ref={ref} className="relative w-full" style={{ height }} role="img" aria-label={`${ariaLabel}；悬停节点追踪上下游路径`}>
        <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block w-full overflow-visible" aria-hidden="true">
          <defs>
            <filter id="flow-label-halo" x="-20%" y="-20%" width="140%" height="140%">
              <feMorphology in="SourceAlpha" operator="dilate" radius="1.6" result="dilate" />
              <feFlood floodColor="var(--panel)" result="flood" />
              <feComposite in="flood" in2="dilate" operator="in" result="halo" />
              <feMerge>
                <feMergeNode in="halo" />
                <feMergeNode in="SourceGraphic" />
              </feMerge>
            </filter>
          </defs>

          {layerLabels.slice(0, layout.columns.length).map((label, index) => {
            const column = layout.columns[index];
            return (
              <text
                key={`layer-${index}`}
                x={X_OFFSET + (column?.x ?? 0) + 7}
                y={11}
                textAnchor="middle"
                className="fill-ink-3"
                style={{ fontSize: 10 }}
              >
                {label}
              </text>
            );
          })}

          {layout.edges.map((edge, index) => {
            const sourceKey = nodeKey(edge.sourceLayer, edge.sourceName);
            const targetKey = nodeKey(edge.targetLayer, edge.targetName);
            const connected = !highlighted || highlighted.has(sourceKey) || highlighted.has(targetKey);
            const shifted = shiftEdge(edge, PAD.left + X_OFFSET, PAD.top);
            // 只有与 colorLayer 直接相连的流带继承端点的分类色；其余一律中性灰。
            const coloredEndpoint = edge.sourceLayer === colorLayer
              ? edge.sourceName
              : edge.targetLayer === colorLayer
                ? edge.targetName
                : null;
            const edgeColor = coloredEndpoint ? color(coloredEndpoint) : "var(--neutral)";
            return (
              <path
                key={`edge-${index}`}
                d={sankeyLinkPath(shifted)}
                fill={edgeColor}
                opacity={connected ? (coloredEndpoint ? 0.18 : 0.13) : 0.06}
                stroke="none"
                style={{ transition: "opacity 120ms ease" }}
              />
            );
          })}

          {layout.columns.map((column, layer) =>
            column.nodes.map((node) => {
              const key = nodeKey(layer, node.name);
              const connected = !highlighted || highlighted.has(key);
              const x = PAD.left + X_OFFSET + node.x;
              const y = PAD.top + node.y;
              const last = layer === layout.columns.length - 1;
              // 末列标签向左排，右端贴住节点；预算取 legacy 的 last-column 半宽，
              // 这样长模型名只会在左侧被省略，不会越过卡片右边缘被裁掉。
              const budget = sankeyLabelBudget({
                step: layout.columns.length > 1 ? layout.columns[1].x - layout.columns[0].x : layoutWidth,
                nodeWidth: 14,
                last,
              });
              return (
                <g key={key} opacity={connected ? 1 : 0.32} style={{ transition: "opacity 120ms ease" }}>
                  <rect
                    x={x}
                    y={y}
                    width={14}
                    height={Math.max(1, node.height)}
                    rx={2}
                    fill={layer === colorLayer ? color(node.name) : "var(--neutral)"}
                    style={{ cursor: onNodeClick ? "pointer" : "default" }}
                    onPointerEnter={(event) => handleNodeEnter(event, layer, node)}
                    onPointerLeave={() => { setActive(null); setHover(null); }}
                    onClick={() => onNodeClick?.({ layer, name: node.name, value: node.value })}
                    role={onNodeClick ? "button" : undefined}
                    tabIndex={onNodeClick ? 0 : undefined}
                    aria-label={onNodeClick ? `查看${layerLabels[layer] ?? `第 ${layer + 1} 层`}：${node.name}` : undefined}
                    onKeyDown={(event) => {
                      if (!onNodeClick || (event.key !== "Enter" && event.key !== " ")) return;
                      event.preventDefault();
                      onNodeClick({ layer, name: node.name, value: node.value });
                    }}
                  />
                  <text
                    x={last ? x - 5 : x + 14 + 5}
                    y={y + Math.min(node.height, 11) / 2 + 3.5}
                    textAnchor={last ? "end" : "start"}
                    className="fill-ink-2"
                    style={{ fontSize: 10, paintOrder: "stroke", stroke: "var(--panel)", strokeWidth: 3, strokeLinejoin: "round" }}
                  >
                    {fitLabel(node.name, budget, 10)}
                  </text>
                </g>
              );
            }),
          )}
        </svg>

        {hover ? (
          <ChartTooltip open x={hover.x} y={hover.y} width={width} title={hover.title} rows={[{ label: metric === "tokens" ? "Token" : "请求", value: hover.value }]} />
        ) : null}
      </div>
    </ChartFrame>
  );
}

function shiftEdge(edge: SankeyEdge, dx: number, dy: number): SankeyEdge {
  return {
    ...edge,
    source: { x: edge.source.x + dx, y: edge.source.y + dy },
    target: { x: edge.target.x + dx, y: edge.target.y + dy },
  };
}

function FlowTable({ links, metric, format, layerLabels }: { links: WeightedLink[]; metric: "requests" | "tokens"; format: (value: number) => string; layerLabels: string[] }) {
  const rows = [...links].sort((a, b) => b.weight - a.weight).slice(0, 100);
  return (
    <ChartTable
      caption="流向数据表"
      head={
        <tr>
          <th className="px-2 py-1.5 font-medium">来源</th>
          <th className="px-2 py-1.5 font-medium">目标</th>
          <th className="whitespace-nowrap px-1 py-1.5 text-right font-medium">{metric === "tokens" ? "Token" : "请求"}</th>
        </tr>
      }
    >
      {rows.map((link, index) => (
        <tr key={`${link.source_layer}-${link.target_layer}-${link.source}-${link.target}-${index}`} className="border-t border-rule">
          <td className="px-2 py-1 text-ink-2">
            <span className="text-[10px] text-ink-3">{layerLabels[Number(link.source_layer)] ?? `第 ${Number(link.source_layer) + 1} 层`} · </span>
            {link.source}
          </td>
          <td className="px-2 py-1 text-ink-2">
            <span className="text-[10px] text-ink-3">{layerLabels[Number(link.target_layer)] ?? `第 ${Number(link.target_layer) + 1} 层`} · </span>
            {link.target}
          </td>
          <td className="whitespace-nowrap px-1 py-1 text-right tabular-nums">{format(metric === "tokens" ? Number(link.total_tokens || 0) : Number(link.requests || 0))}</td>
        </tr>
      ))}
    </ChartTable>
  );
}
