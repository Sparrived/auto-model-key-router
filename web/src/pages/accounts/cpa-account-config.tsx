import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { errorText } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import type { CPAAccount, CPAAccountInstance } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { Sheet } from "@/components/ui/Sheet";
import { Textarea } from "@/components/ui/Textarea";

interface Props {
  instance: CPAAccountInstance;
  account: CPAAccount;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function CPAAccountConfigSheet({ instance, account, open, onOpenChange }: Props) {
  const client = useQueryClient();
  const initial = { label: account.label ?? "", note: String(account.note ?? "") };
  const [draft, setDraft] = useState(initial);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const authIndex = String(account.auth_index ?? "");
  const dirty = draft.label !== initial.label || draft.note !== initial.note;

  async function save() {
    if (saving || !identifiable) return;
    const fields: { label?: string; note?: string } = {};
    if (draft.label !== initial.label) fields.label = draft.label.trim();
    if (draft.note !== initial.note) fields.note = draft.note.trim();
    if (!Object.keys(fields).length) {
      onOpenChange(false);
      return;
    }
    setSaving(true);
    setSaveError("");
    try {
      await api.updateCPAAccountFields(instance.id, authIndex, account.name || "", fields);
      toast.success("CPA 账号信息已保存。");
      await client.invalidateQueries({ queryKey: queryKeys.cpaAccounts() });
      onOpenChange(false);
    } catch (error) {
      setSaveError(errorText(error));
    } finally {
      setSaving(false);
    }
  }

  const identifiable = Boolean(authIndex && account.name);
  return (
    <Sheet
      open={open}
      onOpenChange={onOpenChange}
      title={(account.label || account.name || authIndex) + " · 账号配置"}
      subtitle={(instance.label || instance.id) + " · 仅修改 CPA 凭据的显示名称与备注，不读取或回传 token、自定义请求头等敏感字段。"}
      width="md"
      dirty={dirty}
      onSave={identifiable ? save : undefined}
      saveLabel="保存到 CPA"
      saving={saving}
    >
      <div className="space-y-4">
        {!identifiable ? <Banner tone="critical">账号缺少 name 或 auth_index，无法安全定位到 CPA 凭据。</Banner> : null}
        <Field label="显示名称" hint="写入 CPA 凭据的 label；留空可清除自定义名称。">
          <Input
            value={draft.label}
            disabled={saving || !identifiable}
            onChange={(event) => setDraft((current) => ({ ...current, label: event.target.value }))}
          />
        </Field>
        <Field label="备注" hint="写入 CPA 凭据的 note，用于记录用途、归属或运维说明。">
          <Textarea
            className="min-h-28"
            value={draft.note}
            disabled={saving || !identifiable}
            onChange={(event) => setDraft((current) => ({ ...current, note: event.target.value }))}
          />
        </Field>
        {saveError ? <Banner tone="critical" title="保存 CPA 账号信息失败">{saveError}</Banner> : null}
      </div>
    </Sheet>
  );
}
