import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ConfirmProvider, useConfirm } from "./ConfirmProvider";

// 真实入口：页面里的删除按钮调用 useConfirm()，关闭后必须把全屏遮罩也卸载掉。
// 遮罩若残留，用户看到的是页面正常、但所有点击都被透明层吃掉。
function Harness() {
  const confirm = useConfirm();
  return (
    <>
      <button
        type="button"
        onClick={() => {
          void confirm({
            title: "删除任务",
            message: "删除后不可恢复。",
            confirmLabel: "删除",
            cancelLabel: "取消",
            danger: true,
          });
        }}
      >
        打开确认框
      </button>
      <button type="button">页面按钮</button>
    </>
  );
}

describe("ConfirmProvider", () => {
  it("关闭 ConfirmDialog 后 document.body 下不残留 overlay", async () => {
    const user = userEvent.setup();
    render(
      <ConfirmProvider>
        <Harness />
      </ConfirmProvider>,
    );

    await user.click(screen.getByRole("button", { name: "打开确认框" }));
    const dialog = await screen.findByRole("dialog", { name: "删除任务" });
    expect(dialog).toBeInTheDocument();
    expect(document.querySelector('[data-amkr-overlay="dialog"]')).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "取消" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "删除任务" })).not.toBeInTheDocument();
    });
    await waitFor(() => {
      expect(document.querySelector('[data-amkr-overlay="dialog"]')).toBeNull();
    });

    // 关闭后页面按钮仍能接收点击，确认不会被残留遮罩拦截。
    await user.click(screen.getByRole("button", { name: "页面按钮" }));
  });
});
