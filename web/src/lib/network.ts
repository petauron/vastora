const hostnamePattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/;

export function normalizeHostname(value: string) {
  return value.trim().toLowerCase().replace(/\.$/, "");
}

export function validHostname(value: string) {
  return hostnamePattern.test(normalizeHostname(value));
}

export function validCenterURL(value: string) {
  try {
    const parsed = new URL(value);
    if (parsed.username || parsed.password || parsed.search || parsed.hash || parsed.pathname !== "/") return false;
    if (parsed.protocol === "https:") return true;
    return parsed.protocol === "http:" && (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost");
  } catch {
    return false;
  }
}

export function browserTimezone() {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

export function vastoraDomainDefaults(zoneName: string) {
  const zone = zoneName.trim().toLowerCase().replace(/\.+$/, "");
  if (!zone) return { zone: "", namespace: "", centerURL: "", headscaleURL: "" };
  const namespace = `vastora.${zone}`;
  return {
    zone,
    namespace,
    centerURL: `https://center.${namespace}`,
    headscaleURL: `https://headscale.${namespace}`
  };
}
