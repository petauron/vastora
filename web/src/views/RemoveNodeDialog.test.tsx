// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "../api";
import type { AgentView } from "../types";
import { RemoveNodeDialog } from "./RemoveNodeDialog";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
const onClose = vi.fn();
const node = { id: "expired/node", name: "DMIT CN2", connected: false } as AgentView;
const mutate = async (action: () => Promise<unknown>) => { await action(); };

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  onClose.mockClear();
});

function render(agent = node) {
  if (!root) {
    const container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  act(() => root?.render(<RemoveNodeDialog agent={agent} language="zh-CN" mutate={mutate} onClose={onClose} />));
}

function name(value: string) {
  const input = document.querySelector<HTMLInputElement>("#remove-node-name")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function submit() {
  await act(async () => {
    document.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
}

describe("permanent offline node removal", () => {
  it("requires the displayed name and submits the immutable node ID", async () => {
    const remove = vi.spyOn(api, "removeOfflineAgent").mockResolvedValue({ removing: true });
    render();
    expect(document.body.textContent).toContain("离线服务器上的程序和数据不会被删除");
    expect(document.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true);
    name("DMIT");
    await submit();
    expect(remove).not.toHaveBeenCalled();
    name(" DMIT CN2 ");
    await submit();
    expect(remove).toHaveBeenCalledExactlyOnceWith("expired/node", "DMIT CN2");
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("blocks direct submit when the node comes back online", async () => {
    const remove = vi.spyOn(api, "removeOfflineAgent");
    render(); name(node.name);
    render({ ...node, connected: true });
    await submit();
    expect(remove).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("节点已上线");
  });

  it("shows pending progress without issuing another removal", async () => {
    const remove = vi.spyOn(api, "removeOfflineAgent");
    render({ ...node, removal: { state: "pending" } });
    expect(document.querySelector('button[type="submit"]')).toBeNull();
    expect(document.getElementById("remove-node-name")).toBeNull();
    expect(document.querySelector('[role="status"]')?.textContent).toContain("正在清理关联记录");
    await submit();
    expect(remove).not.toHaveBeenCalled();
  });

  it("retries the same failed node without hiding a retry error", async () => {
    const remove = vi.spyOn(api, "removeOfflineAgent").mockRejectedValue(new APIError("private provider detail", 400, "node_remove_shared"));
    render({ ...node, removal: { state: "failed" } });
    await submit();
    expect(remove).toHaveBeenCalledExactlyOnceWith(node.id, node.name);
    expect(onClose).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toContain("private provider detail");
    expect(document.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(false);
  });
});
