package center

import (
	"context"
	"database/sql"
	"errors"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/glivelaruntime"
)

func applyNativeEgressProjection(ctx context.Context, tx *sql.Tx, p *glivelaRuntimeProjection, endpoint glivela.RealityEndpoint, hy2 glivela.HysteriaEndpoint, vlessEnabled, hy2Enabled int, materials []glivela.CredentialMaterial) error {
	var policy glivela.EgressPolicy
	var needsVerification bool
	err := tx.QueryRowContext(ctx, `SELECT policy,revision<>verified_revision FROM node_egress_policies WHERE node_id=?`, p.agentID).Scan(&policy, &needsVerification)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p.task.NativeEgress = policy
	p.task.ImageReference = glivelaruntime.EgressImage
	if !needsVerification {
		return nil
	}
	var material *glivela.CredentialMaterial
	for i := range materials {
		if materials[i].Credential.Kind == glivela.NativeCredential && materials[i].Credential.Enabled {
			material = &materials[i]
			break
		}
	}
	if material == nil {
		return errors.New("center: an enabled native client is required to verify the egress policy")
	}
	// Use the original subscription entry. The private REALITY backend requires
	// a PROXY header from HAProxy and is not a direct client endpoint.
	if vlessEnabled == 1 {
		endpoint.PrivateKey = ""
		p.task.EgressClients = append(p.task.EgressClients, glivelaruntime.AcceptanceClient{Protocol: glivela.VLESSReality, Reality: &endpoint, Material: *material})
	}
	if hy2Enabled == 1 {
		hy2.PrivateKeyPEM = ""
		hy2.CertificatePEM = ""
		p.task.EgressClients = append(p.task.EgressClients, glivelaruntime.AcceptanceClient{Protocol: glivela.Hysteria2, Hysteria: &hy2, Material: *material})
	}
	return nil
}
