// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { api } from "./api";

afterEach(() => { vi.unstubAllGlobals(); document.cookie = "vastora_csrf=; Max-Age=0; Path=/"; });
it("sends explicit disposition and CSRF to the correct operation without replay", async () => {
  document.cookie = "vastora_csrf=test-csrf; Path=/";
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ recorded: true }))));
  vi.stubGlobal("fetch", fetchMock);
  const input = { action: "abandon" as const, executionStopped: true, note: "Verified stopped" };
  await api.disposeExecution("e/a", "application.apply", input);
  await api.disposeExecution("e/a", "agent.update", input);
  await api.disposeExecution("e/a", "agent.update", { ...input, action: "reexecute" });
  expect(fetchMock.mock.calls.map(([path]) => path)).toEqual(["/api/v1/executions/e%2Fa/abandon", "/api/v1/executions/e%2Fa/resolve-helper", "/api/v1/executions/e%2Fa/reexecute"]);
  expect(new Headers(fetchMock.mock.calls[0][1].headers).get("X-CSRF-Token")).toBe("test-csrf");
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual(input);
  fetchMock.mockRejectedValueOnce(new Error("reply lost"));
  await expect(api.disposeExecution("e", "application.apply", input)).rejects.toThrow("reply lost");
  expect(fetchMock).toHaveBeenCalledTimes(4);
});
it("passes page cursor and cancellation without requesting private evidence contents", async () => {
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ executions: [], nextCursor: 0 }))));
  vi.stubGlobal("fetch", fetchMock);
  const controller = new AbortController();
  await api.executions(101, controller.signal);
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/executions?before=101");
  expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal);
});

it("routes retained completion and archive disposition without enabling legacy replay", async () => {
  const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ recorded: true }))));
  vi.stubGlobal("fetch", fetchMock);
  const input = { action: "confirm-completed" as const, executionStopped: true, note: "Verified actual result" };
  await api.disposeExecution("e/a", "application.apply", input);
  await api.disposeExecution("e/a", "legacy.receipt", { ...input, action: "abandon" });
  await expect(api.disposeExecution("e/a", "legacy.receipt", input)).rejects.toThrow();
  await expect(api.disposeExecution("e/a", "legacy.receipt", { ...input, action: "reexecute" })).rejects.toThrow();
  expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/executions/e%2Fa/confirm-completed", "/api/v1/executions/e%2Fa/resolve-legacy",
  ]);
  expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ ...input, action: "abandon" });
});
