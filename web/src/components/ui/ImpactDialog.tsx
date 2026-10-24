import {
  createContext, useCallback, useContext, useRef, useState, type ReactNode,
} from "react";
import { impactSections, type ImpactConfirmArgs } from "@/lib/model-impact";
import { Button } from "./Button";
import { Dialog } from "./Dialog";

// 与 legacy model-impact.js 的 DEFAULT_IMPACT_INTRO 逐字一致：确认框的措辞不因重写而变。
const DEFAULT_IMPACT_INTRO = "以下内容会一并变动：";

export type ImpactConfirmFn = (args: ImpactConfirmArgs) => Promise<boolean>;

interface PendingImpact {
  args: ImpactConfirmArgs;
  resolve: (value: boolean) => void;
}

const ImpactContext = createContext<ImpactConfirmFn | null>(null);

export function useImpactConfirm(): ImpactConfirmFn {
  const confirm = useContext(ImpactContext);
  if (!confirm) throw new Error("useImpactConfirm 必须在 ImpactProvider 内使用");
  return confirm;
}

// ImpactProvider 负责「预演结果 → 分节清单 → 用户点头」这一步；它不关心预演与真写怎么调，
// 那部分在 lib/model-impact.ts 的 writeWithImpactConfirm 里。
export function ImpactProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<PendingImpact | null>(null);
  const currentRef = useRef<PendingImpact | null>(null);
  // 关闭动画期间仍要画出内容，否则面板会在淡出前先变成空白。
  const lastRef = useRef<PendingImpact | null>(null);
  const queue = useRef<PendingImpact[]>([]);

  const show = useCallback((item: PendingImpact | null) => {
    currentRef.current = item;
    if (item) lastRef.current = item;
    setCurrent(item);
  }, []);

  const confirm = useCallback<ImpactConfirmFn>((args) => (
    new Promise<boolean>((resolve) => {
      const item = { args, resolve };
      if (currentRef.current) queue.current.push(item);
      else show(item);
    })
  ), [show]);

  const finish = useCallback((item: PendingImpact, confirmed: boolean) => {
    if (currentRef.current !== item) return;
    item.resolve(confirmed);
    show(queue.current.shift() ?? null);
  }, [show]);

  const view = current ?? lastRef.current;
  const args = view?.args;
  const sections = args?.impacted && args.impact ? impactSections(args.impact) : [];

  return (
    <ImpactContext.Provider value={confirm}>
      {children}
      <Dialog
        open={current !== null}
        onOpenChange={(next) => {
          if (!next && current) finish(current, false);
        }}
        title={args?.title ?? ""}
        footer={view ? (
          <>
            <Button
              variant="secondary"
              onClick={() => {
                if (current) finish(current, false);
              }}
            >
              取消
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                if (current) finish(current, true);
              }}
            >
              {view.args.confirmLabel}
            </Button>
          </>
        ) : null}
      >
        {args ? (
          <div className="space-y-3">
            <p className="text-body text-ink-2">{args.message}</p>
            {sections.length > 0 ? (
              <p className="text-meta text-ink-3">{args.impactIntro || DEFAULT_IMPACT_INTRO}</p>
            ) : null}
            {sections.map((section, index) => (
              <section key={`${section.title}-${index}`}>
                <h3 className="text-meta font-medium text-ink-2">{section.title}</h3>
                <ul className="mt-1 list-disc space-y-1 pl-5 text-dense text-ink-2">
                  {section.lines.map((line, lineIndex) => (
                    <li key={lineIndex}>{line}</li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
        ) : null}
      </Dialog>
    </ImpactContext.Provider>
  );
}
