import { describe, expect, it } from "vitest";
import { freezeResetCountdown, freezeTriggerText } from "./quota-freeze";
import type { QuotaFreeze } from "./types";

const NOW = Date.parse("2026-10-30T10:00:00Z");

function freeze(overrides: Partial<QuotaFreeze> = {}): QuotaFreeze {
  return {
    provider_id: "openai",
    key_name: "main",
    ...overrides,
  };
}

describe("冻结倒计时口径", () => {
  it("优先使用 expires_at，而不是窗口刷新时间 reset_at", () => {
    const value = freeze({
      expires_at: "2026-10-30T10:02:00Z",
      reset_at: "2026-10-30T11:30:00Z",
    });
    expect(freezeResetCountdown(value, NOW)).toBe("2 分钟后自动解冻");
  });

  it("到点显示正在自动解冻，而不是继续倒数或显示即将重置", () => {
    const value = freeze({
      expires_at: "2026-10-30T09:59:00Z",
      reset_at: "2026-10-30T11:30:00Z",
    });
    expect(freezeResetCountdown(value, NOW)).toBe("正在自动解冻…");
  });

  it("没有 expires_at 的旧数据才退回 reset_at 的窗口重置倒计时", () => {
    expect(freezeResetCountdown(freeze({ reset_at: "2026-10-30T10:30:00Z" }), NOW))
      .toBe("30 分钟后重置");
    expect(freezeResetCountdown(freeze({ reset_at: "2026-10-30T09:59:00Z" }), NOW))
      .toBe("正在自动解冻…");
  });

  it("两个时间都没有时返回空串", () => {
    expect(freezeResetCountdown(freeze(), NOW)).toBe("");
  });
});

describe("冻结触发方式文案", () => {
  it("threshold 显示提前冻结，缺省按硬性冻结处理", () => {
    expect(freezeTriggerText(freeze({ trigger: "threshold", threshold: 0.05 }))).toBe("提前冻结");
    expect(freezeTriggerText(freeze({ trigger: "hard" }))).toBe("额度冻结");
    expect(freezeTriggerText(freeze())).toBe("额度冻结");
  });
});
