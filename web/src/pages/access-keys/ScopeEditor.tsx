// 两份「权限范围」清单的界面件。
//
// 用三段控件而不是复选框列表：`[]`（一个都不许）与「没有这份清单」（不限制）是两个
// **相反**的授权，而空的勾选状态同时长得像这两者。把「全部取消勾选」这个明显的收紧
// 动作解读成放开一切，是这类界面里最危险的一种默认。

import { useMemo, useState, type ReactNode } from "react";
import { ProviderIcon } from "@/lib/brand-icons";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Input } from "@/components/ui/Input";
import { Segmented } from "@/components/ui/Segmented";
import type { ModelGroup, ScopeMode } from "./scope";

export function ScopeSection({
  title, subtitle, mode, onModeChange, summary, children,
}: {
  title: string;
  subtitle: string;
  mode: ScopeMode;
  onModeChange: (mode: ScopeMode) => void;
  summary: string;
  children?: ReactNode;
}) {
  return (
    <section className="space-y-2 rounded-panel border border-rule p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="text-[14px] font-semibold text-ink">{title}</h3>
          <p className="mt-0.5 text-meta text-ink-3">{subtitle}</p>
        </div>
        <Segmented
          aria-label={`${title}范围`}
          value={mode}
          onValueChange={(value) => onModeChange(value as ScopeMode)}
          options={[
            { value: "unrestricted", label: "不限制" },
            { value: "list", label: "指定清单" },
            { value: "none", label: "全部禁止" },
          ]}
        />
      </div>
      {mode === "list" ? children : null}
      <p className={mode === "none" ? "text-meta text-critical-ink" : "text-meta text-ink-3"}>{summary}</p>
    </section>
  );
}

export function ProviderChecklist({
  providers, modelCounts, selected, onChange,
}: {
  providers: Array<{ id: string }>;
  modelCounts: Map<string, Set<string>>;
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const chosen = new Set(selected);
  const known = new Set(providers.map((provider) => provider.id));
  // 候选 = 现有供应商 + 本行已选。已选里可能出现配置之外的名字（供应商被改名/删除），
  // 悄悄丢掉它们等于在运维没看见的情况下削减权限。
  const candidates = [...new Set([...providers.map((provider) => provider.id), ...selected])]
    .sort((a, b) => a.localeCompare(b, "zh-CN"));

  function toggle(name: string, checked: boolean) {
    onChange(checked ? [...selected, name] : selected.filter((item) => item !== name));
  }

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <span className="text-meta text-ink-3">已选 {selected.length} 项</span>
        <div className="flex items-center gap-1">
          <Button size="sm" variant="ghost" onClick={() => onChange([...new Set([...selected, ...candidates])])}>全选</Button>
          <Button size="sm" variant="ghost" onClick={() => onChange([])}>清空</Button>
        </div>
      </div>
      <div className="max-h-56 space-y-0.5 overflow-y-auto rounded-control border border-rule p-2">
        {candidates.length === 0 ? (
          <p className="px-1 py-3 text-center text-meta text-ink-3">还没有配置供应商，先到「供应商」页添加。</p>
        ) : candidates.map((name) => {
          const unknown = !known.has(name);
          const count = modelCounts.get(name)?.size ?? 0;
          return (
            <div key={name} className="flex items-center justify-between gap-2 rounded-chip px-1 py-0.5 hover:bg-hover">
              <Checkbox
                checked={chosen.has(name)}
                onCheckedChange={(checked) => toggle(name, checked)}
                label={(
                  <span className="flex items-center gap-1.5">
                    <ProviderIcon id={name} baseUrl={null} size={16} />
                    <span className="font-mono">{name}</span>
                  </span>
                )}
                description={unknown ? "配置里已经没有这个供应商" : `${count} 个模型`}
              />
              {unknown ? <Badge tone="warn">已不在配置中</Badge> : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}

export function ModelChecklist({
  groups, known, selected, onChange,
}: {
  groups: ModelGroup[];
  known: Set<string>;
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const [keyword, setKeyword] = useState("");
  const chosen = new Set(selected);
  const filter = keyword.trim().toLowerCase();
  const visibleGroups = useMemo(() => groups.map((group) => ({
    ...group,
    models: group.models.filter((option) =>
      !filter
      || option.value.toLowerCase().includes(filter)
      || option.note.toLowerCase().includes(filter)),
  })).filter((group) => group.models.length > 0), [groups, filter]);

  function toggle(name: string, checked: boolean) {
    onChange(checked ? [...selected, name] : selected.filter((item) => item !== name));
  }

  function setGroup(group: ModelGroup, checked: boolean) {
    const values = group.models.map((option) => option.value);
    if (checked) onChange([...new Set([...selected, ...values])]);
    else onChange(selected.filter((value) => !values.includes(value)));
  }

  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          placeholder="搜索模型或别名"
          aria-label="搜索模型或别名"
          size="sm"
          className="max-w-56"
        />
        <span className="text-meta text-ink-3">已选 {selected.length} 项</span>
        <Button size="sm" variant="ghost" onClick={() => onChange([...new Set([...selected, ...known])])}>全选</Button>
        <Button size="sm" variant="ghost" onClick={() => onChange([])}>清空</Button>
      </div>
      <div className="max-h-72 space-y-2 overflow-y-auto rounded-control border border-rule p-2">
        {visibleGroups.length === 0 ? (
          <p className="px-1 py-3 text-center text-meta text-ink-3">没有匹配的模型。</p>
        ) : visibleGroups.map((group) => {
          const allSelected = group.models.every((option) => chosen.has(option.value));
          return (
            <div key={group.provider}>
              <div className="flex items-center justify-between gap-2 px-1 py-1">
                <span className="flex min-w-0 items-center gap-1.5 text-meta font-medium text-ink-2">
                  {group.provider === "__unknown__" ? null : <ProviderIcon id={group.provider} baseUrl={null} size={14} />}
                  <span className="truncate">{group.label}</span>
                  <span className="text-ink-3">{group.models.length}</span>
                </span>
                <button
                  type="button"
                  className="rounded-chip px-1.5 py-0.5 text-meta text-ink-3 transition-colors hover:bg-hover hover:text-ink"
                  onClick={() => setGroup(group, !allSelected)}
                >
                  {allSelected ? "清空本组" : "全选本组"}
                </button>
              </div>
              <div className="space-y-0.5">
                {group.models.map((option) => {
                  const unknown = !known.has(option.value);
                  return (
                    <div key={option.value} className="flex items-center justify-between gap-2 rounded-chip px-1 py-0.5 hover:bg-hover">
                      <Checkbox
                        checked={chosen.has(option.value)}
                        onCheckedChange={(checked) => toggle(option.value, checked)}
                        label={<span className="font-mono">{option.value}</span>}
                        description={option.note || undefined}
                      />
                      {unknown ? <Badge tone="warn">已不在配置中</Badge> : null}
                    </div>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
      <p className="text-meta leading-4 text-ink-3">
        模型按调用方写的原始名字逐字比对：勾了模型 ID 不等于放行它的别名，调用方会写别名时请一并勾上。
        同一模型被多个供应商服务时只列在第一个供应商组下，勾选后仍可经任意已授权供应商调用。
      </p>
    </div>
  );
}

export function MismatchBanner({
  count, items, missingProviders, onGrant,
}: {
  count: number;
  items: Array<{ model: string; needed: string[] }>;
  missingProviders: string[];
  onGrant: () => void;
}) {
  if (count === 0) return null;
  return (
    <Banner
      tone="warn"
      title={`存在权限失配：${count} 个模型未获得所属供应商授权`}
      action={missingProviders.length > 0 ? (
        <Button size="sm" variant="secondary" onClick={onGrant}>
          一并授权缺失的供应商
        </Button>
      ) : null}
    >
      <p>以下模型已被勾选，但其路由目标依赖的供应商未在已选列表中。调用这些模型将因无可用 Key 而失败。</p>
      <ul className="mt-1 space-y-0.5">
        {items.map((item) => (
          <li key={item.model} className="font-mono">
            {item.model} → 需授权供应商: {item.needed.length ? item.needed.join("/") : "未知"}
          </li>
        ))}
      </ul>
    </Banner>
  );
}

export function EffectivePreview({
  scope, totalModels,
}: {
  scope: import("./scope").EffectiveScope;
  totalModels: number;
}) {
  return (
    <section className="flex min-h-0 flex-col rounded-panel border border-rule bg-panel">
      <div className="flex items-center justify-between gap-2 border-b border-rule px-3 py-2">
        <h3 className="text-[14px] font-semibold text-ink">生效权限预览</h3>
        <Badge tone={scope.blocked ? "critical" : scope.mismatched.length ? "warn" : "good"}>
          {scope.blocked ? "无可用权限" : scope.mismatched.length ? "存在失配" : "链路完整"}
        </Badge>
      </div>
      <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-3">
        {scope.blocked ? (
          <p className="text-meta leading-5 text-critical-ink">这把密钥调不动任何模型，外部调用会在鉴权阶段被全部拒绝。</p>
        ) : (
          <>
            <p className="text-meta leading-5 text-ink-3">
              {scope.unrestricted
                ? `完全放行：可使用任意供应商、调用全部 ${totalModels} 个模型名（含别名）。`
                : `实际可调用 ${scope.rows.length} 个模型名（含别名）及其经由的供应商。`}
            </p>
            <ul className="space-y-1">
              {scope.rows.map((row) => (
                <li key={row.model} className="rounded-chip bg-sunken px-2 py-1 text-meta">
                  <span className="font-mono text-ink">{row.model}</span>
                  <span className="text-ink-3"> → </span>
                  <span className="text-ink-2">{row.providers.length ? row.providers.join(" / ") : "供应商未知"}</span>
                </li>
              ))}
            </ul>
            {scope.rows.length === 0 ? <p className="text-meta text-warn-ink">当前没有可调用的模型。</p> : null}
          </>
        )}
      </div>
    </section>
  );
}
