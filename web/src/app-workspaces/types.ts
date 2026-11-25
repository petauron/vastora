import type { ReactNode } from "react";
import type { AppData, Mutate } from "../App";
import type { Application } from "../types";
import type { Language } from "../translations";
import type { InstalledAppGroup, InstalledAppInstance } from "../views/installed-apps-model";

export type AppWorkspaceProps = {
  group: InstalledAppGroup;
  data: AppData;
  language: Language;
  mutate: Mutate;
  showSite: boolean;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onBatchUpgrade?: (title: string, instances: InstalledAppInstance[]) => void;
  onClients: (application: Application) => void;
  onReality: (application: Application) => void;
  submittingUpgradeID?: string | null;
  activeTasksControl?: ReactNode;
  managerApplication?: Application | null;
  onManagerClose?: () => void;
};
export type AppManagerProps = Pick<AppWorkspaceProps, "data" | "language" | "mutate"> & { application: Application | null; onClose: () => void };
