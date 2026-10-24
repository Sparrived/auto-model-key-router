import { Fragment } from "react";
import { modelGroups, type ModelSummary } from "@/lib/endpoint-plans";
import type { ModelKindsIndex } from "@/lib/model-kinds";
import type { ModelSummaryResponse } from "@/lib/types";
import { Select } from "@/components/ui/Select";

export interface ModelSelectProps {
  models: ModelSummaryResponse[];
  kinds: ModelKindsIndex | null | undefined;
  kind: string;
  value: string;
  emptyLabel?: string;
  disabled?: boolean;
  id?: string;
  "aria-label"?: string;
  onChange: (value: string) => void;
}

// ModelSelect 是一个按类型分组的模型下拉。
//
// 分组规则来自 endpoint-plans.modelGroups：本族在前、其它类型居中、未分类垫底，
// 且**任何模型都不会被筛掉**。React 的受控 select 不存在 legacy 里「先挂选项、最后写 value」
// 的脏值问题——value 与 option 在同一棵虚拟树里声明，React 会自己对齐选中项。
export function ModelSelect({
  models, kinds, kind, value, emptyLabel = "", disabled = false, id, onChange, ...rest
}: ModelSelectProps) {
  const groups = modelGroups(models as unknown as ModelSummary[], kinds, kind);
  return (
    <Select
      id={id}
      aria-label={rest["aria-label"]}
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value)}
    >
      {emptyLabel ? <option value="">{emptyLabel}</option> : null}
      {groups.map((group, index) => group.label ? (
        <optgroup key={group.label} label={group.label}>
          {group.names.map((name) => <option key={name} value={name}>{name}</option>)}
        </optgroup>
      ) : (
        <Fragment key={`ungrouped-${index}`}>
          {group.names.map((name) => <option key={name} value={name}>{name}</option>)}
        </Fragment>
      ))}
    </Select>
  );
}

export interface KeySelectProps {
  models: ModelSummaryResponse[];
  modelId: string;
  value: string;
  includeAuto?: boolean;
  disabled?: boolean;
  id?: string;
  onChange: (value: string) => void;
}

export function KeySelect({ models, modelId, value, includeAuto = false, disabled = false, id, onChange }: KeySelectProps) {
  const enabled = (models.find((model) => model.id === modelId)?.keys || []).filter((key) => key.enabled).map((key) => key.name);
  return (
    <Select
      id={id}
      value={value}
      disabled={disabled || !modelId}
      onChange={(event) => onChange(event.target.value)}
    >
      {includeAuto ? <option value="">自动路由</option> : null}
      {enabled.map((name) => <option key={name} value={name}>{name}</option>)}
    </Select>
  );
}
