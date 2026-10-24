import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { Button } from "./Button";
import { Dialog } from "./Dialog";

export interface ConfirmOptions {
  title: ReactNode;
  message: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
}

export type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>;

interface PendingConfirm {
  options: ConfirmOptions;
  resolve: (value: boolean) => void;
}

const ConfirmContext = createContext<ConfirmFn | null>(null);

export function useConfirm(): ConfirmFn {
  const confirm = useContext(ConfirmContext);
  if (!confirm) throw new Error("useConfirm 必须在 ConfirmProvider 内使用");
  return confirm;
}

// ConfirmProvider 把浏览器 confirm() 换成设计系统里的对话框，并保证「确认/取消/关闭」
// 三个出口都会把 Promise 结掉——否则调用方会永远停在 await 上。
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<PendingConfirm | null>(null);
  const currentRef = useRef<PendingConfirm | null>(null);
  // 关闭动画期间 open 已为 false，但面板还要再显示 120ms；留住上一条内容，避免淡出时文字先消失。
  const lastRef = useRef<PendingConfirm | null>(null);
  const queue = useRef<PendingConfirm[]>([]);

  const show = useCallback((item: PendingConfirm | null) => {
    currentRef.current = item;
    if (item) lastRef.current = item;
    setCurrent(item);
  }, []);

  const confirm = useCallback<ConfirmFn>((options) => (
    new Promise<boolean>((resolve) => {
      const item = { options, resolve };
      // 一次只展示一个：后来的排队，前一个结掉后立刻接上，调用方无需关心顺序。
      if (currentRef.current) queue.current.push(item);
      else show(item);
    })
  ), [show]);

  const finish = useCallback((item: PendingConfirm, confirmed: boolean) => {
    // 双击确认按钮时第二次回调可能落在下一条上，用对象身份确认「还在结这一条」。
    if (currentRef.current !== item) return;
    item.resolve(confirmed);
    show(queue.current.shift() ?? null);
  }, [show]);

  const view = current ?? lastRef.current;

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog
        open={current !== null}
        onOpenChange={(next) => {
          if (!next && current) finish(current, false);
        }}
        title={view?.options.title ?? ""}
        footer={view ? (
          <>
            <Button
              variant="secondary"
              onClick={() => {
                if (current) finish(current, false);
              }}
            >
              {view.options.cancelLabel ?? "取消"}
            </Button>
            <Button
              variant={view.options.danger ? "danger" : "primary"}
              onClick={() => {
                if (current) finish(current, true);
              }}
            >
              {view.options.confirmLabel ?? "确认"}
            </Button>
          </>
        ) : null}
      >
        {view ? <div className="text-body text-ink-2">{view.options.message}</div> : null}
      </Dialog>
    </ConfirmContext.Provider>
  );
}
