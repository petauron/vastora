package center

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

var supportedRegionCodes = strings.Fields(`AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW`)

var supportedRegionCodeSet = func() map[string]struct{} {
	values := make(map[string]struct{}, len(supportedRegionCodes))
	for _, code := range supportedRegionCodes {
		values[code] = struct{}{}
	}
	return values
}()

var chineseRegionNames = display.Regions(language.SimplifiedChinese)

type RegionView struct {
	Code   string `json:"code"`
	NameZH string `json:"nameZh"`
	Prefix string `json:"prefix"`
}

type RegionSuggestion struct {
	AgentID       string `json:"agentId"`
	PublicAddress string `json:"publicAddress"`
	RegionCode    string `json:"regionCode"`
	Prefix        string `json:"prefix"`
	Source        string `json:"source"`
}

func regionCode(value string) (string, bool) {
	code := strings.ToUpper(strings.TrimSpace(value))
	_, ok := supportedRegionCodeSet[code]
	return code, ok
}

func regionFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	return string([]rune{rune(code[0]-'A') + 0x1F1E6, rune(code[1]-'A') + 0x1F1E6})
}

func regionPrefix(code string) string {
	return regionFlag(code) + " " + regionNameZH(code)
}

func regionNameZH(code string) string {
	region, err := language.ParseRegion(code)
	if err != nil {
		return code
	}
	name := strings.TrimSpace(chineseRegionNames.Name(region))
	if name == "" {
		return code
	}
	return name
}

func composeRealityDisplayName(code, name string) (string, string, string, error) {
	code, ok := regionCode(code)
	name = strings.TrimSpace(name)
	if !ok || !validThreeXUIClientName(name) {
		return "", "", "", errors.New("center: a standard region and valid node name are required")
	}
	displayName := regionPrefix(code) + name
	if !validThreeXUIClientName(displayName) {
		return "", "", "", errors.New("center: the region-prefixed node name is too long")
	}
	return code, name, displayName, nil
}

func validRegionPrefixedRealityName(code, displayName string) bool {
	code, ok := regionCode(code)
	return ok && validThreeXUIClientName(displayName) && strings.HasPrefix(displayName, regionPrefix(code))
}

func (s *Store) Regions() []RegionView {
	regions := make([]RegionView, 0, len(supportedRegionCodes))
	for _, code := range supportedRegionCodes {
		regions = append(regions, RegionView{Code: code, NameZH: regionNameZH(code), Prefix: regionPrefix(code)})
	}
	return regions
}

func (s *Store) SuggestAgentRegion(ctx context.Context, agentID string) (RegionSuggestion, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return RegionSuggestion{}, errors.New("center: Agent is required for region matching")
	}
	if s.lookupPublicRegion == nil {
		return RegionSuggestion{}, errors.New("center: automatic region matching is disabled; select a region manually")
	}
	var publicAddress string
	if err := s.db.QueryRowContext(ctx, `SELECT public_address FROM agent_network_profiles WHERE agent_id = ? AND direct_public = 1`, agentID).Scan(&publicAddress); err != nil {
		return RegionSuggestion{}, errors.New("center: this Agent has no confirmed public address")
	}
	publicIP := net.ParseIP(strings.TrimSpace(publicAddress))
	if publicIP == nil {
		return RegionSuggestion{}, errors.New("center: this Agent has no usable public address")
	}
	code, err := s.lookupPublicRegion(ctx, publicIP.String())
	if err != nil {
		return RegionSuggestion{}, fmt.Errorf("center: automatic region matching failed: %w", err)
	}
	code, ok := regionCode(code)
	if !ok {
		return RegionSuggestion{}, errors.New("center: automatic region matching returned an unsupported region")
	}
	return RegionSuggestion{AgentID: agentID, PublicAddress: publicIP.String(), RegionCode: code, Prefix: regionPrefix(code), Source: "configured_helper"}, nil
}

func regionLookupAt(client *http.Client, baseURL string) func(context.Context, string) (string, error) {
	return func(ctx context.Context, address string) (string, error) {
		publicIP := net.ParseIP(strings.TrimSpace(address))
		if publicIP == nil {
			return "", errors.New("invalid public address")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/"+url.PathEscape(publicIP.String()), nil)
		if err != nil {
			return "", err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "Vastora Center")
		response, err := client.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("region lookup service returned HTTP %d", response.StatusCode)
		}
		var result struct {
			IP      string `json:"ip"`
			Country string `json:"country"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil {
			return "", errors.New("region lookup service returned invalid JSON")
		}
		resolvedIP := net.ParseIP(strings.TrimSpace(result.IP))
		if resolvedIP == nil || !resolvedIP.Equal(publicIP) {
			return "", errors.New("region lookup service returned a different address")
		}
		if _, ok := regionCode(result.Country); !ok {
			return "", errors.New("region lookup service returned an unsupported region")
		}
		return strings.ToUpper(result.Country), nil
	}
}

func (s *Server) handleListRegions(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"regions": s.store.Regions()})
}

func (s *Server) handleSuggestAgentRegion(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.SuggestAgentRegion(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}
