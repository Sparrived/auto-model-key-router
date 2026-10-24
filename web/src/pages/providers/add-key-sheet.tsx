import { useState } from "react";
import { Check, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import type { Provider, ProviderKey, ProvidersResponse } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { CONCURRENCY_INVALID, CONCURRENCY_SUM_HINT, concurrencyLimit } from "./concurrency";
import { KeyModelBindingBody, useKeyModelBinding } from "./model-binding";

type Step = "form" | "saving" | "probing" | "bind";

interface AddKeySheetProps {
  provider: Provider;
  revision: string;
  onClose: () => void;
  onSaved: () => void;
  loadProviders: () => Promise<ProvidersResponse>;
}

// 添加 Key 是一条三步向导：填表 → 保存并自动探测 → 直接进入「选择服务模型」。
// 探测失败不回滚 Key：它已经保存成功了，把用户挡在模型选择之外只会让人以为整件事失败。
export function AddKeySheet({ provider, revision, onClose, onSaved, loadProviders }: AddKeySheetProps) {
  const [step, setStep] = useState<Step>("form");
  const [name, setName] = useState("");
  const [secret, setSecret] = useState("");
  const [concurrency, setConcurrency] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [probeError, setProbeError] = useState<string | null>(null);
  const [currentRevision, setCurrentRevision] = useState(revision);
  const [createdKey, setCreatedKey] = useState<ProviderKey | null>(null);

  const binding = useKeyModelBinding({
    providerId: provider.id,
    keyName: createdKey?.name || "",
    revision: currentRevision,
    discovered: createdKey?.capabilities?.models || [],
    enabled: step === "bind",
    onSaved,
  });

  const submit = async () => {
    const keyName = name.trim();
    if (!keyName || !secret) {
      setError("请填写 Key 名称与 API Key。");
      return;
    }
    const maxConcurrency = concurrencyLimit(concurrency);
    if (maxConcurrency === undefined) {
      setError(CONCURRENCY_INVALID);
      return;
    }
    setError(null);
    setProbeError(null);
    setStep("saving");
    let nextRevision = currentRevision;
    try {
      await api.createProviderKey(nextRevision, provider.id, keyName, secret, maxConcurrency);
      const data = await loadProviders();
      nextRevision = data.config_revision || nextRevision;
      setCurrentRevision(nextRevision);
    } catch (failure) {
      setStep("form");
      setError(`Key 保存失败。${errorText(failure)}`);
      return;
    }

    setStep("probing");
    try {
      const result = await api.probeKey(nextRevision, provider.id, keyName);
      const withRevision = result as { config_revision?: string };
      nextRevision = withRevision.config_revision || nextRevision;
      const data = await loadProviders();
      setCurrentRevision(data.config_revision || nextRevision);
      const fresh = data.providers.find((item) => item.id === provider.id)?.keys.find((item) => item.name === keyName);
      setCreatedKey(fresh || { name: keyName, enabled: true });
      setStep("bind");
    } catch (failure) {
      // 保存成功但探测失败：保持表单可编辑，直接进入模型选择，用户可以手动添加。
      const data = await loadProviders().catch(() => null);
      if (data?.config_revision) setCurrentRevision(data.config_revision);
      const fresh = data?.providers.find((item) => item.id === provider.id)?.keys.find((item) => item.name === keyName);
      setCreatedKey(fresh || { name: keyName, enabled: true });
      setProbeError(`Key 已保存，但自动探测失败: ${errorText(failure)}`);
      setStep("bind");
    }
  };

  const changed = binding.added > 0 || binding.removed > 0;
  const formDirty = step === "form" && Boolean(name || secret || concurrency);

  const footer = step === "bind" ? (
    <div className="flex w-full items-center justify-between gap-3">
      <span className="text-meta text-ink-2">新增 {binding.added} · 移除 {binding.removed}</span>
      <div className="flex items-center gap-2">
        <Button variant="ghost" onClick={onClose}>稍后设置</Button>
        <Button
          variant="primary"
          loading={binding.saving}
          disabled={!changed}
          onClick={() => {
            void binding.save().then((ok) => {
              if (!ok) return;
              toast(`Key ${createdKey?.name || name} 已添加。`);
              onSaved();
              onClose();
            });
          }}
        >
          保存模型
        </Button>
      </div>
    </div>
  ) : (
    <>
      <Button variant="secondary" disabled={step !== "form"} onClick={onClose}>取消</Button>
      <Button variant="primary" loading={step === "saving" || step === "probing"} onClick={() => void submit()}>
        {step === "form" ? "添加 Key" : step === "saving" ? "正在保存" : "正在探测"}
      </Button>
    </>
  );

  return (
    <Sheet
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title="添加 Key"
      subtitle={`供应商 ${provider.id} · 保存后自动探测可用模型`}
      width="lg"
      dirty={formDirty || (step === "bind" && changed)}
      onSave={step === "form" ? submit : undefined}
      saveLabel="添加 Key"
      saving={step === "saving" || step === "probing"}
      footer={footer}
    >
      <div className="space-y-4">
        <ol className="flex flex-wrap items-center gap-3 text-meta">
          <StepMark index={1} done={step !== "form"} active={step === "form"} label="填写 Key" />
          <StepMark index={2} done={step === "bind"} active={step === "saving" || step === "probing"} label="保存并探测" />
          <StepMark index={3} done={false} active={step === "bind"} label="选择服务模型" />
        </ol>

        {step === "form" || step === "saving" ? (
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Key 名称" required hint="同一供应商内唯一，例如 primary。">
                <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如 primary" />
              </Field>
              <Field label="API Key" required hint="密钥只写入服务端，不会被回显。">
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                />
              </Field>
              <Field
                label="并发上限"
                hint={`留空表示不限制。${CONCURRENCY_SUM_HINT}`}
              >
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
            <p className="text-meta text-ink-3">保存后会自动探测此 Key 的可用模型与路由能力。</p>
            {error ? <Banner tone="critical">{error}</Banner> : null}
          </>
        ) : null}

        {step === "saving" || step === "probing" ? (
          <div className="flex items-center gap-2 rounded-control bg-sunken px-3 py-3 text-body text-ink-2">
            <Loader2 aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
            {step === "saving" ? "正在保存 Key…" : "正在探测可用模型…"}
          </div>
        ) : null}

        {step === "bind" ? (
          <>
            <div className="flex items-center gap-2 rounded-control bg-good-bg px-3 py-2 text-meta text-good-ink">
              <Check aria-hidden className="size-4" />
              Key {createdKey?.name || name} 已保存；探测{probeError ? "失败，可手动添加模型" : "完成"}
            </div>
            {probeError ? <Banner tone="warn">{probeError}</Banner> : null}
            <KeyModelBindingBody binding={binding} />
          </>
        ) : null}
      </div>
    </Sheet>
  );
}

function StepMark({ index, label, done, active }: { index: number; label: string; done: boolean; active: boolean }) {
  return (
    <li className={active ? "flex items-center gap-1.5 text-ink" : "flex items-center gap-1.5 text-ink-3"}>
      <span
        aria-hidden
        className={[
          "grid size-5 place-items-center rounded-pill border text-[11px]",
          done ? "border-good bg-good-bg text-good-ink" : active ? "border-ink text-ink" : "border-rule text-ink-3",
        ].join(" ")}
      >
        {done ? <Check className="size-3" /> : index}
      </span>
      {label}
    </li>
  );
}
