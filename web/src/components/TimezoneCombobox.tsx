import { Combobox } from "@base-ui/react/combobox";
import { CheckIcon, ChevronsUpDownIcon } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import type { Language } from "../translations";
import { copy } from "../views/shared";

const fallbackTimezones = [
  "UTC",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Europe/London",
  "America/Los_Angeles",
  "America/New_York"
];

const timezoneOptions = (() => {
  const supported = typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : fallbackTimezones;
  return [...new Set(["UTC", ...supported])].sort((left, right) => left.localeCompare(right));
})();

export function TimezoneCombobox({ id, language, value, onValueChange }: { id: string; language: Language; value: string; onValueChange: (value: string) => void }) {
  const [open, setOpen] = useState(false);
  const [inputValue, setInputValue] = useState(value);
  useEffect(() => { if (!open) setInputValue(value); }, [open, value]);
  const visibleOptions = useMemo(() => {
    const query = inputValue.trim().toLowerCase();
    if (!query) return [...new Set([value, ...fallbackTimezones])].filter(Boolean);
    return timezoneOptions.filter((timezone) => timezone.toLowerCase().includes(query)).slice(0, 50);
  }, [inputValue, value]);
  return <Combobox.Root autoHighlight inputValue={inputValue} items={visibleOptions} onInputValueChange={setInputValue} onOpenChange={(next) => { setOpen(next); setInputValue(next ? "" : value); }} onValueChange={(next) => { if (next) { onValueChange(next); setInputValue(next); } }} open={open} openOnInputClick required value={value}>
    <div className="relative">
      <Combobox.Input aria-label={copy(language, "搜索并选择时区", "Search and select a time zone")} className="h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 pr-9 text-base outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 md:text-sm dark:bg-input/30" id={id} placeholder={copy(language, "输入城市或时区，例如 Asia/Shanghai", "Type a city or zone, for example Asia/Shanghai")} />
      <Combobox.Trigger aria-label={copy(language, "打开时区列表", "Open time zone list")} className="absolute inset-y-0 right-1 inline-flex w-8 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring"><ChevronsUpDownIcon aria-hidden="true" className="size-4" /></Combobox.Trigger>
    </div>
    <Combobox.Portal>
      <Combobox.Positioner align="start" className="z-60 outline-none" sideOffset={4}>
        <Combobox.Popup className="w-[var(--anchor-width)] max-w-[calc(100vw-2rem)] origin-[var(--transform-origin)] rounded-lg border bg-popover text-popover-foreground shadow-md outline-none transition-[transform,scale,opacity] data-ending-style:scale-95 data-ending-style:opacity-0 data-starting-style:scale-95 data-starting-style:opacity-0">
          <Combobox.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">{copy(language, "没有匹配的时区", "No matching time zone")}</Combobox.Empty>
          <Combobox.List className="max-h-[min(18rem,var(--available-height))] overflow-y-auto p-1">
            {(timezone: string) => <Combobox.Item className="flex min-h-9 cursor-default items-center gap-2 rounded-md px-2.5 text-sm outline-none select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground" key={timezone} value={timezone}>
              <span className="min-w-0 flex-1 truncate">{timezone}</span>
              <Combobox.ItemIndicator className="text-primary"><CheckIcon aria-hidden="true" className="size-4" /></Combobox.ItemIndicator>
            </Combobox.Item>}
          </Combobox.List>
        </Combobox.Popup>
      </Combobox.Positioner>
    </Combobox.Portal>
  </Combobox.Root>;
}
