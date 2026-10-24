import { describe, expect, it } from "vitest";
import { cn } from "./cn";

describe("cn", () => {
  it("同组 Tailwind 类由后者覆盖", () => {
    expect(cn("bg-panel", "bg-raised")).toBe("bg-raised");
    expect(cn("text-body", "text-meta")).toBe("text-meta");
  });

  it("自定义字号与自定义文字颜色不会被误判为同组", () => {
    expect(cn("text-on-accent", "text-body")).toBe("text-on-accent text-body");
    expect(cn("text-meta", "text-good-ink")).toBe("text-meta text-good-ink");
  });
});
