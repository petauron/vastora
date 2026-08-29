export function validRemoteAccessAudience(kind: "email" | "email_domain", rawValue: string) {
  const value = rawValue.trim().toLowerCase().replace(/^@/, "");
  if (kind === "email") return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);
  return /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/.test(value);
}

export function standardHTTPSHostnameInZone(value: string, zone: string) {
  try {
    const parsed = new URL(value);
    const hostname = parsed.hostname.toLowerCase().replace(/\.$/, "");
    const normalizedZone = zone.toLowerCase().replace(/\.$/, "");
    return parsed.protocol === "https:" && parsed.port === "" && parsed.pathname === "/" && !parsed.username && !parsed.password && !parsed.search && !parsed.hash && (hostname === normalizedZone || hostname.endsWith(`.${normalizedZone}`));
  } catch {
    return false;
  }
}

export function centerRemoteAccessHostname(zone: string) {
  const normalized = zone.trim().toLowerCase().replace(/\.$/, "");
  return normalized ? `center-vastora.${normalized}` : "";
}
