package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type meridianPublicationProof struct {
	Request       deployapi.MeridianProbeRequest
	Revision      int64
	AlternateExit string
}

// Snapshot the same saved identity used to render the subscription. A plain
// TLS response from REALITY's camouflage target is not authenticated evidence.
func (s *Store) meridianPublicationProof(ctx context.Context, tx *sql.Tx, serviceID string) (meridianPublicationProof, error) {
	var proof meridianPublicationProof
	var endpointID, applicationID, nodeID, status string
	var applied int64
	var healthy, enabled bool
	var names, shorts []byte
	var reality glivela.RealityEndpoint
	err := tx.QueryRowContext(ctx, `SELECT endpoint.id,endpoint.application_id,application.node_id,
		endpoint.advertise_host,endpoint.advertise_port,endpoint.server_names_json,endpoint.public_key,
		endpoint.short_ids_json,endpoint.fingerprint,endpoint.desired_revision,endpoint.applied_revision,
		endpoint.status,endpoint.runtime_healthy,endpoint.vless_enabled
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.service_id=? AND endpoint.status<>'retired'`, serviceID).
		Scan(&endpointID, &applicationID, &nodeID, &reality.AdvertiseHost, &reality.AdvertisePort,
			&names, &reality.PublicKey, &shorts, &reality.Fingerprint, &proof.Revision, &applied, &status, &healthy, &enabled)
	if err != nil {
		return proof, err
	}
	if status != "ready" || !healthy || !enabled || applied != proof.Revision || json.Unmarshal(names, &reality.ServerNames) != nil || json.Unmarshal(shorts, &reality.ShortIDs) != nil {
		return proof, errors.New("center: current Glivela runtime is not ready for client verification")
	}
	reality.ID, reality.EntryID = endpointID, applicationID
	materials, _, err := s.meridianRuntimeMaterials(ctx, tx, endpointID, applicationID)
	if err != nil {
		return proof, err
	}
	for _, material := range materials {
		if material.Credential.Enabled && material.Credential.Kind == glivela.NativeCredential {
			proof.Request.Client = meridianruntime.AcceptanceClient{Protocol: glivela.VLESSReality, Reality: &reality, Material: material}
			proof.Request.ExpectedExit, err = s.reinstallAcceptanceExit(ctx, tx, nodeID, false)
			if err == nil {
				proof.AlternateExit, err = s.publicationAlternateExit(ctx, tx, nodeID, proof.Request.ExpectedExit)
			}
			return proof, err
		}
	}
	return proof, errors.New("center: an enabled native credential is required for proxy verification")
}

// Only the unique opposite-family address in fresh authenticated inventory is
// eligible. Never guess among multiple exits or borrow another node's address.
func (s *Store) publicationAlternateExit(ctx context.Context, tx *sql.Tx, nodeID, primary string) (string, error) {
	var seen string
	if err := tx.QueryRowContext(ctx, `SELECT last_seen_at FROM agents WHERE id=?`, nodeID).Scan(&seen); err != nil {
		return "", err
	}
	stamp, err := time.Parse(time.RFC3339Nano, seen)
	if err != nil || stamp.After(s.now()) || s.now().Sub(stamp) > 2*time.Minute {
		return "", nil
	}
	targets, err := listIPQualityTargets(ctx, tx, nodeID)
	if err != nil {
		return "", err
	}
	alternate := ""
	for _, target := range targets {
		if target.Family == ipQualityFamily(primary) {
			continue
		}
		if alternate != "" {
			return "", nil
		}
		alternate = target.Address
	}
	return alternate, nil
}

func (s *Store) verifyMeridianPublication(ctx context.Context, publication PublicationView, revision int64) (PublicationView, error) {
	if s.meridianProber == nil {
		return s.recordPublicationVerification(ctx, publication.ID, revision, "authenticated proxy verifier is unavailable; ordinary TLS is not proof of proxy health")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationView{}, err
	}
	proof, err := s.meridianPublicationProof(ctx, tx, publication.ServiceID)
	_ = tx.Rollback()
	if err != nil {
		return s.recordPublicationVerification(ctx, publication.ID, revision, "current runtime, credential or exit evidence is not ready for proxy verification")
	}
	probeErr := s.meridianProber.VerifyMeridianEntry(ctx, proof.Request)
	if probeErr != nil && proof.AlternateExit != "" && ctx.Err() == nil {
		request := proof.Request
		request.ExpectedExit = proof.AlternateExit
		probeErr = s.meridianProber.VerifyMeridianEntry(ctx, request)
	}
	if probeErr != nil {
		return s.recordPublicationVerification(ctx, publication.ID, revision, "authenticated proxy HTTPS and expected exit verification did not pass")
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return PublicationView{}, errors.New("center: encode proxy verification identity")
	}
	digest := sha256.Sum256(encoded)
	// Fence the evidence inside the final readiness transaction: a credential,
	// exit or runtime change during network I/O cannot validate a new revision.
	return s.markPublicationReadyChecked(ctx, publication.ID, revision, func(tx *sql.Tx) error {
		current, err := s.meridianPublicationProof(ctx, tx, publication.ServiceID)
		if err != nil {
			return errors.New("center: proxy verification identity changed; retry verification")
		}
		encoded, err := json.Marshal(current)
		if err != nil || sha256.Sum256(encoded) != digest {
			return errors.New("center: proxy verification identity changed; retry verification")
		}
		return nil
	})
}
