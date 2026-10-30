// 连带影响流程：先预演、再确认、最后真写；取消时一个字节都不落盘。
//
// 这里用注入的 confirm 模拟用户选择，不碰 DOM——流程正确性与对话框长什么样无关。

import { describe, expect, it, vi } from "vitest";
import { hasImpact, impactSections, writeWithImpactConfirm } from "./model-impact";
import type { Impact } from "./model-impact";

const impact: Impact = {
  removed_models: ["old-model"],
  removed_keys: [{ provider: "openai", key: "old-key" }],
  access_keys: [{ id: "ak1", name: "试用", models: ["old-model"] }],
  workspaces: [{ name: "teamA", models_cleared: true }],
  removed_tasks: ["TASK_000001"],
  unified_model: { default: { primary: { model: "new-model" } } },
  endpoint_plans: { image: { primary: { model: "new-model" } } },
  quota_freeze_whitelist: ["old-model"],
};

describe("影响清单", () => {
  it("识别任何一类连带变动", () => {
    expect(hasImpact(null)).toBe(false);
    expect(hasImpact({})).toBe(false);
    expect(hasImpact(impact)).toBe(true);
    expect(hasImpact({ removed_keys: [{ provider: "p", key: "k" }] })).toBe(true);
    expect(hasImpact({ endpoint_plans: {} })).toBe(true);
    expect(hasImpact({ endpoint_plans: { image: {} } })).toBe(true);
    expect(hasImpact({ quota_freeze_whitelist: ["old-model"] })).toBe(true);
  });

  it("分节渲染文案，空清单为空数组", () => {
    expect(impactSections(null)).toEqual([]);
    const sections = impactSections(impact);
    expect(sections.map((section) => section.title)).toEqual(
      ["模型", "供应商 Key", "访问密钥", "工作空间", "任务", "统一模型", "端点计划", "提前冻结白名单"]);
    expect(sections[0].lines[0]).toContain("old-model 失去全部 Key 绑定");
    expect(sections[1].lines[0]).toContain("openai / old-key");
    expect(sections[2].lines[0]).toContain("模型清单移除 old-model");
    expect(sections[3].lines[0]).toContain("模型清单被摘空");
    expect(sections[4].lines[0]).toContain("TASK_000001");
    expect(sections[7].lines[0]).toContain("提前冻结白名单将同步更新");
  });
});

describe("预演 → 确认 → 写", () => {
  it("有影响时先预演，用户确认后才提交", async () => {
    const order: string[] = [];
    const preview = vi.fn(async () => { order.push("preview"); return impact; });
    const commit = vi.fn(async () => { order.push("commit"); return "saved"; });
    const confirm = vi.fn(async () => { order.push("confirm"); return true; });
    const result = await writeWithImpactConfirm({
      title: "保存模型", message: "确认删除？", confirmLabel: "删除模型", preview, commit, confirm,
    });
    expect(order).toEqual(["preview", "confirm", "commit"]);
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({
      title: "保存模型",
      message: "确认删除？",
      impact,
      impacted: true,
      confirmLabel: "删除模型",
    }));
    expect(result).toEqual({ confirmed: true, result: "saved" });
  });

  it("用户取消时不提交，返回 confirmed=false", async () => {
    const commit = vi.fn(async () => "saved");
    const confirm = vi.fn(async () => false);
    const result = await writeWithImpactConfirm({
      title: "保存", message: "确认？", preview: async () => impact, commit, confirm,
    });
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(commit).not.toHaveBeenCalled();
    expect(result).toEqual({ confirmed: false, result: undefined });
  });

  it("无影响且非 alwaysConfirm 时直接写，不打扰用户", async () => {
    const confirm = vi.fn(async () => true);
    const commit = vi.fn(async () => 42);
    const result = await writeWithImpactConfirm({
      title: "保存", message: "确认？", preview: async () => ({}), commit, confirm,
    });
    expect(confirm).not.toHaveBeenCalled();
    expect(commit).toHaveBeenCalledTimes(1);
    expect(result).toEqual({ confirmed: true, result: 42 });
  });

  it("alwaysConfirm 让删除即使无影响也要问一句", async () => {
    const commit = vi.fn(async () => "deleted");
    const confirm = vi.fn(async () => true);
    await writeWithImpactConfirm({
      title: "删除", message: "确认删除？", preview: async () => null, commit, confirm, alwaysConfirm: true,
    });
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ impacted: false, impact: null }));
    expect(commit).toHaveBeenCalledTimes(1);
  });

  it("预演失败不提交", async () => {
    const commit = vi.fn(async () => "saved");
    const confirm = vi.fn(async () => true);
    await expect(writeWithImpactConfirm({
      title: "保存", message: "确认？", preview: async () => { throw new Error("422"); }, commit, confirm,
    })).rejects.toThrow("422");
    expect(commit).not.toHaveBeenCalled();
  });
});
