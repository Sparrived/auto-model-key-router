import { describe, expect, it } from "vitest";
import {
  buildTaskPayload,
  collectParams,
  draftFromTask,
  emptyDraft,
  nextTaskName,
  validateDraft,
} from "./draft";

describe("任务草稿", () => {
  it("下一个任务名取当前空间最大编号 +1", () => {
    expect(nextTaskName([
      { name: "TASK_000001" },
      { name: "TASK_000009" },
      { name: "TEAM_TASK" },
    ])).toBe("TASK_000010");
    expect(nextTaskName([])).toBe("TASK_000001");
  });

  it("备选不能等于首选", () => {
    const draft = { ...emptyDraft("TASK_1"), model: "model-a", fallback: "model-a" };
    expect(validateDraft(draft, true)).toBe("备选模型不能与首选模型相同。");
  });

  it("留空的参数不写进 params，固定了的整数按数字写", () => {
    const draft = emptyDraft("TASK_1");
    draft.numbers.temperature = { fixed: true, value: "0.7" };
    draft.numbers.top_k = { fixed: true, value: "40" };
    draft.stopFixed = true;
    draft.stop = ["END"];
    draft.effort = "high";
    expect(collectParams(draft)).toEqual({
      temperature: 0.7,
      top_k: 40,
      stop: ["END"],
      reasoning_effort: "high",
    });
    expect(buildTaskPayload(draft)).toMatchObject({
      model: null,
      display_name: null,
      fallback_model: null,
    });
  });

  it("既有任务没有模型时保持未指定，不擅自回落到第一个模型", () => {
    expect(draftFromTask({ name: "TASK_1", params: {} }).model).toBe("");
  });
});
