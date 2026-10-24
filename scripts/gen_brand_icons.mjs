// 一次性生成器：从 simple-icons / lobehub 拉取常见供应商图标路径，产出
// web/src/lib/brand-shapes.generated.ts 的数据部分。
//
// 保留它的唯一理由是记录图标的来源与许可（simple-icons 为 CC0，@lobehub/icons 为 MIT），
// 否则内联进品牌图标模块的那堆 path 数据就成了无出处的魔数。产物是源码，运行期
// 绝不联网：WebUI 必须离线可用。改图标集时重跑本脚本，再提交产物。
//
// 生成文件只含 BRAND_SHAPES 与 PROVIDER_PRESETS；brand-icons.tsx 的 React 渲染函数与
// 品牌匹配逻辑不在这里重写，该文件只从本生成文件导入这两项数据。
//
// 用法：node scripts/gen_brand_icons.mjs

import { writeFileSync } from "node:fs";
import path from "node:path";

const SI = (slug) => `https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/${slug}.svg`;
const LOBE = (slug) => `https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/${slug}.svg`;

// 预置供应商：只收录「base_url + AMKR 默认路由路径」能拼出正确上游端点的供应商。
//
// 这是刻意的窄口径。AMKR 的默认路由把 `v1/chat/completions` 拼在 base_url 之后，
// 所以 base_url 必须正好是「去掉了 /v1 的根」。以下几家因此被排除——预置一个错的
// 地址比不预置更糟，探测会失败而用户很难看出是地址错了：
//   - Google Gemini：兼容端点在 /v1beta/openai 之下
//   - Perplexity：端点是 /chat/completions，不在 /v1 之下
//   - 智谱 GLM：兼容端点在 /api/paas/v4 之下
//   - NVIDIA NIM / Azure：需要 /v1 之外的厂商私有前缀或 query string 参数
// 这些仍可在对话框里手填名称与地址，再到「高级路径设置」里改路由。
//
// keys 是 id 匹配词：用户常把供应商改名成 `openai-backup`、`my-deepseek` 这类，
// 按子串匹配即可认出品牌。**不能用 brand 去匹配 id**：xAI 的 brand 是单字符 `x`，
// 拿它做子串匹配会把 `my-box` 这类无关 id 也判成 xAI。
const PRESETS = [
  { id: "openai", label: "OpenAI", brand: "openai", baseUrl: "https://api.openai.com", host: "api.openai.com", keys: ["openai"] },
  { id: "anthropic", label: "Anthropic", brand: "anthropic", baseUrl: "https://api.anthropic.com", host: "api.anthropic.com", keys: ["anthropic", "claude"] },
  { id: "deepseek", label: "DeepSeek", brand: "deepseek", baseUrl: "https://api.deepseek.com", host: "api.deepseek.com", keys: ["deepseek"] },
  { id: "moonshot", label: "Moonshot（Kimi）", brand: "moonshot", baseUrl: "https://api.moonshot.cn", host: "api.moonshot.cn", keys: ["moonshot", "kimi"] },
  { id: "qwen", label: "通义千问", brand: "qwen", baseUrl: "https://dashscope.aliyuncs.com/compatible-mode", host: "dashscope.aliyuncs.com", keys: ["qwen", "dashscope", "tongyi"] },
  { id: "xai", label: "xAI（Grok）", brand: "xai", baseUrl: "https://api.x.ai", host: "api.x.ai", keys: ["xai", "grok"] },
  { id: "mistral", label: "Mistral", brand: "mistral", baseUrl: "https://api.mistral.ai", host: "api.mistral.ai", keys: ["mistral"] },
  { id: "groq", label: "Groq", brand: "groq", baseUrl: "https://api.groq.com/openai", host: "api.groq.com", keys: ["groq"] },
  { id: "openrouter", label: "OpenRouter", brand: "openrouter", baseUrl: "https://openrouter.ai/api", host: "openrouter.ai", keys: ["openrouter"] },
  { id: "siliconflow", label: "硅基流动", brand: "siliconcloud", baseUrl: "https://api.siliconflow.cn", host: "api.siliconflow.cn", keys: ["siliconflow", "siliconcloud"] },
  { id: "ollama", label: "Ollama", brand: "ollama", baseUrl: "http://127.0.0.1:11434", host: "127.0.0.1:11434", keys: ["ollama"] },
  { id: "lmstudio", label: "LM Studio", brand: "lmstudio", baseUrl: "http://127.0.0.1:1234", host: "127.0.0.1:1234", keys: ["lmstudio", "lm-studio"] },
  { id: "vllm", label: "vLLM", brand: "vllm", baseUrl: "http://127.0.0.1:8000", host: "127.0.0.1:8000", keys: ["vllm"] },
];

// 品牌 -> 图标来源。只为上面真正用到的品牌取图标，避免把用不上的 path 塞进包里。
const SOURCES = {
  openai: SI("openai"),
  anthropic: SI("anthropic"),
  deepseek: SI("deepseek"),
  moonshot: SI("moonshotai"),
  qwen: SI("qwen"),
  xai: SI("x"),
  mistral: SI("mistralai"),
  groq: LOBE("groq"),
  openrouter: SI("openrouter"),
  siliconcloud: LOBE("siliconcloud"),
  ollama: SI("ollama"),
  lmstudio: SI("lmstudio"),
  vllm: SI("vllm"),
};

const pathOf = (svg) => {
  const match = svg.match(/<path[^>]*\sd="([^"]+)"/);
  if (!match) throw new Error("图标里没有 path d");
  return match[1];
};

const shapes = {};
for (const [brand, url] of Object.entries(SOURCES)) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${brand}: HTTP ${response.status}`);
  shapes[brand] = pathOf(await response.text());
  console.log(`OK   ${brand}（${shapes[brand].length} 字符）`);
}

const missing = new Set(PRESETS.map((preset) => preset.brand));
for (const brand of missing) {
  if (!shapes[brand]) throw new Error(`预置用到的品牌缺少图标: ${brand}`);
}

const out = [];
out.push("// 本文件由 scripts/gen_brand_icons.mjs 生成，请勿手改。");
out.push("//");
out.push("// 图标取自 simple-icons（CC0）与 @lobehub/icons（MIT）的官方矢量，按 24×24 网格");
out.push("// 抽出单条 path 内联于此。运行期绝不联网，WebUI 离线可用。");
out.push("//");
out.push("// 来源：");
out.push("//   simple-icons: https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/");
out.push("//   @lobehub/icons: https://cdn.jsdelivr.net/npm/@lobehub/icons-static-svg@latest/icons/");
out.push('//');
out.push("// 本文件只负责数据；brand-icons.tsx 从这里导入 BRAND_SHAPES 与 PROVIDER_PRESETS。");
out.push("");
out.push("export interface ProviderPreset {");
out.push("  id: string;");
out.push("  label: string;");
out.push("  brand: string;");
out.push("  baseUrl: string;");
out.push("  host: string;");
out.push("  keys: string[];");
out.push("}");
out.push("");
out.push("// 品牌 -> 单条 path 的 d 属性（viewBox 均为 0 0 24 24）。");
out.push("export const BRAND_SHAPES: Record<string, string> = {");
for (const [brand, d] of Object.entries(shapes)) {
  out.push(`  ${brand}:`);
  out.push(`    "${d}",`);
}
out.push("};");
out.push("");
out.push("// 预置供应商：只收录「base_url + AMKR 默认路由路径」能拼出正确上游端点的供应商。");
out.push("// Gemini、Perplexity、智谱的兼容端点不在 /v1 之下，刻意不预置，留给用户手填。");
out.push("export const PROVIDER_PRESETS: ProviderPreset[] = [");
for (const preset of PRESETS) {
  out.push(`  { id: "${preset.id}", label: "${preset.label}", brand: "${preset.brand}", baseUrl: "${preset.baseUrl}", host: "${preset.host}", keys: [${preset.keys.map((key) => `"${key}"`).join(", ")}] },`);
}
out.push("];");
out.push("");

const target = path.resolve(import.meta.dirname, "../web/src/lib/brand-shapes.generated.ts");
writeFileSync(target, out.join("\n"), "utf8");
console.log(`\n已写入 ${target}`);
