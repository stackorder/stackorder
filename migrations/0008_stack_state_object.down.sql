DROP INDEX IF EXISTS stacks_state_object_run_idx;

ALTER TABLE stacks DROP COLUMN state_object_at;
ALTER TABLE stacks DROP COLUMN state_object_run_id;
ALTER TABLE stacks DROP COLUMN state_object;
