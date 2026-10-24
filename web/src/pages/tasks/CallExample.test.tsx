import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { CallExample } from "./CallExample";
import { emptyDraft } from "./draft";

afterEach(cleanup);

function draft(name = "TASK_000042") {
  return { ...emptyDraft(name), name };
}

describe("调用示例", () => {
  it("非默认空间带 X-AMKR-Workspace，鉴权只放占位符", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <CallExample draft={draft()} workspace="teamA" baseUrl="https://gw.example.com/" />,
    );
    const curl = container.querySelector("code")!.textContent || "";
    expect(curl).toContain("https://gw.example.com/v1/chat/completions");
    expect(curl).toContain("X-AMKR-Workspace: teamA");
    expect(curl).toContain("<你的推理 key>");
    expect(curl).not.toMatch(/amkr_/);

    await user.click(screen.getByRole("tab", { name: "OpenAI Python SDK" }));
    const python = Array.from(container.querySelectorAll("code"))
      .map((node) => node.textContent || "")
      .find((text) => text.includes("OpenAI")) || "";
    expect(python).toContain('base_url="https://gw.example.com/v1"');
    expect(python).toContain('"X-AMKR-Workspace": "teamA"');
  });

  it("默认空间不带工作空间头", () => {
    const { container } = render(
      <CallExample draft={draft()} workspace="default" baseUrl="https://gw.example.com" />,
    );
    const curl = container.querySelector("code")!.textContent || "";
    expect(curl).not.toContain("X-AMKR-Workspace");
  });
});
