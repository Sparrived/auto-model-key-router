import { useEffect, useRef } from "react";

// Radix 的模态 Dialog 关闭时只把焦点还给 <Dialog.Trigger>（react-dialog 的
// DialogContentModal 先 preventDefault，再 triggerRef.current?.focus()）。我们的
// 抽屉和对话框都由外部 state 打开、没有 Trigger，于是关闭后焦点落回 body，键盘
// 用户得从页首重新 Tab。做法与命令面板一致：打开时记住当前焦点，关闭时主动归还。
//
// 返回值直接交给 Content 的 onCloseAutoFocus。嵌套时（抽屉里弹「放弃更改？」）
// 内层记住的是抽屉里的输入框，关闭时它可能已随抽屉卸载，所以先判 isConnected，
// 随后外层抽屉关闭再把焦点还给页面上的触发按钮。
export function useReturnFocus(open: boolean) {
  const returnFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (open) {
      returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
  }, [open]);

  return (event: Event) => {
    event.preventDefault();
    const target = returnFocusRef.current;
    returnFocusRef.current = null;
    if (target?.isConnected) target.focus({ preventScroll: true });
  };
}
