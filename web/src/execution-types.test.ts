import { expect, it } from "vitest";
import { executionActions, type ExecutionView } from "./execution-types";

const execution: ExecutionView = { id: "e", agentId: "a", taskId: "t", kind: "application.apply", attempt: 1, state: "unknown", phase: "apply", lastError: "", updatedAt: "", disposition: "" };
it("offers only implemented dispositions for unresolved stopped executions", () => {
  expect(executionActions(execution)).toEqual(["abandon", "reexecute"]);
  expect(executionActions({ ...execution, canConfirm: true })).toEqual(["confirm-completed", "abandon", "reexecute"]);
  expect(executionActions({ ...execution, kind: "agent.update" })).toEqual(["confirm-completed", "abandon", "reexecute"]);
  for (const state of ["offered", "running", "helper_running", "succeeded"]) expect(executionActions({ ...execution, state })).toEqual([]);
  expect(executionActions({ ...execution, kind: "legacy.receipt" })).toEqual(["abandon"]);
  expect(executionActions({ ...execution, kind: "legacy.receipt", state: "failed" })).toEqual([]);
  expect(executionActions({ ...execution, kind: "unsupported" })).toEqual([]);
  expect(executionActions({ ...execution, disposition: "abandon" })).toEqual([]);
});
