// 并发上限（供应商级 + 单把 Key 级）的共用口径。
//
// 契约：null 或 0 表示**不限制**，正整数表示上限。读侧这两个字段总是存在，写侧可以带
// `max_concurrency`（null = 清除）。三处表单（编辑供应商 / 新建 Key / Key 行内编辑）
// 共用下面几个函数，语义只有一处，避免三份各解释一遍再慢慢走形。
//
// 服务端不变式：某供应商启用中的 Key 的上限之和不得大于该供应商的上限，违反时接口回
// 400。前端**不做这套算术**——它要知道 Key 的启用状态、还要知道服务端此刻的口径，
// 在这里重算一遍只会造出第二个真相源；错误照原样渲染给用户即可。

// CONCURRENCY_SUM_HINT 就是这条不变式的说明文案：它是用户被 400 拒掉时唯一能看懂原因
// 的东西，所以三处表单共用同一份常量。
export const CONCURRENCY_SUM_HINT =
  "与供应商的并发上限同时启用时，该供应商下所有启用 Key 的并发上限之和不得大于供应商上限。";

// CONCURRENCY_INVALID 是三个表单共用的校验提示。
export const CONCURRENCY_INVALID = "并发上限必须是大于 0 的整数；留空表示不限制。";

// concurrencyText 把读侧的值写回输入框：null、0 与字段缺失一律显示成空（= 不限制）。
// 显示成 0 会让人以为"上限是 0，一个请求都出不去"——那是把一条要靠注释才讲得清的
// 契约直接摆到用户面前。
export function concurrencyText(value: unknown): string {
  const limit = Number(value);
  return Number.isFinite(limit) && limit > 0 ? String(limit) : "";
}

// concurrencyLimit 把输入框的文本读成可提交的值：空 → null（清除），正整数 → 该数，
// 其余（0、负数、小数、非数字）→ undefined。undefined 是**无效输入**的专用信号，
// 调用方必须据此中止提交并提示，绝不能把 NaN 发出去：服务端只会回一个笼统的 400，
// 而用户眼前的框里明明写着字，很难想到问题出在这里。
export function concurrencyLimit(raw: string | null | undefined): number | null | undefined {
  const text = String(raw ?? "").trim();
  if (!text) return null;
  // 只认十进制正整数：`1e3`、`3.5`、`+5`、`0x10` 这些 Number() 收得下的写法一律拒绝，
  // 契约里的字段是 JSON 整数，科学计数法和小数到了服务端语义不明。
  if (!/^\d+$/.test(text)) return undefined;
  const value = Number(text);
  return Number.isSafeInteger(value) && value >= 1 ? value : undefined;
}
