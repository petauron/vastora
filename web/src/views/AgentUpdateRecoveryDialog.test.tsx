// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentView } from "../types";
import { AgentUpdateRecoveryDialog } from "./AgentUpdateRecoveryDialog";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
const onClose = vi.fn();
const agent = { id: "test-node", name: "Test node", connected: true, update: { id: "failed-update", state: "failed", lastError: "version check failed" } } as AgentView;
const mutate = async (action: () => Promise<unknown>) => { await action(); };

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  onClose.mockClear();
});

function render(value = agent) {
  if (!root) {
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  act(() => root?.render(<AgentUpdateRecoveryDialog agent={value} failedUpdateId="failed-update" language="zh-CN" mutate={mutate} onClose={onClose} targetVersion="0.1.0-alpha.89" />));
}

function verify() {
  act(() => {
    document.querySelector<HTMLButtonElement>('[role="checkbox"]')!.click();
    const note = document.querySelector<HTMLTextAreaElement>("#update-recovery-note")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(note, " Old updater stopped before installation ");
    note.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function submit() {
  await act(async () => { document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })); });
}

describe("Agent update recovery dialog", () => {
  it("requires explicit verification and submits the exact failed task without native confirmation", async () => {
    const recover = vi.spyOn(api, "recoverAgentUpdate").mockResolvedValue({ id: "new-update", targetVersion: "0.1.0-alpha.89", state: "pending", updatedAt: "2026-01-01T00:00:00Z" });
    const confirm = vi.spyOn(window, "confirm");
    render();
    await submit();
    expect(recover).not.toHaveBeenCalled();
    verify();
    await submit();
    expect(recover).toHaveBeenCalledExactlyOnceWith(agent.id, "failed-update", "Old updater stopped before installation");
    expect(confirm).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("cancels without a request", () => {
    const recover = vi.spyOn(api, "recoverAgentUpdate");
    render();
    act(() => { Array.from(document.querySelectorAll("button")).find((button) => button.textContent === "取消")!.click(); });
    expect(recover).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("rejects a stale failed task after a refresh", async () => {
    const recover = vi.spyOn(api, "recoverAgentUpdate");
    render(); verify();
    render({ ...agent, update: { ...agent.update!, id: "replacement-update", state: "pending" } });
    await submit();
    expect(recover).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("任务状态已变化");
  });

  it("retains the dialog when the server rejects recovery", async () => {
    vi.spyOn(api, "recoverAgentUpdate").mockRejectedValue(new Error("Recovery blocked"));
    render(); verify();
    await submit();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector('[role="alert"]')).not.toBeNull();
    expect(document.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(false);
  });
});
