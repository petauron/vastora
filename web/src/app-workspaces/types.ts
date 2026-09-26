import type { ComponentType } from "react";
import type { AppData, Mutate } from "../App";
import type { Application, LocalizedText } from "../types";
import type { Language } from "../translations";
import type { InstalledAppGroup } from "../views/installed-apps-model";

export type AppUICapability = "ip-quality" | "landing" | "accounts";
export type AppUIManifest = {
  appKey: string;
  apiVersion: 1;
  pages: readonly { id: string; title: LocalizedText; surface: "tab" | "manager"; capabilities: readonly AppUICapability[] }[];
};

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
};
export type AppManagerProps = Pick<AppWorkspaceProps, "data" | "language" | "mutate"> & { application: Application | null; onClose: () => void };
export type AppUIModule = { Workspace: ComponentType<AppWorkspaceProps>; Manager: ComponentType<AppManagerProps> };
