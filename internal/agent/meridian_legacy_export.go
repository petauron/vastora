package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/petauron/vastora/internal/landing"
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
	excludedChildren := map[string]landingControllerGrant{}
	childOwnerByIdentity := map[string]string{}
	accountJournals := map[string]landingControllerAccount{}
	if controller != nil {
		for id, account := range controller.Accounts {
			if account.PendingOperation != "" || account.Blocked {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared account journal has an unresolved operation")
			}
			accountJournals[id] = account
		}
		for _, grant := range controller.Grants {
			// The MVP migrates native entry subscriptions only. Shared-route
			// children are not independent accounts, regardless of their old
			// controller phase. Keep their ownership inventory so an unknown
			// child can never be mistaken for a native subscriber.
			user := grant.Task.Grant.FixedUser
			if user == "" || grant.ChildSubscription == "" || landing.Identity(grant.Task.FixedUUID) != grant.Task.Grant.FixedIdentity {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared child ownership is incomplete")
			}
			if _, duplicate := excludedChildren[user]; duplicate {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: duplicate shared child")
			}
			if _, duplicate := childOwnerByIdentity[grant.Task.Grant.FixedIdentity]; duplicate {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared child has conflicting account ownership")
			}
			excludedChildren[user] = grant
			childOwnerByIdentity[grant.Task.Grant.FixedIdentity] = grant.Task.Grant.ParentID
		}
	}

	listed, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return meridianruntime.LegacyExportResult{}, err
	}
	for _, summary := range listed {
		if grant, excluded := excludedChildren[summary.Email]; excluded {
			detail, detailErr := getThreeXUIClient(ctx, baseURL, token, summary.Email)
			if detailErr != nil || legacyExcludedChildChanged(summary, detail, grant) {
				return meridianruntime.LegacyExportResult{}, errors.New("agent: shared child changed before export")
			}
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
			baseline, observed, found = legacyNativeOnlyUsage(account, identityHash, childOwnerByIdentity)
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

func legacyExcludedChildChanged(summary ThreeXUIClientView, detail threeXUIClientDetail, grant landingControllerGrant) bool {
	return summary.Email != grant.Task.Grant.FixedUser || verifyLandingChild(detail, grant) != nil
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

func legacyNativeOnlyUsage(account landingControllerAccount, identity string, childOwnerByIdentity map[string]string) (int64, int64, bool) {
	if account.ID != identity {
		return 0, 0, false
	}
	var baseline, used int64
	found := false
	seen := map[string]bool{}
	for _, member := range account.Members {
		if seen[member.ID] || (member.ID != identity && childOwnerByIdentity[member.ID] != identity) || member.Baseline < 0 || member.Observed < member.Baseline || member.Observed-member.Baseline > math.MaxInt64-used {
			return 0, 0, false
		}
		seen[member.ID] = true
		used += member.Observed - member.Baseline
		if member.ID == identity {
			baseline, found = member.Baseline, true
		}
	}
	if !found || used > math.MaxInt64-baseline {
		return 0, 0, false
	}
	// Preserve consumption by all retired or omitted child credentials in the
	// one imported native watermark. New Xray counters start at zero and the
	// Center watermark adds only their subsequent deltas.
	return baseline, baseline + used, true
}
