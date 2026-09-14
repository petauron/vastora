export function regionFlag(code?: string) {
  const normalized = code?.trim().toUpperCase() ?? "";
  if (!/^[A-Z]{2}$/.test(normalized)) return "";
  return String.fromCodePoint(...Array.from(normalized, (character) => 0x1f1e6 + character.charCodeAt(0) - 65));
}

export function regionName(code: string, locales: string[]) {
  if (!/^[A-Z]{2}$/.test(code)) return code;
  try {
    return new Intl.DisplayNames(locales, { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}
