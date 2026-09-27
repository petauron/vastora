package center

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/petauron/catalog/catalog"
)

func (s *Server) handleOfficialUIBundle(writer http.ResponseWriter, request *http.Request) {
	appID, version := request.PathValue("appID"), request.PathValue("version")
	asset := request.PathValue("asset")
	if _, err := catalog.OfficialUITargetName(appID, version); err != nil || (asset != "bundle.js" && asset != "bundle.css") {
		http.NotFound(writer, request)
		return
	}
	kind := "script"
	contentType := "text/javascript; charset=utf-8"
	if asset == "bundle.css" {
		kind, contentType = "style", "text/css; charset=utf-8"
	}
	bundle, err := s.store.OfficialUIAsset(request.Context(), appID, version, kind)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, errors.New("center: application UI is not available for this version"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(bundle)
}
