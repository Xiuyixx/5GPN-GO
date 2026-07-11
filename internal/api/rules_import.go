package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Xiuyixx/5GPN-Go/internal/rules"
)

// importRequest asks the server to fetch a remote ruleset (URL) or parse a
// pasted body (Text) into candidate rules. The rules are returned but NOT
// applied — the frontend merges them into the draft and runs dry-run
// before Apply.
type importRequest struct {
	URL          string   `json:"url"`             // optional
	Text         string   `json:"text"`            // optional; used when URL is empty
	Action       string   `json:"action"`          // rewrite non-final rules to this action; default: "PROXY"
	IDPrefix     string   `json:"id_prefix"`       // rule id prefix; default: "imp-"
	StartingPrio int      `json:"starting_priority"` // default: 200 (leaves headroom for manual rules)
	Keep         []string `json:"keep_categories"` // matches PGW_KEEP_CATEGORIES
	DirectCats   []string `json:"direct_categories"` // matches PGW_DIRECT_CATEGORIES
}

type importResponse struct {
	Rules      []rules.Rule `json:"rules"`
	Converted  int          `json:"converted"`
	Dropped    int          `json:"dropped"`
	Categories []string     `json:"categories"`
	SourceURL  string       `json:"source_url,omitempty"`
	SourceKind string       `json:"source_kind"` // "url" | "text"
}

// handleImportRules fetches or parses a ruleset and returns candidate
// Rule[] the frontend can merge into the draft. Never mutates the DB.
func (s *Server) handleImportRules(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "PROXY"
	}
	idPrefix := req.IDPrefix
	if idPrefix == "" {
		idPrefix = "imp-"
	}
	startPrio := req.StartingPrio
	if startPrio == 0 {
		startPrio = 200
	}

	// Load the raw ruleset text either from URL (fetched here) or the
	// caller's paste.
	var body string
	var sourceURL, sourceKind string
	if url := strings.TrimSpace(req.URL); url != "" {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			writeError(w, http.StatusBadRequest, "bad_url", "url must start with http:// or https://")
			return
		}
		fetched, err := fetchRuleset(r.Context(), url, 8*time.Second, 2*1024*1024)
		if err != nil {
			writeError(w, http.StatusBadGateway, "fetch_failed", err.Error())
			return
		}
		body = fetched
		sourceURL = url
		sourceKind = "url"
	} else if txt := req.Text; strings.TrimSpace(txt) != "" {
		body = txt
		sourceKind = "text"
	} else {
		writeError(w, http.StatusBadRequest, "empty", "provide either url or text")
		return
	}

	// Parse via the existing legacy importer.
	csvLines, rep := rules.ImportLegacy(body, rules.ImportLegacyOptions{
		KeepCategories:   req.Keep,
		DirectCategories: req.DirectCats,
	})

	// Convert CSV lines into Rule structs so the frontend can merge them.
	out := make([]rules.Rule, 0, len(csvLines))
	prio := startPrio
	for i, line := range csvLines {
		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 2 {
			continue
		}
		kind := strings.ToUpper(strings.TrimSpace(parts[0]))
		var pattern, ruleAction string
		if kind == "FINAL" || kind == "MATCH" {
			// FINAL is legacy name; normalize to MATCH.
			kind = "MATCH"
			pattern = ""
			ruleAction = strings.TrimSpace(parts[1])
		} else {
			if len(parts) < 3 {
				continue
			}
			pattern = strings.TrimSpace(parts[1])
			ruleAction = strings.TrimSpace(parts[2])
		}
		// Rewrite any non-direct/non-block category to the requested action
		// so imported rules point at a real exit id the operator picked.
		low := strings.ToLower(ruleAction)
		if low != "direct" && low != "block" {
			ruleAction = action
		}
		out = append(out, rules.Rule{
			ID:       fmt.Sprintf("%s%d", idPrefix, i+1),
			Kind:     rules.Kind(kind),
			Pattern:  pattern,
			Action:   ruleAction,
			Priority: prio,
			Enabled:  true,
		})
		prio += 10
	}

	writeJSON(w, http.StatusOK, importResponse{
		Rules:      out,
		Converted:  rep.Converted,
		Dropped:    rep.Dropped,
		Categories: rep.Categories,
		SourceURL:  sourceURL,
		SourceKind: sourceKind,
	})
}

// fetchRuleset does a bounded HTTP GET. Bounded body size + timeout keeps
// the endpoint hard to abuse.
func fetchRuleset(ctx context.Context, url string, timeout time.Duration, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "5gpn-panel/import")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("ruleset exceeds %d bytes", maxBytes)
	}
	return string(data), nil
}
