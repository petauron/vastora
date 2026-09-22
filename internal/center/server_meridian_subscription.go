package center

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/petauron/meridian"
)

func (s *Server) handleMeridianSubscription(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	subscription, err := s.store.MeridianSubscription(request.Context(), request.PathValue("token"))
	if errors.Is(err, errMeridianSubscriptionNotFound) {
		http.NotFound(writer, request)
		return
	}
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("center: subscription is temporarily unavailable"))
		return
	}
	format := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("format")))
	target := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("target")))
	userAgent := strings.ToLower(request.UserAgent())
	mihomo := format == "mihomo" || target == "mihomo" || target == "clash" || strings.Contains(userAgent, "mihomo") || strings.Contains(userAgent, "clash")
	var body []byte
	contentType := "text/plain; charset=utf-8"
	if mihomo {
		body, err = meridian.RenderMihomo(subscription.Entries, subscription.Account.ID, meridian.FixedMode, subscription.Routes)
		contentType = "application/yaml; charset=utf-8"
	} else {
		body, err = meridian.RenderLinks(subscription.Entries, subscription.Account.ID, meridian.FixedMode, subscription.Routes, true)
	}
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("center: subscription is temporarily unavailable"))
		return
	}
	writer.Header().Set("Content-Type", contentType)
	userInfo := "upload=0; download=" + strconv.FormatInt(subscription.Usage.UsedBytes, 10) + "; total=" + strconv.FormatInt(subscription.Account.Plan.TotalBytes, 10)
	if subscription.Account.Plan.ExpiryTime > 0 {
		userInfo += "; expire=" + strconv.FormatInt(subscription.Account.Plan.ExpiryTime/1000, 10)
	}
	writer.Header().Set("Subscription-Userinfo", userInfo)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}
