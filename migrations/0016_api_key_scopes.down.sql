ALTER TABLE api_keys
    DROP COLUMN read_only,
    DROP COLUMN installation_id,
    DROP COLUMN expires_at;
