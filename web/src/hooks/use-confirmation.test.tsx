// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { useConfirmation } from "./use-confirmation";
import { Sheet, SheetContent, SheetTitle, SheetDescription } from "../components/ui/sheet";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let confirm: ReturnType<typeof useConfirmation>["confirm"];
function Harness() {
  const result = useConfirmation("zh-CN");
  confirm = result.confirm;
  return result.confirmation;
}
function mount() {
  vi.stubGlobal("matchMedia", vi.fn((media: string) => ({ matches: false, media, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  act(() => root!.render(<Harness />));
}
afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});
const request = { title: "确认操作", description: "需要明确确认后继续。" };
it("waits for an explicit decision and treats cancel as false", async () => {
  mount();
  const decision = vi.fn();
  await act(async () => { void confirm(request).then(decision); });
  expect(document.querySelector('[role="alertdialog"]')?.textContent).toContain(request.description);
  expect(decision).not.toHaveBeenCalled();
  await act(async () => { [...document.querySelectorAll("button")].find((button) => button.textContent === "取消")!.click(); });
  expect(decision).toHaveBeenCalledExactlyOnceWith(false);
});
it("only explicit confirmation resolves true", async () => {
  mount();
  const decision = vi.fn();
  await act(async () => { void confirm({ ...request, confirmLabel: "放弃修改", destructive: true }).then(decision); });
  await act(async () => { [...document.querySelectorAll("button")].find((button) => button.textContent === "放弃修改")!.click(); });
  expect(decision).toHaveBeenCalledExactlyOnceWith(true);
});
it("cancels a pending decision when its owning view unmounts", async () => {
  mount();
  const decision = vi.fn();
  await act(async () => { void confirm(request).then(decision); });
  await act(async () => { root!.unmount(); root = undefined; });
  expect(decision).toHaveBeenCalledExactlyOnceWith(false);
});
it("cancels a replaced decision without accepting the new one", async () => {
  mount();
  const first = vi.fn();
  const second = vi.fn();
  await act(async () => { void confirm(request).then(first); });
  await act(async () => { void confirm({ ...request, title: "第二次确认" }).then(second); });
  expect(first).toHaveBeenCalledExactlyOnceWith(false);
  expect(second).not.toHaveBeenCalled();
});

it("Escape cancels only the nested confirmation and keeps its parent editor open", async () => {
  const parentClosed = vi.fn();
  const decision = vi.fn();
  function NestedEditor() {
    const result = useConfirmation("zh-CN");
    confirm = result.confirm;
    return <Sheet open onOpenChange={parentClosed}><SheetContent>{result.confirmation}<SheetTitle>编辑套餐</SheetTitle><SheetDescription>尚未保存的编辑</SheetDescription><input aria-label="额度" defaultValue="300" /></SheetContent></Sheet>;
  }
  mount();
  await act(async () => { root!.render(<NestedEditor />); });
  await act(async () => { void confirm(request).then(decision); });
  await act(async () => { document.querySelector('[role="alertdialog"]')!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
  expect(decision).toHaveBeenCalledExactlyOnceWith(false);
  expect(parentClosed).not.toHaveBeenCalled();
  expect(document.querySelector<HTMLInputElement>('input[aria-label="额度"]')?.value).toBe("300");
});
