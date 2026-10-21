package meridianruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

// VerifyPilotDesired binds the retained business plan to the independently
// authenticated controller's private snapshot. Glivela owns rendering; Center
// must not re-render a newer native config with its legacy runtime dependency.
// The mount is the same trusted source used for subsequent ledger receipts.
// No path or standalone Xray config is accepted from the activation request.
func VerifyPilotDesired(path, entryID, certificateID, digest string, plan glivela.XrayPlan, source *landing.PeerIdentity, peers []Peer, exits map[string]string) error {
	invalid := errors.New("meridianruntime: pilot controller plan or identity differs")
	if !glivela.ValidIdentifier(entryID) || !glivela.ValidIdentity(certificateID) || !glivela.ValidIdentity(digest) || plan.Revision == 0 {
		return invalid
	}
	raw, err := readPrivatePilotState(path)
	if err != nil {
		return invalid
	}
	defer clear(raw)
	var nodes []struct {
		ID            string `json:"id"`
		CertificateID string `json:"certificateId"`
		Desired       struct {
			glivela.DesiredArtifact
			Entry   json.RawMessage `json:"entry"`
			Landing json.RawMessage `json:"landing"`
			Network struct {
				Source *landing.PeerIdentity `json:"source"`
				Peers  []struct {
					Peer
					ExpectedExit string `json:"expectedExit"`
				} `json:"peers"`
			} `json:"entryNetwork"`
		} `json:"desired"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return invalid
	}
	expected, err := json.Marshal(plan)
	if err != nil {
		return invalid
	}
	defer clear(expected)
	var expectedValue any
	decoder := json.NewDecoder(bytes.NewReader(expected))
	decoder.UseNumber()
	if decoder.Decode(&expectedValue) != nil {
		return invalid
	}
	found := false
	for _, n := range nodes {
		if n.ID != entryID && n.CertificateID != certificateID {
			continue
		}
		d := n.Desired
		if found || n.ID != entryID || n.CertificateID != certificateID || d.Revision != plan.Revision ||
			d.ConfigSHA256 != digest || d.Validate() != nil ||
			(len(d.Landing) != 0 && !bytes.Equal(d.Landing, []byte("null"))) {
			return invalid
		}
		var actualValue any
		decoder = json.NewDecoder(bytes.NewReader(d.Entry))
		decoder.UseNumber()
		if decoder.Decode(&actualValue) != nil || !reflect.DeepEqual(expectedValue, actualValue) ||
			!reflect.DeepEqual(source, d.Network.Source) || len(peers) != len(d.Network.Peers) || len(exits) != len(peers) {
			return invalid
		}
		remaining := make(map[string]Peer, len(peers))
		for _, p := range peers {
			if p.Validate() != nil {
				return invalid
			}
			remaining[p.EgressID] = p
		}
		if len(remaining) != len(peers) {
			return invalid
		}
		for _, p := range d.Network.Peers {
			want, ok := remaining[p.EgressID]
			if !ok || !reflect.DeepEqual(want, p.Peer) || exits[p.EgressID] == "" || exits[p.EgressID] != p.ExpectedExit {
				return invalid
			}
			delete(remaining, p.EgressID)
		}
		found = true
	}
	if !found {
		return invalid
	}
	return nil
}
