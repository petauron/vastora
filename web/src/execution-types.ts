export type ExecutionView = {
  id: string; agentId: string; taskId: string; kind: string; attempt: number;
  state: string; phase: string; lastError: string; updatedAt: string; disposition: string;
  canConfirm?: boolean;
};
export type ExecutionPage = { executions: ExecutionView[]; nextCursor: number };
export type ExecutionClaimControl = { paused: boolean; actor: string; updatedAt: string };
export type ExecutionAction = "reexecute" | "abandon" | "confirm-completed";
export type ExecutionDisposition = { action: ExecutionAction; executionStopped: boolean; note: string };
export type LegacyReceiptView = {
  executionId: string; taskId: string; kind: string; attempt: number; runtimeGeneration: number;
  state: string; hasCompletion: boolean; createdAt: string; updatedAt: string;
};

const taskKinds = new Set([
  "application.apply", "application.command", "agent.update", "agent.decommission",
  "landing.proxy.apply", "landing.server.apply", "gateway.routes.apply",
  "gateway.component.apply", "node.listener.apply", "tunnel.state.apply",
]);
export function isHelperExecution(kind: string) {
  return kind === "agent.update" || kind === "agent.decommission";
}
export function executionActions(value: ExecutionView): ExecutionAction[] {
  if (value.disposition || !["failed", "unknown"].includes(value.state)) return [];
  if (value.kind === "legacy.receipt") return value.state === "unknown" ? ["abandon"] : [];
  if (!taskKinds.has(value.kind)) return [];
  return isHelperExecution(value.kind) || value.canConfirm ? ["confirm-completed", "abandon", "reexecute"] : ["abandon", "reexecute"];
}
