import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

// text-panel 是 Tailwind 的“面板底色”颜色类，不是字号。把它和
// font-semibold / font-medium 一起写，会覆盖真正的字号意图；历史上这个错误
// 还让 tailwind-merge 把同组的 text-ink 吃掉，标题直接变成看不见的白字。
//
// 唯一允许保留 text-panel + font-medium 的地方是 bg-ink 反白编号圆点：
// 那里的 text-panel 确实是在取颜色，且同时有 text-[11px] 明确字号。

const SRC_ROOT = resolve(import.meta.dirname, "..");

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? walk(path) : [path];
  });
}

function readQuoted(source: string, start: number, quote: string): { value: string; end: number } {
  let value = "";
  let index = start + 1;
  while (index < source.length) {
    const char = source[index];
    if (char === "\\") {
      value += source.slice(index, index + 2);
      index += 2;
      continue;
    }
    if (char === quote) return { value, end: index + 1 };
    value += char;
    index += 1;
  }
  return { value, end: source.length };
}

function readBalanced(source: string, start: number): string {
  let depth = 1;
  let index = start + 1;
  while (index < source.length && depth > 0) {
    const char = source[index];
    if (char === "\"" || char === "'" || char === "`") {
      index = readQuoted(source, index, char).end;
      continue;
    }
    if (char === "{") depth += 1;
    if (char === "}") depth -= 1;
    index += 1;
  }
  return source.slice(start + 1, Math.max(start + 1, index - 1));
}

function classNameExpressions(source: string): string[] {
  const expressions: string[] = [];
  let cursor = 0;
  while (cursor < source.length) {
    const at = source.indexOf("className", cursor);
    if (at < 0) break;
    let index = at + "className".length;
    while (/\s/.test(source[index] ?? "")) index += 1;
    if (source[index] !== "=") {
      cursor = index;
      continue;
    }
    index += 1;
    while (/\s/.test(source[index] ?? "")) index += 1;
    const char = source[index];
    if (char === "\"" || char === "'" || char === "`") {
      const quoted = readQuoted(source, index, char);
      expressions.push(quoted.value);
      cursor = quoted.end;
    } else if (char === "{") {
      expressions.push(readBalanced(source, index));
      cursor = index + 1;
    } else {
      cursor = index + 1;
    }
  }
  return expressions;
}

function isAllowedInverseDot(expression: string): boolean {
  return /\bbg-ink\b/.test(expression) && /\btext-\[11px\]/.test(expression);
}

describe("text-panel 字号守卫", () => {
  it("className 中不把 text-panel 当字号和 font-semibold/medium 混用", () => {
    const violations: string[] = [];
    for (const file of walk(SRC_ROOT).filter((path) => path.endsWith(".tsx"))) {
      const source = readFileSync(file, "utf8");
      for (const expression of classNameExpressions(source)) {
        if (!/\btext-panel\b/.test(expression)) continue;
        if (!/\bfont-(?:semibold|medium)\b/.test(expression)) continue;
        if (isAllowedInverseDot(expression)) continue;
        violations.push(`${file.slice(SRC_ROOT.length + 1)}: ${expression.replace(/\s+/g, " ").trim()}`);
      }
    }
    expect(violations).toEqual([]);
  });
});
