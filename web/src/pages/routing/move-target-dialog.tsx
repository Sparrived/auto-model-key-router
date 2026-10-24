import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { RouteEntry, RoutesResponse } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Select } from "@/components/ui/Select";
import { targetText } from "./shared";

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
  const destinations = routes.filter((item) => item.id !== route.id);
  const target = route.targets[targetIndex];
  const [destinationId, setDestinationId] = useState(destinations[0]?.id || "");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  if (!target) return null;

  async function move() {
    const destination = destinations.find((item) => item.id === destinationId);
    if (!destination) {
      setError("请选择接收的目标路由。");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.updateRoute(
        revision,
        destination.id,
        [...(destination.targets || []), target],
        destination.aliases || [],
        destination.routing_mode || "round_robin",
      );
      const afterAppend = await loadRoutes();
      const freshSource = afterAppend.routes.find((item) => item.id === route.id);
      const remaining = (freshSource?.targets || route.targets).filter((_, index) => index !== targetIndex);
      const nextRevision = afterAppend.config_revision || revision;
      if (!freshSource) {
        onMoved(destination.id, true);
        return;
      }
      await api.updateRoute(
        nextRevision,
        route.id,
        remaining,
        freshSource.aliases || [],
        freshSource.routing_mode || "round_robin",
      );
      await loadRoutes();
      onMoved(remaining.length === 0 ? destination.id : route.id, remaining.length === 0);
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) void loadRoutes();
      setError(`移动失败: ${errorText(failure)}`);
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
          <Button variant="primary" loading={saving} onClick={() => void move()}>移动</Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="font-mono text-meta text-ink-2">{targetText(target)}</p>
        <Field label="目标路由" required hint="原路由若没有别的目标，会被自动删除。">
          <Select value={destinationId} onChange={(event) => setDestinationId(event.target.value)}>
            {destinations.map((item) => (
              <option key={item.id} value={item.id}>{item.id}（{(item.targets || []).length} 个目标）</option>
            ))}
          </Select>
        </Field>
        {!destinations.length ? <Banner tone="warn">没有其它路由可以接收这个目标。</Banner> : null}
        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Dialog>
  );
}
