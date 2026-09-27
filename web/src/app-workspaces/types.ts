import type { AppData, Mutate } from "../App";
import type { Application } from "../types";
import type { Language } from "../translations";
import type { InstalledAppGroup } from "../views/installed-apps-model";

export type AppWorkspaceProps = {
  group: InstalledAppGroup;
  data: AppData;
  language: Language;
  mutate: Mutate;
  showSite: boolean;
  onManage: (application: Application) => void;
  onUpgrade: (application: Application) => void;
  onClients: (application: Application) => void;
  onReality: (application: Application) => void;
  managerApplication?: Application | null;
  onManagerClose?: () => void;
};
export type AppManagerProps = Pick<AppWorkspaceProps, "data" | "language" | "mutate"> & { application: Application | null; onClose: () => void };
