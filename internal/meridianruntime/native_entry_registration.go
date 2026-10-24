package meridianruntime

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

// NativeEntryRegistration is a new entry observed from the same private,
// authenticated Glivela source as ledger receipts. It is not a migration or
// permission to adopt another application's existing credentials.
type NativeEntryRegistration struct {
	Plan          glivela.XrayPlan
	CertificateID string
	ConfigSHA256  string
	Receipt       PilotReceipt
}

func ReadNativeEntryRegistration(path, applicationID string, now time.Time) (NativeEntryRegistration, error) {
	invalid := errors.New("meridianruntime: fresh native entry evidence unavailable")
	if !glivela.ValidIdentifier(applicationID) {
		return NativeEntryRegistration{}, invalid
	}
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return NativeEntryRegistration{}, err
	}
	defer clear(raw)
	var nodes []struct {
		ID            string `json:"id"`
		CertificateID string `json:"certificateId"`
		Desired       struct {
			glivela.DesiredArtifact
			Entry   *glivela.XrayPlan `json:"entry"`
			Landing json.RawMessage   `json:"landing"`
			Network struct {
				Source json.RawMessage   `json:"source"`
				Peers  []json.RawMessage `json:"peers"`
			} `json:"entryNetwork"`
		} `json:"desired"`
		Quota json.RawMessage `json:"entryQuota"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return NativeEntryRegistration{}, invalid
	}
	var result NativeEntryRegistration
	found := false
	for _, node := range nodes {
		if node.ID != applicationID {
			continue
		}
		d := node.Desired
		if found || !glivela.ValidIdentity(node.CertificateID) || d.Validate() != nil || d.Entry == nil ||
			len(d.Landing) > 0 && string(d.Landing) != "null" || len(node.Quota) > 0 && string(node.Quota) != "null" ||
			len(d.Network.Peers) != 0 || len(d.Network.Source) > 0 && string(d.Network.Source) != "null" {
			return NativeEntryRegistration{}, invalid
		}
		plan := *d.Entry
		if plan.Revision != d.Revision || len(plan.RealityEndpoints) != 1 || len(plan.HysteriaEndpoints) != 0 || len(plan.Grants) != 0 || len(plan.Peers) != 0 || len(plan.Credentials) == 0 || len(plan.Credentials) > 1024 {
			return NativeEntryRegistration{}, invalid
		}
		endpoint := plan.RealityEndpoints[0]
		address, err := netip.ParseAddr(endpoint.ListenAddress)
		if err != nil || !address.Is6() || !landing.PublicIP(address) || address.String() != endpoint.ListenAddress || endpoint.EntryID != applicationID || endpoint.ListenPort < 1024 || endpoint.ListenPort > 65535 || endpoint.AdvertisePort != endpoint.ListenPort {
			return NativeEntryRegistration{}, invalid
		}
		// Key and subscription validation remain independent of the legacy
		// renderer's private IPv4 listener contract. Glivela owns native rendering.
		private, err := base64.RawURLEncoding.DecodeString(endpoint.PrivateKey)
		if err != nil {
			return NativeEntryRegistration{}, invalid
		}
		key, err := ecdh.X25519().NewPrivateKey(private)
		clear(private)
		if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != endpoint.PublicKey {
			return NativeEntryRegistration{}, invalid
		}
		credentials, accounts := map[string]bool{}, map[string]bool{}
		for _, material := range plan.Credentials {
			c := material.Credential
			if material.Validate() != nil || c.EntryID != applicationID || c.Kind != glivela.NativeCredential || !c.Enabled || material.HysteriaAuth != "" || credentials[c.ID] || accounts[c.AccountID] {
				return NativeEntryRegistration{}, invalid
			}
			if _, err := glivela.LinkForCredential(endpoint, material, "Native entry"); err != nil {
				return NativeEntryRegistration{}, invalid
			}
			credentials[c.ID], accounts[c.AccountID] = true, true
		}
		receipt, err := readPilotReceiptBytes(raw, applicationID, node.CertificateID, d.ConfigSHA256, d.Revision, now)
		if err != nil || receipt.PublicationSuspended || len(receipt.Counters) != len(credentials) {
			return NativeEntryRegistration{}, invalid
		}
		for _, counter := range receipt.Counters {
			if !credentials[counter.CredentialID] || counter.UpBytes < 0 || counter.DownBytes < 0 {
				return NativeEntryRegistration{}, invalid
			}
			delete(credentials, counter.CredentialID)
		}
		if len(credentials) != 0 {
			return NativeEntryRegistration{}, invalid
		}
		result = NativeEntryRegistration{Plan: plan, CertificateID: node.CertificateID, ConfigSHA256: d.ConfigSHA256, Receipt: receipt}
		found = true
	}
	if !found {
		return NativeEntryRegistration{}, invalid
	}
	return result, nil
}
