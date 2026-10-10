import type { Screen } from "@/types";

export type DesktopWindowEntry = { id: string; screen: Exclude<Screen, "home">; workspaceKey: string | null; minimized: boolean };
export type DesktopWindows = { entries: DesktopWindowEntry[]; stack: string[] };
export const emptyWindows = (): DesktopWindows => ({ entries: [], stack: [] });
export const windowID = (screen: Screen, workspaceKey: string | null = null) => workspaceKey ? `app:${workspaceKey}` : screen;

// Entries retain launch order for Dock; only the separate stack changes on focus.
export function openWindow(state: DesktopWindows, screen: Screen, workspaceKey: string | null = null): DesktopWindows {
  if (screen === "home") return { ...state, entries: state.entries.map((entry) => ({ ...entry, minimized: true })) };
  const id = windowID(screen, workspaceKey);
  const existing = state.entries.some((entry) => entry.id === id);
  return {
    entries: existing ? state.entries.map((entry) => entry.id === id ? { ...entry, minimized: false } : entry) : [...state.entries, { id, screen, workspaceKey, minimized: false }],
    stack: [...state.stack.filter((key) => key !== id), id],
  };
}

export function dismissWindow(state: DesktopWindows, id: string, close: boolean): DesktopWindows {
  return {
    entries: close ? state.entries.filter((entry) => entry.id !== id) : state.entries.map((entry) => entry.id === id ? { ...entry, minimized: true } : entry),
    stack: close ? state.stack.filter((key) => key !== id) : state.stack,
  };
}

export function frontWindow(state: DesktopWindows): DesktopWindowEntry | undefined {
  return [...state.stack].reverse().map((id) => state.entries.find((entry) => entry.id === id)).find((entry) => entry && !entry.minimized);
}
