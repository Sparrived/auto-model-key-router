// 安全的 localStorage 读写：隐私模式（Safari 无痕等）下访问 localStorage 会抛异常。
// 主题、价格缓存这类"锦上添花"的状态绝不该让整个页面挂掉，因此统一从这里走。

function storage(): Storage | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

export function readStorage(key: string): string | null {
  try {
    return storage()?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeStorage(key: string, value: string): void {
  try {
    storage()?.setItem(key, value);
  } catch {
    // 配额不足或被策略拒绝：忽略，下一次读取会退回默认值。
  }
}

export function removeStorage(key: string): void {
  try {
    storage()?.removeItem(key);
  } catch {
    // 同上。
  }
}

export function readJSON<T>(key: string): T | null {
  const raw = readStorage(key);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

export function writeJSON(key: string, value: unknown): void {
  try {
    writeStorage(key, JSON.stringify(value));
  } catch {
    // 值不可序列化时忽略：缓存只是优化。
  }
}
