import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { ConfirmProvider } from "./ConfirmProvider";
import { Sheet } from "./Sheet";

// open 由外部 state 控制，正是 Sheet 与页面约定的用法。
function Harness() {
  const [open, setOpen] = useState(true);
  return (
    <Sheet open={open} onOpenChange={setOpen} title="编辑" dirty>
      <p>表单内容</p>
    </Sheet>
  );
}

function renderSheet() {
  const user = userEvent.setup();
  render(
    <ConfirmProvider>
      <Harness />
    </ConfirmProvider>,
  );
  return { user, sheet: screen.getByRole("dialog", { name: "编辑" }) };
}

describe("Sheet", () => {
  it("dirty 时关闭先确认：继续编辑保持打开，放弃更改才关闭", async () => {
    const { user, sheet } = renderSheet();

    // 第一次「取消」：弹出二次确认。
    await user.click(within(sheet).getByRole("button", { name: "取消" }));
    const confirm = await screen.findByRole("dialog", { name: "放弃未保存的更改？" });
    expect(within(confirm).getByText("关闭后，这次修改不会保存。")).toBeInTheDocument();

    // 继续编辑：确认框关闭，Sheet 仍在。
    await user.click(within(confirm).getByRole("button", { name: "继续编辑" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "放弃未保存的更改？" })).not.toBeInTheDocument();
    });
    expect(screen.getByRole("dialog", { name: "编辑" })).toBeInTheDocument();
    expect(screen.getByText("表单内容")).toBeInTheDocument();

    // 第二次「取消」：放弃更改后 Sheet 消失。
    await user.click(
      within(screen.getByRole("dialog", { name: "编辑" })).getByRole("button", { name: "取消" }),
    );
    await user.click(await screen.findByRole("button", { name: "放弃更改" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "编辑" })).not.toBeInTheDocument();
    });
    await waitFor(() => {
      expect(document.querySelector('[data-amkr-overlay="sheet"]')).toBeNull();
    });
  });

  // 页面都用外部 state 打开抽屉、没有 Dialog.Trigger；Radix 只会把焦点还给 Trigger，
  // 不接管的话关闭后焦点落回 body，键盘用户得从页首重新 Tab。
  it("关闭后焦点回到打开它的按钮（含放弃更改的嵌套确认）", async () => {
    function TriggerHarness({ dirty }: { dirty: boolean }) {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>新建</button>
          <Sheet open={open} onOpenChange={setOpen} title="编辑" dirty={dirty}>
            <input aria-label="名称" />
          </Sheet>
        </>
      );
    }

    for (const dirty of [false, true]) {
      const user = userEvent.setup();
      const { unmount } = render(
        <ConfirmProvider>
          <TriggerHarness dirty={dirty} />
        </ConfirmProvider>,
      );
      const trigger = screen.getByRole("button", { name: "新建" });
      await user.click(trigger);
      // 焦点明确落在抽屉内：否则 jsdom 里焦点可能从没离开按钮，断言会空过。
      await user.click(await screen.findByRole("textbox", { name: "名称" }));

      await user.keyboard("{Escape}");
      if (dirty) await user.click(await screen.findByRole("button", { name: "放弃更改" }));

      await waitFor(() => {
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      });
      await waitFor(() => {
        expect(trigger).toHaveFocus();
      });
      unmount();
    }
  });
});
