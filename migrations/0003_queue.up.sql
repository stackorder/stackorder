CREATE TABLE events (
    id          text PRIMARY KEY,
    kind        text NOT NULL,
    payload     jsonb NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    claimed_by  text NOT NULL DEFAULT '',
    claimed_at  timestamptz,
    attempts    integer NOT NULL DEFAULT 0,
    run_after   timestamptz NOT NULL DEFAULT now(),
    done_at     timestamptz,
    last_error  text NOT NULL DEFAULT ''
);

CREATE INDEX events_pending_idx ON events (done_at, run_after);
CREATE INDEX events_received_idx ON events (received_at);

CREATE TABLE jobs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}',
    dedupe_key text UNIQUE,
    run_after  timestamptz NOT NULL DEFAULT now(),
    claimed_by text NOT NULL DEFAULT '',
    claimed_at timestamptz,
    attempts   integer NOT NULL DEFAULT 0,
    done_at    timestamptz,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobs_pending_idx ON jobs (done_at, run_after);
