ALTER TABLE stacks ADD COLUMN state_object jsonb;
ALTER TABLE stacks ADD COLUMN state_object_run_id uuid REFERENCES runs (id) ON DELETE SET NULL;
ALTER TABLE stacks ADD COLUMN state_object_at timestamptz;

CREATE INDEX stacks_state_object_run_idx ON stacks (state_object_run_id) WHERE state_object_run_id IS NOT NULL;
