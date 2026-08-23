import { useEffect, useMemo, useState } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { CheckIcon, ChevronDownIcon, SearchIcon } from "lucide-react";
import { api } from "../api";
import type { Region } from "../types";
import type { Language } from "../translations";
import { copy } from "./shared";

type RegionOption = Region & {
  label: string;
  searchText: string;
};

function normalizedSearch(value: string) {
  return value.normalize("NFKD").replace(/\p{Diacritic}/gu, "").toLocaleLowerCase();
}

export function regionDisplayName(code: string, name: string) {
  const normalizedCode = code.trim().toUpperCase();
  const normalizedName = name.trim();
  if (!normalizedCode || !normalizedName) return normalizedName;
  return `${regionFlag(normalizedCode)} ${regionName(normalizedCode, ["zh-CN"])}${normalizedName}`;
}

export function regionBaseName(displayName: string, code?: string) {
  if (!code) return displayName;
  const normalizedCode = code.toUpperCase();
  const chineseName = regionName(normalizedCode, ["zh-CN"]);
  const prefixes = [
    `${regionFlag(normalizedCode)} ${chineseName}`,
    `${regionFlag(normalizedCode)} ${normalizedCode} · `,
    `${regionFlag(normalizedCode)} ${normalizedCode} `,
  ].filter(Boolean);
  const prefix = prefixes.find((candidate) => displayName.startsWith(candidate));
  return prefix ? displayName.slice(prefix.length).trim() : displayName;
}

function regionName(code: string, locales: string[]) {
  if (!/^[A-Z]{2}$/.test(code)) return code;
  try {
    return new Intl.DisplayNames(locales, { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

function regionFlag(code: string) {
  if (!/^[A-Z]{2}$/.test(code)) return "";
  return String.fromCodePoint(...Array.from(code, (character) => 0x1f1e6 + character.charCodeAt(0) - 65));
}

export function RegionCombobox({ id, language, onValueChange, value }: { id: string; language: Language; onValueChange: (code: string) => void; value: string }) {
  const [regions, setRegions] = useState<Region[]>([]);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void api.regions().then(({ regions: next }) => {
      if (!cancelled) setRegions(next);
    }).catch(() => {
      if (!cancelled) setLoadError(true);
    });
    return () => { cancelled = true; };
  }, []);

  const options = useMemo(() => {
    const localNames = new Intl.DisplayNames([language], { type: "region" });
    const englishNames = new Intl.DisplayNames(["en"], { type: "region" });
    const chineseNames = new Intl.DisplayNames(["zh-CN"], { type: "region" });
    return regions.map((region) => {
      const localName = language === "zh-CN" ? region.nameZh : localNames.of(region.code) ?? region.code;
      const searchText = normalizedSearch([region.code, localName, region.nameZh, englishNames.of(region.code), chineseNames.of(region.code)].filter(Boolean).join(" "));
      return { ...region, label: `${regionFlag(region.code)} ${localName}`, searchText };
    }).sort((left, right) => left.label.localeCompare(right.label, language));
  }, [language, regions]);
  const selected = options.find((option) => option.code === value) ?? null;

  return <div>
    <Combobox.Root
      autoHighlight
      filter={(option, query) => option.searchText.includes(normalizedSearch(query))}
      isItemEqualToValue={(option, selectedOption) => option.code === selectedOption.code}
      itemToStringLabel={(option) => option.label}
      itemToStringValue={(option) => option.code}
      items={options}
      onValueChange={(option) => onValueChange(option?.code ?? "")}
      value={selected}
    >
      <Combobox.InputGroup className="flex h-10 w-full items-center rounded-lg border border-input bg-transparent shadow-xs transition-[color,box-shadow] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50">
        <SearchIcon aria-hidden className="ml-3 size-4 shrink-0 text-muted-foreground" />
        <Combobox.Input aria-label={copy(language, "搜索国家或地区", "Search country or region")} autoComplete="off" className="h-full min-w-0 flex-1 bg-transparent px-2 text-sm outline-none placeholder:text-muted-foreground" id={id} placeholder={copy(language, "搜索中文、英文或 ISO 代码", "Search name or ISO code")} />
        <Combobox.Trigger aria-label={copy(language, "打开地区列表", "Open region list")} className="mr-1 inline-flex size-8 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring">
          <ChevronDownIcon aria-hidden className="size-4" />
        </Combobox.Trigger>
      </Combobox.InputGroup>
      <Combobox.Portal>
        <Combobox.Positioner className="z-50 w-[var(--anchor-width)]" sideOffset={6}>
          <Combobox.Popup className="max-h-72 overflow-hidden rounded-xl border bg-popover p-1 text-popover-foreground shadow-lg outline-none">
            <Combobox.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">{copy(language, "没有匹配的地区", "No matching region")}</Combobox.Empty>
            <Combobox.List className="max-h-64 overflow-y-auto py-1">
              {(option: RegionOption) => <Combobox.Item className="flex min-h-10 cursor-default items-center gap-2 rounded-lg px-3 text-sm outline-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground" key={option.code} value={option}>
                <span className="min-w-0 flex-1 truncate">{option.label}</span>
                <Combobox.ItemIndicator><CheckIcon aria-hidden className="size-4" /></Combobox.ItemIndicator>
              </Combobox.Item>}
            </Combobox.List>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
    {loadError ? <p className="mt-1.5 text-xs text-destructive">{copy(language, "地区列表加载失败，请刷新后重试。", "Could not load regions. Refresh and try again.")}</p> : null}
  </div>;
}
