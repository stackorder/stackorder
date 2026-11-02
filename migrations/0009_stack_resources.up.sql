CREATE TABLE stack_resources (
    stack_id    uuid PRIMARY KEY REFERENCES stacks (id) ON DELETE CASCADE,
    run_id      uuid REFERENCES runs (id) ON DELETE SET NULL,
    run_mode    text NOT NULL CHECK (run_mode IN ('apply', 'drift')),
    sha         text NOT NULL,
    observed_at timestamptz NOT NULL,
    reported_at timestamptz NOT NULL DEFAULT now(),
    changed_at  timestamptz NOT NULL DEFAULT now(),
    total       integer NOT NULL,
    truncated   boolean NOT NULL DEFAULT false,
    digest      bytea NOT NULL,
    resources   jsonb NOT NULL
);

CREATE INDEX stack_resources_run_idx ON stack_resources (run_id) WHERE run_id IS NOT NULL;
CREATE INDEX run_stacks_applied_idx ON run_stacks (stack_id) WHERE mode = 'apply' AND status = 'applied';
