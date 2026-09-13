// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { ActivityView } from "./ActivityView";

it("shows a superseded gateway revision without rewriting its queued event", () => {
  const html = renderToStaticMarkup(<ActivityView language="zh-CN" agents={[]} actions={[{
    id: "event-1", taskId: "gateway-r1", agentId: "gateway", kind: "gateway.routes.apply",
    revision: 1, event: "queued", currentState: "superseded", createdAt: "2026-09-14T00:00:00Z",
  }]} />);
  expect(html).toContain("已被替代");
  expect(html).toContain("不再排队");
  expect(html).not.toContain("已排队");
  expect(html).toContain("queued");
});
