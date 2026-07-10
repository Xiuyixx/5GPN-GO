package api

import (
	"encoding/json"
	"net/http"

	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
)

// ExitSummary is what the panel renders in the Exit table.
type ExitSummary struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Active   bool   `json:"active"`
	Notes    string `json:"notes,omitempty"`
}

// exitsState is an in-memory registry seeded from parsed URIs. M2 S4
// replaces this with SQLite persistence + orchestrator wiring.
var exitsState = struct {
	items  []ExitSummary
	active string
}{
	items: []ExitSummary{
		{ID: "direct", Protocol: "direct", Server: "-", Port: 0, Active: true, Notes: "built-in"},
	},
	active: "direct",
}

func (s *Server) handleListExits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"exits":  exitsState.items,
		"active": exitsState.active,
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
	p, err := xexit.Parse(req.URI)
	if err != nil {
		writeError(w, http.StatusBadRequest, "parse_error", err.Error())
		return
	}
	host, _ := p["server"].(string)
	port, _ := p["port"].(int)
	proto, _ := p["type"].(string)
	item := ExitSummary{ID: req.ID, Protocol: proto, Server: host, Port: port}
	// dedupe by id
	for i, e := range exitsState.items {
		if e.ID == req.ID {
			exitsState.items[i] = item
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	exitsState.items = append(exitsState.items, item)
	writeJSON(w, http.StatusCreated, item)
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
	if req.ID == exitsState.active {
		writeError(w, http.StatusConflict, "exit_active", "cannot delete the active exit")
		return
	}
	out := exitsState.items[:0]
	for _, e := range exitsState.items {
		if e.ID != req.ID {
			out = append(out, e)
		}
	}
	exitsState.items = out
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
	found := false
	for i, e := range exitsState.items {
		if e.ID == req.ID {
			exitsState.items[i].Active = true
			found = true
		} else {
			exitsState.items[i].Active = false
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no exit with id "+req.ID)
		return
	}
	exitsState.active = req.ID
	writeJSON(w, http.StatusOK, map[string]any{"active": req.ID})
}
