package db

import "database/sql"

// RuleVersion mirrors the rule_versions row.
type RuleVersion struct {
	ID         int64
	SnapshotID int64
	RulesYAML  string
	Active     bool
}

// Snapshot mirrors the snapshots row.
type Snapshot struct {
	ID           int64
	ConfigHash   string
	TarballPath  string
	Note         string
}

// GetActiveRuleVersion returns the row where active = 1. M0 stub.
func GetActiveRuleVersion(handle *sql.DB) (*RuleVersion, error) {
	_ = handle
	return nil, ErrNotImplemented
}

// InsertSnapshot writes a new snapshot row. M0 stub.
func InsertSnapshot(handle *sql.DB, s Snapshot) (int64, error) {
	_ = handle
	_ = s
	return 0, ErrNotImplemented
}
