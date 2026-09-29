package store

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

type graphRow struct {
	ID             uuid.UUID  `db:"id"`
	RepoID         int64      `db:"repo_id"`
	Repo           string     `db:"repo"`
	SHA            string     `db:"sha"`
	TreeHash       string     `db:"tree_hash"`
	Warnings       []string   `db:"warnings"`
	ExternalStacks []v1.Stack `db:"external_stacks"`
	CreatedAt      time.Time  `db:"created_at"`
}

type edgeRow struct {
	FromKind v1.NodeKind       `db:"from_kind"`
	FromKey  string            `db:"from_key"`
	ToKind   v1.NodeKind       `db:"to_kind"`
	ToKey    string            `db:"to_key"`
	Type     v1.EdgeType       `db:"type"`
	Inferred bool              `db:"inferred"`
	Meta     map[string]string `db:"meta"`
}

const graphSelect = `
	SELECT g.id, g.repo_id, r.full_name AS repo, g.sha, g.tree_hash, g.warnings,
	       g.external_stacks, g.created_at
	FROM graphs g JOIN repos r ON r.id = g.repo_id`

// SaveGraph stores the graph of a repository at one commit in a single
// transaction. Stacks are upserted by key so their ids stay stable across
// commits (last_seen_at is touched and removed_at cleared), modules are
// upserted by key, and the graph's membership rows and edges are written.
// Saving the same repository and SHA again replaces the stored graph. Stacks
// marked External, or belonging to another repository, are kept with the
// graph only and get no stack id. The returned map holds the id of every
// local stack by key. The graph keeps each node as uploaded, while a stack
// row with no environment gets v1.DefaultEnvironment.
func (s *Store) SaveGraph(ctx context.Context, repoID int64, g *v1.Graph) (uuid.UUID, map[string]uuid.UUID, error) {
	const op = "save graph"
	if g == nil || g.SHA == "" {
		return uuid.Nil, nil, invalid(op, "graph with a sha is required")
	}
	var (
		graphID  uuid.UUID
		stackIDs map[string]uuid.UUID
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var fullName string
		if err := tx.QueryRow(ctx, `SELECT full_name FROM repos WHERE id = $1`, repoID).Scan(&fullName); err != nil {
			return err
		}
		local, external, err := splitStacks(fullName, g.Stacks)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO graphs (repo_id, sha, tree_hash, warnings, external_stacks)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (repo_id, sha) DO UPDATE SET
				tree_hash = EXCLUDED.tree_hash,
				warnings = EXCLUDED.warnings,
				external_stacks = EXCLUDED.external_stacks
			RETURNING id`,
			repoID, g.SHA, g.TreeHash, nonNil(g.Warnings), nonNil(external)).Scan(&graphID); err != nil {
			return err
		}
		if err := clearGraph(ctx, tx, graphID); err != nil {
			return err
		}
		if stackIDs, err = upsertStacks(ctx, tx, repoID, local); err != nil {
			return err
		}
		if err := insertGraphStacks(ctx, tx, graphID, local, stackIDs); err != nil {
			return err
		}
		moduleIDs, err := upsertModules(ctx, tx, g.Modules)
		if err != nil {
			return err
		}
		if err := insertGraphModules(ctx, tx, graphID, g.Modules, moduleIDs); err != nil {
			return err
		}
		return copyEdges(ctx, tx, graphID, g.Edges)
	})
	if err != nil {
		return uuid.Nil, nil, wrap(op, err)
	}
	return graphID, stackIDs, nil
}

func withInstance(st v1.Stack) v1.Stack {
	if st.Instance == "" {
		_, key := v1.SplitQualifiedStackKey(st.Key)
		st.Instance = KeyInstance(key, st.Path)
	}
	return st
}

func splitStacks(repo string, stacks []v1.Stack) (local, external []v1.Stack, err error) {
	seen := make(map[string]bool, len(stacks))
	for _, st := range stacks {
		if st.Key == "" {
			instance := st.Instance
			if instance == "" && st.Workspace != "default" {
				instance = st.Workspace
			}
			st.Key = v1.StackKey(st.Path, instance)
		}
		st = withInstance(st)
		if st.Key == "" {
			return nil, nil, fmt.Errorf("stack with empty key and path: %w", ErrInvalid)
		}
		if st.External || (st.Repo != "" && !strings.EqualFold(st.Repo, repo)) {
			external = append(external, st)
			continue
		}
		if seen[st.Key] {
			return nil, nil, fmt.Errorf("duplicate stack key %q: %w", st.Key, ErrInvalid)
		}
		seen[st.Key] = true
		local = append(local, st)
	}
	slices.SortFunc(local, func(a, b v1.Stack) int { return cmp.Compare(a.Key, b.Key) })
	return local, external, nil
}

func clearGraph(ctx context.Context, tx pgx.Tx, graphID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM edges WHERE graph_id = $1`, graphID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM graph_stacks WHERE graph_id = $1`, graphID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM graph_modules WHERE graph_id = $1`, graphID)
	return err
}

func upsertStacks(ctx context.Context, tx pgx.Tx, repoID int64, stacks []v1.Stack) (map[string]uuid.UUID, error) {
	n := len(stacks)
	keys, paths, workspaces := make([]string, n), make([]string, n), make([]string, n)
	envs, tools := make([]string, n), make([]string, n)
	backends, configs := make([]*string, n), make([]*string, n)
	for i, st := range stacks {
		keys[i], paths[i], workspaces[i] = st.Key, st.Path, st.Workspace
		envs[i], tools[i] = environmentOrDefault(st.Environment), string(st.Tool)
		var err error
		if backends[i], err = jsonText(st.Backend); err != nil {
			return nil, err
		}
		if configs[i], err = jsonText(st.Config); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO stacks (repo_id, key, path, workspace, backend, environment, tool, config)
		SELECT $1, t.key, t.path, t.workspace, t.backend::jsonb, t.environment, t.tool, t.config::jsonb
		FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
		     AS t(key, path, workspace, backend, environment, tool, config)
		ORDER BY t.key
		ON CONFLICT (repo_id, key) DO UPDATE SET
			path = EXCLUDED.path,
			workspace = EXCLUDED.workspace,
			backend = EXCLUDED.backend,
			environment = EXCLUDED.environment,
			tool = EXCLUDED.tool,
			config = EXCLUDED.config,
			last_seen_at = now(),
			removed_at = NULL
		RETURNING key, id`,
		repoID, keys, paths, workspaces, backends, envs, tools, configs)
	if err != nil {
		return nil, err
	}
	return collectKeyIDs(rows)
}

func insertGraphStacks(ctx context.Context, tx pgx.Tx, graphID uuid.UUID, stacks []v1.Stack, ids map[string]uuid.UUID) error {
	stackIDs := make([]uuid.UUID, len(stacks))
	nodes := make([]string, len(stacks))
	for i, st := range stacks {
		stackIDs[i] = ids[st.Key]
		b, err := json.Marshal(st)
		if err != nil {
			return err
		}
		nodes[i] = string(b)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO graph_stacks (graph_id, stack_id, node)
		SELECT $1, t.stack_id, t.node::jsonb FROM unnest($2::uuid[], $3::text[]) AS t(stack_id, node)`,
		graphID, stackIDs, nodes)
	return err
}

func insertGraphModules(ctx context.Context, tx pgx.Tx, graphID uuid.UUID, modules []v1.Module, ids map[string]uuid.UUID) error {
	moduleIDs := make([]uuid.UUID, len(modules))
	nodes := make([]string, len(modules))
	for i, m := range modules {
		moduleIDs[i] = ids[m.Key]
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		nodes[i] = string(b)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO graph_modules (graph_id, module_id, node)
		SELECT $1, t.module_id, t.node::jsonb FROM unnest($2::uuid[], $3::text[]) AS t(module_id, node)`,
		graphID, moduleIDs, nodes)
	return err
}

func copyEdges(ctx context.Context, tx pgx.Tx, graphID uuid.UUID, edges []v1.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	rows := make([][]any, len(edges))
	for i, e := range edges {
		var meta any
		if len(e.Meta) > 0 {
			meta = e.Meta
		}
		rows[i] = []any{graphID, string(e.From.Kind), e.From.Key, string(e.To.Kind), e.To.Key, string(e.Type), e.Inferred, meta}
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"edges"},
		[]string{"graph_id", "from_kind", "from_key", "to_kind", "to_key", "type", "inferred", "meta"},
		pgx.CopyFromRows(rows))
	return err
}

// GetGraph returns the graph stored for a repository at a commit, and its
// id.
func (s *Store) GetGraph(ctx context.Context, repoID int64, sha string) (*v1.Graph, uuid.UUID, error) {
	return s.loadGraph(ctx, "get graph", graphSelect+` WHERE g.repo_id = $1 AND g.sha = $2`, repoID, sha)
}

// GetGraphByID returns a graph by id, such as the one a run was resolved
// against.
func (s *Store) GetGraphByID(ctx context.Context, id uuid.UUID) (*v1.Graph, error) {
	g, _, err := s.loadGraph(ctx, "get graph by id", graphSelect+` WHERE g.id = $1`, id)
	return g, err
}

// LatestGraph returns the most recently created graph of a repository.
func (s *Store) LatestGraph(ctx context.Context, repoID int64) (*v1.Graph, uuid.UUID, error) {
	return s.loadGraph(ctx, "latest graph", graphSelect+`
		WHERE g.repo_id = $1 ORDER BY g.created_at DESC, g.id DESC LIMIT 1`, repoID)
}

// FindGraphByTreeHash returns the most recent graph of a repository scanned
// from the same tree, which lets a resolve on an unchanged tree reuse it.
func (s *Store) FindGraphByTreeHash(ctx context.Context, repoID int64, treeHash string) (*v1.Graph, uuid.UUID, error) {
	if treeHash == "" {
		return nil, uuid.Nil, notFound("find graph by tree hash")
	}
	return s.loadGraph(ctx, "find graph by tree hash", graphSelect+`
		WHERE g.repo_id = $1 AND g.tree_hash = $2 ORDER BY g.created_at DESC, g.id DESC LIMIT 1`,
		repoID, treeHash)
}

// GraphSHAsWithPrefix returns, in order, up to limit commit SHAs of the
// graphs stored for a repository that start with prefix. An empty prefix
// or a limit below one returns nothing.
func (s *Store) GraphSHAsWithPrefix(ctx context.Context, repoID int64, prefix string, limit int) ([]string, error) {
	const op = "graph shas with prefix"
	if prefix == "" || limit < 1 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT sha FROM graphs WHERE repo_id = $1 AND starts_with(sha, $2)
		ORDER BY sha LIMIT $3`, repoID, prefix, limit)
	if err != nil {
		return nil, wrap(op, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return out, wrap(op, err)
}

// GraphStackIDs maps the key of every local stack of a graph to its id.
func (s *Store) GraphStackIDs(ctx context.Context, graphID uuid.UUID) (map[string]uuid.UUID, error) {
	rows, err := s.db.Query(ctx, `
		SELECT s.key, s.id FROM graph_stacks gs JOIN stacks s ON s.id = gs.stack_id
		WHERE gs.graph_id = $1`, graphID)
	if err != nil {
		return nil, wrap("graph stack ids", err)
	}
	ids, err := collectKeyIDs(rows)
	return ids, wrap("graph stack ids", err)
}

func (s *Store) loadGraph(ctx context.Context, op, query string, args ...any) (*v1.Graph, uuid.UUID, error) {
	row, err := queryOne[graphRow](ctx, s.db, query, args...)
	if err != nil {
		return nil, uuid.Nil, wrap(op, err)
	}
	g := &v1.Graph{Repo: row.Repo, SHA: row.SHA, TreeHash: row.TreeHash}
	if len(row.Warnings) > 0 {
		g.Warnings = row.Warnings
	}
	stacks, err := queryNodes[v1.Stack](ctx, s.db, `SELECT node FROM graph_stacks WHERE graph_id = $1`, row.ID)
	if err != nil {
		return nil, uuid.Nil, wrap(op, err)
	}
	for i := range stacks {
		stacks[i] = withInstance(stacks[i])
	}
	for i := range row.ExternalStacks {
		row.ExternalStacks[i] = withInstance(row.ExternalStacks[i])
	}
	slices.SortFunc(stacks, func(a, b v1.Stack) int { return cmp.Compare(a.Key, b.Key) })
	g.Stacks = slices.Concat(stacks, row.ExternalStacks)
	if g.Modules, err = queryNodes[v1.Module](ctx, s.db, `SELECT node FROM graph_modules WHERE graph_id = $1`, row.ID); err != nil {
		return nil, uuid.Nil, wrap(op, err)
	}
	slices.SortFunc(g.Modules, func(a, b v1.Module) int { return cmp.Compare(a.Key, b.Key) })
	edges, err := queryAll[edgeRow](ctx, s.db, `
		SELECT from_kind, from_key, to_kind, to_key, type, inferred, meta
		FROM edges WHERE graph_id = $1 ORDER BY id`, row.ID)
	if err != nil {
		return nil, uuid.Nil, wrap(op, err)
	}
	g.Edges = make([]v1.Edge, len(edges))
	for i, e := range edges {
		g.Edges[i] = v1.Edge{
			From:     v1.NodeRef{Kind: e.FromKind, Key: e.FromKey},
			To:       v1.NodeRef{Kind: e.ToKind, Key: e.ToKey},
			Type:     e.Type,
			Inferred: e.Inferred,
			Meta:     e.Meta,
		}
	}
	return g, row.ID, nil
}

func queryNodes[T any](ctx context.Context, db dbtx, query string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[T])
	return nonNil(out), err
}

func collectKeyIDs(rows pgx.Rows) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	var (
		key string
		id  uuid.UUID
	)
	_, err := pgx.ForEachRow(rows, []any{&key, &id}, func() error {
		out[key] = id
		return nil
	})
	return out, err
}

func jsonText[T any](v *T) (*string, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// ExternalDependent is an ordering edge from a stack in another repository
// to a stack of the repository asked about.
type ExternalDependent struct {
	// RepoID and Repo identify the dependent stack's repository.
	RepoID int64  `db:"repo_id"`
	Repo   string `db:"repo"`
	// FromKey is the dependent stack's key inside Repo.
	FromKey string `db:"from_key"`
	// ToKey is the key, inside the repository asked about, of the stack it
	// depends on.
	ToKey string      `db:"to_key"`
	Type  v1.EdgeType `db:"type"`
}

// ExternalDependents lists the depends_on and reads_state edges that stacks
// in other repositories of the same installation have to stacks of
// repoID, read from each such repository's default-branch graph, or its
// latest graph when no default is known. They are ordered by repository,
// dependent key and target key.
func (s *Store) ExternalDependents(ctx context.Context, repoID int64) ([]ExternalDependent, error) {
	out, err := queryAll[ExternalDependent](ctx, s.db, `
		WITH target AS (
			SELECT full_name, installation_id FROM repos WHERE id = $1
		),
		current AS (
			SELECT r.id AS repo_id, r.full_name,
			       COALESCE(r.default_graph_id, (
			           SELECT g.id FROM graphs g WHERE g.repo_id = r.id
			           ORDER BY g.created_at DESC, g.id DESC LIMIT 1)) AS graph_id
			FROM repos r JOIN target t ON t.installation_id = r.installation_id
			WHERE r.id <> $1
		)
		SELECT c.repo_id, c.full_name AS repo, e.from_key,
		       substr(e.to_key, length(t.full_name) + 3) AS to_key, e.type
		FROM current c
		CROSS JOIN target t
		JOIN edges e ON e.graph_id = c.graph_id
		WHERE e.from_kind = 'stack' AND e.to_kind = 'stack'
		  AND e.type IN ('depends_on', 'reads_state')
		  AND lower(left(e.to_key, length(t.full_name) + 2)) = lower(t.full_name || '//')
		ORDER BY c.full_name, e.from_key, to_key`, repoID)
	return out, wrap("external dependents", err)
}
