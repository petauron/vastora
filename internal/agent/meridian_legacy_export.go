package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/nodeprotocol"
)

func exportLegacyMeridianState(ctx context.Context, store *Store, command meridianruntime.LegacyExportCommand) (meridianruntime.LegacyExportResult, error) {
	if command.Validate() != nil {
		return meridianruntime.LegacyExportResult{}, errors.New("agent: invalid Meridian legacy export command")
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil || installation.ApplicationID != command.ApplicationID {
		return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy subscription controller identity changed")
	}
	baseURL, token, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return meridianruntime.LegacyExportResult{}, err
	}
	inbounds, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return meridianruntime.LegacyExportResult{}, err
	}
	export := meridianruntime.LegacyExport{ControllerApplicationID: command.ApplicationID}
	managedInboundIDs := map[int]bool{}
	hy2ByPrimaryInbound := map[int]threeXUIRealityInbound{}
	for _, summary := range inbounds {
		if summary.Protocol != "vless" || !managedThreeXUIRealityTag(summary.Tag) || isLegacyRealityGuardInbound(summary) {
			continue
		}
		inbound, detailErr := getThreeXUIInbound(ctx, baseURL, token, summary.ID)
		if detailErr != nil || inbound.ID != summary.ID || inbound.Tag != summary.Tag || inbound.NodeID == nil != (summary.NodeID == nil) || inbound.NodeID != nil && summary.NodeID != nil && *inbound.NodeID != *summary.NodeID {
			return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy REALITY endpoint changed during export")
		}
		var hy2 *threeXUIRealityInbound
		for _, candidate := range inbounds {
			if candidate.Protocol != "hysteria" || !threeXUIInboundMatchesNode(candidate, dereferenceNodeID(inbound.NodeID)) {
				continue
			}
			primaryTag := normalizedThreeXUIInboundTag(inbound.Tag, dereferenceNodeID(inbound.NodeID))
			candidateTag := normalizedThreeXUIInboundTag(candidate.Tag, dereferenceNodeID(inbound.NodeID))
			if candidateTag != nodeprotocol.HY2Tag(primaryTag) {
				continue
			}
			if hy2 != nil {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy Hysteria endpoint identity is ambiguous")
			}
			detail, detailErr := getThreeXUIInbound(ctx, baseURL, token, candidate.ID)
			if detailErr != nil || detail.ID != candidate.ID || detail.Tag != candidate.Tag || detail.Protocol != "hysteria" {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy Hysteria endpoint changed during export")
			}
			hy2 = &detail
		}
		endpoint, managed, parseErr := exportLegacyMeridianEndpoint(inbound, hy2)
		if parseErr != nil {
			return meridianruntime.LegacyExportResult{}, parseErr
		}
		if !managed {
			continue
		}
		managedInboundIDs[inbound.ID] = true
		if hy2 != nil {
			hy2ByPrimaryInbound[inbound.ID] = *hy2
		}
		export.Endpoints = append(export.Endpoints, endpoint)
	}
	if len(export.Endpoints) == 0 {
		return meridianruntime.LegacyExportResult{}, errors.New("agent: no managed REALITY endpoint is available for Meridian cutover")
	}
	slices.SortFunc(export.Endpoints, func(a, b meridianruntime.LegacyEndpoint) int { return a.InboundID - b.InboundID })

	controller, err := store.landingController(ctx)
	if err != nil {
		return meridianruntime.LegacyExportResult{}, err
	}
	childEmails := map[string]bool{}
	accountJournals := map[string]landingControllerAccount{}
	if controller != nil {
		for id, account := range controller.Accounts {
			if account.PendingOperation != "" || account.Blocked {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared account journal has an unresolved operation")
			}
			accountJournals[id] = account
		}
		for _, grant := range controller.Grants {
			if grant.Phase == "revoked" {
				continue
			}
			if !legacyRouteGrantConverged(grant) {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared route journal has not converged")
			}
			if !managedInboundIDs[grant.Task.InboundID] {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared route references an unmanaged endpoint")
			}
			childEmails[grant.Task.Grant.FixedUser] = true
			baseline, observed, found := legacyUsageMember(accountJournals[grant.Task.Grant.ParentID], grant.Task.Grant.FixedIdentity)
			if !found {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared route usage ledger is incomplete")
			}
			export.Routes = append(export.Routes, meridianruntime.LegacyRoute{
				ID: grant.Task.Grant.ID, ParentIdentityHash: grant.Task.Grant.ParentID,
				InboundID: grant.Task.InboundID, InboundTag: grant.Task.Grant.InboundTag,
				BaseUser: grant.Task.Grant.BaseUser, FixedUser: grant.Task.Grant.FixedUser,
				FixedUUID: grant.Task.FixedUUID, EgressNodeID: grant.Task.Grant.Peer.ID,
				Enabled: grant.Task.Grant.Enabled, HideNative: grant.Task.Grant.HideBase,
				Revision: grant.Task.Revision, UsageBaseline: baseline, UsageObserved: observed,
			})
		}
	}
	slices.SortFunc(export.Routes, func(a, b meridianruntime.LegacyRoute) int { return strings.Compare(a.ID, b.ID) })

	listed, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return meridianruntime.LegacyExportResult{}, err
	}
	for _, summary := range listed {
		if childEmails[summary.Email] {
			continue
		}
		detail, err := getThreeXUIClient(ctx, baseURL, token, summary.Email)
		if err != nil {
			return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy client identity inventory is incomplete")
		}
		inboundIDs := make([]int, 0, len(detail.InboundIDs))
		for _, inboundID := range detail.InboundIDs {
			if managedInboundIDs[inboundID] {
				inboundIDs = append(inboundIDs, inboundID)
			}
		}
		slices.Sort(inboundIDs)
		inboundIDs = slices.Compact(inboundIDs)
		if len(inboundIDs) == 0 {
			return meridianruntime.LegacyExportResult{}, fmt.Errorf("agent: legacy client %q has no managed REALITY endpoint", summary.Email)
		}
		uuid, subscriptionToken := clientJSONText(detail.Client, "id"), clientJSONText(detail.Client, "subId")
		var enabled bool
		var totalBytes, expiryTime int64
		var resetDays, limitIP int
		if json.Unmarshal(detail.Client["enable"], &enabled) != nil || json.Unmarshal(detail.Client["totalGB"], &totalBytes) != nil || json.Unmarshal(detail.Client["expiryTime"], &expiryTime) != nil || json.Unmarshal(detail.Client["reset"], &resetDays) != nil || json.Unmarshal(detail.Client["limitIp"], &limitIP) != nil {
			return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy client plan inventory is incomplete")
		}
		baseline, observed := int64(0), summary.UsedBytes
		identityHash := meridianruntime.LegacyIdentityFingerprint(uuid)
		if account, exists := accountJournals[identityHash]; exists {
			if account.Deleted || account.Email != summary.Email || account.SubscriptionToken != subscriptionToken {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared account authority changed")
			}
			enabled, totalBytes, expiryTime, resetDays = account.Enabled, account.Total, account.Expiry, account.ResetDays
			var found bool
			baseline, observed, found = legacyUsageMember(account, identityHash)
			if !found {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared account usage ledger is incomplete")
			}
		}
		hy2AuthByInbound := map[int]string{}
		for _, inboundID := range inboundIDs {
			if hy2, exists := hy2ByPrimaryInbound[inboundID]; exists {
				auth, authErr := legacyHysteriaAuth(hy2, summary.Email)
				if authErr != nil {
					return meridianruntime.LegacyExportResult{}, authErr
				}
				hy2AuthByInbound[inboundID] = auth
			}
		}
		export.Clients = append(export.Clients, meridianruntime.LegacyClient{
			Email: summary.Email, UUID: uuid, SubscriptionToken: subscriptionToken,
			Enabled: enabled, TotalBytes: totalBytes, UsedBytes: summary.UsedBytes,
			UsageBaseline: baseline, UsageObserved: observed,
			ExpiryTime: expiryTime, ResetDays: resetDays, LimitIP: limitIP, InboundIDs: inboundIDs,
			HY2AuthByInbound: hy2AuthByInbound,
		})
	}
	slices.SortFunc(export.Clients, func(a, b meridianruntime.LegacyClient) int { return strings.Compare(a.Email, b.Email) })
	result := meridianruntime.LegacyExportResult{Export: export}
	if err := result.Validate(command); err != nil {
		return meridianruntime.LegacyExportResult{}, errors.New("agent: legacy Meridian export failed validation")
	}
	return result, nil
}

func legacyRouteGrantConverged(grant landingControllerGrant) bool {
	// A confirmed activation is journaled as ready. Active is the Center-side
	// grant status, not an Agent journal phase.
	return grant.Phase == "ready" && grant.Task.Phase == "activate"
}

func exportLegacyMeridianEndpoint(inbound threeXUIRealityInbound, hy2 *threeXUIRealityInbound) (meridianruntime.LegacyEndpoint, bool, error) {
	if inbound.Protocol != "vless" || !managedThreeXUIRealityTag(inbound.Tag) || isLegacyRealityGuardInbound(inbound) {
		return meridianruntime.LegacyEndpoint{}, false, nil
	}
	var stream struct {
		Network     string `json:"network"`
		Security    string `json:"security"`
		Fingerprint string `json:"fingerprint"`
		Reality     struct {
			Target      string   `json:"target"`
			Dest        string   `json:"dest"`
			ServerNames []string `json:"serverNames"`
			PrivateKey  string   `json:"privateKey"`
			PublicKey   string   `json:"publicKey"`
			ShortIDs    []string `json:"shortIds"`
			Fingerprint string   `json:"fingerprint"`
			Settings    struct {
				PublicKey string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal(inbound.StreamSettings, &stream) != nil || strings.ToLower(stream.Security) != "reality" || stream.Network != "" && strings.ToLower(stream.Network) != "tcp" && strings.ToLower(stream.Network) != "raw" {
		return meridianruntime.LegacyEndpoint{}, false, errors.New("agent: managed legacy endpoint has unsupported transport")
	}
	target, publicKey := strings.TrimSpace(stream.Reality.Target), strings.TrimSpace(stream.Reality.PublicKey)
	if target == "" {
		target = strings.TrimSpace(stream.Reality.Dest)
	}
	if publicKey == "" {
		publicKey = strings.TrimSpace(stream.Reality.Settings.PublicKey)
	}
	fingerprint := strings.TrimSpace(stream.Reality.Fingerprint)
	if fingerprint == "" {
		fingerprint = strings.TrimSpace(stream.Fingerprint)
	}
	if fingerprint == "" {
		fingerprint = "chrome"
	}
	remoteNodeID := 0
	if inbound.NodeID != nil {
		remoteNodeID = *inbound.NodeID
	}
	result := meridianruntime.LegacyEndpoint{
		InboundID: inbound.ID, RemoteNodeID: remoteNodeID, Tag: inbound.Tag,
		DisplayName: inbound.Remark, Listen: inbound.Listen, Port: inbound.Port,
		Enabled: inbound.Enable, TotalBytes: inbound.Total, UsedBytes: inbound.Up + inbound.Down,
		Target: target, ServerNames: slices.Clone(stream.Reality.ServerNames),
		PrivateKey: strings.TrimSpace(stream.Reality.PrivateKey), PublicKey: publicKey,
		ShortIDs: slices.Clone(stream.Reality.ShortIDs), Fingerprint: fingerprint,
		TrafficResetDay: inbound.TrafficResetDay,
		VLESSEnabled:    inbound.Enable,
	}
	if hy2 != nil {
		var stream struct {
			Network  string `json:"network"`
			Security string `json:"security"`
			TLS      struct {
				ServerName   string `json:"serverName"`
				Certificates []struct {
					Certificate []string `json:"certificate"`
					Key         []string `json:"key"`
				} `json:"certificates"`
			} `json:"tlsSettings"`
			Hysteria struct {
				Version int `json:"version"`
			} `json:"hysteriaSettings"`
		}
		if json.Unmarshal(hy2.StreamSettings, &stream) != nil || stream.Network != "hysteria" || stream.Security != "tls" || stream.Hysteria.Version != 2 || stream.TLS.ServerName == "" || len(stream.TLS.Certificates) != 1 {
			return meridianruntime.LegacyEndpoint{}, false, errors.New("agent: managed legacy Hysteria endpoint has unsupported transport")
		}
		result.HY2Configured = true
		result.HY2Enabled = hy2.Enable
		result.HY2Tag = hy2.Tag
		result.HY2ServerName = stream.TLS.ServerName
		result.HY2Certificate = strings.Join(stream.TLS.Certificates[0].Certificate, "\n")
		result.HY2PrivateKey = strings.Join(stream.TLS.Certificates[0].Key, "\n")
	}
	return result, true, nil
}

func dereferenceNodeID(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func legacyHysteriaAuth(inbound threeXUIRealityInbound, email string) (string, error) {
	var settings struct {
		Clients []struct {
			Email string `json:"email"`
			Auth  string `json:"auth"`
		} `json:"clients"`
	}
	if json.Unmarshal(inbound.Settings, &settings) != nil {
		return "", errors.New("agent: legacy Hysteria credential inventory is invalid")
	}
	for _, client := range settings.Clients {
		if client.Email == email && strings.TrimSpace(client.Auth) != "" {
			return client.Auth, nil
		}
	}
	return "", errors.New("agent: legacy Hysteria credential is unavailable")
}

func legacyUsageMember(account landingControllerAccount, identity string) (int64, int64, bool) {
	for _, member := range account.Members {
		if member.ID == identity {
			return member.Baseline, member.Observed, true
		}
	}
	return 0, 0, false
}
