export type UpgradeVersionOptions = {
  fromVersion?: string;
  toVersion?: string;
  fromRevision?: number;
  toRevision?: number;
  prefix?: string;
};

export function isValidRevision(value: number | undefined): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0;
}

export function isSameVersionRevisionUpgrade({
  fromVersion,
  toVersion,
  fromRevision,
  toRevision,
}: Omit<UpgradeVersionOptions, "prefix">): boolean {
  return Boolean(fromVersion && toVersion)
    && fromVersion === toVersion
    && isValidRevision(fromRevision)
    && isValidRevision(toRevision)
    && fromRevision !== toRevision;
}

export function formatUpgradeTransition({
  fromVersion,
  toVersion,
  fromRevision,
  toRevision,
  prefix = "",
}: UpgradeVersionOptions): string {
  if (isSameVersionRevisionUpgrade({ fromVersion, toVersion, fromRevision, toRevision })) {
    return `${prefix}${toVersion} (r${fromRevision} → r${toRevision})`;
  }
  const from = fromVersion ? `${prefix}${fromVersion}` : "—";
  const to = toVersion ? `${prefix}${toVersion}` : "—";
  return `${from} → ${to}`;
}

export function formatUpgradeTarget({
  fromVersion,
  toVersion,
  fromRevision,
  toRevision,
  prefix = "v",
}: UpgradeVersionOptions): string {
  const version = toVersion || fromVersion;
  if (!version) return "—";
  if (isSameVersionRevisionUpgrade({ fromVersion, toVersion, fromRevision, toRevision })) {
    return `${prefix}${toVersion} (r${fromRevision} → r${toRevision})`;
  }
  return `${prefix}${version}`;
}
