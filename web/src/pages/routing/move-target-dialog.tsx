import { useMemo, useState } from "react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import { writeWithImpactConfirm, type Impact } from "@/lib/model-impact";
import type { RouteEntry, RoutesResponse } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Combobox, type ComboboxOption } from "@/components/ui/Combobox";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { useImpactConfirm } from "@/components/ui/ImpactDialog";
import { normalizeMode, targetIdentity, targetText } from "./shared";

interface MoveTargetDialogProps {
  route: RouteEntry;
  targetIndex: number;
  routes: RouteEntry[];
  revision: string;
  loadRoutes: () => Promise<RoutesResponse>;
  onClose: () => void;
  onMoved: (destinationId: string, sourceDeleted: boolean) => void;
}

// 移动目标的两步写必须**先接收方、后来源**：中间断掉只会留下一条可手动删掉的重复目标，
// 反过来则会把目标直接弄丢。两次调用都不带 endpoints——搬的是目标，不是端点计划。
export function MoveTargetDialog({ route, targetIndex, routes, revision, loadRoutes, onClose, onMoved }: MoveTargetDialogProps) {
  const impactConfirm = useImpactConfirm();
  const destinations = useMemo(() => routes.filter((item) => item.id !== route.id), [route.id, routes]);
  const target = route.targets[targetIndex];
  const [destinationId, setDestinationId] = useState(destinations[0]?.id || "");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const destinationOptions = useMemo<ComboboxOption[]>(() => (
    destinations.map((item) => ({
      value: item.id,
      label: item.id,
      note: `${(item.targets || []).length} 个目标`,
      keywords: [item.id, ...(item.aliases || [])],
    }))
  ), [destinations]);

  if (!target) return null;

  const effectiveDestinationId = destinations.some((item) => item.id === destinationId)
    ? destinationId
    : (destinations[0]?.id || "");
  const destination = destinations.find((item) => item.id === effectiveDestinationId) || null;
  const effectiveUpstream = String(target.upstream_model || "").trim() || route.id;
  const movingTarget = { ...target, upstream_model: effectiveUpstream };
  const isDuplicate = Boolean(
    destination?.targets?.some((item) =>
      targetIdentity(item, destination.id) === targetIdentity(movingTarget, destination.id)),
  );
  const isLastTarget = (route.targets || []).length === 1;

  async function executeMove(dest: RouteEntry) {
    let appended = false;
    try {
      await api.updateRoute(
        revision,
        dest.id,
        [...(dest.targets || []), movingTarget],
        dest.aliases || [],
        normalizeMode(dest.routing_mode),
      );
      appended = true;
      const afterAppend = await loadRoutes();
      const freshSource = afterAppend.routes.find((item) => item.id === route.id);
      const remaining = (freshSource?.targets || route.targets).filter((_, index) => index !== targetIndex);
      const nextRevision = afterAppend.config_revision || revision;
      if (!freshSource) {
        toast.success(`已将目标移动到 ${dest.id}。`);
        onMoved(dest.id, true);
        return;
      }
      await api.updateRoute(
        nextRevision,
        route.id,
        remaining,
        freshSource.aliases || [],
        normalizeMode(freshSource.routing_mode),
      );
      await loadRoutes();
      toast.success(
        remaining.length === 0
          ? `已将目标移动到 ${dest.id}，原路由 ${route.id} 已删除。`
          : `已将目标移动到 ${dest.id}。`,
      );
      onMoved(remaining.length === 0 ? dest.id : route.id, remaining.length === 0);
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) void loadRoutes();
      if (appended) {
        void loadRoutes();
        setError(`目标已添加到 ${dest.id}，但从原路由 ${route.id} 移除失败：${errorText(failure)}。请检查并手动清理重复目标。`);
      } else {
        setError(`移动失败: ${errorText(failure)}`);
      }
    }
  }

  async function move() {
    if (!destination) {
      setError("请选择接收的目标路由。");
      return;
    }
    if (isDuplicate) {
      setError(`目标路由 ${destination.id} 已经包含相同的目标。`);
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (isLastTarget) {
        // 移走最后一条目标等于删除原路由：先预演连带变动并确认，避免半途才发现被引用或误删。
        const { confirmed } = await writeWithImpactConfirm({
          title: "移动并删除原路由",
          message: `这是路由 ${route.id} 的最后一条目标，移到 ${destination.id} 后原路由会被删除。`,
          confirmLabel: "移动并删除原路由",
          alwaysConfirm: true,
          preview: async () => {
            const raw = (await api.deleteRoute(revision, route.id, { dryRun: true })) as unknown as Impact;
            // 这把 Key 会先搬到 destination，不会因为删原路由而成为孤立 Key。
            return {
              ...raw,
              removed_keys: raw?.removed_keys?.filter(
                (item) => !(item.provider === movingTarget.provider && item.key === movingTarget.key),
              ),
            };
          },
          commit: () => executeMove(destination),
          confirm: impactConfirm,
        });
        if (!confirmed) return;
      } else {
        await executeMove(destination);
      }
    } catch (failure) {
      setError(`预演移动影响失败: ${errorText(failure)}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="移动到其它路由"
      description="上游模型名保持不变。"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>取消</Button>
          <Button
            variant="primary"
            disabled={!destination || isDuplicate}
            loading={saving}
            onClick={() => void move()}
          >
            移动
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="rounded-control border border-rule bg-sunken px-2.5 py-2 font-mono text-meta text-ink-2">
          {targetText(movingTarget)}
        </p>
        <Field label="目标路由" required hint="可按路由名或别名搜索接收方。">
          <Combobox
            options={destinationOptions}
            value={effectiveDestinationId}
            placeholder={destinations.length ? "选择目标路由" : "没有其它可用路由"}
            disabled={!destinations.length || saving}
            aria-label="目标路由"
            onValueChange={(next) => {
              setDestinationId(String(next));
              setError(null);
            }}
          />
        </Field>
        {isLastTarget && destinations.length ? (
          <Banner tone="warn">这是路由 {route.id} 的最后一条目标，移走后原路由会被自动删除，提交前会先预演连带影响。</Banner>
        ) : null}
        {isDuplicate && destination ? (
          <Banner tone="warn">目标路由 {destination.id} 已经包含相同的目标，无法重复移动。</Banner>
        ) : null}
        {!destinations.length ? <Banner tone="warn">没有其它路由可以接收这个目标。</Banner> : null}
        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Dialog>
  );
}
