import { useState } from "react";
import { Pencil, Power } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import { PLAN_FAMILIES } from "@/lib/endpoint-plans";
import type { ModelSummaryResponse, UnifiedModel } from "@/lib/types";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
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
}

export function UnifiedDetail({ unified, models, revision, onEdit, onChanged }: UnifiedDetailProps) {
  const confirm = useConfirm();
  const [disabling, setDisabling] = useState(false);
  const primary = unified?.default?.primary;
  const fallback = (unified?.default as { fallback?: { model?: string; key?: string | null } | null } | undefined)?.fallback;
  const effort = models.find((model) => model.id === primary?.model)?.reasoning_effort || "";

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

  return (
    <div className="space-y-4">
      <Panel
        title={UNIFIED_ID}
        description="按入站端点族分派的固定名称：调用方把 model 写成它，AMKR 就按入站路径挑一条计划，再按计划选真实模型与 Key。"
        actions={
          <>
            <Badge tone={unified ? "good" : "neutral"} dot>{unified ? "已启用" : "未启用"}</Badge>
            <Button size="sm" variant="secondary" leadingIcon={<Pencil aria-hidden className="size-3.5" />} onClick={onEdit}>
              编辑
            </Button>
          </>
        }
      >
        {unified ? (
          <div className="space-y-5">
            <div className="grid gap-x-6 gap-y-2 sm:grid-cols-2 lg:grid-cols-4">
              <Summary label="默认模型" value={primary?.model || "未配置"} mono />
              <Summary label="路由方式" value={primary?.key ? `固定 Key · ${primary.key}` : "自动路由"} />
              <Summary label="回退模型" value={fallback?.model ? `${fallback.model}${fallback.key ? ` · ${fallback.key}` : ""}` : "未配置"} mono />
              <Summary label="推理强度" value={effort || "默认"} />
            </div>

            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] border-collapse text-dense">
                <thead>
                  <tr className="border-b border-rule text-left text-meta text-ink-3">
                    <th className="px-2 py-2 font-medium">端点族</th>
                    <th className="px-2 py-2 font-medium">入站路径</th>
                    <th className="px-2 py-2 font-medium">模型</th>
                    <th className="px-2 py-2 font-medium">Key</th>
                  </tr>
                </thead>
                <tbody>
                  {PLAN_FAMILIES.map((family) => {
                    const plan = (unified[family.id] || null) as { primary?: { model?: string; key?: string | null } | null } | null;
                    const model = plan?.primary?.model || "";
                    const key = plan?.primary?.key || "";
                    return (
                      <tr key={family.id} className="border-b border-rule last:border-b-0">
                        <td className={model ? "px-2 py-2 text-ink" : "px-2 py-2 text-ink-3"}>{family.label}</td>
                        <td className="px-2 py-2 font-mono text-meta text-ink-3">{family.paths}</td>
                        <td className="px-2 py-2 font-mono text-meta">
                          {model || <span className="font-sans text-ink-3">未配置（回落到默认模型）</span>}
                        </td>
                        <td className="px-2 py-2 text-meta text-ink-2">{model ? (key ? `固定 Key ${key}` : "自动路由") : "-"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>

            <div className="flex items-center justify-between gap-3">
              <p className="text-meta leading-5 text-ink-3">
                unified-model 是统一入口：/v1/embeddings → 嵌入，/v1/audio/speech → 语音合成，以此类推。
                没有配置的族会回落到文本（对话）族的默认模型。
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

function Summary({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-meta text-ink-3">{label}</span>
      <span className={mono ? "break-all font-mono text-dense text-ink" : "text-dense text-ink"}>{value}</span>
    </div>
  );
}
