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
  resetValue: string;
  renewalDate: string;
  onQuotaChange: (value: string) => void;
  onResetValueChange: (value: string) => void;
  onRenewalDateChange?: (value: string) => void;
  minimumDate?: string;
  level: "inbound" | "subscription";
  compact?: boolean;
};

function RenewingTrafficPlanFields({ idPrefix, language, quota, resetValue, renewalDate, onQuotaChange, onResetValueChange, onRenewalDateChange, level, compact = false, minimumDate }: RenewingTrafficPlanFieldsProps) {
  const inbound = level === "inbound";
  const renewalEnabled = Number(resetValue) > 0;
  const Icon = inbound ? GaugeIcon : CalendarSyncIcon;
  const title = inbound ? copy(language, "VPS 月流量套餐", "VPS monthly traffic plan") : copy(language, "客户端额度（可选）", "Client allowance (optional)");
  const description = inbound
    ? copy(language, "填写 VPS 服务商提供的月度总流量。Vastora 按上传 + 下载合计用量，并在每月账单日只清零这个节点。", "Enter the VPS provider's monthly allowance. Vastora counts upload plus download and resets only this node on its monthly billing day.")
    : copy(language, "这是额外的用户级限制，不是 VPS 套餐。订阅会自动携带上传、下载、总额度和到期信息，支持的客户端会显示已用与剩余流量。", "This is an optional user-level cap, not the VPS plan. The subscription automatically includes upload, download, total, and expiry metadata for compatible clients.");
  return <FieldSet className={compact ? "grid gap-4" : "rounded-2xl border bg-muted/20 p-4"}>
    {!compact ? <>
      <FieldLegend className="flex items-center gap-2"><Icon className="size-4 text-muted-foreground" aria-hidden="true" />{title}</FieldLegend>
      <FieldDescription>{description}</FieldDescription>
    </> : null}
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-quota`}>{inbound ? copy(language, "节点总流量（GB）", "Node traffic allowance (GB)") : copy(language, "订阅总流量（GB）", "Total subscription traffic (GB)")}</FieldLabel>
        <Input id={`${idPrefix}-quota`} inputMode="decimal" min="0" onChange={(event) => onQuotaChange(event.target.value)} placeholder={copy(language, "留空表示不限", "Leave empty for unlimited")} step="0.1" type="number" value={quota} />
        {inbound ? <FieldDescription>{copy(language, "按服务商标注填写；上下行流量合计计费。", "Use the provider's advertised quota; upload and download are billed together.")}</FieldDescription> : null}
      </Field>
      <Field>
        <FieldLabel htmlFor={`${idPrefix}-${inbound ? "reset-day" : "reset-days"}`}>{inbound ? copy(language, "每月重置日", "Monthly reset day") : copy(language, "自动续期（天）", "Auto-renew every (days)")}</FieldLabel>
        <Input id={`${idPrefix}-${inbound ? "reset-day" : "reset-days"}`} inputMode="numeric" max={inbound ? "31" : "3650"} min="0" onChange={(event) => onResetValueChange(event.target.value)} placeholder={inbound ? copy(language, "例如 1；留空不重置", "For example, 1; blank disables reset") : copy(language, "0 表示不自动续期", "0 disables auto-renewal")} step="1" type="number" value={resetValue} />
        {inbound ? <FieldDescription>{copy(language, "按节点所在地时区执行；填 31 时，短月份使用最后一天。", "Uses the node location timezone; day 31 means the last day in shorter months.")}</FieldDescription> : null}
      </Field>
    </div>
    {inbound && !renewalDate ? <FieldDescription>{renewalEnabled
      ? copy(language, "保存后，Center 会按节点所在地时区计算首次重置日期。", "After saving, Center calculates the first reset date in the node location timezone.")
      : copy(language, "未开启每月重置；这个节点的已用流量不会自动清零。", "Monthly reset is off, so this node's usage will not reset automatically.")}</FieldDescription> : <Field>
        <FieldLabel htmlFor={`${idPrefix}-renewal-date`}>{inbound || renewalEnabled ? copy(language, "下次重置日", "Next reset date") : copy(language, "到期日期", "Expiry date")}</FieldLabel>
        <Input className="sm:max-w-64" id={`${idPrefix}-renewal-date`} min={minimumDate ?? localDateInputValue(new Date())} onChange={onRenewalDateChange ? (event) => onRenewalDateChange(event.target.value) : undefined} readOnly={!onRenewalDateChange} required={Boolean(onRenewalDateChange) && renewalEnabled} type="date" value={renewalDate} />
        <FieldDescription>{renewalEnabled
          ? inbound
            ? copy(language, "该日期由 Center 按位置时区计算。到期后 Vastora 只重置这个入站，不会清零订阅用户流量。", "Center calculated this date in the location timezone. Vastora resets only this inbound without clearing subscriber usage.")
            : copy(language, "到达该日期后，3x-ui 会清零用户用量、重新启用客户端，并把日期向后顺延。", "On this date, 3x-ui clears subscriber usage, re-enables the client, and advances the date.")
          : inbound
            ? copy(language, "未开启每月重置；这个节点的已用流量不会自动清零。", "Monthly reset is off, so this node's usage will not reset automatically.")
            : copy(language, "留空表示永不过期。", "Leave empty to never expire.")}</FieldDescription>
      </Field>}
  </FieldSet>;
}

export function InboundTrafficPlanFields({ idPrefix, language, quota, resetDay, nextResetAt, onQuotaChange, onResetDayChange }: {
  idPrefix: string;
  language: Language;
  quota: string;
  resetDay: string;
  nextResetAt: string;
  onQuotaChange: (value: string) => void;
  onResetDayChange: (value: string) => void;
}) {
  return <RenewingTrafficPlanFields idPrefix={idPrefix} language={language} level="inbound" onQuotaChange={onQuotaChange} onResetValueChange={onResetDayChange} quota={quota} renewalDate={nextResetAt} resetValue={resetDay} />;
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
  return <RenewingTrafficPlanFields compact={compact} idPrefix={idPrefix} language={language} level="subscription" minimumDate={minimumDate} onQuotaChange={onQuotaChange} onRenewalDateChange={onExpiryChange} onResetValueChange={onResetDaysChange} quota={quota} renewalDate={expiry} resetValue={resetDays} />;
}
