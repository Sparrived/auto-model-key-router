// 常见供应商的品牌图标与预置信息。
//
// 图标的 path 数据与预置清单由 scripts/gen_brand_icons.mjs 生成到
// brand-shapes.generated.ts；本文件只保留 React 渲染与品牌匹配逻辑。

import type { ReactElement } from "react";
import { Server } from "lucide-react";

import { BRAND_SHAPES, PROVIDER_PRESETS, type ProviderPreset } from "./brand-shapes.generated";

// 保留原有从 brand-icons 导入这两个符号的调用方。
export { BRAND_SHAPES, PROVIDER_PRESETS };
export type { ProviderPreset };

// 品牌图标：填充路径，颜色继承 currentColor。未知品牌返回 null，由调用方回退。
export interface BrandIconProps {
  brand: string | null | undefined;
  size?: number;
  title?: string;
  className?: string;
}

export function BrandIcon({ brand, size = 20, title, className }: BrandIconProps): ReactElement | null {
  const d = brand ? BRAND_SHAPES[brand] : undefined;
  if (!d) return null;
  return (
    <svg
      viewBox="0 0 24 24"
      fill="currentColor"
      width={size}
      height={size}
      className={className ? `brand-icon ${className}` : "brand-icon"}
      aria-hidden={title ? undefined : true}
      role={title ? "img" : undefined}
      aria-label={title}
    >
      {title ? <title>{title}</title> : null}
      <path d={d} />
    </svg>
  );
}

// 从 base_url 的 host（含非默认端口）或供应商 id 猜品牌，猜不中返回 null。
//
// 顺序是关键：先按 host 精确匹配（同家供应商常被改名），再退回按 id 子串匹配，
// 这样 `openai-backup`、`my-deepseek` 这类自定义命名也能拿到对的图标。
export function brandForProvider(id: string | null | undefined, baseUrl: string | null | undefined): string | null {
  const host = hostOf(baseUrl);
  if (host) {
    for (const preset of PROVIDER_PRESETS) {
      if (host === preset.host || host.endsWith(`.${preset.host}`)) return preset.brand;
    }
  }
  const key = String(id || "").toLowerCase();
  if (key) {
    for (const preset of PROVIDER_PRESETS) {
      if (preset.keys.some((token) => key.includes(token))) return preset.brand;
    }
  }
  return null;
}

// host 保留非默认端口：本地部署正是靠端口区分 Ollama / LM Studio / vLLM。
function hostOf(baseUrl: string | null | undefined): string {
  try {
    return new URL(String(baseUrl || "")).host.toLowerCase();
  } catch {
    return "";
  }
}

export interface ProviderIconProps {
  id: string | null | undefined;
  baseUrl: string | null | undefined;
  size?: number;
  title?: string;
  className?: string;
}

// 供应商图标：认得出品牌就用品牌标志，否则回退到通用的机架图标。
export function ProviderIcon({ id, baseUrl, size = 20, title, className }: ProviderIconProps): ReactElement {
  const brand = brandForProvider(id, baseUrl);
  if (brand) return <BrandIcon brand={brand} size={size} title={title} className={className} />;
  return (
    <Server
      size={size}
      strokeWidth={1.75}
      className={className}
      aria-hidden={title ? undefined : true}
      aria-label={title}
    />
  );
}
