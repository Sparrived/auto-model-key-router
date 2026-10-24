import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { api, ApiError } from "@/lib/api";
import { PROVIDER_PRESETS, BrandIcon } from "@/lib/brand-icons";
import { errorText } from "@/lib/format";
import type { Provider } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { CONCURRENCY_INVALID, CONCURRENCY_SUM_HINT, concurrencyLimit, concurrencyText } from "./concurrency";
import { ROUTE_MODES } from "./route-modes";

interface AddProviderDialogProps {
  open: boolean;
  revision: string;
  onClose: () => void;
  onCreated: (id: string) => void;
  onRefresh: () => Promise<unknown>;
}

// 常见供应商预置：点一下把名称与地址填进表单，用户仍可改。预置只填不提交，
// 所以万一地址不对，用户也看得见（就摆在输入框里），不会静默生效。
export function AddProviderDialog({ open, revision, onClose, onCreated, onRefresh }: AddProviderDialogProps) {
  const [id, setId] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  // 每次重新打开都从空白开始；轮询/重新取数不会走到这里，因此不会覆盖正在填的内容。
  useEffect(() => {
    if (!open) return;
    setId("");
    setBaseUrl("");
    setError(null);
    setSaving(false);
  }, [open]);

  const currentUrl = baseUrl.trim().replace(/\/+$/, "").toLowerCase();
  const submit = async () => {
    const name = id.trim();
    const url = baseUrl.trim();
    if (!name || !url) {
      setError("请填写供应商名称与地址。");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await api.createProvider(revision, name, url);
      await onRefresh();
      toast("供应商已添加。");
      onCreated(name);
      onClose();
    } catch (failure) {
      setError(`添加失败: ${errorText(failure)}`);
      if (failure instanceof ApiError && failure.isConflict) void onRefresh();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="添加供应商"
      description="先选一个常见供应商，或手动填写名称与地址。"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>取消</Button>
          <Button variant="primary" loading={saving} onClick={() => void submit()}>添加供应商</Button>
        </>
      }
    >
      <div className="space-y-4">
        <div>
          <p className="mb-2 text-meta font-medium text-ink-2">常见供应商</p>
          <div role="group" aria-label="常见供应商" className="grid grid-cols-3 gap-2 sm:grid-cols-4">
            {PROVIDER_PRESETS.map((preset) => {
              const active = preset.baseUrl.toLowerCase() === currentUrl;
              return (
                <button
                  key={preset.id}
                  type="button"
                  aria-pressed={active}
                  title={`${preset.label} · ${preset.baseUrl}`}
                  onClick={() => {
                    setId(preset.id);
                    setBaseUrl(preset.baseUrl);
                  }}
                  className={[
                    "flex min-h-16 flex-col items-center justify-center gap-1 rounded-control border px-2 py-2 text-meta transition-colors",
                    active
                      ? "border-ink bg-select text-ink"
                      : "border-rule bg-panel text-ink-2 hover:bg-hover hover:text-ink",
                  ].join(" ")}
                >
                  <BrandIcon brand={preset.brand} size={22} />
                  <span className="max-w-full truncate">{preset.label}</span>
                </button>
              );
            })}
          </div>
        </div>
        <Field label="名称" required>
          <Input value={id} onChange={(event) => setId(event.target.value)} placeholder="例如 openai" />
        </Field>
        <Field label="地址" required hint="上游基准地址，例如 https://api.example.com。">
          <Input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://api.example.com" />
        </Field>
        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Dialog>
  );
}

interface ProviderEditSheetProps {
  provider: Provider;
  revision: string;
  onClose: () => void;
  onSaved: (newId: string) => void;
  onRefresh: () => Promise<unknown>;
}

export function ProviderEditSheet({ provider, revision, onClose, onSaved, onRefresh }: ProviderEditSheetProps) {
  // 草稿只在打开时初始化一次：后续轮询/重新取数不会覆盖用户正在编辑的内容。
  const [name, setName] = useState(provider.id);
  const [baseUrl, setBaseUrl] = useState(provider.base_url);
  const [concurrency, setConcurrency] = useState(concurrencyText(provider.max_concurrency));
  const [routes, setRoutes] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {};
    for (const mode of ROUTE_MODES) initial[mode.id] = provider.routes?.[mode.id] || "";
    return initial;
  });
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [saving, setSaving] = useState(false);

  const dirty = useMemo(() => {
    if (name !== provider.id || baseUrl !== provider.base_url) return true;
    if (concurrency !== concurrencyText(provider.max_concurrency)) return true;
    return ROUTE_MODES.some((mode) => (routes[mode.id] || "") !== (provider.routes?.[mode.id] || ""));
  }, [baseUrl, concurrency, name, provider, routes]);

  const save = async () => {
    const nextName = name.trim();
    const nextUrl = baseUrl.trim();
    if (!nextName || !nextUrl) {
      setError("请填写供应商名称与地址。");
      return;
    }
    // 先校验再动任何东西：非法输入必须**一个字节都不发**，否则用户改完并发上限顺手一存，
    // 整次保存会因为一个数字被服务端拒掉，而同一份表单里的路径改动也跟着一起没落盘。
    const maxConcurrency = concurrencyLimit(concurrency);
    if (maxConcurrency === undefined) {
      setError(CONCURRENCY_INVALID);
      return;
    }
    const nextRoutes: Record<string, string | null> = {};
    let routesChanged = false;
    for (const mode of ROUTE_MODES) {
      const value = (routes[mode.id] || "").trim();
      const original = provider.routes?.[mode.id] || "";
      if (value !== original) routesChanged = true;
      if (value) nextRoutes[mode.id] = value;
      else if (original) nextRoutes[mode.id] = null;
    }
    setSaving(true);
    setError(null);
    setConflict(false);
    try {
      await api.updateProvider(
        revision,
        provider.id,
        nextName,
        nextUrl,
        routesChanged ? (nextRoutes as Record<string, string>) : provider.routes || {},
        maxConcurrency,
      );
      await onRefresh();
      toast("配置已更新。");
      onSaved(nextName);
      onClose();
    } catch (failure) {
      if (failure instanceof ApiError && failure.isConflict) {
        setConflict(true);
        void onRefresh();
      }
      setError(`操作失败: ${errorText(failure)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Sheet
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="编辑供应商"
      subtitle={provider.id}
      width="lg"
      dirty={dirty}
      saving={saving}
      saveLabel="保存"
      onSave={save}
    >
      <div className="space-y-4">
        {conflict ? (
          <Banner tone="warn" title="配置已在别处更新">
            已重新读取最新配置，请核对后再保存。
          </Banner>
        ) : null}
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="供应商名称" required hint="改名称会同时改写模型目标与访问密钥的供应商清单里指向它的引用。">
            <Input value={name} onChange={(event) => setName(event.target.value)} />
          </Field>
          <Field label="供应商地址" required>
            <Input value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} />
          </Field>
          <Field label="并发上限" hint={`并发上限是该供应商全部上游请求的同时在途上限，留空表示不限制。${CONCURRENCY_SUM_HINT}`}>
            <Input
              type="number"
              min="1"
              step="1"
              value={concurrency}
              placeholder="留空不限制"
              onChange={(event) => setConcurrency(event.target.value)}
            />
          </Field>
        </div>

        <details className="rounded-control border border-rule px-3 py-2">
          <summary className="cursor-pointer text-meta font-medium text-ink-2">
            高级路径设置 · 已自定义 {ROUTE_MODES.filter((mode) => provider.routes?.[mode.id]).length} / {ROUTE_MODES.length}
          </summary>
          <p className="mt-2 text-meta leading-5 text-ink-3">
            路径留空即使用该模式的标准路径（占位符里就是它）。填前缀时 AMKR 会把标准路径接在后面：
            填 gateway/embed 会转发到 &lt;供应商地址&gt;/gateway/embed/v1/embeddings；填以标准路径结尾的完整路径则原样使用。
            语音、视频与重排是透传端点：AMKR 只替换请求体里的模型名，其余原样转发，
            因此所选模型必须绑定到确实提供该端点的上游 Key。
          </p>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            {ROUTE_MODES.map((mode) => (
              <Field key={mode.id} label={mode.label}>
                <Input
                  value={routes[mode.id] || ""}
                  placeholder={`留空使用默认：${mode.path}`}
                  title={`${mode.label} 默认路径 ${mode.path}`}
                  onChange={(event) => setRoutes((current) => ({ ...current, [mode.id]: event.target.value }))}
                />
              </Field>
            ))}
          </div>
        </details>

        {error ? <Banner tone="critical">{error}</Banner> : null}
      </div>
    </Sheet>
  );
}
