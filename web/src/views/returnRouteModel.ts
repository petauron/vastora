import type { Carrier, ReturnRoute } from "../node-diagnostics-types";

export type RouteTier = "premium" | "optimized" | "standard" | "unknown";
export type RouteLine = { name: string; tier: RouteTier };

function octets(address?: string): number[] {
  if (!address) return [];
  const parts = address.split(".");
  if (parts.length !== 4) return [];
  const values = parts.map(Number);
  return values.every((value, index) => Number.isInteger(value) && value >= 0 && value <= 255 && String(value) === parts[index]) ? values : [];
}

function signature(address: string | undefined, carrier: Carrier): string {
  const ip = octets(address);
  if (!ip.length) return "";
  const [a, b, c] = ip;
  if (carrier === "telecom") {
    if (a === 59 && b === 43) return "cn2";
    if (a === 202 && b === 97) return "163";
    if (a === 69 && b === 194 || a === 203 && b === 22) return "ctgnet";
  }
  if (carrier === "unicom") {
    if (a === 218 && b === 105 || a === 210 && b === 51) return "9929";
    if (a === 219 && b === 158) return "4837";
    if (a === 203 && b === 160 && c >= 64 && c <= 95 || a === 202 && b === 77 || a === 43 && b === 252 || a === 61 && b === 14) return "10099";
  }
  if (carrier === "mobile") {
    if (a === 223 && (b === 120 && c >= 128 || b === 118 && c === 32 || b === 119 && (c >= 8 && c <= 15 || c >= 26 && c <= 29 || c >= 32 && c <= 37 || [74, 75, 88, 89, 100, 252, 253].includes(c)))) return "cmin2";
    if (a === 223 && b >= 118 && b <= 121) return "cmi";
    if (a === 221 && b === 183 || a === 111 && b === 24) return "cmnet";
  }
  return "";
}

// A responding hop is evidence of a backbone segment, not proof that every
// packet used it. Single ordinary-backbone hops may only be destination access.
export function routeLine(route: ReturnRoute): RouteLine {
  const seen = route.hops.map((hop) => signature(hop.address, route.carrier));
  const first = (name: string) => seen.indexOf(name);
  const count = (name: string) => seen.filter((value) => value === name).length;
  if (route.carrier === "telecom") {
    if (count("cn2") >= 2) return first("163") >= 0 && first("163") < first("cn2") ? { name: "CN2 混合", tier: "optimized" } : { name: "CN2", tier: "premium" };
    if (first("ctgnet") >= 0) return { name: "CTGNET", tier: "premium" };
    if (count("163") >= 2) return { name: "163", tier: "standard" };
  }
  if (route.carrier === "unicom") {
    if (first("9929") >= 0) return first("4837") >= 0 && first("4837") < first("9929") ? { name: "9929 混合", tier: "optimized" } : { name: "9929", tier: "premium" };
    if (first("10099") >= 0) return { name: "10099", tier: "optimized" };
    if (count("4837") >= 2) return { name: "4837", tier: "standard" };
  }
  if (route.carrier === "mobile") {
    if (first("cmin2") >= 0) return first("cmi") >= 0 && first("cmi") < first("cmin2") ? { name: "CMIN2 混合", tier: "optimized" } : { name: "CMIN2", tier: "premium" };
    if (first("cmi") >= 0) return { name: "CMI", tier: "standard" };
    if (count("cmnet") >= 2) return { name: "CMNET", tier: "standard" };
  }
  return { name: "", tier: "unknown" };
}
