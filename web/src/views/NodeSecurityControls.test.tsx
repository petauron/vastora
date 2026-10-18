// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { NodeSecurity, SecurityResult } from "../node-security-types";
import { NodeSecurityControls } from "./NodeSecurityControls";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
afterEach(() => { if (root) act(() => root?.unmount()); root = undefined; document.body.replaceChildren(); vi.restoreAllMocks(); });
async function render(view: NodeSecurity) {
  vi.spyOn(api, "nodeSecurity").mockResolvedValue(view);
  const container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  await act(async () => { root?.render(<NodeSecurityControls nodeId="fixture-node" language="zh-CN" />); });
  return container;
}
const result: SecurityResult = { state: "pending_confirmation", revision: "a".repeat(64), deadline: "2099-01-01T00:00:00Z", policy: { publicInterfaces: ["eth0"], managementSources: ["203.0.113.10/32"], sshPorts: ["22"].map(Number), publicRules: [{ protocol: "tcp", port: 443 }] }, listeners: [], banned: [], reasons: [], supported: true };
const findButton = (container: HTMLElement, label: string) => [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(label))!;

it("does not install or enable anything on opening the node page", async () => {
  const mutate = vi.spyOn(api, "configureNodeSecurity");
  const container = await render({ id: "", state: "unconfigured" });
  expect(mutate).not.toHaveBeenCalled();
  expect(container.textContent).toContain("尚未配置");
  expect(findButton(container, "应用，5 分钟内验证确认")).toBeUndefined();
});

it("requires new SSH and business verification before confirmation", async () => {
  const mutate = vi.spyOn(api, "configureNodeSecurity").mockResolvedValue({ id: "confirm", state: "succeeded", result: { ...result, state: "verified" } });
  const container = await render({ id: "apply", state: "succeeded", result });
  expect(container.textContent).toContain("security-verify-ssh");
  expect(container.textContent).toContain("原 SSH 会话仍连通不算验证");
  const confirm = findButton(container, "确认此修订"); expect(confirm.disabled).toBe(true);
  await act(async () => { container.querySelector<HTMLElement>('[role="checkbox"]')?.click(); });
  expect(confirm.disabled).toBe(false);
  await act(async () => { confirm.click(); });
  expect(mutate).toHaveBeenCalledWith("fixture-node", expect.objectContaining({ action: "confirm", revision: result.revision }));
  expect(container.textContent).toContain("最近防护验证通过");
});

it("never retries a mutation after a lost response", async () => {
  const mutate = vi.spyOn(api, "configureNodeSecurity").mockRejectedValue(new Error("connection lost"));
  const container = await render({ id: "", state: "unconfigured" });
  await act(async () => { findButton(container, "准备缺少的系统依赖").click(); });
  expect(mutate).toHaveBeenCalledTimes(1);
  expect(api.nodeSecurity).toHaveBeenCalledTimes(2);
  expect(container.querySelector('[role="alert"]')).not.toBeNull();
});

it("does not offer confirmation when only a preview succeeded", async () => {
  const container = await render({ id: "preview", state: "succeeded", result: { ...result, state: "preview" } });
  expect(container.textContent).toContain("仅预览，未应用");
  expect(findButton(container, "确认此修订")).toBeUndefined();
});
