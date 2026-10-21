// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { SecretConfigInput } from "./SecretConfigInput";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
afterEach(() => { act(() => root.unmount()); document.body.replaceChildren(); });

it("preserves multiline secrets through masked paste, reveal, hide and submission", () => {
  const submit = vi.fn();
  function Form() {
    const [value, setValue] = useState("");
    return <form onSubmit={(event) => { event.preventDefault(); submit(value); }}>
      <label htmlFor="secret">Secret</label>
      <SecretConfigInput id="secret" value={value} onChange={setValue} required language="en" />
    </form>;
  }
  const container = document.createElement("div"); document.body.append(container);
  root = createRoot(container); act(() => root.render(<Form />));
  expect(container.querySelector("textarea")).toBeNull();
  expect(container.querySelector("input")?.type).toBe("password");
  expect(container.querySelector("form")?.checkValidity()).toBe(false);
  const pem = "-----BEGIN PRIVATE KEY-----\nsynthetic-only\n-----END PRIVATE KEY-----\n";
  const paste = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(paste, "clipboardData", { value: { getData: () => pem } });
  act(() => container.querySelector("input")!.dispatchEvent(paste));
  expect(paste.defaultPrevented).toBe(true);
  expect(container.querySelector("input")!.value).not.toContain("synthetic");
  act(() => container.querySelector("button")!.click());
  expect(container.querySelector("textarea")!.value).toBe(pem);
  act(() => container.querySelector("button")!.click());
  expect(container.querySelector("textarea")).toBeNull();
  act(() => container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  expect(submit).toHaveBeenCalledWith(pem);
  act(() => container.querySelector("button")!.click());
  const textarea = container.querySelector("textarea")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea, "");
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(container.querySelector("form")!.checkValidity()).toBe(false);
});
