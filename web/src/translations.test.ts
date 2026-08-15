import { describe, expect, it } from "vitest";
import { messages } from "./translations";

describe("translations", () => {
  it("ships every English key in Simplified Chinese", () => {
    expect(Object.keys(messages["zh-CN"]).sort()).toEqual(Object.keys(messages.en).sort());
  });
});
