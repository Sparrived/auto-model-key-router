import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SecretProvider, useSecretDialog } from "./SecretDialog";

// 真实入口：按钮点击后走 useSecretDialog()，和业务里创建 Key 后的调用方式一致。
function Trigger({ onDone }: { onDone: () => void }) {
  const showSecret = useSecretDialog();
  return (
    <button
      type="button"
      onClick={() => {
        void showSecret({
          title: "访问 Key 已创建",
          items: [{ label: "密钥", value: "sk-demo" }],
        }).then(onDone);
      }}
    >
      创建 Key
    </button>
  );
}

function setup() {
  const onDone = vi.fn();
  const user = userEvent.setup();
  render(
    <SecretProvider>
      <Trigger onDone={onDone} />
    </SecretProvider>,
  );
  return { onDone, user };
}

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "创建 Key" }));
  return screen.findByRole("dialog", { name: "访问 Key 已创建" });
}

describe("SecretDialog", () => {
  it("勾选「我已保存这些凭据」后「完成」才可用，点击后关闭对话框", async () => {
    const { onDone, user } = setup();
    const dialog = await openDialog(user);

    expect(within(dialog).getByText("密钥")).toBeInTheDocument();
    expect(within(dialog).getByText("sk-demo")).toBeInTheDocument();

    const done = within(dialog).getByRole("button", { name: "完成" });
    expect(done).toBeDisabled();

    await user.click(within(dialog).getByRole("checkbox", { name: "我已保存这些凭据" }));
    expect(done).toBeEnabled();

    await user.click(done);

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "访问 Key 已创建" })).not.toBeInTheDocument();
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("未勾选时点击「完成」不关闭对话框、也不结束 Promise", async () => {
    const { onDone, user } = setup();
    const dialog = await openDialog(user);

    const done = within(dialog).getByRole("button", { name: "完成" });
    expect(done).toBeDisabled();

    await user.click(done);

    // 等一拍再断言，避免异步 resolve 漏判。
    await waitFor(() => expect(onDone).not.toHaveBeenCalled());
    expect(screen.getByRole("dialog", { name: "访问 Key 已创建" })).toBeInTheDocument();
    expect(within(dialog).getByRole("checkbox", { name: "我已保存这些凭据" })).not.toBeChecked();
  });
});
