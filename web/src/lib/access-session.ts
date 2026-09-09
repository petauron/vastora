import type { Language } from "../translations";

const durations = [
  ["15m", "15 分钟", "15 minutes"],
  ["30m", "30 分钟", "30 minutes"],
  ["1h", "1 小时", "1 hour"],
  ["6h", "6 小时", "6 hours"],
  ["12h", "12 小时", "12 hours"],
  ["24h", "24 小时（默认）", "24 hours (default)"],
  ["48h", "2 天", "2 days"],
  ["72h", "3 天", "3 days"],
  ["168h", "7 天", "7 days"],
  ["720h", "30 天", "30 days"],
] as const;

export function accessSessionOptions(language: Language) {
  return durations.map(([value, zh, en]) => ({ value, label: language === "zh-CN" ? zh : en }));
}

export function validAccessSessionDuration(value: string) {
  return durations.some(([duration]) => duration === value);
}
