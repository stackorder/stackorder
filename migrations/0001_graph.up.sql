CREATE TABLE installations (
    id           bigint PRIMARY KEY,
    account      text NOT NULL,
    account_type text NOT NULL DEFAULT '',
    suspended_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repos (
    id              bigint PRIMARY KEY,
    installation_id bigint NOT NULL REFERENCES installations (id) ON DELETE CASCADE,
    full_name       text NOT NULL UNIQUE,
    default_branch  text NOT NULL DEFAULT 'main',
    config          jsonb,
    config_sha      text NOT NULL DEFAULT '',
    private         boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX repos_installation_idx ON repos (installation_id);
CREATE INDEX repos_full_name_lower_idx ON repos (lower(full_name));

CREATE TABLE graphs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id         bigint NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    sha             text NOT NULL,
    tree_hash       text NOT NULL DEFAULT '',
    warnings        jsonb NOT NULL DEFAULT '[]',
    external_stacks jsonb NOT NULL DEFAULT '[]',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repo_id, sha)
);

CREATE INDEX graphs_repo_created_idx ON graphs (repo_id, created_at DESC);
CREATE INDEX graphs_repo_tree_hash_idx ON graphs (repo_id, tree_hash, created_at DESC);

CREATE TABLE stacks (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id       bigint NOT NULL REFERENCES repos (id) ON DELETE CASCADE,
    key           text NOT NULL,
    path          text NOT NULL,
    workspace     text NOT NULL DEFAULT '',
    backend       jsonb,
    environment   text NOT NULL DEFAULT '',
    tool          text NOT NULL DEFAULT '',
    config        jsonb,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    removed_at    timestamptz,
    UNIQUE (repo_id, key)
);

CREATE TABLE graph_stacks (
    graph_id uuid NOT NULL REFERENCES graphs (id) ON DELETE CASCADE,
    stack_id uuid NOT NULL REFERENCES stacks (id) ON DELETE CASCADE,
    node     jsonb NOT NULL,
    PRIMARY KEY (graph_id, stack_id)
);

CREATE INDEX graph_stacks_stack_idx ON graph_stacks (stack_id);

CREATE TABLE modules (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_key text NOT NULL UNIQUE,
    base_key   text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('local', 'git', 'registry')),
    source     text NOT NULL DEFAULT '',
    path       text NOT NULL DEFAULT '',
    ref        text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX modules_base_key_idx ON modules (base_key);

CREATE TABLE graph_modules (
    graph_id  uuid NOT NULL REFERENCES graphs (id) ON DELETE CASCADE,
    module_id uuid NOT NULL REFERENCES modules (id) ON DELETE CASCADE,
    node      jsonb NOT NULL,
    PRIMARY KEY (graph_id, module_id)
);

CREATE INDEX graph_modules_module_idx ON graph_modules (module_id);

CREATE TABLE edges (
    id        bigserial PRIMARY KEY,
    graph_id  uuid NOT NULL REFERENCES graphs (id) ON DELETE CASCADE,
    from_kind text NOT NULL CHECK (from_kind IN ('stack', 'module')),
    from_key  text NOT NULL,
    to_kind   text NOT NULL CHECK (to_kind IN ('stack', 'module')),
    to_key    text NOT NULL,
    type      text NOT NULL CHECK (type IN ('depends_on', 'uses_module', 'reads_state')),
    inferred  boolean NOT NULL DEFAULT false,
    meta      jsonb
);

CREATE INDEX edges_graph_idx ON edges (graph_id);
CREATE INDEX edges_graph_to_idx ON edges (graph_id, to_kind, to_key);

CREATE TABLE module_versions (
    module_id uuid NOT NULL REFERENCES modules (id) ON DELETE CASCADE,
    version   text NOT NULL,
    sha       text NOT NULL DEFAULT '',
    tagged_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (module_id, version)
);
