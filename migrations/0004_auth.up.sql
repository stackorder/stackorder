CREATE TABLE oidc_jtis (
    jti        text PRIMARY KEY,
    expires_at timestamptz NOT NULL
);

CREATE INDEX oidc_jtis_expires_idx ON oidc_jtis (expires_at);

CREATE TABLE sessions (
    id         text PRIMARY KEY,
    login      text NOT NULL,
    user_id    bigint NOT NULL,
    avatar_url text NOT NULL DEFAULT '',
    orgs       jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    prefix       text NOT NULL,
    created_by   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);

CREATE TABLE audit (
    id      bigserial PRIMARY KEY,
    at      timestamptz NOT NULL DEFAULT now(),
    actor   text NOT NULL,
    action  text NOT NULL,
    target  text NOT NULL DEFAULT '',
    details jsonb
);

CREATE INDEX audit_at_idx ON audit (at DESC);
