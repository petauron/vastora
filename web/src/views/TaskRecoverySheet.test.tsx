// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { ExecutionView } from "../execution-types";
import { TaskRecoverySheet } from "./TaskRecoverySheet";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
afterEach(() => { if (root) act(() => root?.unmount()); root = undefined; document.body.replaceChildren(); vi.restoreAllMocks(); });
const execution: ExecutionView = { id: "execution-latest", agentId: "agent-a", taskId: "deployment-a", kind: "application.apply", attempt: 2, state: "unknown", phase: "reported", lastError: "health check failed", disposition: "", updatedAt: "2026-01-01T00:00:00Z" };
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find((value) => value.textContent === label);
async function mount() {
  const host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  const onSaved = vi.fn();
  await act(async () => root?.render(<TaskRecoverySheet taskId="deployment-a" agentId="agent-a" name="Example node" language="en" onClose={vi.fn()} onSaved={onSaved} />));
  return onSaved;
}

it("loads the exact task and requires the existing operator attestation before retry", async () => {
  const inspect = vi.spyOn(api, "taskExecution").mockResolvedValue(execution);
  const legacy = vi.spyOn(api, "retryTaskReconciliation");
  const dispose = vi.spyOn(api, "disposeExecution").mockResolvedValue({});
  const saved = await mount();
  expect(inspect).toHaveBeenCalledWith("deployment-a", expect.any(AbortSignal));
  expect(legacy).not.toHaveBeenCalled();
  expect(dispose).not.toHaveBeenCalled();
  expect(button("Confirm completed")).toBeUndefined();
  await act(async () => button("Execute again")!.click());
  expect(button("Execute again")!.disabled).toBe(true);
  await act(async () => (document.querySelector('[role="checkbox"]') as HTMLElement).click());
  expect(button("Execute again")!.disabled).toBe(true);
  await act(async () => {
    const note = document.querySelector("#execution-note") as HTMLTextAreaElement;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(note, "Old execution stopped; existing resources verified");
    note.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => button("Execute again")!.click());
  expect(dispose).toHaveBeenCalledExactlyOnceWith("execution-latest", "application.apply", { action: "reexecute", executionStopped: true, note: "Old execution stopped; existing resources verified" });
  expect(saved).toHaveBeenCalledTimes(1);
});

it("offers completion only when the server retains sufficient success evidence", async () => {
  vi.spyOn(api, "taskExecution").mockResolvedValue({ ...execution, canConfirm: true });
  const dispose = vi.spyOn(api, "disposeExecution");
  await mount();
  expect(button("Confirm completed")).toBeDefined();
  expect(dispose).not.toHaveBeenCalled();
});

it.each([
  { state: "running", disposition: "" },
  { state: "succeeded", disposition: "" },
  { state: "unknown", disposition: "reexecute" },
])("does not replay a latest attempt that is already $state/$disposition", async (state) => {
  vi.spyOn(api, "taskExecution").mockResolvedValue({ ...execution, ...state });
  await mount();
  expect(button("Execute again")).toBeUndefined();
  expect(button("Abandon task")).toBeUndefined();
});

it.each([
  { taskId: "other-task" }, { agentId: "other-agent" }, { kind: "application.command" },
])("fails closed on mismatched execution identity %j", async (identity) => {
  vi.spyOn(api, "taskExecution").mockResolvedValue({ ...execution, ...identity });
  await mount();
  expect(document.querySelector('[role="alert"]')?.textContent).toContain("does not match");
  expect(button("Execute again")).toBeUndefined();
});

it("keeps a failed lookup visible without falling back to the obsolete retry endpoint", async () => {
  const inspect = vi.spyOn(api, "taskExecution").mockRejectedValue(new Error("unavailable"));
  const legacy = vi.spyOn(api, "retryTaskReconciliation");
  const dispose = vi.spyOn(api, "disposeExecution");
  await mount();
  expect(document.querySelector('[role="alert"]')).not.toBeNull();
  expect(inspect).toHaveBeenCalledTimes(1);
  expect(legacy).not.toHaveBeenCalled();
  expect(dispose).not.toHaveBeenCalled();
});

it("cancels the lookup when the recovery sheet closes", async () => {
  const inspect = vi.spyOn(api, "taskExecution").mockImplementation(() => new Promise(() => {}));
  await mount();
  const signal = inspect.mock.calls[0][1]!;
  expect(signal.aborted).toBe(false);
  act(() => root?.unmount()); root = undefined;
  expect(signal.aborted).toBe(true);
});
