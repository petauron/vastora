import { describe, expect, it } from "vitest";
import { dockerInstallCommand, dockerInstallDocsURLs } from "./docker-install";

describe("Docker installation command", () => {
  it("detects only the supported target-server distributions before installing packages", () => {
    expect(dockerInstallCommand).toContain(". /etc/os-release");
    expect(dockerInstallCommand).toContain('case "${ID:-}:${VERSION_ID:-}" in');
    for (const release of [
      "debian:12) distro=debian; codename=bookworm ;;",
      "debian:13) distro=debian; codename=trixie ;;",
      "ubuntu:22.04) distro=ubuntu; codename=jammy ;;",
      "ubuntu:24.04) distro=ubuntu; codename=noble ;;",
      "ubuntu:26.04) distro=ubuntu; codename=resolute ;;",
    ]) {
      expect(dockerInstallCommand).toContain(release);
    }
    expect(dockerInstallCommand).toContain("Unsupported system:");
    expect(dockerInstallCommand).not.toContain("ID_LIKE");
    expect(dockerInstallCommand).not.toContain("debian:11)");
    expect(dockerInstallCommand).not.toContain("ubuntu:20.04)");
    expect(dockerInstallCommand.indexOf("Unsupported system:")).toBeLessThan(dockerInstallCommand.indexOf("apt-get update"));
    expect(dockerInstallCommand).toContain('arch="$(dpkg --print-architecture)"');
    expect(dockerInstallCommand).toContain("amd64|arm64) ;;");
    expect(dockerInstallCommand).toContain("URIs: https://download.docker.com/linux/$distro");
    expect(dockerInstallCommand).toContain("Suites: $codename");
    expect(dockerInstallCommand).toContain("Architectures: $arch");
    expect(dockerInstallDocsURLs.debian).toBe("https://docs.docker.com/engine/install/debian/");
    expect(dockerInstallDocsURLs.ubuntu).toBe("https://docs.docker.com/engine/install/ubuntu/");
  });

  it("preserves existing runtimes and confines failures to a subshell", () => {
    expect(dockerInstallCommand.startsWith("(\nset -eu\n")).toBe(true);
    expect(dockerInstallCommand.endsWith("\n)")).toBe(true);
    expect(dockerInstallCommand).toContain("Docker is already installed.");
    expect(dockerInstallCommand).toContain("Conflicting package:");
    expect(dockerInstallCommand.indexOf("Docker is already installed.")).toBeLessThan(dockerInstallCommand.indexOf("apt-get update"));
    expect(dockerInstallCommand.indexOf("Conflicting package:")).toBeLessThan(dockerInstallCommand.indexOf("apt-get update"));
    expect(dockerInstallCommand).toContain('if [ "$(id -u)" -eq 0 ]; then\n    "$@"');
    expect(dockerInstallCommand).toContain('sudo "$@"');
    expect(dockerInstallCommand).not.toMatch(/apt-get (remove|purge|upgrade)/);
    expect(dockerInstallCommand).toContain("${db:Status-Status}");
    expect(dockerInstallCommand).toContain("apt-get install --no-remove -y");
    expect(dockerInstallCommand).not.toContain("/var/lib/docker");
    expect(dockerInstallCommand).toContain("docker-compose-plugin");
  });
});
