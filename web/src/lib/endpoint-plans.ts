// 端点计划：把「端点族 → 模型（+ 固定 Key）」这段界面抽出来，供模型路由页的两处使用。
//
// 两处形状完全一样（服务端也是同一套解析/序列化，见 internal/config 的 UnifiedModelConfig
// 与 ModelConfig.Endpoints）：
//   - `unified-model` 固定项自己的七个计划，default 就是文本/对话族；
//   - 任意模型名下的端点覆盖表：直呼这个模型时，落在配置过的族上的请求改发给那一族指定的模型。
//
// 本模块只做展示与取值，不持有状态：两处的草稿归属不同（前者在 unified 配置上，后者在
// 编辑器的草稿上），由调用方决定改完重画哪一棵树。

import { UNKNOWN_KIND, kindOf, type ModelKindsIndex } from "./model-kinds";

export interface EndpointFamily {
  id: string;
  label: string;
  keyLabel: string;
  kind: string;
  paths: string;
}

// TEXT_FAMILY 是默认族（文本/对话）：unified 的 default 计划与模型的 default 覆盖都落
// 在它上面。标签刻意写成「文本（对话）」——在模型名下它是**覆盖**，得与模型自己的
// targets 区分开，光写「模型」会让人以为那是这个模型本身。
export const TEXT_FAMILY: EndpointFamily = {
  id: "default",
  label: "文本（对话）",
  keyLabel: "文本 Key",
  kind: "text",
  paths: "/v1/chat/completions、/v1/messages、/v1/responses",
};

// FAMILIES 是 default 之外的分族计划；顺序即界面顺序，与服务端
// config.UnifiedPlanNames 去掉 default 之后逐字一致（default 永远在最前）。
//
// kind 只用于给这个族的下拉排序（见 webui/model-kinds.js）；paths 是这条计划服务的入站
// 端点族，写在编辑器里——「语音识别」一条计划同时服务 /v1/audio/transcriptions 与
// /v1/audio/translations，这在界面上必须说清楚，否则用户会以为漏配了一条。
export const FAMILIES: EndpointFamily[] = [
  { id: "image", label: "图像模型", keyLabel: "图像 Key", kind: "image", paths: "/v1/images/generations" },
  { id: "embeddings", label: "嵌入模型", keyLabel: "嵌入 Key", kind: "embedding", paths: "/v1/embeddings" },
  { id: "speech", label: "语音合成模型", keyLabel: "语音合成 Key", kind: "tts", paths: "/v1/audio/speech" },
  { id: "transcriptions", label: "语音识别模型", keyLabel: "语音识别 Key", kind: "stt", paths: "/v1/audio/transcriptions 与 /v1/audio/translations" },
  { id: "video", label: "视频模型", keyLabel: "视频 Key", kind: "video", paths: "/v1/videos" },
  { id: "rerank", label: "重排模型", keyLabel: "重排 Key", kind: "rerank", paths: "/v1/rerank" },
];

// PLAN_FAMILIES 是全部计划族，顺序与服务端 UnifiedPlanNames 一致。
export const PLAN_FAMILIES: EndpointFamily[] = [TEXT_FAMILY, ...FAMILIES];

// PLAN_NAMES 是提交体里的键名（接口层按它认计划）。
export const PLAN_NAMES: string[] = PLAN_FAMILIES.map((family) => family.id);

export interface ModelSummary {
  id: string;
  keys?: Array<{ name: string; enabled?: boolean }>;
}

// enabledKeys 返回某个模型已启用的 Key 名。
export const enabledKeys = (models: ModelSummary[], modelId: string): string[] =>
  (models.find((model) => model.id === modelId)?.keys || [])
    .filter((key) => key.enabled)
    .map((key) => key.name);

// knownModel 把「已不存在的模型名」折成空串。
//
// 配置与模型清单是两次独立取数的结果，中间可能刚好有供应商被删掉：那时计划仍指向一个
// 已随供应商消失的模型。此时必须把它当成「没选」——否则下拉里根本没有这个选项、界面显示
// 空值，而保存时却仍会把它发给服务端，被「引用了未配置的模型」顶回来（页面看起来就是
// 保存无效）。
export function knownModel(models: ModelSummary[], id: string): string {
  return models.some((model) => model.id === id) ? id : "";
}

export interface ModelGroup {
  label: string;
  names: string[];
}

// modelGroups 把一个族的候选模型按类型分成「本族 / 其它类型 / 未分类」三组。
//
// 三条不变量：
//   1. **任何模型都不会被筛掉**：判据只是排序与分组。自定义模型名在服务端没有任何档案
//      （未分类），把它们挡在下拉之外，用户就再也选不回自己的模型；
//   2. 类型读数取不到时平铺：给一个"全都是未分类"的下拉加分组标题，只会让人以为这些模型
//      有问题；
//   3. 一个已分类的模型都没有时同样平铺（同上一条）。
export function modelGroups(
  models: ModelSummary[],
  kinds: ModelKindsIndex | null | undefined,
  kind: string,
): ModelGroup[] {
  const names = () => models.map((model) => model.id);
  const classified = kinds ? names().filter((name) => kindOf(kinds, name) !== UNKNOWN_KIND) : [];
  if (!classified.length) return [{ label: "", names: names() }];
  const buckets: ModelGroup[] = [
    { label: "", names: [] },
    { label: "其它类型", names: [] },
    { label: "未分类", names: [] },
  ];
  for (const name of names()) {
    const actual = kindOf(kinds, name);
    buckets[actual === kind ? 0 : actual === UNKNOWN_KIND ? 2 : 1].names.push(name);
  }
  return buckets.filter((bucket) => bucket.names.length);
}

export interface EndpointPlan {
  primary?: { model?: string | null; key?: string | null } | null;
}

export type EndpointPlans = Record<string, EndpointPlan | null | undefined>;

export interface EndpointPlanDraft {
  model: string;
  key: string;
}

// readEndpointPlans 把服务端的端点表折成编辑草稿：每族一项 {model, key}，没配置的族是空模型。
export function readEndpointPlans(
  endpoints: EndpointPlans | null | undefined,
  models: ModelSummary[],
): Record<string, EndpointPlanDraft> {
  const draft: Record<string, EndpointPlanDraft> = {};
  for (const family of PLAN_FAMILIES) {
    const plan = endpoints?.[family.id];
    const model = knownModel(models, plan?.primary?.model || "");
    draft[family.id] = { model, key: model ? plan?.primary?.key || "" : "" };
  }
  return draft;
}

// endpointPlanPayload 把编辑草稿折成提交体。
//
// 只写配置了模型的族：服务端是**整体替换**这张表，没写到的族就等于清掉——「不覆盖」与
// 「不传键」在这里是同一件事，因此不需要（也不能）靠 null 表达。
export function endpointPlanPayload(
  draft: Record<string, EndpointPlanDraft | null | undefined>,
): Record<string, { primary: { model: string; key: string | null } }> {
  const payload: Record<string, { primary: { model: string; key: string | null } }> = {};
  for (const family of PLAN_FAMILIES) {
    const picked = draft[family.id];
    if (!picked?.model) continue;
    payload[family.id] = { primary: { model: picked.model, key: picked.key || null } };
  }
  return payload;
}
