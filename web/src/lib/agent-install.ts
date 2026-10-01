import type { AgentEnrollment } from "../types";

export function agentInstallCommand({ centerURL, enrollment, installerAvailable }: { centerURL: string; enrollment: AgentEnrollment; installerAvailable: boolean }) {
  const enrollmentCenterURL = enrollment.centerUrl || centerURL;
  const caCertificate = enrollment.caCertificatePem?.trim() ?? "";
  const caPath = caCertificate ? "/tmp/vastora-center-ca.pem" : "";
  const writeCA = caCertificate ? `printf '%s' ${shellQuote(caCertificate)} > ${caPath} && chmod 0600 ${caPath} && ` : "";
  if (installerAvailable) {
    const installer = "/tmp/vastora-agent-install.sh";
    const bootstrapUsesCA = Boolean(caCertificate) && enrollment.installerUrl.replace(/\/$/, "") === enrollmentCenterURL.replace(/\/$/, "");
    const bootstrapTrust = bootstrapUsesCA ? `--cacert ${caPath} ` : "";
    return `${writeCA}curl ${bootstrapTrust}-fsSL ${shellQuote(`${enrollment.installerUrl.replace(/\/$/, "")}/install/agent.sh`)} -o ${installer} && chmod +x ${installer} && ${installer} ${shellQuote(enrollment.token)} ${shellQuote(caPath)} ${bootstrapUsesCA ? "1" : "0"}`;
  }
  const caArgument = caPath ? ` --ca-certificate ${caPath}` : "";
  return `${writeCA}printf '%s' ${shellQuote(enrollment.token)} | sudo /usr/local/bin/vastora agent install --center-url ${shellQuote(enrollmentCenterURL)} --token-file -${caArgument}`;
}

export function shellQuote(value: string) { return `'${value.replaceAll("'", `'\\''`)}'`; }
