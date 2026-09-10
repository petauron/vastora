import { ActivityIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import type { AppData } from "../types";
import type { Language } from "../translations";
import { isInstalledApplication, pulsePrivateAccess } from "./appAccess";
import { copy } from "./shared";

export function PulseSetupNotice({ data, language }: { data: AppData; language: Language }) {
  const ready = Boolean(pulsePrivateAccess(data));
  const collectors = data.applications.filter((app) => app.appKey === "vastora-official/pulse-agent" && isInstalledApplication(app)).length;
  return <Alert>
    <ActivityIcon aria-hidden="true" />
    <AlertTitle>{copy(language, "全局节点监控", "Global node monitoring")}</AlertTitle>
    <AlertDescription>{ready
      ? copy(language, `已安装 ${collectors} 个探针。可从应用商店为其他节点安装“Pulse 探针”，接入信息自动配置。`, `${collectors} collector(s) installed. Install Pulse Agent on other nodes from the App Store; enrollment is automatic.`)
      : copy(language, "先在下方添加私网 HTTPS 入口，再从应用商店安装 Pulse 探针。现有 Komari 不受影响。", "Add a private HTTPS access point below, then install Pulse Agent from the App Store. Existing Komari installations are unchanged.")}
    </AlertDescription>
  </Alert>;
}
