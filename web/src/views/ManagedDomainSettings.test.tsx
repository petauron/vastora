// @vitest-environment jsdom
import { act } from "react";
import { expect, it, vi } from "vitest";
import { api } from "../api";
import { ManagedDomainSettings } from "./ManagedDomainSettings";
import { dashboard, render } from "./views.test-support";

vi.mock("@/components/SelectControl", () => ({ SelectControl: ({ id, value, options, onValueChange }: { id: string; value: string; options: { value: string; label: string }[]; onValueChange: (value: string) => void }) => <select id={id} value={value} onChange={event => onValueChange(event.target.value)}><option value="">Choose</option>{options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}</select> }));

it("requires a new confirmation for the exact family migration and sends its prior address and revision", async () => {
 const data=dashboard();const app=data.applications[0];app.status="running";
 const domain={id:"domain",key:"reality-site",prefix:"sg",hostname:"sg-random.example.com",address:"2001:4860:4860::8888",type:"AAAA" as const,revision:7,appliedRevision:7,status:"ready" as const};
 vi.spyOn(api,"managedDomains").mockResolvedValue({domains:[domain]});
 const save=vi.spyOn(api,"ensureManagedDomain").mockResolvedValue({...domain,address:"1.1.1.1",type:"A",revision:8,appliedRevision:8});
 const container=render(<ManagedDomainSettings applications={[app]} agents={data.agents} language="en" />);
 await act(async()=>{const select=container.querySelector("select")!;select.value=app.id;select.dispatchEvent(new Event("change",{bubbles:true}));});
 await act(async()=>{[...container.querySelectorAll("button")].find(x=>x.textContent?.includes("reality-site"))!.click();});
 const set=(id:string,value:string)=>act(()=>{const input=container.querySelector<HTMLInputElement>(id)!;Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,"value")!.set!.call(input,value);input.dispatchEvent(new Event("input",{bubbles:true}));});
 set("#managed-domain-address","8.8.8.8");set("#managed-domain-previous",domain.address);
 const submit=container.querySelector<HTMLButtonElement>('button[type="submit"]')!;
 expect(submit.disabled).toBe(true);
 act(()=>container.querySelector<HTMLElement>("#managed-domain-family")!.click());expect(submit.disabled).toBe(false);
 set("#managed-domain-address","1.1.1.1");expect(submit.disabled).toBe(true);
 act(()=>container.querySelector<HTMLElement>("#managed-domain-family")!.click());
 await act(async()=>{container.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));});
 expect(save).toHaveBeenCalledWith({applicationId:app.id,key:domain.key,prefix:"sg",hostname:domain.hostname,address:"1.1.1.1",expectedRevision:7,expectedRecordAddress:domain.address,migrateAddressFamily:true});
});


it("adds the other family on the exact retained domain and revision", async () => {
 const data=dashboard();const app=data.applications[0];app.status="running";
 const domain={id:"dual-domain",key:"ingress",hostname:"random.example.test",address:"2001:4860:4860::8888",type:"AAAA" as const,revision:7,appliedRevision:7,status:"ready" as const};
 vi.spyOn(api,"managedDomains").mockResolvedValue({domains:[domain]});
 const save=vi.spyOn(api,"ensureManagedDomain").mockResolvedValue({...domain,secondaryAddress:"1.1.1.1",revision:8,status:"pending"});
 const container=render(<ManagedDomainSettings applications={[app]} agents={data.agents} language="en" />);
 await act(async()=>{const select=container.querySelector("select")!;select.value=app.id;select.dispatchEvent(new Event("change",{bubbles:true}));});
 await act(async()=>{[...container.querySelectorAll("button")].find(x=>x.textContent?.includes("random.example.test"))!.click();});
 act(()=>{const input=container.querySelector<HTMLInputElement>("#managed-domain-secondary")!;Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,"value")!.set!.call(input,"1.1.1.1");input.dispatchEvent(new Event("input",{bubbles:true}));});
 await act(async()=>{container.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true}));});
 expect(save).toHaveBeenLastCalledWith({applicationId:app.id,key:"ingress",prefix:"",hostname:domain.hostname,address:domain.address,secondaryAddress:"1.1.1.1",expectedRevision:7,expectedRecordAddress:undefined,migrateAddressFamily:undefined});
});
