CREATE TABLE object_deletions (
    run_id      uuid NOT NULL,
    stack_key   text NOT NULL,
    enqueued_at timestamptz NOT NULL DEFAULT now(),
    attempts    integer NOT NULL DEFAULT 0,
    last_error  text NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, stack_key)
);

CREATE INDEX object_deletions_enqueued_idx ON object_deletions (enqueued_at);
