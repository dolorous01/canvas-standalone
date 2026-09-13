package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/buildinfo"
	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
)

type ObjectProber interface {
	Probe(context.Context) error
}

type Handler struct {
	DB      *sql.DB
	Objects ObjectProber
	Logger  *slog.Logger
	Release string
	Prefix  string
}

type response struct {
	Status        string         `json:"status"`
	Release       string         `json:"release"`
	Build         buildinfo.Info `json:"build"`
	SchemaVersion string         `json:"schema_version,omitempty"`
}

func (handler Handler) Register(mux *http.ServeMux) {
	prefix := handler.Prefix
	if prefix == "" {
		prefix = "/canvas-api"
	}
	mux.HandleFunc("GET "+prefix+"/health/live", handler.Live)
	mux.HandleFunc("GET "+prefix+"/health/ready", handler.Ready)
}

func (handler Handler) Live(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, response{Status: "live", Release: handler.Release, Build: buildinfo.Current()})
}

func (handler Handler) Ready(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	if err := handler.DB.PingContext(ctx); err != nil {
		handler.unavailable(writer, "database readiness failed", err)
		return
	}
	status, err := migrate.Verify(ctx, handler.DB)
	if err != nil {
		handler.unavailable(writer, "schema readiness failed", err)
		return
	}
	if err := handler.Objects.Probe(ctx); err != nil {
		handler.unavailable(writer, "object-store readiness failed", err)
		return
	}
	writeJSON(writer, http.StatusOK, response{
		Status: "ready", Release: handler.Release, Build: buildinfo.Current(), SchemaVersion: status.SchemaVersion,
	})
}

func (handler Handler) unavailable(writer http.ResponseWriter, message string, err error) {
	if handler.Logger != nil {
		handler.Logger.Error(message, "error", err)
	}
	writeJSON(writer, http.StatusServiceUnavailable, response{Status: "unavailable", Release: handler.Release, Build: buildinfo.Current()})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
