export function regionFlag(code?: string) {
  const normalized = code?.trim().toUpperCase() ?? "";
  if (!/^[A-Z]{2}$/.test(normalized)) return "";
  return String.fromCodePoint(...Array.from(normalized, (character) => 0x1f1e6 + character.charCodeAt(0) - 65));
}

export function displayRegionFlag(code?: string) {
  return code?.trim().toUpperCase() === "TW" ? "🇨🇳" : regionFlag(code);
}

export function regionName(code: string, locales: string[]) {
  if (!/^[A-Z]{2}$/.test(code)) return code;
  try {
    return new Intl.DisplayNames(locales, { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

const siteRegionCodes: Record<string, string> = {
  "中国": "CN", "china": "CN",
  "台湾": "TW", "taiwan": "TW",
  "香港": "HK", "hong kong": "HK",
  "新加坡": "SG", "singapore": "SG",
  "美国": "US", "美西": "US", "美东": "US", "united states": "US", "usa": "US",
  "德国": "DE", "germany": "DE",
  "日本": "JP", "japan": "JP",
  "韩国": "KR", "korea": "KR",
  "英国": "GB", "uk": "GB", "united kingdom": "GB",
  "荷兰": "NL", "netherlands": "NL",
  "法国": "FR", "france": "FR",
  "加拿大": "CA", "canada": "CA",
  "澳大利亚": "AU", "australia": "AU",
};

export function siteRegionCode(site: { name: string }) {
  const [first = "", second = ""] = site.name.trim().split(/[-–—·｜|]/, 2).map((part) => part.trim().toLowerCase());
  return (first === "中国" || first === "china" ? siteRegionCodes[second] : undefined) ?? siteRegionCodes[first] ?? "";
}

export function siteLabel(site: { name: string }) {
  const flag = displayRegionFlag(siteRegionCode(site));
  return flag ? `${flag} ${site.name}` : site.name;
}
