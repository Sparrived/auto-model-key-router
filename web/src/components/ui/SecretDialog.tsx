import {
  createContext, useCallback, useContext, useRef, useState, type ReactNode,
} from "react";
import { Copy } from "lucide-react";
import { toast } from "sonner";
import { copyText } from "@/lib/format";
import { Banner } from "./Banner";
import { Button, IconButton } from "./Button";
import { Checkbox } from "./Checkbox";
import { Dialog } from "./Dialog";

export interface SecretItem {
  label: string;
  value: string;
  hint?: string;
}

export interface SecretRequest {
  title: string;
  warning?: ReactNode;
  items: SecretItem[];
  extra?: ReactNode;
}

export type ShowSecretFn = (request: SecretRequest) => Promise<void>;

interface PendingSecret {
  request: SecretRequest;
  resolve: () => void;
}

const SecretContext = createContext<ShowSecretFn | null>(null);

export function useSecretDialog(): ShowSecretFn {
  const showSecret = useContext(SecretContext);
  if (!showSecret) throw new Error("useSecretDialog 必须在 SecretProvider 内使用");
  return showSecret;
}

// SecretProvider 管「一次性明文」的展示与确认。它只负责把内容摆出来并等用户勾选完成；
// 明文不写进任何持久状态，关闭即随组件卸载消失。
export function SecretProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<PendingSecret | null>(null);
  const currentRef = useRef<PendingSecret | null>(null);
  // 关闭动画期间还要画内容；lastRef 只活到面板卸载，明文不会留在任何持久位置。
  const lastRef = useRef<PendingSecret | null>(null);
  const queue = useRef<PendingSecret[]>([]);
  const [saved, setSaved] = useState(false);

  const show = useCallback((item: PendingSecret | null) => {
    currentRef.current = item;
    if (item) lastRef.current = item;
    setCurrent(item);
    setSaved(false);
  }, []);

  const showSecret = useCallback<ShowSecretFn>((request) => (
    new Promise<void>((resolve) => {
      const item = { request, resolve };
      if (currentRef.current) queue.current.push(item);
      else show(item);
    })
  ), [show]);

  const finish = useCallback((item: PendingSecret) => {
    if (currentRef.current !== item) return;
    item.resolve();
    show(queue.current.shift() ?? null);
  }, [show]);

  // 每换一条请求都要重新确认「已保存」，不能沿用上一条的勾选；show 里统一重置。
  const request = (current ?? lastRef.current)?.request;

  return (
    <SecretContext.Provider value={showSecret}>
      {children}
      <Dialog
        open={current !== null}
        onOpenChange={() => {
          // Esc、遮罩与 X 均已由 preventClose 拦下；这里不接受任何隐式关闭。
        }}
        title={request?.title ?? ""}
        preventClose
        hideClose
        footer={request ? (
          <div className="flex w-full items-center justify-between gap-3">
            <Checkbox
              checked={saved}
              onCheckedChange={setSaved}
              label="我已保存这些凭据"
            />
            <Button
              variant="primary"
              disabled={!saved}
              onClick={() => {
                if (current) finish(current);
              }}
            >
              完成
            </Button>
          </div>
        ) : null}
      >
        {request ? (
          <div className="space-y-4">
            <Banner tone="warn">
              {request.warning ?? "这些内容只显示这一次，关闭后无法再查看。"}
            </Banner>
            <div className="space-y-3">
              {request.items.map((item, index) => (
                <div key={`${item.label}-${index}`}>
                  <div className="text-meta text-ink-3">{item.label}</div>
                  <div className="mt-1 flex items-center gap-2">
                    <code className="min-w-0 flex-1 break-all rounded-chip bg-sunken px-2 py-1.5 text-dense text-ink">
                      {item.value}
                    </code>
                    <IconButton
                      aria-label={`复制${item.label}`}
                      tooltip={null}
                      size="sm"
                      onClick={async () => {
                        try {
                          await copyText(item.value);
                          toast.success("已复制");
                        } catch {
                          toast.error("复制失败");
                        }
                      }}
                    >
                      <Copy size={16} />
                    </IconButton>
                  </div>
                  {item.hint ? <p className="mt-1 text-meta text-ink-3">{item.hint}</p> : null}
                </div>
              ))}
            </div>
            {request.extra ? <div>{request.extra}</div> : null}
          </div>
        ) : null}
      </Dialog>
    </SecretContext.Provider>
  );
}
