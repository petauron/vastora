import { useId } from "react";
import { cn } from "@/lib/utils";

type Artwork = "desktop" | "store" | "server" | "gear" | "network" | "chart" | "clock" | "assistant" | "compass" | "pulse" | "probe" | "radar" | "plane" | "gateway" | "folder" | "globe" | "update" | "backup" | "package" | "window";
type IconDefinition = { artwork: Artwork; colors: readonly [string, string] };
const silver = ["#f8fafc", "#c6cbd2"] as const;
const blue = ["#39baff", "#0061dc"] as const;
const violet = ["#b796ff", "#6533cc"] as const;
const teal = ["#35d4df", "#00718f"] as const;
const icons: Record<string, IconDefinition> = {
  home: { artwork: "desktop", colors: silver },
  apps: { artwork: "store", colors: blue },
  nodes: { artwork: "server", colors: silver },
  settings: { artwork: "gear", colors: silver },
  network: { artwork: "network", colors: violet },
  overview: { artwork: "chart", colors: silver },
  activity: { artwork: "clock", colors: ["#53d89a", "#129e62"] },
  assistant: { artwork: "assistant", colors: violet },
  updates: { artwork: "update", colors: blue },
  backup: { artwork: "backup", colors: ["#62d9a0", "#159468"] },
  catalog: { artwork: "package", colors: ["#ffc366", "#ee8b20"] },
  "vastora-official/meridian": { artwork: "compass", colors: teal },
  "vastora-official/pulse": { artwork: "pulse", colors: ["#35454d", "#111b21"] },
  "vastora-official/pulse-agent": { artwork: "probe", colors: teal },
  "vastora-official/komari-agent": { artwork: "radar", colors: violet },
  "vastora-official/telegram-bot": { artwork: "plane", colors: blue },
  "vastora-official/headscale": { artwork: "network", colors: violet },
  "vastora-official/cpa": { artwork: "gateway", colors: blue },
  "vastora-official/keeper": { artwork: "chart", colors: ["#eef7ff", "#c9e5ff"] },
  "vastora-official/filebrowser": { artwork: "folder", colors: blue },
  "vastora-official/3x-ui": { artwork: "globe", colors: teal },
};

// Native SVG artwork keeps the same silhouette at menu, Dock and desktop sizes.
// Per-instance IDs avoid gradient collisions when one app appears in several places.
export function AppIcon({ appKey, className }: { appKey: string; className?: string }) {
  const id = useId();
  const { artwork, colors }: IconDefinition = icons[appKey] ?? { artwork: "window", colors: silver };
  return <span aria-hidden="true" className={cn("desktop-app-icon size-14", className)}>
    <svg viewBox="0 0 64 64" fill="none" focusable="false">
      <defs>
        <linearGradient id={`${id}-tile`} x1="32" y1="2" x2="32" y2="62" gradientUnits="userSpaceOnUse"><stop stopColor={colors[0]} /><stop offset="1" stopColor={colors[1]} /></linearGradient>
        <linearGradient id={`${id}-light`} x1="16" y1="12" x2="46" y2="52" gradientUnits="userSpaceOnUse"><stop stopColor="#fff" /><stop offset="1" stopColor="#d6e4f4" /></linearGradient>
        <linearGradient id={`${id}-metal`} x1="32" y1="12" x2="32" y2="52" gradientUnits="userSpaceOnUse"><stop stopColor="#f7f9fb" /><stop offset=".48" stopColor="#bac3cb" /><stop offset="1" stopColor="#88939e" /></linearGradient>
        <linearGradient id={`${id}-blue`} x1="24" y1="18" x2="40" y2="50" gradientUnits="userSpaceOnUse"><stop stopColor="#83dcff" /><stop offset="1" stopColor="#1478e8" /></linearGradient>
        <linearGradient id={`${id}-sheen`} x1="32" y1="2" x2="32" y2="62" gradientUnits="userSpaceOnUse"><stop stopColor="#fff" stopOpacity=".55" /><stop offset=".5" stopColor="#fff" stopOpacity=".04" /><stop offset="1" stopColor="#000" stopOpacity=".12" /></linearGradient>
      </defs>
      <rect x="2" y="2" width="60" height="60" rx="14" fill={`url(#${id}-tile)`} />
      <rect x="2.5" y="2.5" width="59" height="59" rx="13.5" stroke={`url(#${id}-sheen)`} />
      <g className="desktop-app-artwork">{drawArtwork(artwork, id)}</g>
    </svg>
  </span>;
}

function drawArtwork(artwork: Artwork, id: string) {
  const light = `url(#${id}-light)`;
  const metal = `url(#${id}-metal)`;
  const blueFill = `url(#${id}-blue)`;
  switch (artwork) {
    case "desktop": return <>
      <path d="M26 44h12l2 9H24l2-9Z" fill={metal} /><rect x="20" y="51" width="24" height="3" rx="1.5" fill="#89939e" />
      <rect x="9" y="12" width="46" height="34" rx="5" fill="#53616f" /><rect x="11" y="14" width="42" height="27" rx="3" fill={blueFill} />
      <path d="M11 36 24 23l13 10 8-6 8 8v6H11Z" fill="#3266af" /><path d="m11 40 17-9 10 7 15-7v10H11Z" fill="#d4e9ff" fillOpacity=".75" />
      <circle cx="43" cy="21" r="4" fill="#fff5d7" />
    </>;
    case "store": return <>
      <path d="M17 23h30l4 27H13l4-27Z" fill={light} /><path d="M24 24v-5a8 8 0 0 1 16 0v5" stroke="#eaf7ff" strokeWidth="3.5" strokeLinecap="round" />
      <rect x="22" y="31" width="8" height="8" rx="2" fill="#188eee" /><rect x="33" y="31" width="8" height="8" rx="2" fill="#44bafa" /><rect x="22" y="42" width="8" height="4" rx="1.5" fill="#79caf9" /><rect x="33" y="42" width="8" height="4" rx="1.5" fill="#258ded" />
    </>;
    case "server": return <>
      <rect x="11" y="12" width="42" height="41" rx="6" fill="#687580" />
      {[14, 27, 40].map((y) => <g key={y}><rect x="12" y={y} width="40" height="11" rx="3" fill={metal} /><path d={`M17 ${y + 5.5}h12`} stroke="#65727e" strokeWidth="2" strokeLinecap="round" /><circle cx="45" cy={y + 5.5} r="2" fill="#5dd572" /><circle cx="45" cy={y + 5} r=".8" fill="#d5ffc8" /></g>)}
    </>;
    case "gear": return <>
      <circle cx="32" cy="32" r="23" fill="#59616b" /><circle cx="32" cy="32" r="20.5" stroke="#e4e8ed" strokeWidth="1.5" />
      {[0, 45, 90, 135, 180, 225, 270, 315].map((angle) => <rect key={angle} x="28" y="10" width="8" height="11" rx="1.5" transform={`rotate(${angle} 32 32)`} fill={metal} />)}
      <circle cx="32" cy="32" r="17" fill={metal} />
      <circle cx="32" cy="32" r="12" fill="#636e78" stroke="#f4f7fa" strokeWidth="1.5" /><circle cx="32" cy="32" r="6.5" fill={metal} />
    </>;
    case "network": return <>
      <path d="M32 17 15 45h34L32 17Z" stroke="#e0d4ff" strokeWidth="3" strokeLinejoin="round" />
      {[[32, 17], [15, 45], [49, 45]].map(([cx, cy]) => <circle key={cx} cx={cx} cy={cy} r="7" fill={light} />)}
      <circle cx="32" cy="17" r="3" fill="#af8cfa" />
    </>;
    case "chart": return <>
      <rect x="12" y="32" width="11" height="19" rx="2.5" fill="#138ef1" /><rect x="27" y="14" width="11" height="37" rx="2.5" fill="#37c971" /><rect x="42" y="24" width="11" height="27" rx="2.5" fill="#ffa32c" />
      <path d="M14 34h7M29 16h7M44 26h7" stroke="#fff" strokeOpacity=".4" strokeLinecap="round" />
    </>;
    case "clock": return <>
      <circle cx="32" cy="32" r="21" fill={light} /><circle cx="32" cy="32" r="18" stroke="#c7d6db" />
      <path d="M32 18v14l10 6" stroke="#354753" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round" /><circle cx="32" cy="32" r="2.5" fill="#169a62" />
    </>;
    case "assistant": return <>
      <path d="M30 11c2 12 7 17 19 19-12 2-17 7-19 19-2-12-7-17-19-19 12-2 17-7 19-19Z" fill={light} /><path d="M48 38c1 6 3 8 9 9-6 1-8 3-9 9-1-6-3-8-9-9 6-1 8-3 9-9Z" fill="#d5c8ff" />
    </>;
    case "compass": return <>
      <circle cx="32" cy="32" r="23" fill="#004d73" fillOpacity=".35" stroke="#9be7ed" strokeWidth="1.5" /><circle cx="32" cy="32" r="20" stroke="#c8ffff" strokeOpacity=".35" />
      <path d="M32 10v4m0 36v4M10 32h4m36 0h4" stroke="#c8ffff" strokeWidth="1.5" />
      <path d="m47 16-9 23-23 9 9-23 23-9Z" fill={light} /><path d="m15 48 17-16 6 7-23 9Z" fill="#93bed9" /><circle cx="32" cy="32" r="2" fill="#fff" />
    </>;
    case "pulse": return <>
      <path d="M10 22h44M10 32h44M10 42h44M22 12v40m10-40v40m10-40v40" stroke="#8aada0" strokeOpacity=".13" />
      <path d="M10 36h8l5-13 7 24 7-33 6 27 4-12 4 7h3" stroke="#85e55c" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
    </>;
    case "probe": return <>
      <path d="M19 39a18 18 0 1 1 26 0M25 33a10 10 0 1 1 14 0" stroke="#d8fbff" strokeWidth="3" strokeLinecap="round" /><circle cx="32" cy="25" r="5" fill={light} /><path d="m29 29-5 22h16l-5-22Z" fill={light} />
    </>;
    case "radar": return <>
      <circle cx="32" cy="32" r="22" fill="#35216f" fillOpacity=".45" stroke="#e0d5ff" strokeWidth="1.5" /><circle cx="32" cy="32" r="14" stroke="#c4b2f4" /><path d="M32 10v44M10 32h44" stroke="#c4b2f4" strokeOpacity=".6" /><path d="M32 32V10a22 22 0 0 1 21 16Z" fill="#d3c5ff" fillOpacity=".6" /><circle cx="41" cy="22" r="3" fill="#fff" /><circle cx="23" cy="38" r="2" fill="#d8e8ff" />
    </>;
    case "plane": return <>
      <path d="M12 30 51 14 42 51 30 39l-7 7-1-12-10-4Z" fill={light} /><path d="m22 34 24-16-16 21-7 7-1-12Z" fill="#9ec8e9" /><path d="m26 36 20-18-16 21-4-3Z" fill="#dbefff" />
    </>;
    case "gateway": return <>
      <path d="m14 27 12-12 10 10-6 6-4-4-6 6v8l7 7-6 6L8 43V33Z" fill="#c4efff" /><path d="m50 37-12 12-10-10 6-6 4 4 6-6v-8l-7-7 6-6 13 13v10Z" fill={blueFill} /><path d="m24 38 14-14" stroke="#e1f5ff" strokeWidth="6" strokeLinecap="round" />
    </>;
    case "folder": return <>
      <path d="M10 21a4 4 0 0 1 4-4h13l5 5h18a4 4 0 0 1 4 4v23H10Z" fill="#0a6ccc" /><rect x="13" y="26" width="38" height="20" rx="2" fill="#f0f8ff" /><rect x="10" y="30" width="44" height="22" rx="4" fill={blueFill} /><path d="M14 31h36" stroke="#c9efff" strokeOpacity=".7" />
    </>;
    case "globe": return <>
      <circle cx="32" cy="32" r="22" fill={light} /><circle cx="32" cy="32" r="19" fill={blueFill} /><ellipse cx="32" cy="32" rx="10" ry="19" stroke="#fff" strokeOpacity=".8" strokeWidth="1.5" /><path d="M13 32h38M16 23h32M16 41h32M32 13v38" stroke="#fff" strokeOpacity=".8" strokeWidth="1.5" />
    </>;
    case "update": return <>
      <path d="M48 28a17 17 0 1 0-4 16" stroke="#f1f8ff" strokeWidth="4" strokeLinecap="round" /><path d="m41 26 8 6 5-11Z" fill={light} /><path d="M32 43V23m-7 7 7-7 7 7" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
    </>;
    case "backup": return <>
      <rect x="12" y="18" width="40" height="32" rx="5" fill={metal} /><path d="M16 44h32" stroke="#7b9197" strokeWidth="2" /><path d="M32 14v22m-7-7 7 7 7-7" stroke="#258261" strokeWidth="4" strokeLinecap="round" strokeLinejoin="round" /><circle cx="46" cy="45" r="1.5" fill="#62db7a" />
    </>;
    case "package": return <>
      <path d="m32 12 21 11v24L32 57 11 47V23Z" fill={light} /><path d="m11 23 21 11 21-11M32 34v23" stroke="#d19042" strokeWidth="1.5" /><path d="m22 17 21 11v10l-8 4V32L15 21Z" fill="#e8ae62" />
    </>;
    case "window": return <>
      <rect x="10" y="14" width="44" height="37" rx="4" fill={light} stroke="#82909d" /><path d="M10 24h44" stroke="#a0aeb8" /><path d="M24 25v25" stroke="#c3cdd6" /><circle cx="16" cy="19" r="1.5" fill="#fa7068" /><circle cx="21" cy="19" r="1.5" fill="#efbb51" /><circle cx="26" cy="19" r="1.5" fill="#62bf73" /><rect x="29" y="30" width="19" height="14" rx="2" fill={blueFill} />
    </>;
  }
}
