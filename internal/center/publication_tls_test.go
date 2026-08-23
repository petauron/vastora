package center

import (
	"context"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestExistingPrivatePublicationCanSwitchBetweenHTTPAndHTTPS(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "private-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.64", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.64", LANAddress: "10.0.0.64", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Private", Code: "private", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	configureCloudflareZoneForTest(t, store, "example.test")
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "10.0.0.64")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 || services[0].ApplicationID != applicationID {
		t.Fatalf("services = %#v, err=%v", services, err)
	}
	publication, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: node.ID, Hostname: "cpa.private.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var certificate managedCertificate
	store.issuePrivateCertificate = func(_ context.Context, dnsNames ...string) (managedCertificate, error) {
		certificate = testManagedCertificate(t, dnsNames...)
		return certificate, nil
	}
	publication, err = store.UpdatePublicationTLS(ctx, publication.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !publication.TLSEnabled || publication.Status != "pending" || publication.CertificateExpiresAt == nil {
		t.Fatalf("HTTPS was not queued: %#v", publication)
	}

	task := claimTask(t, store, node)
	if task.Kind != "gateway.routes.apply" || task.GatewayState == nil || len(task.GatewayState.Routes) != 1 || !task.GatewayState.Routes[0].TLSEnabled {
		t.Fatalf("HTTPS route was not sent to the gateway: %#v", task)
	}
	if len(task.GatewayCertificates) != 1 || task.GatewayCertificates[0].PrivateKeyPEM != certificate.PrivateKeyPEM {
		t.Fatalf("certificate was not delivered with the gateway task: %#v", task.GatewayCertificates)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
		t.Fatal(err)
	}
	publication, err = store.Publication(ctx, publication.ID)
	if err != nil || !strings.HasPrefix(publication.AccessURL, "https://") {
		t.Fatalf("HTTPS access URL = %q, err=%v", publication.AccessURL, err)
	}

	var certificateID string
	if err := store.db.QueryRowContext(ctx, `SELECT secret_id FROM site_certificates WHERE site_id = ?`, testSiteID(t, store)).Scan(&certificateID); err != nil {
		t.Fatal(err)
	}
	publication, err = store.UpdatePublicationTLS(ctx, publication.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if publication.TLSEnabled || publication.CertificateExpiresAt != nil || publication.Status != "pending" {
		t.Fatalf("HTTP fallback was not queued: %#v", publication)
	}
	var retained int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = ?`, certificateID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("Site certificate was not retained for reuse: count=%d err=%v", retained, err)
	}

	task = claimTask(t, store, node)
	if task.GatewayState == nil || len(task.GatewayState.Routes) != 1 || task.GatewayState.Routes[0].TLSEnabled || len(task.GatewayCertificates) != 0 {
		t.Fatalf("HTTP route state is invalid: %#v", task)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
		t.Fatal(err)
	}
	publication, err = store.Publication(ctx, publication.ID)
	if err != nil || !strings.HasPrefix(publication.AccessURL, "http://") {
		t.Fatalf("HTTP access URL = %q, err=%v", publication.AccessURL, err)
	}
}
