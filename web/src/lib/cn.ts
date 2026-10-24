import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// 项目用 @theme 把设计令牌映射成自定义的 text-* 名称。tailwind-merge 默认不认识它们，
// 会把 text-body（字号）与 text-on-accent（颜色）当成同一组互相覆盖——主按钮因此白底白字。
// 这里显式登记两类名称，让冲突合并只在同组内发生。
const merge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        {
          text: ["axis", "meta", "dense", "body", "title", "page", "kpi", "hero"],
        },
      ],
      "text-color": [
        {
          text: [
            "paper",
            "panel",
            "sunken",
            "raised",
            "rule",
            "rule-strong",
            "ink",
            "ink-2",
            "ink-3",
            "accent",
            "on-accent",
            "accent-hover",
            "select",
            "hover",
            "focus",
            "good",
            "warn",
            "serious",
            "critical",
            "neutral",
            "good-ink",
            "warn-ink",
            "serious-ink",
            "critical-ink",
            "neutral-ink",
            "good-bg",
            "warn-bg",
            "serious-bg",
            "critical-bg",
            "neutral-bg",
            "series-1",
            "series-2",
            "series-3",
            "series-4",
            "series-5",
            "series-6",
            "series-7",
            "series-8",
            "series-other",
            "seq-1",
            "seq-2",
            "seq-3",
            "seq-4",
            "seq-5",
            "seq-6",
          ],
        },
      ],
    },
  },
});

/** 合并条件类名，并让后写的 Tailwind 冲突类覆盖前面的。 */
export function cn(...inputs: ClassValue[]): string {
  return merge(clsx(inputs));
}
