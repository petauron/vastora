package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// This is an in-process Agent endpoint, bound only to the installed private
// service address. It cannot forward management paths or arbitrary upstreams.
func (s *Store) landingSubscriptionHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, private")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		token, clash, ok := landingSubscriptionRequest(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		state, err := s.landingController(r.Context())
		if err != nil || state == nil {
			http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
			return
		}
		var native *landingNativeSubscription
		for _, value := range state.Subscriptions {
			if sameSubscriptionToken(token, value.Token) {
				if native != nil {
					http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
					return
				}
				copy := value
				native = &copy
			}
		}
		if native == nil {
			http.NotFound(w, r)
			return
		}
		var account *landingControllerAccount
		grants := []landing.SubscriptionGrant{}
		for _, grant := range state.Grants {
			if sameSubscriptionToken(token, grant.ChildSubscription) {
				http.NotFound(w, r)
				return
			}
		}
		if value, ok := state.Accounts[native.ID]; ok {
			copy := value
			account = &copy
		}
		if account == nil && (!native.Enabled || native.Expiry > 0 && native.Expiry <= s.now().UnixMilli() || native.Total > 0 && native.Used >= native.Total) {
			http.NotFound(w, r)
			return
		}
		mode := landing.FixedMode
		used, total, expiry := native.Used, native.Total, native.Expiry
		if account != nil {
			_, aggregateUsed, quotaErr := landing.AllocateQuota(account.Total, account.Enabled, account.Members)
			if quotaErr != nil || account.Blocked || account.Deleted || account.PendingOperation != "" || !account.Enabled || !account.Mode.Valid() || account.Expiry > 0 && account.Expiry <= s.now().UnixMilli() || account.Total > 0 && aggregateUsed >= account.Total {
				http.NotFound(w, r)
				return
			}
			mode, used, total, expiry = account.Mode, aggregateUsed, account.Total, account.Expiry
			for _, grant := range state.Grants {
				if grant.Task.Grant.ParentID != account.ID || grant.Phase != "ready" || !grant.Task.Grant.Enabled {
					continue
				}
				grants = append(grants, landing.SubscriptionGrant{Grant: grant.Task.Grant.Published(account.Mode), EntryName: grant.Task.EntryName, LandingRegionCode: grant.Task.LandingRegionCode, BaseLink: grant.Material.BaseLink, FixedLink: grant.Material.FixedLink})
			}
			slices.SortFunc(grants, func(a, b landing.SubscriptionGrant) int { return strings.Compare(a.Grant.ID, b.Grant.ID) })
		}
		var body []byte
		if clash {
			body, err = landing.RenderMihomo(native.Links, native.ID, mode, grants)
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		} else {
			body, err = landing.RenderLinks(native.Links, native.ID, mode, grants, true)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		if err != nil {
			http.Error(w, "暂时无法生成此格式的订阅", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Profile-Title", "Vastora")
		w.Header().Set("Profile-Update-Interval", "24")
		w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", used, total, expiry/1000))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}

func sameSubscriptionToken(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func landingSubscriptionRequest(r *http.Request) (string, bool, bool) {
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		return "", false, false
	}
	path := r.URL.Path
	clash := strings.HasPrefix(path, threeXUIClashSubscriptionPath)
	prefix := threeXUIRawSubscriptionPath
	if clash {
		prefix = threeXUIClashSubscriptionPath
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	token := strings.TrimPrefix(path, prefix)
	if !validSubscriptionToken(token) {
		return "", false, false
	}
	ua := strings.ToLower(r.UserAgent())
	return token, clash || strings.Contains(ua, "clash") || strings.Contains(ua, "mihomo"), true
}

func validSubscriptionToken(token string) bool {
	if len(token) < 8 || len(token) > 128 {
		return false
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Store) runLandingSubscriptions(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var server *http.Server
	var address string
	var blockedAddress string
	var inputRule []string
	stop := func() {
		s.landingSubscriptionMu.Lock()
		s.landingSubscriptionAddress = ""
		s.landingSubscriptionMu.Unlock()
		if server != nil {
			_ = server.Close()
			server = nil
		}
		if inputRule != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := runSubscriptionIPTables(cleanupCtx, append([]string{"-D", "INPUT"}, inputRule...)...); err != nil && report != nil {
				report(errors.New("agent: private subscription input rule cleanup failed"))
			}
			cancel()
			inputRule = nil
		}
		address = ""
	}
	defer stop()
	for {
		installation, err := s.AppliedInstallation(ctx, threeXUIKey)
		if err != nil || (landing.ServerPlan{Revision: 1, Address: installation.ServiceAddress}).Validate() != nil {
			stop()
		} else if address != installation.ServiceAddress && blockedAddress != installation.ServiceAddress {
			stop()
			listener, err := net.Listen("tcp4", net.JoinHostPort(installation.ServiceAddress, strconv.Itoa(landing.SubscriptionPort)))
			if err == nil {
				inputRule, err = openLandingSubscriptionAccess(ctx, installation)
				if err != nil {
					_ = listener.Close()
					// Do not replay an uncertain firewall mutation every five seconds.
					blockedAddress = installation.ServiceAddress
				}
			}
			if err != nil {
				if report != nil {
					report(errors.New("agent: private subscription endpoint is unavailable"))
				}
			} else {
				address = installation.ServiceAddress
				server = &http.Server{Handler: s.landingSubscriptionHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
				s.landingSubscriptionMu.Lock()
				s.landingSubscriptionAddress = address
				s.landingSubscriptionMu.Unlock()
				go func(serving *http.Server, bound string) {
					if err := serving.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
						s.landingSubscriptionMu.Lock()
						if s.landingSubscriptionAddress == bound {
							s.landingSubscriptionAddress = ""
						}
						s.landingSubscriptionMu.Unlock()
						if report != nil {
							report(errors.New("agent: private subscription endpoint stopped"))
						}
					}
				}(server, address)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.landingSubscriptionMu.RLock()
		healthy := s.landingSubscriptionAddress == address
		s.landingSubscriptionMu.RUnlock()
		if !healthy {
			stop()
		}
	}
}
