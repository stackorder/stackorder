ALTER TABLE api_keys
    ADD COLUMN read_only       boolean     NOT NULL DEFAULT false,
    ADD COLUMN installation_id bigint,
    ADD COLUMN expires_at      timestamptz;
