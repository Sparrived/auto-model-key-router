import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Segmented } from "./Segmented";

// 受控用法：checked / tabIndex 全部由 value 推导，键盘改变选中后必须重渲染才能验证。
function Harness() {
  const [value, setValue] = useState("list");
  return (
    <Segmented
      aria-label="视图"
      value={value}
      onValueChange={setValue}
      options={[
        { value: "list", label: "列表" },
        { value: "grid", label: "网格", disabled: true },
        { value: "board", label: "看板" },
      ]}
    />
  );
}

function renderSegmented() {
  const user = userEvent.setup();
  render(<Harness />);
  const radio = (name: string) => screen.getByRole("radio", { name });
  return { user, radio };
}

describe("Segmented", () => {
  it("初始选中项是唯一的 tab 落点，禁用项不可选", () => {
    const { radio } = renderSegmented();

    expect(radio("列表")).toHaveAttribute("aria-checked", "true");
    expect(radio("网格")).toHaveAttribute("aria-checked", "false");
    expect(radio("网格")).toBeDisabled();
    expect(radio("看板")).toHaveAttribute("aria-checked", "false");

    expect(radio("列表")).toHaveAttribute("tabindex", "0");
    expect(radio("看板")).toHaveAttribute("tabindex", "-1");
  });

  it("方向键 / Home / End 跳过禁用项移动选中与焦点", async () => {
    const { user, radio } = renderSegmented();

    // 真实键盘操作：Tab 进入单选组，焦点落在当前选中项。
    await user.tab();
    expect(radio("列表")).toHaveFocus();

    // 向右跳过禁用的「网格」，直接到「看板」。
    await user.keyboard("{ArrowRight}");
    expect(radio("看板")).toHaveFocus();
    expect(radio("看板")).toHaveAttribute("aria-checked", "true");
    expect(radio("列表")).toHaveAttribute("aria-checked", "false");
    expect(radio("网格")).toHaveAttribute("aria-checked", "false");

    // 向左回到「列表」，同样跳过禁用项。
    await user.keyboard("{ArrowLeft}");
    expect(radio("列表")).toHaveFocus();
    expect(radio("列表")).toHaveAttribute("aria-checked", "true");
    expect(radio("看板")).toHaveAttribute("aria-checked", "false");

    // 从首项向左循环到末项（跳过禁用项）。
    await user.keyboard("{ArrowLeft}");
    expect(radio("看板")).toHaveFocus();

    await user.keyboard("{Home}");
    expect(radio("列表")).toHaveFocus();
    expect(radio("列表")).toHaveAttribute("aria-checked", "true");

    await user.keyboard("{End}");
    expect(radio("看板")).toHaveFocus();
    expect(radio("看板")).toHaveAttribute("aria-checked", "true");
    expect(radio("网格")).toHaveAttribute("aria-checked", "false");
  });
});
