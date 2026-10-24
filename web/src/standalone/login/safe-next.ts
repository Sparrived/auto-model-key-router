// 登录页的开放重定向防护。
//
// `next` 是攻击者完全可控的 URL 参数：`?next=//evil.example` 会让用户在本站输入真实
// Key 后被送去站外。这里只接受「同源绝对路径」，并先按浏览器的规则剥掉 tab/LF/CR——
// 浏览器在解析 URL 前会做同样的归一化，若校验原始串，`/\t/evil.example` 会骗过这里
// 却仍被浏览器当成 `//evil.example`。
export function safeNext(raw: string | null): string | null {
  const value = String(raw || "").replace(/[\t\n\r]/g, "").trim();
  if (!value.startsWith("/")) return null;
  if (/^\/[/\\]/.test(value)) return null;
  return value;
}

export function currentNext(search: string): string | null {
  return safeNext(new URLSearchParams(search || "").get("next"));
}
