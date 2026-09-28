DROP INDEX IF EXISTS runs_workflow_run_idx;
DROP INDEX IF EXISTS audit_action_target_idx;
DROP INDEX IF EXISTS run_stacks_dispatch_idx;

ALTER TABLE run_stacks DROP COLUMN dispatch_id;
ALTER TABLE run_stacks DROP COLUMN blocked_by;
ALTER TABLE run_stacks DROP COLUMN plan_output;
ALTER TABLE run_stacks DROP COLUMN plan_url;

ALTER TABLE dispatches DROP CONSTRAINT dispatches_run_id_wave_environment_mode_chunk_key;
DELETE FROM dispatches WHERE chunk <> 0;
ALTER TABLE dispatches ADD CONSTRAINT dispatches_run_id_wave_environment_mode_key
    UNIQUE (run_id, wave, environment, mode);
ALTER TABLE dispatches DROP COLUMN sent_at;
ALTER TABLE dispatches DROP COLUMN chunk;

ALTER TABLE runs DROP COLUMN check_runs;

ALTER TABLE repos DROP COLUMN default_graph_id;
