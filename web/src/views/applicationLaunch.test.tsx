// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { Publication } from "../types";
import { senulunDashboardURL } from "./appAccess";
import { applicationLaunchURL, desktopApplications } from "./applicationLaunch";
import { installedAppGroups } from "./installed-apps-model";
import { SenlunWorkspace } from "./SenlunWorkspace";
import { dashboard, render } from "./views.test-support";

function access(kind: Publication["kind"], hostname: string): Publication {
  return { id: kind, serviceId: "dashboard", kind, ingress: kind === "cloudflare_tunnel" ? { owner: "tunnel_connector", entryNodeId: "agent" } : { owner: "site_gateway", entryNodeId: "agent" }, hostname, dnsProvider: kind === "cloudflare_tunnel" ? "cloudflare" : "headscale", tlsEnabled: true, desiredRevision: 1, appliedRevision: 1, status: "ready", accessUrl: `https://${hostname}/`, createdAt: "", updatedAt: "" };
}

function senulun() {
  const data = dashboard();
  data.applications = [{ ...data.applications[0], id: "senulun", name: "Senlun", appKey: "vastora-official/pulse" }];
  data.apps = [];
  data.services = [{ id: "dashboard", applicationId: "senulun", siteId: "site", name: "dashboard", protocol: "http", containerPort: 8080, hostPort: 18080, endpoint: "192.0.2.1:18080", source: "catalog", management: true, status: "ready", createdAt: "", updatedAt: "" }];
  data.publications = [access("headscale_gateway", "senulun.private.example"), access("cloudflare_tunnel", "senulun.example.com")];
  return data;
}

describe("Senlun browser access", () => {
  it("does not use retained shortcuts or URLs while desktop data is unavailable", () => {
    const data = senulun();
    data.desktopDataUnavailable = true;
    expect(desktopApplications(data, "en")).toEqual([]);
    expect(applicationLaunchURL(data, "vastora-official/pulse")).toBeUndefined();
    data.desktopDataUnavailable = false;
    expect(desktopApplications(data, "en")[0].url).toBe("https://senulun.example.com/");
  });

  it("opens the configured public tunnel", () => {
    const data = senulun();
    expect(applicationLaunchURL(data, "vastora-official/pulse")).toBe("https://senulun.example.com/");
    expect(desktopApplications(data, "en")[0].url).toBe("https://senulun.example.com/");
  });

  it("does not guess a global shortcut when services are installed on multiple hosts", () => {
    const data = senulun();
    data.applications.push({ ...data.applications[0], id: "other-senulun", nodeId: "other-node" });
    expect(senulunDashboardURL(data)).toBeUndefined();
  });

  it("opens the same browser URL from the application workspace", () => {
    const data = senulun();
    const noop = () => undefined;
    const container = render(<SenlunWorkspace data={data} group={installedAppGroups(data)[0]} language="en" showSite={false} mutate={async () => undefined} onManage={noop} onUpgrade={noop} onClients={noop} onReality={noop} />);
    expect(container.querySelector('a')?.getAttribute("href")).toBe("https://senulun.example.com/");
  });

  it("keeps private-only installations private", () => {
    const data = senulun();
    data.publications = [data.publications[0]];
    expect(senulunDashboardURL(data)).toBe("https://senulun.private.example/");
    data.publications[0] = access("lan_gateway", "senulun.lan.example");
    expect(senulunDashboardURL(data)).toBe("https://senulun.lan.example/");
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
    expect(senulunDashboardURL(data)).toBe("https://senulun.private.example/");
  });

  it("does not open a stopped application or an unhealthy private-only entry", () => {
    const data = senulun();
    data.applications[0].status = "stopped";
    expect(senulunDashboardURL(data)).toBeUndefined();
    data.applications[0].status = "running";
    data.publications = [{ ...data.publications[0], actionRequired: true }];
    expect(senulunDashboardURL(data)).toBeUndefined();
  });
});
