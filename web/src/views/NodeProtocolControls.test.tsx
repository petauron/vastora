// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { ApplicationCommand } from "../types";
import { NodeProtocolControls } from "./NodeProtocolControls";

const { execute } = vi.hoisted(() => ({ execute: vi.fn() }));
vi.mock("../hooks/use-application-command-executor", () => ({ useApplicationCommandExecutor: () => ({ execute }) }));
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
const completed: ApplicationCommand = { id: "protocol-command", kind: "3xui.protocols.configure", applicationId: "app", gatewayNodeId: "node", state: "succeeded", hostname: "", dnsProvider: "manual", resultAvailable: false, createdAt: "", updatedAt: "" };
beforeEach(() => {
  execute.mockImplementation(async (start: () => Promise<ApplicationCommand>, adopt: (value: ApplicationCommand) => void) => { const value = await start(); adopt(value); return value; });
});
afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  execute.mockReset();
});
async function renderControls(onUpdated = vi.fn(async () => undefined)) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => { root?.render(<NodeProtocolControls serviceId="node-service" language="zh-CN" onUpdated={onUpdated} />); });
  return container;
}
function button(container: HTMLElement, text: string) { return [...container.querySelectorAll("button")].find((value) => value.textContent?.includes(text))!; }

it("defaults to VLESS and adds HY2 without AnyTLS", async () => {
  vi.spyOn(api, "nodeProtocols").mockResolvedValue({ vless: true, hy2: false, state: "succeeded" });
  const save = vi.spyOn(api, "configureNodeProtocols").mockResolvedValue(completed);
  const updated = vi.fn(async () => undefined);
  const container = await renderControls(updated);
  expect(container.querySelector<HTMLInputElement>("input#node-service-vless")?.checked).toBe(true);
  expect(container.querySelector<HTMLInputElement>("input#node-service-hy2")?.checked).toBe(false);
  expect(container.textContent).not.toContain("AnyTLS");
  expect(button(container, "保存协议").disabled).toBe(true);
  await act(async () => { container.querySelector<HTMLElement>("#node-service-hy2")?.click(); });
  await act(async () => { button(container, "保存协议").click(); });
  expect(save).toHaveBeenCalledWith("node-service", { vless: true, hy2: true });
  expect(updated).toHaveBeenCalledOnce();
});

it("prevents an empty selection without losing the draft", async () => {
  vi.spyOn(api, "nodeProtocols").mockResolvedValue({ vless: true, hy2: false, state: "succeeded" });
  const save = vi.spyOn(api, "configureNodeProtocols");
  const container = await renderControls();
  await act(async () => { container.querySelector<HTMLElement>("#node-service-vless")?.click(); });
  expect(container.textContent).toContain("至少选择一种协议");
  expect(button(container, "保存协议").disabled).toBe(true);
  expect(save).not.toHaveBeenCalled();
});

it("does not offer guessed defaults after a failed read", async () => {
  vi.spyOn(api, "nodeProtocols").mockRejectedValue(new Error("unavailable"));
  const container = await renderControls();
  expect(container.querySelector('[role="checkbox"]')).toBeNull();
  expect(container.querySelector('[role="alert"]')).not.toBeNull();
  expect(button(container, "重试").disabled).toBe(false);
});

it("resumes a failed phase instead of creating a second protocol operation", async () => {
  vi.spyOn(api, "nodeProtocols").mockResolvedValue({ vless: true, hy2: true, commandId: "protocol-command-verify", state: "failed" });
  vi.spyOn(api, "applicationCommand").mockResolvedValueOnce({ ...completed, id: "protocol-command-verify", state: "failed", reconciliationRequired: true }).mockResolvedValue({ ...completed, id: "protocol-command-verify" });
  const retry = vi.spyOn(api, "retryTaskReconciliation").mockResolvedValue({ taskId: "protocol-command-verify", kind: "application.command", queued: true });
  const configure = vi.spyOn(api, "configureNodeProtocols");
  const container = await renderControls();
  await act(async () => { button(container, "保存协议").click(); });
  expect(retry).toHaveBeenCalledWith("protocol-command-verify");
  expect(configure).not.toHaveBeenCalled();
});
