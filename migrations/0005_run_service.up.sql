ALTER TABLE repos ADD COLUMN default_graph_id uuid REFERENCES graphs (id) ON DELETE SET NULL;

ALTER TABLE runs ADD COLUMN check_runs jsonb NOT NULL DEFAULT '{}';

ALTER TABLE dispatches ADD COLUMN chunk integer NOT NULL DEFAULT 0;
ALTER TABLE dispatches ADD COLUMN sent_at timestamptz;
ALTER TABLE dispatches DROP CONSTRAINT dispatches_run_id_wave_environment_mode_key;
ALTER TABLE dispatches ADD CONSTRAINT dispatches_run_id_wave_environment_mode_chunk_key
    UNIQUE (run_id, wave, environment, mode, chunk);

ALTER TABLE run_stacks ADD COLUMN plan_url text NOT NULL DEFAULT '';
ALTER TABLE run_stacks ADD COLUMN plan_output text NOT NULL DEFAULT '';
ALTER TABLE run_stacks ADD COLUMN blocked_by text[] NOT NULL DEFAULT '{}';
ALTER TABLE run_stacks ADD COLUMN dispatch_id uuid REFERENCES dispatches (id) ON DELETE SET NULL;

CREATE INDEX run_stacks_dispatch_idx ON run_stacks (dispatch_id) WHERE dispatch_id IS NOT NULL;
CREATE INDEX audit_action_target_idx ON audit (action, target, at DESC);
CREATE INDEX runs_workflow_run_idx ON runs (repo_id, workflow_run_id) WHERE workflow_run_id <> 0;
