// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { ExecutionView } from "../execution-types";
import { ExecutionSettings } from "./ExecutionSettings";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
afterEach(() => { if (root) act(() => root.unmount()); document.body.replaceChildren(); vi.restoreAllMocks(); });
const execution: ExecutionView = { id: "execution-a", agentId: "agent-a", taskId: "task-a", kind: "application.apply", attempt: 1, state: "unknown", phase: "apply", lastError: "", updatedAt: "2026-09-13T00:00:00Z", disposition: "" };
const button = (text: string) => [...document.querySelectorAll("button")].find((value) => value.textContent === text)!;
async function mount() {
  vi.spyOn(api, "executionClaimControl").mockResolvedValue({ paused: true, actor: "migration:75", updatedAt: "" });
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => root.render(<ExecutionSettings language="en" agents={[{ id: "agent-a", name: "Test node" }]} />));
}

it("loads actual pages and does not offer execution mutations until opened and confirmed", async () => {
  const list = vi.spyOn(api, "executions").mockResolvedValueOnce({ executions: [execution], nextCursor: 10 }).mockResolvedValueOnce({ executions: [{ ...execution, id: "older", disposition: "abandon" }], nextCursor: 0 });
  const dispose = vi.spyOn(api, "disposeExecution");
  await mount();
  expect(document.body.textContent).toContain("Outcome unconfirmed");
  expect(document.body.textContent).toContain("New task claims paused");
  expect(button("Execute again")).toBeUndefined();
  await act(async () => button("Older records").click());
  expect(list.mock.calls[1][0]).toBe(10);
  expect(document.body.textContent).toContain("Abandoned");
  expect(button("Older records")).toBeUndefined();
  expect(dispose).not.toHaveBeenCalled();
});

it("requires a separate explicit confirmation before resuming claims", async () => {
  vi.spyOn(api, "executions").mockResolvedValue({ executions: [], nextCursor: 0 });
  const save = vi.spyOn(api, "setExecutionClaimControl").mockResolvedValue({ recorded: true });
  await mount();
  await act(async () => button("Resume claims").click());
  expect(document.body.textContent).toContain("Complete the upgrade cutover");
  expect(save).not.toHaveBeenCalled();
  await act(async () => button("Confirm").click());
  expect(save).toHaveBeenCalledExactlyOnceWith(false);
  expect(document.body.textContent).toContain("Claim settings saved.");
});

it("blocks disposition until stopped attestation and notes are supplied", async () => {
  vi.spyOn(api, "executions").mockResolvedValue({ executions: [execution], nextCursor: 0 });
  const dispose = vi.spyOn(api, "disposeExecution").mockResolvedValue({});
  await mount();
  const row = [...document.querySelectorAll("button")].find((value) => value.textContent?.includes("Test node"))!;
  await act(async () => row.click());
  await act(async () => button("Execute again").click());
  expect(button("Execute again").disabled).toBe(true);
  await act(async () => (document.querySelector("#execution-stopped") as HTMLElement).click());
  expect(button("Execute again").disabled).toBe(true);
  const note = document.querySelector("#execution-note") as HTMLTextAreaElement;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(note, "Old process stopped; resource inspected");
    note.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(button("Execute again").disabled).toBe(false);
  await act(async () => button("Execute again").click());
  expect(dispose).toHaveBeenCalledExactlyOnceWith("execution-a", "application.apply", { action: "reexecute", executionStopped: true, note: "Old process stopped; resource inspected" });
});

it("keeps a failed resume visible without automatically sending it again", async () => {
  vi.spyOn(api, "executions").mockResolvedValue({ executions: [], nextCursor: 0 });
  const save = vi.spyOn(api, "setExecutionClaimControl").mockRejectedValue(new Error("connection closed"));
  await mount();
  await act(async () => button("Resume claims").click());
  await act(async () => button("Confirm").click());
  expect(save).toHaveBeenCalledTimes(1);
  expect(document.querySelector('[role="alert"]')).not.toBeNull();
  expect(document.body.textContent).toContain("New task claims paused");
  expect(button("Cancel")).toBeDefined();
});

it("cancels pending reads when leaving settings", async () => {
  const list = vi.spyOn(api, "executions").mockImplementation(() => new Promise(() => {}));
  await mount();
  const signal = list.mock.calls[0][1]!;
  expect(signal.aborted).toBe(false);
  act(() => root.unmount());
  expect(signal.aborted).toBe(true);
});
