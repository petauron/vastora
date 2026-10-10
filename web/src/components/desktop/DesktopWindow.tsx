import { Activity, useId, useImperativeHandle, type ReactNode, type Ref } from "react";
import { Grid2X2Icon, Maximize2Icon, Minimize2Icon, MinusIcon, XIcon } from "lucide-react";
import type { Language } from "@/translations";
import { copy } from "@/views/shared";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { resizeEdges, useDesktopWindow } from "./useDesktopWindow";
import type { DesktopWindowEntry } from "./windowState";

export type DesktopWindowHandle = { toggleMaximize: () => void };

export function DesktopWindow({ entry, title, active, stackIndex, launchIndex, language, loading, children, onFocus, onDismiss, showLauncher, ref }: {
  entry: DesktopWindowEntry; title: string; active: boolean; stackIndex: number; launchIndex: number;
  language: Language; loading: boolean; children: ReactNode; ref?: Ref<DesktopWindowHandle>;
  onFocus: (entry: DesktopWindowEntry) => void; onDismiss: (id: string, close: boolean) => void;
  showLauncher: (mode: "all" | "search") => void;
}) {
  const desktopWindow = useDesktopWindow(entry.id, launchIndex);
  const helpID = useId();
  useImperativeHandle(ref, () => ({ toggleMaximize: desktopWindow.toggleMaximize }));
  const hidden = entry.minimized || (desktopWindow.compact && !active);
  return <Activity mode={hidden ? "hidden" : "visible"}>
    <div className="desktop-window" style={{ ...desktopWindow.style, zIndex: 10 + stackIndex }} data-window-id={entry.id} data-active={active} data-maximized={desktopWindow.maximized} onPointerDownCapture={() => { if (!active) onFocus(entry); }} onFocusCapture={() => { if (!active) onFocus(entry); }} role="region" aria-label={title} data-interacting={desktopWindow.interacting || undefined}>
      <div className="desktop-window-bar" {...desktopWindow.titleBarProps} tabIndex={desktopWindow.compact ? undefined : 0} role="group" aria-label={copy(language, `${title}窗口标题栏`, `${title} window title bar`)} aria-describedby={helpID}>
        <div className="desktop-traffic-lights">
          <button className="desktop-traffic-light" data-kind="close" aria-label={copy(language, "关闭窗口", "Close window")} title={copy(language, "关闭窗口", "Close window")} onClick={() => onDismiss(entry.id, true)}><XIcon /></button>
          <button className="desktop-traffic-light" data-kind="minimize" aria-label={copy(language, "最小化窗口", "Minimize window")} title={copy(language, "最小化窗口", "Minimize window")} onClick={() => onDismiss(entry.id, false)}><MinusIcon /></button>
          <button className="desktop-traffic-light" data-kind="zoom" disabled={desktopWindow.compact} aria-label={desktopWindow.maximized ? copy(language, "还原窗口", "Restore window") : copy(language, "最大化窗口", "Maximize window")} title={desktopWindow.maximized ? copy(language, "还原窗口", "Restore window") : copy(language, "最大化窗口", "Maximize window")} aria-pressed={desktopWindow.maximized} onClick={desktopWindow.toggleMaximize}>{desktopWindow.maximized ? <Minimize2Icon /> : <Maximize2Icon />}</button>
        </div>
        <div className="desktop-window-title"><span>{title}</span></div>
        <div className="desktop-window-tools">{loading ? <Spinner aria-label={copy(language, "正在更新", "Updating")} /> : null}<Button aria-label={copy(language, "所有应用", "All applications")} onClick={() => showLauncher("all")} size="icon" variant="ghost"><Grid2X2Icon /></Button></div>
      </div>
      <div className="desktop-window-body">{children}</div>
      <p id={helpID} className="sr-only">{copy(language, "拖动标题栏移动，双击最大化或还原；拖动边缘调整大小。聚焦标题栏后，Alt 加方向键移动，Alt 加 Shift 加方向键调整大小，Esc 取消拖动。", "Drag the title bar to move; double-click to maximize or restore. Drag an edge to resize. With the title bar focused, Alt + arrows moves, Alt + Shift + arrows resizes, and Escape cancels dragging.")}</p>
      {!desktopWindow.compact && !desktopWindow.maximized ? resizeEdges.map((edge) => <div key={edge} className="desktop-window-resize" data-edge={edge} aria-hidden="true" {...desktopWindow.resizeProps(edge)} />) : null}
    </div>
  </Activity>;
}
