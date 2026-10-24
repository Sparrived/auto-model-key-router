import {
  useState,
  type ChangeEvent,
  type ClipboardEvent,
  type InputHTMLAttributes,
  type KeyboardEvent,
} from "react";
import { cn } from "@/lib/cn";
import { Chip } from "./Chip";

const SEPARATOR = /[,，\n]/;

export interface TokenInputProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "defaultValue"> {
  value: string[];
  onChange: (value: string[]) => void;
  invalid?: boolean;
}

export function TokenInput({
  value,
  onChange,
  invalid = false,
  disabled = false,
  placeholder,
  className,
  ...props
}: TokenInputProps) {
  const [draft, setDraft] = useState("");

  function commit(raw: string) {
    const tokens = raw
      .split(SEPARATOR)
      .map((token) => token.trim())
      .filter(Boolean);
    if (tokens.length > 0) onChange([...value, ...tokens]);
    setDraft("");
  }

  // 一次粘贴/输入里的全部分隔符合并成一次 onChange；末尾残片留在输入框继续编辑。
  function commitSeparated(raw: string) {
    const parts = raw.split(SEPARATOR);
    const tail = parts.pop() ?? "";
    const tokens = parts.map((token) => token.trim()).filter(Boolean);
    if (tokens.length > 0) onChange([...value, ...tokens]);
    setDraft(tail);
  }

  function handleChange(event: ChangeEvent<HTMLInputElement>) {
    const raw = event.target.value;
    if (!SEPARATOR.test(raw)) {
      setDraft(raw);
      return;
    }
    commitSeparated(raw);
  }

  function handlePaste(event: ClipboardEvent<HTMLInputElement>) {
    const text = event.clipboardData.getData("text");
    // input 单行控件会吞掉换行，必须在浏览器改写前拿到剪贴板原文。
    if (!SEPARATOR.test(text)) return;
    event.preventDefault();
    const tokens = (draft + text)
      .split(SEPARATOR)
      .map((token) => token.trim())
      .filter(Boolean);
    if (tokens.length > 0) onChange([...value, ...tokens]);
    setDraft("");
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Enter") {
      event.preventDefault();
      if (draft.trim()) commit(draft);
      return;
    }
    if (event.key === "Backspace" && draft === "" && value.length > 0) {
      event.preventDefault();
      onChange(value.slice(0, -1));
    }
  }

  return (
    <div
      className={cn(
        "flex min-h-8 w-full flex-wrap items-center gap-1 rounded-control border bg-sunken px-2 py-1 transition-colors",
        "focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-focus",
        invalid ? "border-critical" : "border-rule-strong",
        disabled && "opacity-60",
        className,
      )}
    >
      {value.map((token, index) => (
        <Chip
          key={`${token}-${index}`}
          label={token}
          disabled={disabled}
          onRemove={disabled ? undefined : () => onChange(value.filter((_, itemIndex) => itemIndex !== index))}
        >
          {token}
        </Chip>
      ))}
      <input
        value={draft}
        disabled={disabled}
        placeholder={value.length === 0 ? placeholder : undefined}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onPaste={handlePaste}
        className="h-6 min-w-16 flex-1 border-0 bg-transparent p-0 text-body text-ink placeholder:text-ink-3 focus-visible:outline-none disabled:cursor-not-allowed"
        {...props}
      />
    </div>
  );
}
