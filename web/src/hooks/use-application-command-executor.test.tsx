// @vitest-environment jsdom

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "../api";
import type { ApplicationCommand } from "../types";
import { useApplicationCommandExecutor } from "./use-application-command-executor";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const pendingCommand: ApplicationCommand = {
  id: "command-1",
  applicationId: "three-x-ui",
  gatewayNodeId: "agent",
  kind: "3xui.clients.manage",
  state: "pending",
  hostname: "",
  dnsProvider: "manual",
  resultAvailable: false,
  createdAt: "2026-08-24T00:00:00Z",
  updatedAt: "2026-08-24T00:00:00Z"
};

let root: Root | undefined;

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function Harness({ onError, onResult }: { onError: (error: unknown) => void; onResult: (command: ApplicationCommand | null) => void }) {
  const { execute } = useApplicationCommandExecutor("three-x-ui");
  useEffect(() => {
    void execute(() => Promise.resolve(pendingCommand), () => undefined).then(onResult, onError);
  }, [execute, onError, onResult]);
  return null;
}

function renderHarness(onResult: (command: ApplicationCommand | null) => void, onError: (error: unknown) => void) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  root.render(<Harness onError={onError} onResult={onResult} />);
}

describe("useApplicationCommandExecutor", () => {
  it("reports an expired Center session immediately after the event stream fails", async () => {
    class FailedEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() { queueMicrotask(() => this.onerror?.()); }
      close() {}
    }
    vi.stubGlobal("EventSource", FailedEventSource);
    vi.spyOn(api, "applicationCommand").mockRejectedValue(new APIError("authentication required", 401, "unauthorized"));
    const onResult = vi.fn();
    const onError = vi.fn();

    await act(async () => {
      renderHarness(onResult, onError);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(onResult).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining("session expired") }));
  });

  it("reconnects after a transient event-stream failure and adopts the final command", async () => {
    vi.useFakeTimers();
    let connections = 0;
    class RecoveringEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        connections += 1;
        const attempt = connections;
        queueMicrotask(() => {
          if (attempt === 1) this.onerror?.();
          else this.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ ...pendingCommand, state: "succeeded" }) }));
        });
      }
      close() {}
    }
    vi.stubGlobal("EventSource", RecoveringEventSource);
    vi.spyOn(api, "applicationCommand").mockResolvedValue(pendingCommand);
    const onResult = vi.fn();
    const onError = vi.fn();

    await act(async () => {
      renderHarness(onResult, onError);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      vi.advanceTimersByTime(1_000);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(connections).toBe(2);
    expect(onError).not.toHaveBeenCalled();
    expect(onResult).toHaveBeenCalledWith(expect.objectContaining({ state: "succeeded" }));
  });

  it("keeps reconnecting after repeated event-stream failures while REST reports an active command", async () => {
    vi.useFakeTimers();
    let connections = 0;
    class RepeatedlyFailingEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        connections += 1;
        const attempt = connections;
        queueMicrotask(() => {
          if (attempt <= 4) this.onerror?.();
          else this.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ ...pendingCommand, state: "succeeded" }) }));
        });
      }
      close() {}
    }
    vi.stubGlobal("EventSource", RepeatedlyFailingEventSource);
    const poll = vi.spyOn(api, "applicationCommand").mockResolvedValue(pendingCommand);
    const onResult = vi.fn();
    const onError = vi.fn();

    await act(async () => {
      renderHarness(onResult, onError);
      await Promise.resolve();
      await Promise.resolve();
    });
    for (let attempt = 0; attempt < 4; attempt += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(4_000);
      });
    }

    expect(connections).toBe(5);
    expect(poll).toHaveBeenCalledTimes(4);
    expect(onError).not.toHaveBeenCalled();
    expect(onResult).toHaveBeenCalledWith(expect.objectContaining({ state: "succeeded" }));
  });

  it("polls after the event-stream watchdog and reconnects when REST still reports an active command", async () => {
    vi.useFakeTimers();
    let connections = 0;
    class IdleThenSuccessfulEventSource {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor() {
        connections += 1;
        if (connections === 2) {
          queueMicrotask(() => this.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ ...pendingCommand, state: "succeeded" }) })));
        }
      }
      close() {}
    }
    vi.stubGlobal("EventSource", IdleThenSuccessfulEventSource);
    const poll = vi.spyOn(api, "applicationCommand").mockResolvedValue(pendingCommand);
    const onResult = vi.fn();
    const onError = vi.fn();

    await act(async () => {
      renderHarness(onResult, onError);
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(120_000);
    });

    expect(poll).toHaveBeenCalledTimes(1);
    expect(onError).not.toHaveBeenCalled();
    expect(onResult).not.toHaveBeenCalled();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });

    expect(connections).toBe(2);
    expect(onError).not.toHaveBeenCalled();
    expect(onResult).toHaveBeenCalledWith(expect.objectContaining({ state: "succeeded" }));
  });
});
