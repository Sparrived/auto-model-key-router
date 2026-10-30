// 页面禁用了轮询，冻结倒计时与「到点自动刷新」只靠两个轻量计时器：
// - useNow 让展示层按固定节奏重算剩余时间；
// - useDeadlineRefresh 只在最近一条冻结的 expires_at 到点时回调一次，触发一次
//   refetch，让徽标自己消失，而不是等用户手动刷新。

import { useEffect, useRef, useState } from "react";

export type DeadlineValue = string | number | Date | null | undefined;

function deadlineValue(value: DeadlineValue): number | null {
  if (value === null || value === undefined || value === "") return null;
  const millis = value instanceof Date
    ? value.getTime()
    : typeof value === "number"
      ? value
      : Date.parse(String(value));
  return Number.isFinite(millis) ? millis : null;
}

// 浏览器 setTimeout 的上限是 2^31-1 毫秒；超过时先睡一段再继续等，避免被当成 0
// 立即触发、把一次性回调变成重试循环。
const MAX_TIMEOUT_MS = 2_147_483_647;

// 到点后再等一秒才 refetch：服务端计时器与浏览器时钟有几十毫秒到几秒的差，贴着
// expires_at 发请求可能刚好撞上「服务端还没清理完」，拿回旧冻结后页面就不会再刷新。
// 一秒的延迟不可感知，却能把这类竞态挡掉。
const DEADLINE_GRACE_MS = 1000;

/** 以固定节奏返回当前时间戳（毫秒）。默认 15 秒一次，足够让分钟级倒计时前进。 */
export function useNow(intervalMs = 15000): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const interval = Math.max(1, Math.floor(intervalMs));
    const timer = window.setInterval(() => setNow(Date.now()), interval);
    return () => window.clearInterval(timer);
  }, [intervalMs]);

  return now;
}

/**
 * 在最近一条 deadline 到点时调用一次 onDue。
 *
 * 只安排一个 setTimeout，不轮询；deadline 列表变化时会清掉旧 timer 并按新列表重排。
 * 同一条 deadline 在一次挂载内最多触发一次，避免「refetch 回来的数据仍带着已过期
 * 冻结」时反复刷新。调用方应把 onDue 用于 refetch 对应查询。
 */
export function useDeadlineRefresh(deadlines: readonly DeadlineValue[] | null | undefined, onDue: () => void): void {
  const onDueRef = useRef(onDue);
  useEffect(() => {
    onDueRef.current = onDue;
  }, [onDue]);

  // 把列表规范化成稳定字符串作为 effect 依赖：父组件每次渲染都会新建数组，
  // 但内容没变时不应重排 timer。
  const key = [...new Set((deadlines ?? [])
    .map(deadlineValue)
    .filter((value): value is number => value !== null))]
    .sort((a, b) => a - b)
    .join(",");

  // 已触发过的 deadline 在本次挂载内不再触发；列表里同时存在过期项时，按从早到晚
  // 各触发一次（例如打开页面时已有两条过期冻结，刷新后仍有残留）。
  const firedRef = useRef<Set<number>>(new Set());

  useEffect(() => {
    if (!key) return;
    const timestamps = key.split(",").map(Number).filter((value) => Number.isFinite(value));
    let timer = 0;
    let cancelled = false;

    const schedule = (due: number) => {
      if (cancelled) return;
      const remaining = due - Date.now() + DEADLINE_GRACE_MS;
      if (remaining > MAX_TIMEOUT_MS) {
        timer = window.setTimeout(() => schedule(due), MAX_TIMEOUT_MS);
        return;
      }
      timer = window.setTimeout(() => {
        if (cancelled) return;
        firedRef.current.add(due);
        onDueRef.current();
        // 若 refetch 回来的数据仍含其它未来冻结，继续等下一最近的截止点；
        // 已过期的项不追打请求，交给同一次 refetch 一起清掉。
        const next = timestamps.find((value) => !firedRef.current.has(value) && value > Date.now());
        if (next !== undefined) schedule(next);
      }, Math.max(0, remaining));
    };

    const first = timestamps.find((value) => !firedRef.current.has(value));
    if (first !== undefined) schedule(first);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [key]);
}
