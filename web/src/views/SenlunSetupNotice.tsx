import { ActivityIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import type { Language } from "../translations";
import { copy } from "./shared";

export function SenlunSetupNotice({ language }: { language: Language }) {
  return <Alert>
    <ActivityIcon aria-hidden="true" />
    <AlertTitle>{copy(language, "在 Senlun 中管理监控", "Manage monitoring in Senlun")}</AlertTitle>
    <AlertDescription>{copy(language, "打开 Senlun 完成管理员设置，并在其中创建探针配对令牌。安装探针时填写可访问的 Senlun HTTPS 地址、节点名称和令牌；监控主机可以独立部署在 Vastora 之外。", "Open Senlun to set up its administrator and create an Agent enrollment token. Install the Agent with a reachable Senlun HTTPS URL, node name and token. The monitoring service can run outside Vastora.")}
    </AlertDescription>
  </Alert>;
}
