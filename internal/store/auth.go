package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	v1 "github.com/stackorder/stackorder/api/v1"
)

const (
	// APIKeyPrefix starts every automation key.
	APIKeyPrefix = "sk_"
	// MaxAPIKeyNameLen is the longest an API key name may be, in characters
	// (Unicode code points), enforced on every path that creates a key.
	MaxAPIKeyNameLen = 100
	apiKeyShown      = 10
	tokenBytes       = 32

	actionAPIKeyCreated = "api_key_created"
	actionAPIKeyRevoked = "api_key_revoked"
)

// SeenJTI records an OIDC token id until exp and reports whether it had
// been recorded before, which is how replayed runner tokens are refused.
func (s *Store) SeenJTI(ctx context.Context, jti string, exp time.Time) (bool, error) {
	const op = "seen jti"
	if jti == "" {
		return false, invalid(op, "jti is required")
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO oidc_jtis (jti, expires_at) VALUES ($1, $2)
		ON CONFLICT (jti) DO NOTHING`, jti, exp)
	if err != nil {
		return false, wrap(op, err)
	}
	return tag.RowsAffected() == 0, nil
}

// PruneJTIs deletes token ids whose tokens have expired and returns how
// many were deleted.
func (s *Store) PruneJTIs(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM oidc_jtis WHERE expires_at < now()`)
	if err != nil {
		return 0, wrap("prune jtis", err)
	}
	return tag.RowsAffected(), nil
}

// Session is a signed-in human. ID is the SHA-256 of the session token;
// the token itself is never stored.
type Session struct {
	ID        string    `db:"id"`
	Login     string    `db:"login"`
	UserID    int64     `db:"user_id"`
	AvatarURL string    `db:"avatar_url"`
	Orgs      []string  `db:"orgs"`
	CreatedAt time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

// ToV1 converts the session for GET /v1/me.
func (s Session) ToV1() v1.Whoami {
	return v1.Whoami{Login: s.Login, AvatarURL: s.AvatarURL, Orgs: nonNil(s.Orgs)}
}

// NewSession describes a session to create.
type NewSession struct {
	Login     string
	UserID    int64
	AvatarURL string
	Orgs      []string
	TTL       time.Duration
}

const sessionCols = `id, login, user_id, avatar_url, orgs, created_at, expires_at`

// CreateSession stores a session and returns its random bearer token.
func (s *Store) CreateSession(ctx context.Context, n NewSession) (string, Session, error) {
	const op = "create session"
	if n.Login == "" || n.TTL <= 0 {
		return "", Session{}, invalid(op, "login and a positive ttl are required")
	}
	token, err := randomToken()
	if err != nil {
		return "", Session{}, wrap(op, err)
	}
	out, err := queryOne[Session](ctx, s.db, `
		INSERT INTO sessions (id, login, user_id, avatar_url, orgs, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + $6::bigint * interval '1 microsecond')
		RETURNING `+sessionCols,
		hashToken(token), n.Login, n.UserID, n.AvatarURL, nonNil(n.Orgs), micros(n.TTL))
	if err != nil {
		return "", Session{}, wrap(op, err)
	}
	return token, out, nil
}

// GetSession returns the unexpired session of a token, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, token string) (Session, error) {
	out, err := queryOne[Session](ctx, s.db, `
		SELECT `+sessionCols+` FROM sessions WHERE id = $1 AND expires_at > now()`, hashToken(token))
	return out, wrap("get session", err)
}

// DeleteSession removes the session of a token. Unknown tokens are not an
// error.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, hashToken(token))
	return wrap("delete session", err)
}

// PruneSessions deletes expired sessions and returns how many were deleted.
func (s *Store) PruneSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, wrap("prune sessions", err)
	}
	return tag.RowsAffected(), nil
}

// APIKey is an automation key. Only the SHA-256 of the key is stored;
// Prefix is its first characters, for display.
type APIKey struct {
	ID         uuid.UUID  `db:"id"`
	Name       string     `db:"name"`
	Prefix     string     `db:"prefix"`
	CreatedBy  string     `db:"created_by"`
	CreatedAt  time.Time  `db:"created_at"`
	LastUsedAt *time.Time `db:"last_used_at"`
	RevokedAt  *time.Time `db:"revoked_at"`
}

const keyColumns = `id, name, prefix, created_by, created_at, last_used_at, revoked_at`

// ToV1 converts the key's metadata for the management API; it never carries
// the secret, which exists only at creation.
func (k APIKey) ToV1() v1.APIKey {
	return v1.APIKey{
		ID:         k.ID.String(),
		Name:       k.Name,
		Prefix:     k.Prefix,
		CreatedBy:  k.CreatedBy,
		CreatedAt:  k.CreatedAt.UTC(),
		LastUsedAt: utc(k.LastUsedAt),
		RevokedAt:  utc(k.RevokedAt),
	}
}

// CreateAPIKey creates a key and records an api_key_created audit entry in the
// same transaction, so a usable key never exists without its audit record. It
// returns the key's plaintext, which starts with APIKeyPrefix and is shown
// once. createdBy is the key's creator and the actor of the audit entry.
func (s *Store) CreateAPIKey(ctx context.Context, name, createdBy string) (string, APIKey, error) {
	const op = "create api key"
	switch {
	case name == "":
		return "", APIKey{}, invalid(op, "name is required")
	case utf8.RuneCountInString(name) > MaxAPIKeyNameLen:
		return "", APIKey{}, invalid(op, fmt.Sprintf("name must be at most %d characters", MaxAPIKeyNameLen))
	}
	token, err := randomToken()
	if err != nil {
		return "", APIKey{}, wrap(op, err)
	}
	plaintext := APIKeyPrefix + token
	var out APIKey
	err = s.InTx(ctx, func(tx *Store) error {
		created, err := queryOne[APIKey](ctx, tx.db, `
			INSERT INTO api_keys (name, key_hash, prefix, created_by) VALUES ($1, $2, $3, $4)
			RETURNING `+keyColumns, name, hashToken(plaintext), plaintext[:apiKeyShown], createdBy)
		if err != nil {
			return wrap(op, err)
		}
		out = created
		_, err = tx.RecordAudit(ctx, keyAudit(createdBy, actionAPIKeyCreated, created))
		return err
	})
	if err != nil {
		return "", APIKey{}, err
	}
	return plaintext, out, nil
}

func keyAudit(actor, action string, key APIKey) AuditEntry {
	return AuditEntry{
		Actor:   actor,
		Action:  action,
		Target:  key.Name,
		Details: map[string]any{"key_id": key.ID.String(), "prefix": key.Prefix},
	}
}

// GetAPIKey returns the key with id, revoked or not, or ErrNotFound.
func (s *Store) GetAPIKey(ctx context.Context, id uuid.UUID) (APIKey, error) {
	out, err := queryOne[APIKey](ctx, s.db, `SELECT `+keyColumns+` FROM api_keys WHERE id = $1`, id)
	return out, wrap("get api key", err)
}

// VerifyAPIKey returns the unrevoked key matching plaintext and stamps its
// last use. Unknown, malformed and revoked keys return ErrNotFound.
func (s *Store) VerifyAPIKey(ctx context.Context, plaintext string) (APIKey, error) {
	const op = "verify api key"
	if !strings.HasPrefix(plaintext, APIKeyPrefix) || len(plaintext) <= len(APIKeyPrefix) {
		return APIKey{}, notFound(op)
	}
	out, err := queryOne[APIKey](ctx, s.db, `
		UPDATE api_keys SET last_used_at = now()
		WHERE key_hash = $1 AND revoked_at IS NULL
		RETURNING `+keyColumns, hashToken(plaintext))
	return out, wrap(op, err)
}

// RevokeAPIKey revokes a key and returns its metadata. Only the call that
// actually revokes the key records an api_key_revoked audit entry, in the same
// transaction as the revocation; revoking an already-revoked key keeps the
// first revocation time and records no further entry. actor is the audit
// actor. An unknown id is ErrNotFound.
func (s *Store) RevokeAPIKey(ctx context.Context, id uuid.UUID, actor string) (APIKey, error) {
	const op = "revoke api key"
	var out APIKey
	err := s.InTx(ctx, func(tx *Store) error {
		var revokedAt *time.Time
		if err := tx.db.QueryRow(ctx,
			`SELECT revoked_at FROM api_keys WHERE id = $1 FOR UPDATE`, id).Scan(&revokedAt); err != nil {
			return wrap(op, err)
		}
		updated, err := queryOne[APIKey](ctx, tx.db, `
			UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1
			RETURNING `+keyColumns, id)
		if err != nil {
			return wrap(op, err)
		}
		out = updated
		if revokedAt != nil {
			return nil
		}
		_, err = tx.RecordAudit(ctx, keyAudit(actor, actionAPIKeyRevoked, updated))
		return err
	})
	if err != nil {
		return APIKey{}, err
	}
	return out, nil
}

// ListAPIKeys returns every key, revoked ones included, newest first.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	out, err := queryAll[APIKey](ctx, s.db, `SELECT `+keyColumns+` FROM api_keys ORDER BY created_at DESC, id`)
	return out, wrap("list api keys", err)
}

func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
