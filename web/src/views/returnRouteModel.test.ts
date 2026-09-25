import { describe, expect, it } from "vitest";
import type { Carrier, ReturnRoute } from "../node-diagnostics-types";
import { routeLine } from "./returnRouteModel";

// Invented host octets exercise the known backbone prefixes without copying
// addresses from a real traceroute into public fixtures.
const ip = (a: number, b: number, c: number, d: number) => [a, b, c, d].join(".");
function route(carrier: Carrier, ...addresses: string[]): ReturnRoute {
  return { carrier, stopReason: "max_hops", hops: addresses.map((address, index) => ({ ttl: index + 1, address })) };
}

const other = ip(192, 0, 2, 1);

describe("return route summary", () => {
  it("identifies CN2, AS10099, and CMIN2 backbone segments", () => {
    expect(routeLine(route("telecom", other, ip(59, 43, 0, 1), ip(59, 43, 0, 2), other))).toEqual({ name: "CN2", tier: "premium" });
    expect(routeLine(route("unicom", other, ip(203, 160, 64, 1), ip(203, 160, 64, 2), other))).toEqual({ name: "10099", tier: "optimized" });
    expect(routeLine(route("mobile", other, ip(223, 120, 128, 1), ip(223, 120, 128, 2)))).toEqual({ name: "CMIN2", tier: "premium" });
  });

  it("does not treat one delivery hop or an unannounced Unicom address as a premium backbone", () => {
    expect(routeLine(route("unicom", other, ip(219, 158, 0, 1), other))).toEqual({ name: "", tier: "unknown" });
    expect(routeLine(route("telecom", ip(59, 43, 0, 1)))).toEqual({ name: "", tier: "unknown" });
  });

  it("distinguishes CMI from CMIN2 and classifies ordinary backbones", () => {
    expect(routeLine(route("mobile", ip(223, 120, 0, 1)))).toEqual({ name: "CMI", tier: "standard" });
    expect(routeLine(route("unicom", ip(219, 158, 0, 1), ip(219, 158, 0, 2)))).toEqual({ name: "4837", tier: "standard" });
    expect(routeLine(route("telecom", ip(202, 97, 0, 1), ip(202, 97, 0, 2)))).toEqual({ name: "163", tier: "standard" });
    expect(routeLine(route("unicom", ip(219, 158, 0, 1), ip(218, 105, 0, 1)))).toEqual({ name: "9929 混合", tier: "optimized" });
  });
});
