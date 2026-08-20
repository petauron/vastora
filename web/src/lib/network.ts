export function validCenterURL(value: string) {
  try {
    const parsed = new URL(value);
    if (parsed.username || parsed.password || parsed.search || parsed.hash || parsed.pathname !== "/") return false;
    if (parsed.protocol === "https:") return true;
    return parsed.protocol === "http:" && (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost" || parsed.hostname === "::1");
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
