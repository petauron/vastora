// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { Publication } from "../types";
import { pulseDashboardURL, pulsePrivateAccess } from "./appAccess";
import { applicationLaunchURL, desktopApplications } from "./applicationLaunch";
import { installedAppGroups } from "./installed-apps-model";
import { PulseWorkspace } from "./PulseWorkspace";
import { dashboard, render } from "./views.test-support";

function access(kind: Publication["kind"], hostname: string): Publication {
  return { id: kind, serviceId: "dashboard", kind, ingress: kind === "cloudflare_tunnel" ? { owner: "tunnel_connector", entryNodeId: "agent" } : { owner: "site_gateway", entryNodeId: "agent" }, hostname, dnsProvider: kind === "cloudflare_tunnel" ? "cloudflare" : "headscale", tlsEnabled: true, desiredRevision: 1, appliedRevision: 1, status: "ready", accessUrl: `https://${hostname}/`, createdAt: "", updatedAt: "" };
}

function senulun() {
  const data = dashboard();
  data.applications = [{ ...data.applications[0], id: "senulun", name: "Senulun", appKey: "vastora-official/pulse" }];
  data.apps = [];
  data.services = [{ id: "dashboard", applicationId: "senulun", siteId: "site", name: "dashboard", protocol: "http", containerPort: 8080, hostPort: 18080, endpoint: "192.0.2.1:18080", source: "catalog", management: true, status: "ready", createdAt: "", updatedAt: "" }];
  data.publications = [access("headscale_gateway", "senulun.private.example"), access("cloudflare_tunnel", "senulun.example.com")];
  return data;
}

describe("Senulun browser access", () => {
  it("opens the configured public tunnel while retaining private access for probes", () => {
    const data = senulun();
    expect(applicationLaunchURL(data, "vastora-official/pulse")).toBe("https://senulun.example.com/");
    expect(desktopApplications(data, "en")[0].url).toBe("https://senulun.example.com/");
    expect(pulsePrivateAccess(data)?.accessUrl).toBe("https://senulun.private.example/");
  });

  it("opens the same browser URL from the application workspace", () => {
    const data = senulun();
    const noop = () => undefined;
    const container = render(<PulseWorkspace data={data} group={installedAppGroups(data)[0]} language="en" showSite={false} mutate={async () => undefined} onManage={noop} onUpgrade={noop} onClients={noop} onReality={noop} />);
    expect(container.querySelector('a')?.getAttribute("href")).toBe("https://senulun.example.com/");
  });

  it("keeps private-only installations private", () => {
    const data = senulun();
    data.publications = [data.publications[0]];
    expect(pulseDashboardURL(data)).toBe("https://senulun.private.example/");
    data.publications[0] = access("lan_gateway", "senulun.lan.example");
    expect(pulseDashboardURL(data)).toBe("https://senulun.lan.example/");
  });

  it.each([
    { status: "failed" as const },
    { actionRequired: true },
    { lastError: "Access not ready" },
    { tlsEnabled: false },
    { accessUrl: "http://senulun.example.com/" },
    { accessUrl: "https://user:secret@senulun.example.com/" },
    { serviceId: "another-service" },
  ])("ignores an unusable public entry: %j", (change) => {
    const data = senulun();
    Object.assign(data.publications[1], change);
    expect(pulseDashboardURL(data)).toBe("https://senulun.private.example/");
  });

  it("does not open a stopped application or an unhealthy private-only entry", () => {
    const data = senulun();
    data.applications[0].status = "stopped";
    expect(pulseDashboardURL(data)).toBeUndefined();
    data.applications[0].status = "running";
    data.publications = [{ ...data.publications[0], actionRequired: true }];
    expect(pulseDashboardURL(data)).toBeUndefined();
  });
});
