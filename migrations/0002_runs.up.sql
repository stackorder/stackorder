CREATE TABLE runs (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id              bigint NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    sha                  text NOT NULL,
    base_sha             text NOT NULL DEFAULT '',
    pr_number            integer NOT NULL DEFAULT 0,
    trigger              text NOT NULL
        CHECK (trigger IN ('pull_request', 'comment', 'push', 'schedule', 'rerequest', 'manual')),
    mode                 text NOT NULL CHECK (mode IN ('plan', 'apply', 'drift')),
    status               text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'planning', 'planned', 'applying', 'applied', 'failed', 'unconfirmed', 'superseded')),
    requested_by         text NOT NULL DEFAULT '',
    graph_id             uuid REFERENCES graphs (id) ON DELETE SET NULL,
    waves                integer NOT NULL DEFAULT 0,
    current_wave         integer NOT NULL DEFAULT 0,
    warnings             jsonb NOT NULL DEFAULT '[]',
    workflow_run_id      bigint NOT NULL DEFAULT 0,
    workflow_run_attempt integer NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    started_at           timestamptz,
    finished_at          timestamptz
);

CREATE INDEX runs_repo_pr_idx ON runs (repo_id, pr_number);
CREATE INDEX runs_repo_sha_idx ON runs (repo_id, sha);
CREATE INDEX runs_created_idx ON runs (created_at DESC, id DESC);

CREATE TABLE run_stacks (
    run_id              uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    stack_id            uuid NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
    wave                integer NOT NULL DEFAULT 0,
    mode                text NOT NULL DEFAULT 'plan' CHECK (mode IN ('plan', 'apply', 'drift')),
    status              text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'planning', 'planned', 'applying', 'applied', 'failed', 'blocked', 'noop', 'unconfirmed', 'unknown', 'skipped')),
    reasons             text[] NOT NULL DEFAULT '{}',
    environment         text NOT NULL DEFAULT '',
    adds                integer NOT NULL DEFAULT 0,
    changes             integer NOT NULL DEFAULT 0,
    destroys            integer NOT NULL DEFAULT 0,
    replaces            integer NOT NULL DEFAULT 0,
    has_changes         boolean NOT NULL DEFAULT false,
    exit_code           integer,
    job_url             text NOT NULL DEFAULT '',
    plan_artifact       text NOT NULL DEFAULT '',
    plan_run_id         bigint NOT NULL DEFAULT 0,
    summary             jsonb,
    plan_text           text,
    plan_text_truncated boolean NOT NULL DEFAULT false,
    error_text          text NOT NULL DEFAULT '',
    started_at          timestamptz,
    finished_at         timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stack_id)
);

CREATE INDEX run_stacks_stack_idx ON run_stacks (stack_id);
CREATE INDEX run_stacks_plan_text_idx ON run_stacks (updated_at) WHERE plan_text IS NOT NULL;

CREATE TABLE checks (
    run_id      uuid NOT NULL,
    stack_id    uuid NOT NULL,
    name        text NOT NULL,
    status      text NOT NULL CHECK (status IN ('pass', 'fail', 'warn')),
    summary     text NOT NULL DEFAULT '',
    details     text NOT NULL DEFAULT '',
    details_url text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stack_id, name),
    FOREIGN KEY (run_id, stack_id) REFERENCES run_stacks (run_id, stack_id) ON DELETE CASCADE
);

CREATE TABLE dispatches (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    wave            integer NOT NULL,
    environment     text NOT NULL,
    mode            text NOT NULL CHECK (mode IN ('plan', 'apply', 'drift')),
    workflow_run_id bigint,
    dispatched_at   timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz,
    conclusion      text NOT NULL DEFAULT '',
    UNIQUE (run_id, wave, environment, mode)
);

CREATE UNIQUE INDEX dispatches_workflow_run_idx ON dispatches (workflow_run_id) WHERE workflow_run_id IS NOT NULL;
CREATE INDEX dispatches_open_idx ON dispatches (dispatched_at) WHERE completed_at IS NULL;

CREATE TABLE locks (
    stack_id  uuid PRIMARY KEY REFERENCES stacks (id) ON DELETE CASCADE,
    run_id    uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    pr_number integer NOT NULL DEFAULT 0,
    taken_at  timestamptz NOT NULL DEFAULT now(),
    reason    text NOT NULL DEFAULT ''
);

CREATE INDEX locks_run_idx ON locks (run_id);

CREATE TABLE drift (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id     uuid NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
    run_id       uuid REFERENCES runs (id) ON DELETE SET NULL,
    checked_at   timestamptz NOT NULL DEFAULT now(),
    drifted      boolean NOT NULL,
    summary      jsonb,
    issue_number integer NOT NULL DEFAULT 0
);

CREATE INDEX drift_stack_checked_idx ON drift (stack_id, checked_at DESC);
