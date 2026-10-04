// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api";
import { emptyAppData } from "../app-data";
import { HomeTaskSummary } from "./HomeTaskSummary";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
afterEach(() => { if (root) act(() => root.unmount()); document.body.replaceChildren(); vi.restoreAllMocks(); });
async function mount() {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  const data = emptyAppData({ version: "test", agentInstallerAvailable: false, agentConnectionMode: "headscale", agentConnectUrl: "" });
  const navigate = vi.fn();
  await act(async () => root.render(<HomeTaskSummary data={data} language="zh-CN" onNavigate={navigate} />));
  return navigate;
}

it("does not mistake a failed task read for a healthy system", async () => {
  const request = vi.spyOn(api, "executions").mockRejectedValue(new Error("unavailable"));
  const navigate = await mount();
  expect(request.mock.calls[0][2]).toBe("attention");
  expect(document.body.textContent).toContain("待处理任务暂时无法读取");
  expect(document.body.textContent).not.toContain("没有待处理任务");
  await act(async () => document.querySelector('button')!.click());
  expect(navigate).toHaveBeenCalledWith("activity");
});

it("reports no pending tasks only after a successful read", async () => {
  vi.spyOn(api, "executions").mockResolvedValue({ executions: [], nextCursor: 0 });
  await mount();
  expect(document.body.textContent).toContain("没有待处理任务");
});
