package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
)

// ExitSummary is what the panel renders in the Exit table. Fields
// mirror the JSON shape Dashboard AC7 depends on — do not rename.
type ExitSummary struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Active   bool   `json:"active"`
	Notes    string `json:"notes,omitempty"`
}

func exitToSummary(e xexit.Exit) ExitSummary {
	if e.Protocol == "direct" {
		return ExitSummary{
			ID: e.ExitID, Protocol: "direct", Server: "-", Port: 0,
			Active: e.Active, Notes: "built-in",
		}
	}
	host, _ := e.ProxyConfig["server"].(string)
	port, _ := e.ProxyConfig["port"].(int)
	return ExitSummary{
		ID: e.ExitID, Protocol: e.Protocol, Server: host, Port: port,
		Active: e.Active,
	}
}

func (s *Server) handleListExits(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_error", err.Error())
		return
	}
	out := make([]ExitSummary, 0, len(items))
	active := ""
	for _, e := range items {
		sum := exitToSummary(e)
		if e.Active {
			active = e.ExitID
		}
		out = append(out, sum)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exits":  out,
		"active": active,
	})
}

type addExitRequest struct {
	ID  string `json:"id"`
	URI string `json:"uri"`
}

func (s *Server) handleAddExit(w http.ResponseWriter, r *http.Request) {
	var req addExitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.ID == "" || req.URI == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id and uri required")
		return
	}

	e, err := s.Store.Add(r.Context(), req.ID, req.URI)
	if err != nil {
		switch {
		case errors.Is(err, xexit.ErrInvalidExitID):
			writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
		case strings.Contains(err.Error(), "parse uri"):
			writeError(w, http.StatusBadRequest, "parse_error", err.Error())
		case strings.Contains(err.Error(), "UNIQUE constraint"):
			writeError(w, http.StatusConflict, "exit_exists", "exit id already in use")
		default:
			writeError(w, http.StatusInternalServerError, "add_error", err.Error())
		}
		return
	}

	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "exits.add",
		Target: req.ID,
		After:  req.URI,
		Result: "ok",
	})

	writeJSON(w, http.StatusCreated, exitToSummary(e))
}

type deleteExitRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleDeleteExit(w http.ResponseWriter, r *http.Request) {
	var req deleteExitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id required")
		return
	}

	if err := s.Applier.DeleteExit(r.Context(), req.ID); err != nil {
		switch {
		case errors.Is(err, core.ErrExitSwitchInFlight):
			writeError(w, http.StatusConflict, "exit_switch_in_flight", err.Error())
		case errors.Is(err, xexit.ErrExitActive):
			writeError(w, http.StatusConflict, "exit_active", err.Error())
		case errors.Is(err, xexit.ErrExitNotFound):
			writeError(w, http.StatusNotFound, "not_found", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "delete_error", err.Error())
		}
		return
	}

	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "exits.delete",
		Target: req.ID,
		Result: "ok",
	})

	w.WriteHeader(http.StatusNoContent)
}

type switchExitRequest struct {
	ID string `json:"id"`
}

func (s *Server) handleSwitchExit(w http.ResponseWriter, r *http.Request) {
	var req switchExitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id required")
		return
	}

	res, err := s.Applier.SwitchExit(r.Context(), req.ID)
	if err != nil {
		actor, _ := r.Context().Value(ctxUsername).(string)
		_ = db.AppendAudit(s.DB, db.AuditEntry{
			Actor:  actor,
			Action: "exits.switch",
			Target: req.ID,
			Result: "failed",
		})
		switch {
		case errors.Is(err, xexit.ErrExitNotFound):
			writeError(w, http.StatusNotFound, "not_found", err.Error())
		case errors.Is(err, orchestrator.ErrApplyInFlight):
			writeError(w, http.StatusConflict, "apply_in_flight", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "switch_error", err.Error())
		}
		return
	}

	actor, _ := r.Context().Value(ctxUsername).(string)
	result := "ok"
	if res.Health == "observing" {
		result = "observing"
	}
	if res.RolledBack {
		result = "rolled_back"
	}
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor:  actor,
		Action: "exits.switch",
		Target: req.ID,
		Result: result,
	})
	entry := s.trackApplyResult(req.ID, "exit_switch", 0, res)
	if entry.Status == "pending" {
		w.Header().Set("Location", "/api/v1/applies/"+entry.ID)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"active": req.ID, "health": res.Health, "status": "pending",
			"apply_id": entry.ID, "snapshot_id": res.SnapshotID,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"active": req.ID,
		"health": res.Health,
	})
}
