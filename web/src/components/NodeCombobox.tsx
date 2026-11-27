import { Fragment, useEffect, useMemo, useState } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { CheckIcon, ChevronsUpDownIcon, MapPinIcon, SearchIcon } from "lucide-react";
import type { AgentView, Site } from "@/types";
import type { Language } from "@/translations";
import { siteRegionCode } from "@/lib/regions";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { RegionFlag } from "@/views/RegionFlag";
import { copy } from "@/views/shared";

type NodeOption = {
  id: string;
  name: string;
  siteId: string;
  siteName: string;
  regionCode: string;
  architecture: string;
  landingOnly: boolean;
  searchText: string;
  isGroupStart: boolean;
  groupCount: number;
};

export function NodeCombobox({
  id,
  language,
  nodes,
  sites,
  value,
  onValueChange,
  disabled = false,
  required = false,
  fallbackLabel,
  "aria-describedby": ariaDescribedBy,
  "aria-invalid": ariaInvalid,
}: {
  id: string;
  language: Language;
  nodes: AgentView[];
  sites: Site[];
  value: string;
  onValueChange: (agentId: string) => void;
  disabled?: boolean;
  required?: boolean;
  fallbackLabel?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  const siteByID = useMemo(() => new Map(sites.map((site) => [site.id, site])), [sites]);

  const allOptions = useMemo(() => {
    const list = nodes.map((agent) => {
      const site = siteByID.get(agent.siteId);
      const siteName = site?.name ?? agent.siteId;
      const regionCode = site ? siteRegionCode(site) : siteRegionCode({ name: siteName });
      const architecture = agent.architecture === "arm64" ? "ARM64" : "x64";
      return {
        id: agent.id,
        name: agent.name,
        siteId: agent.siteId,
        siteName,
        regionCode,
        architecture,
        landingOnly: Boolean(agent.capabilities.landingOnly),
        searchText: [agent.name, siteName, site?.code, architecture].filter(Boolean).join(" ").toLocaleLowerCase(),
      };
    });
    return list.sort((left, right) => left.siteName.localeCompare(right.siteName, language) || left.name.localeCompare(right.name, language));
  }, [nodes, siteByID, language]);

  const selectedOption = allOptions.find((option) => option.id === value) ?? null;
  const displayValue = selectedOption?.name ?? fallbackLabel ?? "";

  const [open, setOpen] = useState(false);
  const [inputValue, setInputValue] = useState(displayValue);

  useEffect(() => {
    if (!open) setInputValue(displayValue);
  }, [open, displayValue]);

  const visibleOptions: NodeOption[] = useMemo(() => {
    const query = open ? inputValue.trim().toLocaleLowerCase() : "";
    const filtered = query
      ? allOptions.filter((option) => option.searchText.includes(query))
      : allOptions;
    const siteCounts = new Map<string, number>();
    for (const item of filtered) {
      siteCounts.set(item.siteId, (siteCounts.get(item.siteId) ?? 0) + 1);
    }
    const seenSites = new Set<string>();
    return filtered.map((item) => {
      const isGroupStart = !seenSites.has(item.siteId);
      seenSites.add(item.siteId);
      return {
        ...item,
        isGroupStart,
        groupCount: siteCounts.get(item.siteId) ?? 1,
      };
    });
  }, [allOptions, inputValue, open]);

  const selectedItem: NodeOption | null = selectedOption
    ? { ...selectedOption, isGroupStart: false, groupCount: 1 }
    : null;
  const multipleSites = new Set(allOptions.map((option) => option.siteId)).size > 1;

  return (
    <Combobox.Root
      autoHighlight
      disabled={disabled}
      filter={() => true}
      inputValue={inputValue}
      isItemEqualToValue={(option, selected) => option.id === selected.id}
      itemToStringLabel={(option) => option.name}
      itemToStringValue={(option) => option.id}
      items={visibleOptions}
      onInputValueChange={setInputValue}
      onOpenChange={(next, eventDetails) => {
        setOpen(next);
        if (next && eventDetails?.reason !== "input-change") setInputValue("");
      }}
      onValueChange={(option) => {
        if (option) {
          onValueChange(option.id);
          setInputValue(option.name);
        }
      }}
      open={open}
      openOnInputClick
      required={required}
      value={selectedItem}
    >
      <div className="relative">
        <SearchIcon aria-hidden="true" className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Combobox.Input
          aria-describedby={ariaDescribedBy}
          aria-invalid={ariaInvalid}
          aria-label={copy(language, "搜索并选择节点", "Search and select a node")}
          autoComplete="off"
          className={cn(
            "h-11 w-full min-w-0 rounded-lg border border-input bg-transparent py-1 pr-12 pl-8 text-base outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:bg-muted disabled:opacity-70 md:h-8 md:pr-9 md:text-sm dark:bg-input/30",
            ariaInvalid && "border-destructive",
          )}
          data-slot="input"
          disabled={disabled}
          id={id}
          placeholder={copy(language, "搜索节点名或站点…", "Search node name or site…")}
        />
        <Combobox.Trigger
          aria-label={copy(language, "打开节点列表", "Open node list")}
          className="absolute inset-y-0 right-1 inline-flex w-11 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 md:w-8 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
          disabled={disabled}
        >
          <ChevronsUpDownIcon aria-hidden="true" className="size-4" />
        </Combobox.Trigger>
      </div>
      <Combobox.Portal>
        <Combobox.Positioner align="start" className="z-60 outline-none" sideOffset={4}>
          <Combobox.Popup className="w-[var(--anchor-width)] max-w-[calc(100vw-2rem)] origin-[var(--transform-origin)] rounded-lg border bg-popover text-popover-foreground shadow-md outline-none transition-[transform,scale,opacity] data-ending-style:scale-95 data-ending-style:opacity-0 data-starting-style:scale-95 data-starting-style:opacity-0">
            <Combobox.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
              {nodes.length === 0
                ? copy(language, "没有可用节点", "No eligible nodes")
                : copy(language, "没有匹配的节点", "No matching node")}
            </Combobox.Empty>
            <Combobox.List className="max-h-[min(18rem,var(--available-height))] overflow-y-auto p-1">
              {(option: NodeOption) => (
                <Fragment key={option.id}>
                  {option.isGroupStart && multipleSites ? (
                    <div
                      className="flex items-center gap-1.5 px-2.5 pt-2 pb-1 text-[11px] font-medium text-muted-foreground first:pt-1"
                      role="presentation"
                    >
                      {option.regionCode ? (
                        <RegionFlag code={option.regionCode} language={language} />
                      ) : (
                        <MapPinIcon aria-hidden="true" className="size-3 shrink-0" />
                      )}
                      <span className="truncate">{option.siteName}</span>
                      <span className="tabular-nums opacity-70">· {option.groupCount}</span>
                    </div>
                  ) : null}
                  <Combobox.Item
                    className="flex min-h-11 cursor-default items-center gap-2 rounded-md px-2.5 text-sm outline-none select-none data-highlighted:bg-accent data-highlighted:text-accent-foreground md:min-h-9 [@media(pointer:coarse)]:min-h-11"
                    data-slot="select-item"
                    value={option}
                  >
                    <span className="min-w-0 flex-1 truncate font-medium">{option.name}</span>
                    <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                      {!multipleSites && option.siteName ? <span className="max-w-28 truncate">{option.siteName}</span> : null}
                      {option.landingOnly ? <Badge className="text-[10px]" variant="secondary">{copy(language, "仅落地", "Landing")}</Badge> : null}
                      <span className="tabular-nums">{option.architecture}</span>
                    </span>
                    <Combobox.ItemIndicator className="text-primary">
                      <CheckIcon aria-hidden="true" className="size-4" />
                    </Combobox.ItemIndicator>
                  </Combobox.Item>
                </Fragment>
              )}
            </Combobox.List>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  );
}
