import { useState } from "react";
import { Pencil, Play, Power } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import { FAMILIES, TEXT_FAMILY } from "@/lib/endpoint-plans";
import type { ModelSummaryResponse, UnifiedModel } from "@/lib/types";
import { navigate } from "@/app/router";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { CopyChip } from "@/components/ui/CopyChip";
import { Empty } from "@/components/ui/Empty";
import { Panel } from "@/components/ui/Panel";
import { useConfirm } from "@/components/ui/ConfirmProvider";
import { UNIFIED_ID } from "./shared";

interface UnifiedDetailProps {
  unified: UnifiedModel | null;
  models: ModelSummaryResponse[];
  revision: string;
  onEdit: () => void;
  onChanged: () => Promise<unknown> | void;
  onSelectRoute?: (routeId: string) => void;
}

export function UnifiedDetail({ unified, models, revision, onEdit, onChanged, onSelectRoute }: UnifiedDetailProps) {
  const confirm = useConfirm();
  const [disabling, setDisabling] = useState(false);
  const primary = unified?.default?.primary;
  const fallback = (unified?.default as { fallback?: { model?: string; key?: string | null } | null } | undefined)?.fallback;
  const primaryModel = primary?.model || "";
  const fallbackModel = fallback?.model || "";
  const primaryKnown = primaryModel ? models.some((model) => model.id === primaryModel) : true;
  const fallbackKnown = fallbackModel ? models.some((model) => model.id === fallbackModel) : true;
  const effort = models.find((model) => model.id === primaryModel)?.reasoning_effort || "";

  async function disable() {
    const ok = await confirm({
      title: "停用统一模型",
      message: "停用统一模型后，统一入口将不再接管请求。是否继续？",
      confirmLabel: "停用",
      danger: true,
    });
    if (!ok) return;
    setDisabling(true);
    try {
      await api.deleteUnified(revision);
      toast("统一模型已停用。");
      await onChanged();
    } catch (failure) {
      toast.error(errorText(failure));
    } finally {
      setDisabling(false);
    }
  }

  function renderModelLink(modelId: string, known: boolean) {
    if (!modelId) return <span className="font-sans text-ink-3">未配置</span>;
    return (
      <span className="inline-flex flex-wrap items-center gap-1.5">
        {known && onSelectRoute ? (
          <button
            type="button"
            onClick={() => onSelectRoute(modelId)}
            className="break-all font-mono text-ink underline-offset-2 hover:text-accent hover:underline"
          >
            {modelId}
          </button>
        ) : (
          <span className="break-all font-mono text-ink">{modelId}</span>
        )}
        {!known ? <Badge tone="warn">不在模型列表</Badge> : null}
      </span>
    );
  }

  return (
    <div className="space-y-4">
      <Panel
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <span>{UNIFIED_ID}</span>
            <CopyChip text={UNIFIED_ID} title="复制统一模型名" />
          </span>
        }
        description="按入站端点族分派的固定名称：调用方把 model 写成它，AMKR 就按入站路径挑一条计划，再按计划选真实模型与 Key。"
        actions={
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={unified ? "good" : "neutral"} dot>{unified ? "已启用" : "未启用"}</Badge>
            {unified ? (
              <Button
                size="sm"
                variant="ghost"
                leadingIcon={<Play aria-hidden className="size-3.5" />}
                onClick={() => navigate("playground", { model: UNIFIED_ID })}
              >
                试验场试跑
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" leadingIcon={<Pencil aria-hidden className="size-3.5" />} onClick={onEdit}>
              编辑
            </Button>
          </div>
        }
      >
        {unified ? (
          <div className="space-y-4">
            <div className="space-y-2">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="text-dense font-medium text-ink">{TEXT_FAMILY.label}</span>
                  <Badge tone="neutral">默认回落</Badge>
                </div>
                <span className="font-mono text-[11px] text-ink-3">{TEXT_FAMILY.paths}</span>
              </div>
              <div className="grid divide-y divide-rule overflow-hidden rounded-control border border-rule bg-sunken sm:grid-cols-2 sm:divide-x sm:divide-y-0 lg:grid-cols-4">
                <div className="p-2.5">
                  <Summary label="默认模型" value={renderModelLink(primaryModel, primaryKnown)} />
                </div>
                <div className="p-2.5">
                  <Summary label="路由方式" value={primary?.key ? `固定 Key · ${primary.key}` : "自动路由"} />
                </div>
                <div className="p-2.5">
                  <Summary
                    label="回退模型"
                    value={fallbackModel ? (
                      <span className="inline-flex flex-wrap items-center gap-1.5">
                        {renderModelLink(fallbackModel, fallbackKnown)}
                        {fallback?.key ? <span className="text-meta text-ink-3">· {fallback.key}</span> : null}
                      </span>
                    ) : <span className="text-ink-3">未配置</span>}
                  />
                </div>
                <div className="p-2.5">
                  <Summary label="推理强度" value={effort || "默认"} />
                </div>
              </div>
            </div>

            <div className="space-y-1.5">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <h3 className="text-dense font-medium text-ink">专用端点族分派</h3>
                <span className="text-meta text-ink-3">未配置的族会自动回落到上方默认模型</span>
              </div>
              <div className="overflow-x-auto rounded-control border border-rule">
                <table className="w-full min-w-[600px] border-collapse text-dense">
                  <thead>
                    <tr className="border-b border-rule bg-sunken/60 text-left text-meta text-ink-3">
                      <th className="px-2.5 py-2 font-medium">端点族</th>
                      <th className="px-2.5 py-2 font-medium">入站路径</th>
                      <th className="px-2.5 py-2 font-medium">模型</th>
                      <th className="px-2.5 py-2 font-medium">Key</th>
                    </tr>
                  </thead>
                  <tbody>
                    {FAMILIES.map((family) => {
                      const plan = (unified[family.id] || null) as { primary?: { model?: string; key?: string | null } | null } | null;
                      const model = plan?.primary?.model || "";
                      const key = plan?.primary?.key || "";
                      const known = model ? models.some((item) => item.id === model) : true;
                      return (
                        <tr key={family.id} className="border-b border-rule last:border-b-0">
                          <td className={model ? "px-2.5 py-2 font-medium text-ink" : "px-2.5 py-2 text-ink-3"}>{family.label}</td>
                          <td className="px-2.5 py-2 font-mono text-meta text-ink-3">{family.paths}</td>
                          <td className="px-2.5 py-2 text-meta">
                            {model ? renderModelLink(model, known) : <span className="text-ink-3">未配置（回落到默认模型）</span>}
                          </td>
                          <td className="px-2.5 py-2 text-meta text-ink-2">{model ? (key ? `固定 Key ${key}` : "自动路由") : "-"}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>

            <div className="flex flex-wrap items-center justify-between gap-3">
              <p className="text-meta leading-5 text-ink-3">
                unified-model 是统一入口：/v1/embeddings → 嵌入，/v1/audio/speech → 语音合成，以此类推。
              </p>
              <Button
                variant="danger-ghost"
                leadingIcon={<Power aria-hidden className="size-4" />}
                loading={disabling}
                onClick={() => void disable()}
              >
                停用统一模型
              </Button>
            </div>
          </div>
        ) : (
          <Empty
            title="统一模型尚未启用"
            description="启用后，调用方可以把 model 写成 unified-model，由 AMKR 按入站端点族自动分派。"
            action={<Button variant="primary" onClick={onEdit}>启用统一模型</Button>}
          />
        )}
      </Panel>
    </div>
  );
}

function Summary({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-meta text-ink-3">{label}</span>
      <span className="text-dense text-ink">{value}</span>
    </div>
  );
}
