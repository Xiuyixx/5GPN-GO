package api

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	xdb "github.com/Xiuyixx/5GPN-Go/internal/db"
)

// authClaims is the panel JWT payload.
type authClaims struct {
	SessionID string `json:"sid"`
	UserID    int64  `json:"uid"`
	Username  string `json:"usr"`
	jwt.RegisteredClaims
}

// Authenticator issues and validates panel JWTs backed by SQLite sessions.
type Authenticator struct {
	DB        *sql.DB
	Secret    []byte
	TokenTTL  time.Duration
	Issuer    string
}

// Issue mints a JWT and inserts a session row so it can be revoked later.
func (a *Authenticator) Issue(userID int64, username, ip, ua string) (string, string, error) {
	sid := uuid.NewString()
	jti := uuid.NewString()
	now := time.Now()
	exp := now.Add(a.TokenTTL)
	claims := authClaims{
		SessionID: sid,
		UserID:    userID,
		Username:  username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    a.Issuer,
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti,
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(a.Secret)
	if err != nil {
		return "", "", fmt.Errorf("auth: sign token: %w", err)
	}
	if err := xdb.InsertPanelSession(a.DB, xdb.PanelSession{
		ID:        sid,
		UserID:    userID,
		JWTID:     jti,
		IssuedAt:  now,
		ExpiresAt: exp,
		IP:        ip,
		UserAgent: ua,
	}); err != nil {
		return "", "", err
	}
	return signed, sid, nil
}

// Verify parses a JWT and confirms the session is still active in SQLite.
func (a *Authenticator) Verify(tokenString string) (*authClaims, error) {
	tok, err := jwt.ParseWithClaims(tokenString, &authClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method: %v", t.Header["alg"])
		}
		return a.Secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := tok.Claims.(*authClaims)
	if !ok || !tok.Valid {
		return nil, errors.New("auth: invalid token")
	}
	active, err := xdb.IsSessionActive(a.DB, claims.SessionID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, errors.New("auth: session revoked or expired")
	}
	return claims, nil
}

// Revoke marks the session id as revoked (used by /logout).
func (a *Authenticator) Revoke(sessionID string) error {
	return xdb.RevokeSession(a.DB, sessionID)
}

// CheckPassword hashes the plaintext against the stored bcrypt value.
func CheckPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// bcryptCost is the work factor used when hashing new panel passwords.
// Kept as a package variable so tests can lower it to bcrypt.MinCost;
// production callers must not touch it.
var bcryptCost = 12

// HashPassword computes a bcrypt hash at the current cost.
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}
