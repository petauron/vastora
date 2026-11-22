// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import type { NodeSwap, NodeSwapResult } from "../node-swap-types";
import { NodeSwapControls } from "./NodeSwapControls";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

async function render(view: NodeSwap, connected = true) {
  vi.spyOn(api, "nodeSwap").mockResolvedValue(view);
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<NodeSwapControls nodeId="fixture-node" connected={connected} language="zh-CN" />);
  });
  return container;
}

const eligibleResult: NodeSwapResult = {
  state: "eligible",
  revision: "b".repeat(64),
  memoryTotalBytes: 1024 * 1024 * 1024,
  memoryAvailableBytes: 700 * 1024 * 1024,
  diskFreeBytes: 8 * 1024 * 1024 * 1024,
  virtualization: "kvm",
  systemd: true,
  swappiness: 60,
  swaps: [],
  target: {
    tierMiB: 1024,
    zramBytes: 256 * 1024 * 1024,
    zramAlgorithm: "lz4",
    zramPriority: 100,
    swapfileBytes: 512 * 1024 * 1024,
    swapfilePriority: -2,
    swappiness: 60,
  },
  canApply: true,
};

const findButton = (container: HTMLElement, label: string) =>
  [...container.querySelectorAll("button")].find((b) => b.textContent?.includes(label));

it("reads durable state on mount without queueing any host swap task", async () => {
  const mutate = vi.spyOn(api, "configureNodeSwap");
  const container = await render({ id: "", state: "unconfigured" });
  expect(mutate).not.toHaveBeenCalled();
  expect(container.textContent).toContain("尚未检测");
  expect(findButton(container, "应用固定档位优化")).toBeUndefined();
});

it("gates apply behind explicit confirmation and binds the inspected revision", async () => {
  const appliedResult: NodeSwapResult = {
    ...eligibleResult,
    state: "applied",
    revision: "c".repeat(64),
    canApply: false,
    canRestore: true,
  };
  const mutate = vi.spyOn(api, "configureNodeSwap").mockResolvedValue({
    id: "node-swap-1",
    state: "succeeded",
    result: appliedResult,
  });
  const container = await render({ id: "node-swap-0", state: "succeeded", result: eligibleResult });
  const applyButton = findButton(container, "应用固定档位优化")!;
  expect(applyButton.disabled).toBe(true);
  await act(async () => {
    container.querySelector<HTMLElement>('[role="checkbox"]')?.click();
  });
  expect(applyButton.disabled).toBe(false);
  await act(async () => {
    applyButton.click();
  });
  expect(mutate).toHaveBeenCalledWith("fixture-node", { action: "apply", revision: eligibleResult.revision });
  expect(container.textContent).toContain("已启用受管 zram / swap");
});

it("shows read-only state without apply/restore controls on ineligible hosts", async () => {
  const readOnlyResult: NodeSwapResult = {
    ...eligibleResult,
    state: "read_only",
    virtualization: "container",
    canApply: false,
    reason: "Requires a Linux systemd KVM host; containers and non-KVM hosts remain read-only.",
  };
  const container = await render({ id: "node-swap-lxc", state: "succeeded", result: readOnlyResult });
  expect(container.textContent).toContain("只读");
  expect(findButton(container, "应用固定档位优化")).toBeUndefined();
  expect(findButton(container, "恢复应用前状态")).toBeUndefined();
});

it("re-reads durable state without retrying after a lost response", async () => {
  const mutate = vi.spyOn(api, "configureNodeSwap").mockRejectedValue(new Error("network interrupted"));
  const container = await render({ id: "", state: "unconfigured" });
  await act(async () => {
    findButton(container, "读取节点实际状态")!.click();
  });
  expect(mutate).toHaveBeenCalledTimes(1);
  expect(api.nodeSwap).toHaveBeenCalledTimes(2);
  expect(container.querySelector('[role="alert"]')).not.toBeNull();
});
