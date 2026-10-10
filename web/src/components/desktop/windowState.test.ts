import { describe, expect, it } from "vitest";
import { dismissWindow, emptyWindows, frontWindow, openWindow } from "./windowState";

describe("desktop window lifecycle", () => {
  it("keeps creation order stable while focusing and never duplicates the same application", () => {
    let state = openWindow(emptyWindows(), "apps", "example/one");
    state = openWindow(state, "apps", "example/two");
    state = openWindow(state, "nodes");
    state = openWindow(state, "apps", "example/one");
    expect(state.entries.map((entry) => entry.id)).toEqual(["app:example/one", "app:example/two", "nodes"]);
    expect(state.stack).toEqual(["app:example/two", "nodes", "app:example/one"]);
    expect(frontWindow(state)?.workspaceKey).toBe("example/one");
  });
  it("minimizes only one window and chooses the next visible one, while close removes it", () => {
    let state = openWindow(openWindow(emptyWindows(), "overview"), "nodes");
    state = dismissWindow(state, "nodes", false);
    expect(frontWindow(state)?.screen).toBe("overview");
    expect(state.entries).toHaveLength(2);
    state = dismissWindow(state, "overview", true);
    expect(frontWindow(state)).toBeUndefined();
    expect(state.entries.map((entry) => entry.id)).toEqual(["nodes"]);
    state = openWindow(state, "nodes");
    expect(frontWindow(state)?.screen).toBe("nodes");
  });
  it("shows the desktop by minimizing all windows and restores one without restoring the others", () => {
    let state = openWindow(openWindow(emptyWindows(), "settings"), "nodes");
    state = openWindow(state, "home");
    expect(frontWindow(state)).toBeUndefined();
    expect(state.entries).toHaveLength(2);
    state = openWindow(state, "settings");
    expect(frontWindow(state)?.screen).toBe("settings");
    expect(state.entries.find((entry) => entry.id === "nodes")?.minimized).toBe(true);
  });
});
