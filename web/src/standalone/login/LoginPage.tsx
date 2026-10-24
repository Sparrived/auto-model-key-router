import { useEffect, useRef, useState, type FormEvent } from "react";
import { Eye, EyeOff, KeyRound, RefreshCw } from "lucide-react";
import { ApiError, api, apiBase, setKey } from "@/lib/api";
import { errorText } from "@/lib/format";
import { Banner } from "@/components/ui/Banner";
import { Button, IconButton } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { currentNext } from "./safe-next";

const HEALTH_INTERVAL = 5000;
const KEY_HINT = "可在终端执行 amkr --show-api-key 获取本地鉴权 Key。";
const KEY_INVALID = "本地鉴权 Key 无效，请重新核对后重试。可在终端执行 amkr --show-api-key 获取。";
const KEY_EXPIRED = "本地鉴权 Key 已失效，请重新输入。";

export interface LoginPageProps {
  /** 测试可注入的跳转函数；生产环境用 location.replace。 */
  navigate?: (url: string) => void;
  /** 测试可注入的首页地址；生产环境按部署路径推导。 */
  homeUrl?: string;
  /** 测试可缩短；0 表示不启动轮询。 */
  healthIntervalMs?: number;
}

function defaultHome(): string {
  return import.meta.env.DEV ? "/" : `${apiBase()}/ui/`;
}

function defaultNavigate(url: string): void {
  window.location.replace(url);
}

export function LoginPage({
  navigate = defaultNavigate,
  homeUrl = defaultHome(),
  healthIntervalMs = HEALTH_INTERVAL,
}: LoginPageProps) {
  const params = new URLSearchParams(window.location.search || "");
  const expired = params.get("reason") === "expired";
  const [value, setValue] = useState("");
  const [visible, setVisible] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(expired ? KEY_EXPIRED : null);
  const [health, setHealth] = useState<{ version?: string; base_url?: string; local_auth_enabled?: boolean } | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);
  const [healthChecking, setHealthChecking] = useState(true);
  const redirected = useRef(false);

  async function refreshHealth(): Promise<void> {
    setHealthChecking(true);
    try {
      const next = await api.health();
      setHealth(next);
      setHealthError(null);
      if (!next.local_auth_enabled && !redirected.current) {
        redirected.current = true;
        navigate(homeUrl);
      }
    } catch (failure) {
      setHealth(null);
      setHealthError(errorText(failure));
    } finally {
      setHealthChecking(false);
    }
  }

  useEffect(() => {
    let alive = true;
    async function tick() {
      if (!alive) return;
      await refreshHealth();
    }
    void tick();
    if (healthIntervalMs <= 0) return () => { alive = false; };
    const timer = window.setInterval(() => { void tick(); }, healthIntervalMs);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
    // refreshHealth 只依赖稳定的 setter/navigate/homeUrl；这个 effect 只在挂载或轮询间隔变化时重跑。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [healthIntervalMs, homeUrl]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const key = value.trim();
    if (!key) {
      setNotice("请先填写本地鉴权 Key。");
      return;
    }
    setNotice(null);
    setHealthError(null);
    setBusy(true);
    setKey(key);
    try {
      await api.settings();
      const next = currentNext(window.location.search) || homeUrl;
      navigate(next);
    } catch (failure) {
      if (failure instanceof ApiError && failure.isUnauthorized) {
        // 未经服务端验证的 Key 不能留在本机；输入框保留，方便用户直接改。
        setKey("");
        setNotice(KEY_INVALID);
      } else {
        // 不可达不是 Key 错误：保留刚输入的 Key，服务恢复后可直接重试。
        setHealthError(errorText(failure));
        setNotice(`无法连接 AMKR 服务：${errorText(failure)}`);
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-paper px-4 py-10">
      <div className="w-full max-w-[420px]">
        <div className="mb-6 flex items-center gap-3">
          <span aria-hidden className="grid size-10 place-items-center rounded-panel bg-accent text-on-accent">
            <KeyRound className="size-5" />
          </span>
          <div>
            <div className="text-page font-semibold tracking-tight text-ink">AMKR</div>
            <div className="text-meta text-ink-3">Auto Model Key Router</div>
          </div>
        </div>

        <section className="rounded-panel border border-rule bg-panel p-5" aria-labelledby="login-title">
          <h1 id="login-title" className="text-page font-semibold text-ink">连接到 AMKR</h1>
          <p className="mt-1 text-dense text-ink-3">管理接口需要本地鉴权 Key。验证通过后进入控制台。</p>

          {notice ? <Banner tone={expired && notice === KEY_EXPIRED ? "warn" : "critical"} className="mt-4">{notice}</Banner> : null}
          {healthError && notice !== `无法连接 AMKR 服务：${healthError}` ? (
            <Banner
              tone="critical"
              className="mt-4"
              title="服务暂未连接"
              action={<Button size="sm" variant="secondary" leadingIcon={<RefreshCw aria-hidden className="size-3.5" />} onClick={() => void refreshHealth()}>重试连接</Button>}
            >
              无法连接 AMKR 服务：{healthError}
            </Banner>
          ) : null}
          {healthChecking && !health && !healthError ? (
            <p className="mt-4 text-meta text-ink-3" role="status">正在检查服务连接…</p>
          ) : null}

          <form className="mt-5 space-y-4" onSubmit={submit}>
            <Field label="本地鉴权 Key" hint={KEY_HINT}>
              <div className="relative">
                <Input
                  type={visible ? "text" : "password"}
                  autoFocus
                  autoComplete="off"
                  aria-label="本地鉴权 Key"
                  placeholder="粘贴本地鉴权 Key"
                  value={value}
                  onChange={(event) => setValue(event.target.value)}
                  className="pr-10"
                />
                <IconButton
                  aria-label={visible ? "隐藏 Key" : "显示 Key"}
                  tooltip={null}
                  size="sm"
                  className="absolute right-0.5 top-1/2 -translate-y-1/2"
                  onClick={() => setVisible((current) => !current)}
                >
                  {visible ? <EyeOff aria-hidden className="size-4" /> : <Eye aria-hidden className="size-4" />}
                </IconButton>
              </div>
            </Field>
            <Button type="submit" variant="primary" className="w-full" loading={busy}>
              {busy ? "连接中…" : "连接"}
            </Button>
          </form>
        </section>

        <p className="mt-4 text-center text-meta text-ink-3">
          {[health?.version ? `版本 v${health.version}` : null, health?.base_url || null].filter(Boolean).join(" · ") || "AMKR WebUI"}
        </p>
      </div>
    </main>
  );
}
