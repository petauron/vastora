package center

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivatePublicationVerificationUsesConfirmedGatewayAddress(t *testing.T) {
	var receivedHost string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedHost = request.Host
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	serverAddress := strings.TrimPrefix(server.URL, "http://")
	gatewayAddress, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		t.Fatal(err)
	}
	client, closeClient := publicationVerificationHTTPClient(gatewayAddress)
	defer closeClient()
	response, err := client.Get("http://private-service.example.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || receivedHost != "private-service.example.test:"+port {
		t.Fatalf("status=%d host=%q", response.StatusCode, receivedHost)
	}
}

func TestPrivatePublicationVerificationTargetFailsClosed(t *testing.T) {
	publication := PublicationView{Kind: publicationHeadscale, DNSRecord: &DNSRecordInstruction{Value: "100.64.0.10"}}
	address, privateEntry, err := privatePublicationVerificationAddress(publication)
	if err != nil || !privateEntry || address != "100.64.0.10" {
		t.Fatalf("address=%q private=%t err=%v", address, privateEntry, err)
	}
	publication.DNSRecord = nil
	if _, privateEntry, err = privatePublicationVerificationAddress(publication); err == nil || !privateEntry {
		t.Fatalf("missing private entry address was accepted: private=%t err=%v", privateEntry, err)
	}
	if address, privateEntry, err = privatePublicationVerificationAddress(PublicationView{Kind: publicationPublic}); err != nil || privateEntry || address != "" {
		t.Fatalf("public publication unexpectedly received a private target: address=%q private=%t err=%v", address, privateEntry, err)
	}
}
