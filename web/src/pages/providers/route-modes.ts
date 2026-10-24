// ROUTE_MODES 是 upstream_routes 支持的全部模式：mode → 该模式的上游路径。
//
// 前 5 个是既有族，后 5 个是透传端点族（语音三兄弟、视频、重排）。顺序、id 与 label
// 都与服务端 internal/config 的 upstreamRouteModes / upstreamRouteLabels 逐字一致：
// 同一模式在这里和错误文案里显示成两个名字，排障的人会以为它们是两回事。
//
// path 只用于输入框的占位提示，**不是**前端在决定默认值——真正的默认路径在服务端，
// 这里写错只会让提示不准（供应商的探测请求仍会打到服务端的默认路径上）。
export interface RouteMode {
  id: string;
  label: string;
  path: string;
}

export const ROUTE_MODES: RouteMode[] = [
  { id: "openai", label: "OpenAI Chat", path: "v1/chat/completions" },
  { id: "anthropic", label: "Anthropic Messages", path: "v1/messages" },
  { id: "responses", label: "OpenAI Responses", path: "v1/responses" },
  { id: "images", label: "OpenAI Images", path: "v1/images/generations" },
  { id: "embeddings", label: "OpenAI Embeddings", path: "v1/embeddings" },
  { id: "speech", label: "OpenAI Speech（语音合成）", path: "v1/audio/speech" },
  { id: "transcriptions", label: "OpenAI Transcriptions（语音转写）", path: "v1/audio/transcriptions" },
  { id: "translations", label: "OpenAI Translations（语音翻译）", path: "v1/audio/translations" },
  { id: "video", label: "OpenAI Videos（视频生成）", path: "v1/videos" },
  { id: "rerank", label: "Rerank（重排）", path: "v1/rerank" },
];
