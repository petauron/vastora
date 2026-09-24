package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/dockerruntime"
)

func testXrayWorkerState() xrayWorkerState {
	return xrayWorkerState{
		ApplicationID: "application", ImageReference: xrayWorkerImageReference, Address: "100.64.0.10", PanelPort: 2053, APIToken: "secret", Revision: 1, AppliedRevision: 1, NextInboundID: 2,
		XraySetting: defaultXrayWorkerSettings(),
		Inbounds:    []json.RawMessage{json.RawMessage(`{"id":1,"enable":true,"tag":"managed","protocol":"vless","listen":"0.0.0.0","port":443,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"reality","sockopt":{"acceptProxyProtocol":true}}}`)},
	}
}

func TestXrayWorkerImageReferenceMustBeOfficialTaggedAndPinned(t *testing.T) {
	for _, value := range []string{
		"ghcr.io/xtls/xray-core:26.7.28",
		"ghcr.io/xtls/xray-core@sha256:45338c4df61fda061c47ce62aafda6c5d7d59cbdefc33f2e335d8b0c748b748a",
		"example.test/xtls/xray-core:26.7.28@sha256:b697cda1588faca696ab7f7755dd1161f60862af3ff6026300e44cff6aedd558",
	} {
		if validXrayWorkerImageReference(value) {
			t.Fatalf("accepted untrusted Xray image reference %q", value)
		}
	}
	if !validXrayWorkerImageReference(xrayWorkerImageReference) || xrayWorkerImageVersion(xrayWorkerImageReference) != xrayWorkerVersion {
		t.Fatal("audited Xray image reference is not accepted")
	}
}

func TestResumeXrayWorkerDoesNotResurrectKeepDataUninstall(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.saveXrayWorkerState(context.Background(), testXrayWorkerState()); err != nil {
		t.Fatal(err)
	}
	if err := store.ResumeXrayWorker(context.Background(), "not-a-docker-socket"); err != nil {
		t.Fatalf("retained state tried to resume after uninstall: %v", err)
	}
}

func TestXrayWorkerMigrationFingerprintIgnoresTrafficButDetectsAuthorityChanges(t *testing.T) {
	state := testXrayWorkerState()
	base, err := xrayWorkerMigrationFingerprint(state.Inbounds, state.XraySetting)
	if err != nil {
		t.Fatal(err)
	}
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["up"], inbound["down"] = float64(100), float64(200)
	inbound["clientStats"] = []any{map[string]any{"email": "phone", "up": float64(10), "down": float64(20)}}
	withTraffic, _ := json.Marshal(inbound)
	trafficFingerprint, err := xrayWorkerMigrationFingerprint([]json.RawMessage{withTraffic}, state.XraySetting)
	if err != nil || trafficFingerprint != base {
		t.Fatalf("traffic changed migration identity: %q %v", trafficFingerprint, err)
	}
	inbound["total"] = float64(1024)
	changed, _ := json.Marshal(inbound)
	changedFingerprint, err := xrayWorkerMigrationFingerprint([]json.RawMessage{changed}, state.XraySetting)
	if err != nil || changedFingerprint == base {
		t.Fatalf("authority change was not detected: %q %v", changedFingerprint, err)
	}
}

func TestXrayWorkerAcceptsControllerFormPayload(t *testing.T) {
	form := url.Values{
		"enable": {"true"}, "tag": {"remote-node"}, "protocol": {"vless"}, "port": {"443"},
		"shareAddrStrategy": {"custom"}, "shareAddr": {"node.example.test"}, "disableFlow": {"true"}, "trafficReset": {"monthly"},
		"settings":       {`{"clients":[],"decryption":"none"}`},
		"streamSettings": {`{"network":"tcp","security":"reality","sockopt":{"acceptProxyProtocol":true}}`},
		"sniffing":       {`{"enabled":true}`},
	}
	request := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/add", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, next, err := xrayWorkerRequest(xrayWorkerState{ApplicationID: "application", ImageReference: xrayWorkerImageReference, Address: "100.64.0.10", PanelPort: 2053, APIToken: "secret", Revision: 1, AppliedRevision: 1, NextInboundID: 1, XraySetting: defaultXrayWorkerSettings()}, request)
	if err != nil || next == nil || len(next.Inbounds) != 1 {
		t.Fatalf("controller form rejected: %v", err)
	}
	var inbound map[string]any
	_ = json.Unmarshal(next.Inbounds[0], &inbound)
	if inbound["tag"] != "remote-node" || inbound["settings"].(map[string]any)["clients"] == nil || inbound["shareAddr"] != "node.example.test" || inbound["disableFlow"] != true || inbound["trafficReset"] != "monthly" {
		t.Fatalf("form was not normalized: %#v", inbound)
	}
}

func TestXrayWorkerOwnsSubscriptionHostGroups(t *testing.T) {
	state := testXrayWorkerState()
	group := threeXUIHostGroup{
		GroupID: "vastora-public-1", InboundIDs: []int{1}, Hosts: []string{"node.example.test"},
		Remark: "{{INBOUND}}", ServerDescription: "Managed by Vastora", Tags: []string{"vastora"},
		Port: 443, Security: "same", SNI: "www.example.test", Fingerprint: "chrome", MihomoIPVersion: "dual",
	}
	payload, _ := json.Marshal(group)
	request := httptest.NewRequest(http.MethodPost, "/panel/api/hosts/add", bytes.NewReader(payload))
	_, added, err := xrayWorkerRequest(state, request)
	if err != nil || added == nil || len(added.HostGroups) != 1 {
		t.Fatalf("subscription host group add failed: state=%#v err=%v", added, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/panel/api/hosts/byInbound/1", nil)
	object, mutation, err := xrayWorkerRequest(*added, request)
	groups, ok := object.([]threeXUIHostGroup)
	if err != nil || mutation != nil || !ok || len(groups) != 1 || groups[0].GroupID != group.GroupID {
		t.Fatalf("subscription host group inventory failed: object=%#v mutation=%#v err=%v", object, mutation, err)
	}
	request = httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/del/1", nil)
	_, deleted, err := xrayWorkerRequest(*added, request)
	if err != nil || deleted == nil || len(deleted.HostGroups) != 0 {
		t.Fatalf("inbound delete retained subscription host group: state=%#v err=%v", deleted, err)
	}
}

func TestXrayWorkerStatsSurviveRuntimeRestart(t *testing.T) {
	state := testXrayWorkerState()
	first := []byte(`{"stat":[{"name":"inbound>>>managed>>>traffic>>>uplink","value":100},{"name":"user>>>phone>>>traffic>>>downlink","value":"50"}]}`)
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone"}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	state, err := applyXrayWorkerStats(state, first)
	if err != nil {
		t.Fatal(err)
	}
	state, err = applyXrayWorkerStats(state, []byte(`{"stat":[{"name":"inbound>>>managed>>>traffic>>>uplink","value":20},{"name":"user>>>phone>>>traffic>>>downlink","value":10}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	if up, _ := jsonInteger64(inbound["up"]); up != 120 {
		t.Fatalf("cumulative uplink = %d", up)
	}
	stats := inbound["clientStats"].([]any)[0].(map[string]any)
	if down, _ := jsonInteger64(stats["down"]); down != 60 {
		t.Fatalf("cumulative client downlink = %d", down)
	}
}

func TestXrayWorkerStatsPreserveNamesContainingTheXrayDelimiter(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "team>>>phone", "id": "00000000-0000-4000-8000-000000000001"}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	observed, err := applyXrayWorkerStats(state, []byte(`{"stat":[{"name":"user>>>team>>>phone>>>traffic>>>uplink","value":25}]}`))
	if err != nil || observed.AccountStats["team>>>phone"].Up != 25 {
		t.Fatalf("delimiter-bearing account stats=%#v err=%v", observed.AccountStats, err)
	}
}

func TestXrayWorkerAccountTrafficIsNotDuplicatedAcrossProtocols(t *testing.T) {
	state := testXrayWorkerState()
	var reality map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &reality)
	client := map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001"}
	reality["settings"] = map[string]any{"clients": []any{client}}
	state.Inbounds[0], _ = json.Marshal(reality)
	state.Inbounds = append(state.Inbounds, json.RawMessage(`{"id":2,"enable":true,"tag":"managed-hy2","protocol":"hysteria","port":443,"settings":{"clients":[{"email":"phone","auth":"secret"}]},"streamSettings":{"network":"hysteria","security":"tls"}}`))
	state.NextInboundID = 3
	observed, err := applyXrayWorkerStats(state, []byte(`{"stat":[{"name":"user>>>phone>>>traffic>>>downlink","value":"50"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if traffic := observed.AccountStats["phone"]; traffic.Down != 50 {
		t.Fatalf("cross-protocol account traffic = %#v", traffic.Down)
	}
}

func TestRenderXrayWorkerConfigOwnsPrivateRealitySocket(t *testing.T) {
	encoded, err := renderXrayWorkerConfig(testXrayWorkerState())
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
		Policy    map[string]any   `json:"policy"`
		Stats     map[string]any   `json:"stats"`
		Routing   struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	if json.Unmarshal(encoded, &config) != nil || len(config.Inbounds) != 2 {
		t.Fatal("invalid rendered config")
	}
	if config.Inbounds[1]["listen"] != "0.0.0.0" || config.Inbounds[1]["id"] != nil || config.Inbounds[1]["total"] != nil {
		t.Fatalf("panel metadata leaked into Xray config: %#v", config.Inbounds[1])
	}
	if slices.ContainsFunc(config.Outbounds, func(value map[string]any) bool { return value["tag"] == "api" }) || len(config.Routing.Rules) == 0 || config.Routing.Rules[0]["outboundTag"] != "api" {
		t.Fatalf("Xray API route conflicts with the built-in API outbound: %#v %#v", config.Outbounds, config.Routing.Rules)
	}
	levels, _ := config.Policy["levels"].(map[string]any)
	level, _ := levels["0"].(map[string]any)
	if level["statsUserUplink"] != true || config.Stats == nil {
		t.Fatalf("Xray traffic statistics were not enforced: %#v %#v", config.Policy, config.Stats)
	}
}

func TestXrayWorkerTrafficOnlyStateDoesNotReloadConfig(t *testing.T) {
	previous := testXrayWorkerState()
	next := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(previous.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": 1}}}
	previous.Inbounds[0], _ = json.Marshal(inbound)
	_ = json.Unmarshal(next.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": 1}}}
	inbound["up"], inbound["down"] = 12, 34
	inbound["clientStats"] = []any{map[string]any{"email": "phone", "up": 1, "down": 2}}
	next.Inbounds[0], _ = json.Marshal(inbound)
	changed, err := xrayWorkerConfigChanged(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("traffic-only observation changed the effective Xray configuration")
	}
}

func TestXrayWorkerControllerTrafficOnlyReloadsAtEnforcementBoundary(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": int64(1000), "expiryTime": int64(0)}}}
	state.Inbounds[0], _ = json.Marshal(inbound)

	request := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/pushClientTraffics", bytes.NewBufferString(`{"masterGuid":"controller","traffics":[{"email":"phone","up":200,"down":300}]}`))
	_, observed, err := xrayWorkerRequest(state, request)
	if err != nil || observed == nil {
		t.Fatal("controller traffic snapshot was rejected", err)
	}
	withinQuota := reconcileXrayWorkerAccounts(*observed, 1)
	if changed, err := xrayWorkerConfigChanged(state, withinQuota); err != nil || changed {
		t.Fatalf("ordinary traffic observation reloaded Xray: changed=%t err=%v", changed, err)
	}

	request = httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/pushClientTraffics", bytes.NewBufferString(`{"masterGuid":"controller","traffics":[{"email":"phone","up":600,"down":500}]}`))
	_, observed, err = xrayWorkerRequest(withinQuota, request)
	if err != nil || observed == nil {
		t.Fatal("depleted controller traffic snapshot was rejected", err)
	}
	depleted := reconcileXrayWorkerAccounts(*observed, 1)
	if !depleted.BlockedAccounts["phone"] {
		t.Fatal("shared controller quota did not block the account")
	}
	if changed, err := xrayWorkerConfigChanged(withinQuota, depleted); err != nil || !changed {
		t.Fatalf("quota boundary did not revise Xray: changed=%t err=%v", changed, err)
	}
}

func TestXrayWorkerControllerTrafficSnapshotCannotReduceWatermark(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{
		"clients": []any{map[string]any{
			"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": int64(1000),
		}},
	}
	state.Inbounds[0], _ = json.Marshal(inbound)
	state.ControllerID = "controller"
	state.ControllerStats = map[string]xrayWorkerTraffic{"phone": {Up: 300, Down: 400}}

	request := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/pushClientTraffics", bytes.NewBufferString(`{"masterGuid":"controller","traffics":[{"email":"phone","up":100,"down":200}]}`))
	_, observed, err := xrayWorkerRequest(state, request)
	if err != nil || observed == nil {
		t.Fatal("controller traffic snapshot was rejected", err)
	}
	if observed.ControllerStats["phone"] != (xrayWorkerTraffic{Up: 300, Down: 400}) {
		t.Fatalf("stale controller snapshot reduced the watermark: %#v", observed.ControllerStats["phone"])
	}
}

func TestXrayWorkerBlocksAtExactQuotaAndMovesAccountingWithIdentity(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{
		"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": int64(100), "expiryTime": int64(0),
	}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	state.AccountStats = map[string]xrayWorkerTraffic{"phone": {Up: 40, Down: 10}}
	state.ControllerID = "controller"
	state.ControllerStats = map[string]xrayWorkerTraffic{"phone": {Up: 40, Down: 60}}
	state = reconcileXrayWorkerAccounts(state, 1)
	if !state.BlockedAccounts["phone"] {
		t.Fatal("account at its exact shared quota remained active")
	}

	request := httptest.NewRequest(http.MethodPost, "/panel/api/clients/update/phone?inboundIds=1", bytes.NewBufferString(`{"email":"tablet","id":"00000000-0000-4000-8000-000000000001","enable":true,"totalGB":100,"expiryTime":0}`))
	_, renamed, err := xrayWorkerRequest(state, request)
	if err != nil || renamed == nil {
		t.Fatal("client rename failed", err)
	}
	if renamed.AccountStats["tablet"] != (xrayWorkerTraffic{Up: 40, Down: 10}) || renamed.ControllerStats["tablet"] != (xrayWorkerTraffic{Up: 40, Down: 60}) || !renamed.BlockedAccounts["tablet"] {
		t.Fatalf("accounting did not follow the renamed identity: %#v", renamed)
	}
	if _, exists := renamed.AccountStats["phone"]; exists {
		t.Fatal("renamed local accounting retained the old identity")
	}
	if _, exists := renamed.ControllerStats["phone"]; exists {
		t.Fatal("renamed controller accounting retained the old identity")
	}

	request = httptest.NewRequest(http.MethodPost, "/panel/api/clients/del/tablet", bytes.NewBufferString(`{}`))
	_, deleted, err := xrayWorkerRequest(*renamed, request)
	if err != nil || deleted == nil {
		t.Fatal("client delete failed", err)
	}
	if _, exists := deleted.AccountStats["tablet"]; exists {
		t.Fatal("deleted client retained local accounting")
	}
	if _, exists := deleted.ControllerStats["tablet"]; exists {
		t.Fatal("deleted client retained controller accounting")
	}
	if deleted.BlockedAccounts["tablet"] {
		t.Fatal("deleted client retained its enforcement state")
	}
}

func TestXrayWorkerExpiryAndTrafficResetAreExplicitState(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "totalGB": int64(1000), "expiryTime": int64(100)}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	state.AccountStats = map[string]xrayWorkerTraffic{"phone": {Up: 10, Down: 20}}
	state.ControllerID = "controller"
	state.ControllerStats = map[string]xrayWorkerTraffic{"phone": {Up: 30, Down: 40}}
	state = reconcileXrayWorkerAccounts(state, 100)
	if !state.BlockedAccounts["phone"] {
		t.Fatal("expired account remained active")
	}

	request := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/resetAllTraffics", nil)
	_, reset, err := xrayWorkerRequest(state, request)
	if err != nil || reset == nil || reset.AccountStats["phone"] != (xrayWorkerTraffic{}) || reset.ControllerStats["phone"] != (xrayWorkerTraffic{}) {
		t.Fatalf("traffic reset did not clear both accounting inputs: state=%#v err=%v", reset, err)
	}
}

func TestXrayWorkerLocalReconcilerAppliesExpiryOnce(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.now = func() time.Time { return time.UnixMilli(100) }
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true, "expiryTime": int64(100)}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	applies := 0
	apply := func(_ context.Context, _, _ xrayWorkerState) error {
		applies++
		return nil
	}
	observe := func(_ context.Context, current xrayWorkerState) (xrayWorkerState, error) { return current, nil }
	if err := store.reconcileXrayWorkerRuntime(context.Background(), apply, observe); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileXrayWorkerRuntime(context.Background(), apply, observe); err != nil {
		t.Fatal(err)
	}
	applied, err := store.loadXrayWorkerState(context.Background())
	if err != nil || applies != 1 || applied.Revision != 2 || applied.AppliedRevision != 2 || !applied.BlockedAccounts["phone"] {
		t.Fatalf("expiry reconciliation = state %#v applies %d err %v", applied, applies, err)
	}
}

func TestXrayWorkerUsesHardenedBridgeRuntime(t *testing.T) {
	options := xrayWorkerContainerOptions(DeploymentTask{AppKey: meridianKey, ApplicationID: "application", ID: "deployment"}, "xray:test", "/var/lib/vastora/xray-worker/config.json", false, false)
	host := options.HostConfig
	if host == nil || host.NetworkMode != container.NetworkMode(dockerruntime.NetworkName) || !host.ReadonlyRootfs || len(host.PortBindings) != 0 || options.NetworkingConfig == nil {
		t.Fatalf("unexpected Xray network/runtime options: %#v", host)
	}
	if options.Config == nil {
		t.Fatal("Xray worker container configuration is missing")
	}
	_, tcpExposed := options.Config.ExposedPorts[dockernetwork.MustParsePort("443/tcp")]
	if options.Config.User != strconv.Itoa(xrayWorkerRuntimeUID()) || !tcpExposed {
		t.Fatalf("Xray worker does not use its dedicated runtime identity: %#v", options.Config)
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || len(host.CapAdd) != 0 || len(host.SecurityOpt) != 1 || host.SecurityOpt[0] != "no-new-privileges:true" || host.Sysctls["net.ipv4.ip_unprivileged_port_start"] != "0" {
		t.Fatalf("unexpected Xray capability boundary: %#v", host)
	}
	if host.PidsLimit == nil || *host.PidsLimit != 512 || host.LogConfig.Type != "json-file" || host.LogConfig.Config["max-size"] != "10m" || host.LogConfig.Config["max-file"] != "3" {
		t.Fatalf("unexpected Xray resource/log boundary: %#v", host)
	}
	hy2 := xrayWorkerContainerOptions(DeploymentTask{AppKey: meridianKey, ApplicationID: "application", ID: "deployment"}, "xray:test", "/var/lib/vastora/xray-worker/config.json", true, false)
	bindings := hy2.HostConfig.PortBindings[hy2DockerPort]
	_, udpExposed := hy2.Config.ExposedPorts[hy2DockerPort]
	if len(bindings) != 1 || bindings[0].HostIP != netip.IPv4Unspecified() || bindings[0].HostPort != "443" || !udpExposed {
		t.Fatalf("HY2 UDP publication = %#v", bindings)
	}
}

func TestObserveMeridianStateAcceptsCurrentHysteriaStreamMethod(t *testing.T) {
	config := []byte(`{
		"inbounds":[
			{"listen":"127.0.0.1","port":10085,"protocol":"dokodemo-door","tag":"api","streamSettings":{}},
			{"listen":"0.0.0.0","port":443,"protocol":"vless","tag":"meridian-entry","streamSettings":{"method":"raw","security":"reality"}},
			{"listen":"0.0.0.0","port":443,"protocol":"hysteria","tag":"meridian-hy2","streamSettings":{"method":"hysteria","security":"tls"}}
		]
	}`)
	digest := sha256.Sum256(config)
	observed, err := observeMeridianState(meridian.DesiredArtifact{Revision: 1, Config: config, ConfigSHA256: hex.EncodeToString(digest[:])})
	if err != nil || len(observed) != 1 || observed[0].InboundTag != "meridian-entry" || observed[0].Port != 443 {
		t.Fatalf("current Hysteria stream method was not observed: %#v err=%v", observed, err)
	}
}

func TestXrayWorkerRecreatedRuntimeStateRequiresManagedIdentity(t *testing.T) {
	state := testXrayWorkerState()
	state.ImageReference = "ghcr.io/xtls/xray-core:26.9.9@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inspected := client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "replacement",
		Config: &container.Config{
			Image: xrayWorkerImageReference,
			User:  strconv.Itoa(xrayWorkerRuntimeUID()),
			Labels: map[string]string{
				xrayWorkerRuntimeLabel:       "xray",
				applicationInstallationLabel: state.ApplicationID,
			},
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(dockerruntime.NetworkName)},
		State:      &container.State{Running: true},
	}}
	updated, changed, err := xrayWorkerRecreatedRuntimeState(state, inspected)
	if err != nil || !changed || updated.ImageReference != xrayWorkerImageReference {
		t.Fatalf("managed replacement was not adopted: changed=%v state=%#v err=%v", changed, updated, err)
	}
	for name, mutate := range map[string]func(*client.ContainerInspectResult){
		"foreign application": func(value *client.ContainerInspectResult) {
			value.Container.Config.Labels[applicationInstallationLabel] = "other"
		},
		"unreviewed image": func(value *client.ContainerInspectResult) {
			value.Container.Config.Image = "ghcr.io/xtls/xray-core:26.8.0@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"stopped runtime": func(value *client.ContainerInspectResult) { value.Container.State.Running = false },
		"wrong network":   func(value *client.ContainerInspectResult) { value.Container.HostConfig.NetworkMode = "host" },
		"wrong user":      func(value *client.ContainerInspectResult) { value.Container.Config.User = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := inspected
			candidate.Container.Config = &container.Config{Image: inspected.Container.Config.Image, User: inspected.Container.Config.User, Labels: maps.Clone(inspected.Container.Config.Labels)}
			candidate.Container.HostConfig = &container.HostConfig{NetworkMode: inspected.Container.HostConfig.NetworkMode}
			candidate.Container.State = &container.State{Running: inspected.Container.State.Running}
			mutate(&candidate)
			if _, _, err := xrayWorkerRecreatedRuntimeState(state, candidate); err == nil {
				t.Fatal("unsafe recreated runtime was accepted")
			}
		})
	}
}

func TestXrayWorkerOnlyCreatesEmptyAuthorityOnFirstInstall(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := DeploymentTask{ApplicationID: "application", Operation: "upgrade", Secrets: json.RawMessage(`{"api_token":"secret"}`)}
	if _, _, err := prepareXrayWorkerState(context.Background(), store, task, "100.64.0.10", 2053, false); err == nil {
		t.Fatal("state-less worker upgrade created an empty authority")
	}
	task.Operation = "install"
	state, token, err := prepareXrayWorkerState(context.Background(), store, task, "100.64.0.10", 2053, false)
	if err != nil || token != "secret" || state.Revision != 1 || state.AppliedRevision != 0 || len(state.Inbounds) != 0 {
		t.Fatalf("fresh worker state=%#v token=%q err=%v", state, token, err)
	}
}

func TestPrepareXrayWorkerBlocksExpiredImportedAccountBeforeRuntimeStarts(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{"email": "expired", "id": "00000000-0000-4000-8000-000000000001", "expiryTime": int64(1)}}}
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	prepared, _, err := prepareXrayWorkerState(context.Background(), store, DeploymentTask{ApplicationID: state.ApplicationID, Operation: "upgrade"}, state.Address, state.PanelPort, false)
	if err != nil || prepared.Revision != state.Revision+1 || prepared.AppliedRevision != state.AppliedRevision || !prepared.BlockedAccounts["expired"] {
		t.Fatalf("expired account was not fenced before startup: state=%#v err=%v", prepared, err)
	}
}

func TestXrayWorkerRestrictedInboundAPI(t *testing.T) {
	state := testXrayWorkerState()
	request := httptest.NewRequest(http.MethodPost, "/panel/api/inbounds/add", bytes.NewBufferString(`{"enable":false,"tag":"second-hy2","protocol":"hysteria","port":443,"settings":{"clients":[]},"streamSettings":{"network":"hysteria","security":"tls"}}`))
	object, next, err := xrayWorkerRequest(state, request)
	if err != nil || next == nil || len(next.Inbounds) != 2 || next.NextInboundID != 3 {
		t.Fatalf("add failed: %v %#v", err, next)
	}
	var added map[string]any
	encoded, _ := json.Marshal(object)
	_ = json.Unmarshal(encoded, &added)
	if id, _ := jsonInteger(added["id"]); id != 2 {
		t.Fatalf("unexpected id %#v", added["id"])
	}

	unsupported := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	if _, _, err := xrayWorkerRequest(state, unsupported); err == nil {
		t.Fatal("panel UI endpoint was accepted")
	}
}

func TestXrayWorkerRoutingMutationIsNodeLocal(t *testing.T) {
	state := testXrayWorkerState()
	form := url.Values{"xraySetting": {string(defaultXrayWorkerSettings())}}

	remote := httptest.NewRequest(http.MethodPost, "/panel/api/xray/update", strings.NewReader(form.Encode()))
	remote.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	remote.RemoteAddr = "100.64.0.20:32000"
	if _, _, err := xrayWorkerRequest(state, remote); err == nil {
		t.Fatal("remote controller overwrote Vastora-owned routing settings")
	}

	local := httptest.NewRequest(http.MethodPost, "/panel/api/xray/update", strings.NewReader(form.Encode()))
	local.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	local.RemoteAddr = net.JoinHostPort(state.Address, "32000")
	_, next, err := xrayWorkerRequest(state, local)
	if err != nil || next == nil {
		t.Fatalf("node-local landing reconciliation was rejected: mutation=%#v error=%v", next, err)
	}
}

func TestXrayWorkerOnlyAcceptsNodeSyncClientMutations(t *testing.T) {
	state := testXrayWorkerState()
	var first map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &first)
	first["settings"] = map[string]any{"clients": []any{map[string]any{"email": "alice", "id": "00000000-0000-4000-8000-000000000001", "subId": "subscription", "enable": true, "totalGB": int64(100)}}}
	first["clientStats"] = []any{map[string]any{"email": "alice", "up": int64(10), "down": int64(20)}}
	state.Inbounds[0], _ = json.Marshal(first)
	state.Inbounds = append(state.Inbounds, json.RawMessage(`{"id":2,"enable":false,"tag":"hy2","protocol":"hysteria","port":443,"settings":{"clients":[]},"streamSettings":{"network":"hysteria","security":"tls"}}`))
	state.NextInboundID = 3

	if xrayWorkerEndpointAllowed(http.MethodGet, "/panel/api/clients/get/alice") || xrayWorkerEndpointAllowed(http.MethodGet, "/panel/api/clients/list/paged") || xrayWorkerEndpointAllowed(http.MethodPost, "/panel/api/clients/bulkAttach") {
		t.Fatal("worker exposed controller-only client inventory endpoints")
	}

	updateRequest := httptest.NewRequest(http.MethodPost, "/panel/api/clients/update/alice", bytes.NewBufferString(`{"email":"alice","id":"00000000-0000-4000-8000-000000000001","enable":true}`))
	_, next, err := xrayWorkerRequest(state, updateRequest)
	if err != nil || next == nil {
		t.Fatalf("update failed: %v", err)
	}
	if !xrayWorkerHasClient(*next, "alice") {
		t.Fatal("updated client disappeared")
	}

	rotatedRequest := httptest.NewRequest(http.MethodPost, "/panel/api/clients/update/alice", bytes.NewBufferString(`{"email":"alice","id":"00000000-0000-4000-8000-000000000002","enable":true}`))
	if _, _, err := xrayWorkerRequest(*next, rotatedRequest); err == nil {
		t.Fatal("controller adapter rotated a Vastora-owned client credential")
	}

	detachRequest := httptest.NewRequest(http.MethodPost, "/panel/api/clients/alice/detach", bytes.NewBufferString(`{"inboundIds":[1]}`))
	_, detached, err := xrayWorkerRequest(*next, detachRequest)
	if err != nil || detached == nil || xrayWorkerHasClient(*detached, "alice") {
		t.Fatalf("detach failed: mutation=%#v error=%v", detached, err)
	}
}

func TestXrayWorkerRejectsPublicRealityWithoutProxyProtocol(t *testing.T) {
	state := testXrayWorkerState()
	var inbound map[string]any
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	stream := inbound["streamSettings"].(map[string]any)
	delete(stream, "sockopt")
	inbound["streamSettings"] = stream
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := state.validate(); err == nil {
		t.Fatal("REALITY worker accepted an inbound that bypasses HAProxy Proxy Protocol")
	}
	state = testXrayWorkerState()
	_ = json.Unmarshal(state.Inbounds[0], &inbound)
	inbound["tag"] = "api"
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := state.validate(); err == nil {
		t.Fatal("worker accepted a data inbound that collides with its loopback API")
	}
}

func TestXrayWorkerRejectsHysteriaFinalmask(t *testing.T) {
	state := testXrayWorkerState()
	state.Inbounds = []json.RawMessage{json.RawMessage(`{"id":1,"enable":true,"tag":"managed-hy2","protocol":"hysteria","port":443,"settings":{"clients":[]},"streamSettings":{"network":"hysteria","security":"tls","finalmask":{"udp":[{"type":"salamander","settings":{"password":"secret"}}]}}}`)}
	if err := state.validate(); err == nil || !strings.Contains(err.Error(), "finalmask") {
		t.Fatalf("Hysteria finalmask was accepted: %v", err)
	}
}

func TestXrayWorkerStatusRejectsUnappliedRevision(t *testing.T) {
	state := testXrayWorkerState()
	state.Revision++
	request := httptest.NewRequest(http.MethodGet, "/panel/api/server/status", nil)
	if _, _, err := xrayWorkerRequest(state, request); err == nil {
		t.Fatal("worker reported ready before its desired revision was applied")
	}
}

func TestXrayWorkerStatusMatchesControllerHeartbeatShape(t *testing.T) {
	state := testXrayWorkerState()
	request := httptest.NewRequest(http.MethodGet, "/panel/api/server/status", nil)
	object, mutation, err := xrayWorkerRequest(state, request)
	status, ok := object.(map[string]any)
	xray, xrayOK := status["xray"].(map[string]any)
	if err != nil || mutation != nil || !ok || !xrayOK || xray["state"] != "running" || xray["version"] != xrayWorkerVersion || status["panelGuid"] != state.ApplicationID {
		t.Fatalf("unexpected heartbeat status: object=%#v mutation=%#v err=%v", object, mutation, err)
	}
	if _, legacy := status["xrayState"]; legacy {
		t.Fatal("worker retained the obsolete flat heartbeat shape")
	}
}

func TestXrayWorkerRestartEndpointForcesOneAppliedRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	applies := 0
	handler := store.xrayWorkerHandler(func(_ context.Context, previous, next xrayWorkerState) error {
		applies++
		if previous.Revision != 1 || next.Revision != 2 || next.AppliedRevision != 1 {
			t.Fatalf("unexpected restart revisions: previous=%#v next=%#v", previous, next)
		}
		return nil
	}, nil)
	request := httptest.NewRequest(http.MethodPost, "/panel/api/server/restartXrayService", nil)
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	applied, loadErr := store.loadXrayWorkerState(context.Background())
	if response.Code != http.StatusOK || applies != 1 || loadErr != nil || applied.Revision != 2 || applied.AppliedRevision != 2 {
		t.Fatalf("restart response=%d applies=%d state=%#v err=%v", response.Code, applies, applied, loadErr)
	}
}

func TestXrayWorkerAppliedReceiptBindsRevisionAndConfig(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	if _, err := store.writeXrayWorkerConfig(state); err != nil {
		t.Fatal(err)
	}
	if err := store.recordXrayWorkerApplied(state); err != nil || !store.xrayWorkerAppliedReceiptMatches(state) {
		t.Fatalf("applied receipt was not confirmed: %v", err)
	}
	state.Revision++
	if store.xrayWorkerAppliedReceiptMatches(state) {
		t.Fatal("applied receipt matched another desired revision")
	}
}

func TestXrayWorkerAppliedReceiptCanOnlyRecoverMatchingActiveConfig(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	configPath, err := store.writeXrayWorkerConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileXrayWorkerAppliedReceipt(state); err != nil || !store.xrayWorkerAppliedReceiptMatches(state) {
		t.Fatalf("matching active configuration was not recovered: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileXrayWorkerAppliedReceipt(state); err == nil {
		t.Fatal("foreign active configuration was accepted")
	}
}

func TestMeridianArtifactHY2DetectionControlsUDP443Publication(t *testing.T) {
	artifact := func(config string) meridian.DesiredArtifact {
		encoded := []byte(config)
		digest := sha256.Sum256(encoded)
		return meridian.DesiredArtifact{Revision: 1, Config: encoded, ConfigSHA256: hex.EncodeToString(digest[:])}
	}
	hasHY2, err := meridianArtifactHY2Enabled(artifact(`{"inbounds":[{"protocol":"hysteria","port":443}]}`))
	if err != nil || !hasHY2 {
		t.Fatalf("HY2 artifact = %v, err=%v", hasHY2, err)
	}
	hasHY2, err = meridianArtifactHY2Enabled(artifact(`{"inbounds":[{"protocol":"vless","port":443}]}`))
	if err != nil || hasHY2 {
		t.Fatalf("VLESS-only artifact = %v, err=%v", hasHY2, err)
	}
	if _, err := meridianArtifactHY2Enabled(artifact(`{"inbounds":[{"protocol":"hysteria","port":10443}]}`)); err == nil {
		t.Fatal("Meridian accepted Hysteria away from UDP 443")
	}
}

func TestXrayWorkerClientDisableAppliesRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	var inbound map[string]any
	if err := json.Unmarshal(state.Inbounds[0], &inbound); err != nil {
		t.Fatal(err)
	}
	inbound["settings"] = map[string]any{"clients": []any{map[string]any{
		"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": true,
	}}}
	state.Inbounds[0], err = json.Marshal(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	applies := 0
	handler := store.xrayWorkerHandler(func(_ context.Context, previous, next xrayWorkerState) error {
		applies++
		changed, err := xrayWorkerConfigChanged(previous, next)
		if err != nil || !changed || previous.Revision != 1 || next.Revision != 2 {
			t.Fatalf("disabled client did not revise runtime: changed=%t previous=%d next=%d err=%v", changed, previous.Revision, next.Revision, err)
		}
		return nil
	}, nil)
	body, err := json.Marshal(map[string]any{
		"email": "phone", "id": "00000000-0000-4000-8000-000000000001", "enable": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/panel/api/clients/update/phone?inboundIds=1", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	applied, loadErr := store.loadXrayWorkerState(context.Background())
	if response.Code != http.StatusOK || applies != 1 || loadErr != nil || applied.Revision != 2 || applied.AppliedRevision != 2 {
		t.Fatalf("client update response=%d applies=%d revisions=%d/%d err=%v", response.Code, applies, applied.Revision, applied.AppliedRevision, loadErr)
	}
}

func TestXrayWorkerReconcilerPersistsObservedCounters(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := testXrayWorkerState()
	state.RuntimeStats = map[string]int64{"counter": 1}
	state.AccountStats = map[string]xrayWorkerTraffic{"phone": {Up: 1}}
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	applies := 0
	apply := func(_ context.Context, _, _ xrayWorkerState) error {
		applies++
		return nil
	}
	observe := func(_ context.Context, current xrayWorkerState) (xrayWorkerState, error) {
		current.RuntimeStats["counter"] = 2
		current.AccountStats["phone"] = xrayWorkerTraffic{Up: 2}
		return current, nil
	}
	if err := store.reconcileXrayWorkerRuntime(context.Background(), apply, observe); err != nil {
		t.Fatal(err)
	}
	observed, loadErr := store.loadXrayWorkerState(context.Background())
	if loadErr != nil || applies != 0 || observed.RuntimeStats["counter"] != 2 || observed.AccountStats["phone"].Up != 2 {
		t.Fatalf("observed counters were lost: applies=%d runtime=%d account=%d err=%v", applies, observed.RuntimeStats["counter"], observed.AccountStats["phone"].Up, loadErr)
	}
}
