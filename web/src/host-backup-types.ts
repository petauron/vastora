export type HostBackupSettings = {
  enabled: boolean;
  directory: string;
  timeUTC: string;
  intervalDays: number;
  keepLast: number;
};
export type HostBackupStatus = {
  available: boolean;
  passwordSet: boolean;
  settings: HostBackupSettings;
  execution: { state: string; at?: string; message?: string; snapshotId?: string; bytes?: number };
  coverage: { containers: string[]; units: string[]; sources: string[] };
  snapshots: { id: string; time: string; bytes: number }[];
};
