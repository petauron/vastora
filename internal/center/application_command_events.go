package center

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) handleApplicationCommandEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("center: streaming is unavailable"))
		return
	}
	commandID := request.PathValue("id")
	if _, err := s.store.ApplicationCommand(request.Context(), commandID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	fallback := time.NewTicker(time.Second)
	defer fallback.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	var previous []byte
	for {
		key := "task:" + commandID
		changed := s.store.taskChanges.subscribe(key)
		command, err := s.store.ApplicationCommand(request.Context(), commandID)
		if err != nil {
			s.store.taskChanges.unsubscribe(key, changed)
			return
		}
		payload, err := json.Marshal(command)
		if err != nil {
			s.store.taskChanges.unsubscribe(key, changed)
			return
		}
		if !bytes.Equal(previous, payload) {
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
				s.store.taskChanges.unsubscribe(key, changed)
				return
			}
			flusher.Flush()
			previous = payload
		}
		if command.State == "succeeded" || command.State == "failed" {
			s.store.taskChanges.unsubscribe(key, changed)
			return
		}
		select {
		case <-request.Context().Done():
			s.store.taskChanges.unsubscribe(key, changed)
			return
		case <-changed:
		case <-fallback.C:
			s.store.taskChanges.unsubscribe(key, changed)
		case <-keepalive.C:
			s.store.taskChanges.unsubscribe(key, changed)
			if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
