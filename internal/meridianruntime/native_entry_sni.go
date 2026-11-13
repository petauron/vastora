package meridianruntime

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/glivela"
)

// AppliedNativeEntryEndpoint contains only public presentation data. Reading
// it does not adopt credentials, advance accounting or project a runtime.
type AppliedNativeEntryEndpoint struct {
	ServerNames   []string
	ListenAddress string
	ListenPort    int
	AdvertiseHost string
	PublicKey     string
}

// ReadAppliedNativeEntryEndpoint permits routed entries, unlike first-time
// native registration. Identity stays pinned to the registered checkpoint;
// metadata is accepted only from the exact fresh applied artifact.
func ReadAppliedNativeEntryEndpoint(path, applicationID, endpointID, certificateID string, now time.Time) (AppliedNativeEntryEndpoint, error) {
	invalid := errors.New("meridianruntime: applied native entry metadata unavailable")
	if !glivela.ValidIdentifier(applicationID) || !glivela.ValidIdentifier(endpointID) || !glivela.ValidIdentity(certificateID) {
		return AppliedNativeEntryEndpoint{}, invalid
	}
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return AppliedNativeEntryEndpoint{}, err
	}
	defer clear(raw)
	var nodes []struct {
		ID                   string    `json:"id"`
		CertificateID        string    `json:"certificateId"`
		PublicationSuspended bool      `json:"publicationSuspended"`
		ReportedAt           time.Time `json:"reportedAt"`
		Desired              struct {
			glivela.DesiredArtifact
			Entry *struct {
				Revision         uint64                       `json:"revision"`
				RealityEndpoints []glivela.RealityEndpoint    `json:"realityEndpoints"`
				Credentials      []glivela.CredentialMaterial `json:"credentials"`
			} `json:"entry"`
		} `json:"desired"`
		Receipt *glivela.AppliedReceipt `json:"receipt"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return AppliedNativeEntryEndpoint{}, invalid
	}
	var result AppliedNativeEntryEndpoint
	found := false
	for _, node := range nodes {
		if node.ID != applicationID && node.CertificateID != certificateID {
			continue
		}
		d := node.Desired
		if found || node.ID != applicationID || node.CertificateID != certificateID || node.PublicationSuspended || d.Validate() != nil || d.Entry == nil || d.Entry.Revision != d.Revision || node.Receipt == nil || !node.Receipt.RuntimeReady || node.Receipt.Revision != d.Revision || node.Receipt.ConfigSHA256 != d.ConfigSHA256 || node.ReportedAt.IsZero() || node.ReportedAt.After(now) || now.Sub(node.ReportedAt) > 30*time.Second {
			return AppliedNativeEntryEndpoint{}, invalid
		}
		found = true
		matched := false
		for _, endpoint := range d.Entry.RealityEndpoints {
			if endpoint.ID != endpointID {
				continue
			}
			if matched || endpoint.EntryID != applicationID || endpoint.AdvertisePort != endpoint.ListenPort {
				return AppliedNativeEntryEndpoint{}, invalid
			}
			// The pinned library's renderer validates legacy private IPv4 only.
			// Reuse its subscription validation with an actual native credential;
			// the caller separately pins the registered public listener and key.
			valid := false
			for _, material := range d.Entry.Credentials {
				if material.Credential.Kind != glivela.NativeCredential || material.Credential.EntryID != applicationID || !material.Credential.Enabled {
					continue
				}
				if _, err := glivela.LinkForCredential(endpoint, material, "Native entry"); err == nil {
					valid = true
					break
				}
			}
			if !valid {
				return AppliedNativeEntryEndpoint{}, invalid
			}
			matched = true
			result = AppliedNativeEntryEndpoint{ServerNames: append([]string(nil), endpoint.ServerNames...), ListenAddress: endpoint.ListenAddress, ListenPort: endpoint.ListenPort, AdvertiseHost: endpoint.AdvertiseHost, PublicKey: endpoint.PublicKey}
		}
		if !matched {
			return AppliedNativeEntryEndpoint{}, invalid
		}
	}
	if !found {
		return AppliedNativeEntryEndpoint{}, invalid
	}
	return result, nil
}
