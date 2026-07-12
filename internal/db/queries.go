package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNoRows is returned when a lookup finds nothing.
var ErrNoRows = sql.ErrNoRows

// PanelUser mirrors the panel_users row.
type PanelUser struct {
	ID          int64
	Username    string
	BcryptHash  string
	CreatedAt   time.Time
	LastLoginAt sql.NullTime
}

// PanelSession mirrors the panel_sessions row.
type PanelSession struct {
	ID        string
	UserID    int64
	JWTID     string
	IssuedAt  time.Time
	ExpiresAt time.Time
	RevokedAt sql.NullTime
	IP        string
	UserAgent string
}

// Snapshot mirrors the snapshots row.
type Snapshot struct {
	ID          int64
	CreatedAt   time.Time
	ConfigHash  string
	TarballPath string
	Note        string
}

// RuleVersion mirrors the rule_versions row.
type RuleVersion struct {
	ID         int64
	SnapshotID int64
	RulesYAML  string
	CreatedAt  time.Time
	Active     bool
}

// AuditEntry mirrors the audit_log row.
type AuditEntry struct {
	Actor  string
	Action string
	Target string
	Before string
	After  string
	Result string
	IP     string
}

// InsertPanelUser creates a new panel user.
func InsertPanelUser(db *sql.DB, username, bcryptHash string) (int64, error) {
	res, err := db.Exec(`INSERT INTO panel_users(username, bcrypt_hash) VALUES(?, ?)`, username, bcryptHash)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertFirstPanelUser atomically inserts the bootstrap user only while the
// table is empty. created=false means another bootstrap request won the race.
func InsertFirstPanelUser(db *sql.DB, username, bcryptHash string) (id int64, created bool, err error) {
	res, err := db.Exec(`INSERT INTO panel_users(username, bcrypt_hash)
		SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM panel_users)`, username, bcryptHash)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	id, err = res.LastInsertId()
	return id, true, err
}

// GetPanelUserByUsername returns a user by username.
func GetPanelUserByUsername(db *sql.DB, username string) (*PanelUser, error) {
	row := db.QueryRow(`SELECT id, username, bcrypt_hash, created_at, last_login FROM panel_users WHERE username = ?`, username)
	var u PanelUser
	if err := row.Scan(&u.ID, &u.Username, &u.BcryptHash, &u.CreatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// CountPanelUsers is used at bootstrap to detect first-run state.
func CountPanelUsers(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM panel_users`).Scan(&n)
	return n, err
}

// TouchLastLogin records a successful login timestamp.
func TouchLastLogin(db *sql.DB, userID int64) error {
	_, err := db.Exec(`UPDATE panel_users SET last_login = CURRENT_TIMESTAMP WHERE id = ?`, userID)
	return err
}

// InsertPanelSession stores a JWT-backed session so it can be revoked.
func InsertPanelSession(db *sql.DB, s PanelSession) error {
	_, err := db.Exec(
		`INSERT INTO panel_sessions(id, user_id, jwt_id, issued_at, expires_at, ip, user_agent)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.JWTID, s.IssuedAt, s.ExpiresAt, s.IP, s.UserAgent,
	)
	return err
}

// IsSessionActive reports whether a session is still valid (unrevoked and unexpired).
func IsSessionActive(db *sql.DB, sessionID string) (bool, error) {
	var revoked sql.NullTime
	var expires time.Time
	err := db.QueryRow(
		`SELECT revoked_at, expires_at FROM panel_sessions WHERE id = ?`,
		sessionID,
	).Scan(&revoked, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if revoked.Valid {
		return false, nil
	}
	if time.Now().After(expires) {
		return false, nil
	}
	return true, nil
}

// RevokeSession marks a session as revoked.
func RevokeSession(db *sql.DB, sessionID string) error {
	_, err := db.Exec(`UPDATE panel_sessions SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, sessionID)
	return err
}

// RevokeSessionsExcept revokes every non-revoked session owned by userID
// except the one whose jwt_id matches keepJWTID. Used by password change so
// the caller does not self-kick from their active browser session.
func RevokeSessionsExcept(db *sql.DB, userID int64, keepJWTID string) error {
	_, err := db.Exec(
		`UPDATE panel_sessions
		    SET revoked_at = CURRENT_TIMESTAMP
		  WHERE user_id = ?
		    AND revoked_at IS NULL
		    AND jwt_id <> ?`,
		userID, keepJWTID,
	)
	return err
}

// UpdatePanelUserPassword rewrites the stored bcrypt hash for a user.
func UpdatePanelUserPassword(db *sql.DB, userID int64, bcryptHash string) error {
	_, err := db.Exec(
		`UPDATE panel_users SET bcrypt_hash = ? WHERE id = ?`,
		bcryptHash, userID,
	)
	return err
}

// InsertSnapshot writes a snapshot row and returns its id. Config hashes are
// unique, so an identical snapshot is idempotently reused.
func InsertSnapshot(db *sql.DB, s Snapshot) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO snapshots(config_hash, tarball_path, note) VALUES(?, ?, ?)
		 ON CONFLICT(config_hash) DO NOTHING`,
		s.ConfigHash, s.TarballPath, s.Note,
	)
	if err != nil {
		return 0, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if inserted == 1 {
		return res.LastInsertId()
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM snapshots WHERE config_hash = ?`, s.ConfigHash).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// GetSnapshotByID returns a snapshot row.
func GetSnapshotByID(db *sql.DB, id int64) (*Snapshot, error) {
	row := db.QueryRow(`SELECT id, created_at, config_hash, tarball_path, COALESCE(note, '') FROM snapshots WHERE id = ?`, id)
	var s Snapshot
	if err := row.Scan(&s.ID, &s.CreatedAt, &s.ConfigHash, &s.TarballPath, &s.Note); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListSnapshots returns snapshots ordered by created_at DESC.
func ListSnapshots(db *sql.DB, limit int) ([]Snapshot, error) {
	rows, err := db.Query(
		`SELECT id, created_at, config_hash, tarball_path, COALESCE(note, '') FROM snapshots ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.ConfigHash, &s.TarballPath, &s.Note); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InsertRuleVersion writes a new rules snapshot; setActive=true marks it as
// the live version (and demotes the previous one, atomically).
func InsertRuleVersion(db *sql.DB, snapshotID int64, rulesYAML string, setActive bool) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	if setActive {
		if _, err := tx.Exec(`UPDATE rule_versions SET active = 0 WHERE active = 1`); err != nil {
			return 0, err
		}
	}
	res, err := tx.Exec(
		`INSERT INTO rule_versions(snapshot_id, rules_yaml, active) VALUES(?, ?, ?)`,
		snapshotID, rulesYAML, boolToInt(setActive),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// SetActiveRuleVersion switches which rule_versions row is active.
func SetActiveRuleVersion(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE rule_versions SET active = 0 WHERE active = 1`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE rule_versions SET active = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNoRows
	}
	return tx.Commit()
}

// ClearActiveRuleVersion leaves every rule version inactive atomically.
func ClearActiveRuleVersion(db *sql.DB) error {
	_, err := db.Exec(`UPDATE rule_versions SET active = 0 WHERE active = 1`)
	return err
}

// GetRuleVersionByID returns one rule version by primary key.
func GetRuleVersionByID(db *sql.DB, id int64) (*RuleVersion, error) {
	row := db.QueryRow(`SELECT id, snapshot_id, rules_yaml, created_at, active FROM rule_versions WHERE id = ?`, id)
	var r RuleVersion
	var active int
	if err := row.Scan(&r.ID, &r.SnapshotID, &r.RulesYAML, &r.CreatedAt, &active); err != nil {
		return nil, err
	}
	r.Active = active != 0
	return &r, nil
}

// GetRuleVersionBySnapshot returns the newest rule version attached to a
// snapshot. It avoids bounded history scans when rolling back an old snapshot.
func GetRuleVersionBySnapshot(db *sql.DB, snapshotID int64) (*RuleVersion, error) {
	row := db.QueryRow(
		`SELECT id, snapshot_id, rules_yaml, created_at, active
		   FROM rule_versions
		  WHERE snapshot_id = ?
		  ORDER BY id DESC
		  LIMIT 1`, snapshotID,
	)
	var r RuleVersion
	var active int
	if err := row.Scan(&r.ID, &r.SnapshotID, &r.RulesYAML, &r.CreatedAt, &active); err != nil {
		return nil, err
	}
	r.Active = active != 0
	return &r, nil
}

// GetActiveRuleVersion returns the row where active = 1 (or ErrNoRows).
func GetActiveRuleVersion(db *sql.DB) (*RuleVersion, error) {
	row := db.QueryRow(`SELECT id, snapshot_id, rules_yaml, created_at, active FROM rule_versions WHERE active = 1`)
	var r RuleVersion
	var active int
	if err := row.Scan(&r.ID, &r.SnapshotID, &r.RulesYAML, &r.CreatedAt, &active); err != nil {
		return nil, err
	}
	r.Active = active != 0
	return &r, nil
}

// ListRuleVersions returns the N most recent rule versions.
func ListRuleVersions(db *sql.DB, limit int) ([]RuleVersion, error) {
	rows, err := db.Query(
		`SELECT id, snapshot_id, rules_yaml, created_at, active FROM rule_versions ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RuleVersion
	for rows.Next() {
		var r RuleVersion
		var active int
		if err := rows.Scan(&r.ID, &r.SnapshotID, &r.RulesYAML, &r.CreatedAt, &active); err != nil {
			return nil, err
		}
		r.Active = active != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// AppendAudit writes an audit log entry.
func AppendAudit(db *sql.DB, e AuditEntry) error {
	_, err := db.Exec(
		`INSERT INTO audit_log(actor, action, target, before, after, result, ip)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		e.Actor, e.Action, e.Target, e.Before, e.After, e.Result, e.IP,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetRuleSourceETag returns the stored ETag for a given URL, or ("", nil) if not found.
func GetRuleSourceETag(db *sql.DB, url string) (string, error) {
	var etag sql.NullString
	err := db.QueryRow(`SELECT etag FROM rule_sources WHERE url = ?`, url).Scan(&etag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return etag.String, nil
}

// UpsertRuleSourceETag inserts or updates the etag + last_synced for a URL.
func UpsertRuleSourceETag(db *sql.DB, url, kind, etag string) error {
	_, err := db.Exec(
		`INSERT INTO rule_sources(url, kind, last_synced, etag)
		 VALUES(?, ?, CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(url) DO UPDATE SET last_synced = CURRENT_TIMESTAMP, etag = excluded.etag`,
		url, kind, etag,
	)
	return err
}

// ApplyStatus mirrors an apply_status row. State is one of
// 'submitted' | 'confirmed' | 'rolled_back' | 'failed' (CHECK-constrained).
type ApplyStatus struct {
	ID         int64
	SnapshotID int64
	State      string
	Reason     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// InsertApplyStatus writes a 'submitted' row for a snapshot and returns
// its id. Applier later writes a terminal confirmed/rolled_back/failed state.
func InsertApplyStatus(db *sql.DB, snapshotID int64, reason string) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO apply_status(snapshot_id, state, reason)
		 VALUES(?, 'submitted', ?)`,
		snapshotID, reason,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateApplyStatus flips an existing row's state and reason.
func UpdateApplyStatus(db *sql.DB, id int64, state, reason string) error {
	res, err := db.Exec(
		`UPDATE apply_status
		   SET state = ?, reason = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		state, reason, id,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("update apply_status %d: affected %d rows", id, n)
	}
	return nil
}

// LatestApplyStatus returns the most recently updated apply_status row, or
// (nil, nil) if the table is empty. Powers GET /api/v1/apply/status.
func LatestApplyStatus(db *sql.DB) (*ApplyStatus, error) {
	row := db.QueryRow(
		`SELECT id, snapshot_id, state, COALESCE(reason,''), created_at, updated_at
		   FROM apply_status
		  ORDER BY updated_at DESC, id DESC
		  LIMIT 1`,
	)
	var a ApplyStatus
	if err := row.Scan(&a.ID, &a.SnapshotID, &a.State, &a.Reason, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// ApplyStatusBySnapshot returns the newest lifecycle row for snapshotID.
func ApplyStatusBySnapshot(db *sql.DB, snapshotID int64) (*ApplyStatus, error) {
	row := db.QueryRow(
		`SELECT id, snapshot_id, state, COALESCE(reason,''), created_at, updated_at
		   FROM apply_status
		  WHERE snapshot_id = ?
		  ORDER BY id DESC
		  LIMIT 1`, snapshotID,
	)
	var a ApplyStatus
	if err := row.Scan(&a.ID, &a.SnapshotID, &a.State, &a.Reason, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}
