import { CalendarSyncIcon, GaugeIcon } from "lucide-react";
import type { Language } from "../translations";
import { copy } from "./shared";
import { Field, FieldDescription, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

export const gibibyte = 1024 ** 3;

export function bytesFromGB(value: string): number {
  if (!value.trim()) return 0;
  return Math.round(Number(value) * gibibyte);
}

export function gigabytesFromBytes(value?: number): string {
  if (!value) return "";
  return String(Math.round(value / gibibyte * 100) / 100);
}

export function formatBytes(value?: number): string {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${(value / 1024 ** index).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

export function nextRenewalDate(days: number): string {
  if (!Number.isInteger(days) || days <= 0) return "";
  const next = new Date();
  next.setHours(12, 0, 0, 0);
  next.setDate(next.getDate() + days);
  return localDateInputValue(next);
}

type CalendarParts = { year: number; month: number; day: number; hour?: number; minute?: number; second?: number };

function calendarPartsInTimeZone(value: Date, timeZone?: string): CalendarParts | null {
  if (!timeZone) return null;
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      day: "2-digit",
      hour: "2-digit",
      hourCycle: "h23",
      minute: "2-digit",
      month: "2-digit",
      second: "2-digit",
      timeZone,
      year: "numeric"
    }).formatToParts(value);
    const number = (type: string) => Number(parts.find((part) => part.type === type)?.value);
    const year = number("year");
    const month = number("month");
    const day = number("day");
    if (!year || !month || !day) return null;
    return { year, month, day, hour: number("hour"), minute: number("minute"), second: number("second") };
  } catch {
    return null;
  }
}

export function nextRenewalDateInTimeZone(days: number, timeZone?: string): string {
  if (!Number.isInteger(days) || days <= 0) return "";
  const today = calendarPartsInTimeZone(new Date(), timeZone);
  if (!today) return nextRenewalDate(days);
  const next = new Date(Date.UTC(today.year, today.month - 1, today.day + days));
  return `${next.getUTCFullYear()}-${String(next.getUTCMonth() + 1).padStart(2, "0")}-${String(next.getUTCDate()).padStart(2, "0")}`;
}

export function endOfDayEpochInTimeZone(value: string, timeZone?: string): number {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return Number.NaN;
  const desired = { year: Number(match[1]), month: Number(match[2]), day: Number(match[3]), hour: 23, minute: 59, second: 59 };
  if (!timeZone) return new Date(`${value}T23:59:59`).getTime();
  let epoch = Date.UTC(desired.year, desired.month - 1, desired.day, desired.hour, desired.minute, desired.second);
  for (let attempt = 0; attempt < 3; attempt += 1) {
    const actual = calendarPartsInTimeZone(new Date(epoch), timeZone);
    if (!actual) return new Date(`${value}T23:59:59`).getTime();
    const delta = Date.UTC(desired.year, desired.month - 1, desired.day, desired.hour, desired.minute, desired.second) - Date.UTC(actual.year, actual.month - 1, actual.day, actual.hour ?? 0, actual.minute ?? 0, actual.second ?? 0);
    if (delta === 0) return epoch;
    epoch += delta;
  }
  return epoch;
}

export function localDateInputValue(value: Date | number | string): string {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 10);
}

export function dateInputValueInTimeZone(value: Date | number | string, timeZone?: string): string {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  if (!timeZone) return date.toISOString().slice(0, 10);
  try {
    const parts = new Intl.DateTimeFormat("en-US", { day: "2-digit", month: "2-digit", year: "numeric", timeZone }).formatToParts(date);
    const part = (type: "year" | "month" | "day") => parts.find((valuePart) => valuePart.type === type)?.value ?? "";
    const year = part("year");
    const month = part("month");
    const day = part("day");
    return year && month && day ? `${year}-${month}-${day}` : "";
  } catch {
    return date.toISOString().slice(0, 10);
  }
}

type RenewingTrafficPlanFieldsProps = {
  idPrefix: string;
  language: Language;
  quota: string;
  resetDays: string;
  renewalDate: string;
  onQuotaChange: (value: string) => void;
  onResetDaysChange: (value: string) => void;
  onRenewalDateChange?: (value: string) => void;
  minimumDate?: string;
  level: "inbound" | "subscription";
  compact?: boolean;
};

function RenewingTrafficPlanFields({ idPrefix, language, quota, resetDays, renewalDate, onQuotaChange, onResetDaysChange, onRenewalDateChange, level, compact = false, minimumDate }: RenewingTrafficPlanFieldsProps) {
  const inbound = level === "inbound";
  const renewalEnabled = Number(resetDays) > 0;
  const Icon = inbound ? GaugeIcon : CalendarSyncIcon;
  const title = inbound ? copy(language, "当前节点套餐", "Current node plan") : copy(language, "此客户端的订阅总额度（所有节点合计）", "This client's subscription allowance (all nodes combined)");
  const description = inbound
    ? copy(language, "只统计这个 VLESS 入站的流量。其他节点使用各自的独立套餐。", "Only traffic on this VLESS inbound counts here. Other nodes keep independent plans.")
    : copy(language, "这个额度只属于当前客户端，由订阅主机统一管理；它会合并计算该客户端在所有 VLESS 节点上的用量。", "This allowance belongs only to this client. The subscription controller combines this client's usage across every VLESS node.");
  return <FieldSet className={compact ? "grid gap-4" : "rounded-2xl border bg-muted/20 p-4"}>
    {!compact ? <>
      <FieldLegend className="flex items-center gap-2"><Icon className="size-4 text-muted-foreground" aria-hidden="true" />{title}</FieldLegend>
      <FieldDescription>{description}</FieldDescription>
    </> : null}
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-quota`}>{inbound ? copy(language, "节点总流量（GB）", "Node traffic allowance (GB)") : copy(language, "订阅总流量（GB）", "Total subscription traffic (GB)")}</FieldLabel>
        <Input id={`${idPrefix}-quota`} inputMode="decimal" min="0" onChange={(event) => onQuotaChange(event.target.value)} placeholder={copy(language, "留空表示不限", "Leave empty for unlimited")} step="0.1" type="number" value={quota} />
      </Field>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-reset-days`}>{copy(language, "自动续期（天）", "Auto-renew every (days)")}</FieldLabel>
        <Input id={`${idPrefix}-reset-days`} inputMode="numeric" max="3650" min="0" onChange={(event) => onResetDaysChange(event.target.value)} placeholder={copy(language, "0 表示不自动续期", "0 disables auto-renewal")} step="1" type="number" value={resetDays} />
      </Field>
    </div>
    <Field>
      <FieldLabel htmlFor={`${idPrefix}-renewal-date`}>{inbound || renewalEnabled ? copy(language, "下次续期日", "Next renewal date") : copy(language, "到期日期", "Expiry date")}</FieldLabel>
      <Input className="sm:max-w-64" id={`${idPrefix}-renewal-date`} min={minimumDate ?? localDateInputValue(new Date())} onChange={onRenewalDateChange ? (event) => onRenewalDateChange(event.target.value) : undefined} readOnly={!onRenewalDateChange} required={Boolean(onRenewalDateChange) && renewalEnabled} type="date" value={renewalDate} />
      <FieldDescription>{renewalEnabled
        ? inbound
          ? renewalDate
            ? copy(language, "该日期由 Center 按位置时区计算。到期后 Vastora 只重置这个入站，不会清零订阅用户流量。", "Center calculated this date in the location timezone. Vastora resets only this inbound without clearing subscriber usage.")
            : copy(language, "保存后，Center 会按位置时区计算首次续期日；不会使用当前浏览器时区推测。", "After saving, Center calculates the first renewal date in the location timezone instead of guessing from the browser timezone.")
          : copy(language, "到达该日期后，3x-ui 会清零用户用量、重新启用客户端，并把日期向后顺延。", "On this date, 3x-ui clears subscriber usage, re-enables the client, and advances the date.")
        : inbound
          ? copy(language, "未开启自动续期；这个节点的已用流量不会自动清零。", "Auto-renewal is off, so this node's usage will not reset automatically.")
          : copy(language, "留空表示永不过期。", "Leave empty to never expire.")}</FieldDescription>
    </Field>
  </FieldSet>;
}

export function InboundTrafficPlanFields({ idPrefix, language, quota, resetDays, nextResetAt, onQuotaChange, onResetDaysChange }: {
  idPrefix: string;
  language: Language;
  quota: string;
  resetDays: string;
  nextResetAt: string;
  onQuotaChange: (value: string) => void;
  onResetDaysChange: (value: string) => void;
}) {
  return <RenewingTrafficPlanFields idPrefix={idPrefix} language={language} level="inbound" onQuotaChange={onQuotaChange} onResetDaysChange={onResetDaysChange} quota={quota} renewalDate={nextResetAt} resetDays={resetDays} />;
}

export function SubscriptionTrafficPlanFields({ idPrefix, language, quota, resetDays, expiry, onQuotaChange, onResetDaysChange, onExpiryChange, compact = false, minimumDate }: {
  idPrefix: string;
  language: Language;
  quota: string;
  resetDays: string;
  expiry: string;
  onQuotaChange: (value: string) => void;
  onResetDaysChange: (value: string) => void;
  onExpiryChange: (value: string) => void;
  compact?: boolean;
  minimumDate?: string;
}) {
  return <RenewingTrafficPlanFields compact={compact} idPrefix={idPrefix} language={language} level="subscription" minimumDate={minimumDate} onQuotaChange={onQuotaChange} onRenewalDateChange={onExpiryChange} onResetDaysChange={onResetDaysChange} quota={quota} renewalDate={expiry} resetDays={resetDays} />;
}
