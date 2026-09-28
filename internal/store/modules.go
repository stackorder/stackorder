package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	v1 "github.com/stackorder/stackorder/api/v1"
)

// Module is a module node known to the server. Git and registry modules
// are identified per pinned ref, as in api/v1; every pinned module also has
// a family row whose key is the ref-less BaseKey. Released versions are
// recorded on the family row. Local modules are their own family.
type Module struct {
	ID        uuid.UUID     `db:"id"`
	Key       string        `db:"source_key"`
	BaseKey   string        `db:"base_key"`
	Kind      v1.ModuleKind `db:"kind"`
	Source    string        `db:"source"`
	Path      string        `db:"path"`
	Ref       string        `db:"ref"`
	CreatedAt time.Time     `db:"created_at"`
}

// Family reports whether the row is the ref-less family of its module.
func (m Module) Family() bool { return m.Key == m.BaseKey }

// ToV1 converts the row to a graph node.
func (m Module) ToV1() v1.Module {
	return v1.Module{Key: m.Key, Kind: m.Kind, Path: m.Path, Source: m.Source, Ref: m.Ref}
}

// ModuleVersion is one released version of a module family.
type ModuleVersion struct {
	ModuleID uuid.UUID `db:"module_id"`
	Version  string    `db:"version"`
	SHA      string    `db:"sha"`
	TaggedAt time.Time `db:"tagged_at"`
}

// ToV1 converts the row for the module page.
func (v ModuleVersion) ToV1() v1.ModuleVersion {
	return v1.ModuleVersion{Version: v.Version, SHA: v.SHA, TaggedAt: v.TaggedAt.UTC()}
}

// ModuleConsumer records that a stack, in the current graph of its
// repository, reaches a module over uses_module edges and pins it at Ref.
// Latest is the newest released version of the module family and Behind
// counts versions tagged after Ref; both are empty when unknown.
type ModuleConsumer struct {
	StackID   uuid.UUID `db:"stack_id"`
	RepoID    int64     `db:"repo_id"`
	Repo      string    `db:"repo"`
	StackKey  string    `db:"stack_key"`
	ModuleID  uuid.UUID `db:"module_id"`
	ModuleKey string    `db:"module_key"`
	Ref       string    `db:"ref"`
	Latest    string    `db:"latest"`
	Behind    int       `db:"behind"`
}

// ToV1 converts the row for the module page.
func (c ModuleConsumer) ToV1() v1.ModuleConsumer {
	return v1.ModuleConsumer{
		StackID:  c.StackID.String(),
		Repo:     c.Repo,
		StackKey: c.StackKey,
		Ref:      c.Ref,
		Behind:   c.Behind,
	}
}

// Consume converts the row for the stack page.
func (c ModuleConsumer) Consume() v1.ModuleConsume {
	return v1.ModuleConsume{ModuleKey: c.ModuleKey, Ref: c.Ref, Latest: c.Latest, Behind: c.Behind}
}

// ModuleFilter narrows ListModules. Zero fields match everything.
type ModuleFilter struct {
	Kind v1.ModuleKind
	// Prefix matches the start of the module key, e.g. "acme/modules//".
	Prefix string
	// FamiliesOnly drops the per-ref rows of pinned modules.
	FamiliesOnly bool
}

const moduleColumns = `id, source_key, base_key, kind, source, path, ref, created_at`

// UpsertModules records modules by key and returns their ids. Pinned
// modules also get their family row.
func (s *Store) UpsertModules(ctx context.Context, modules []v1.Module) (map[string]uuid.UUID, error) {
	var ids map[string]uuid.UUID
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		ids, err = upsertModules(ctx, tx, modules)
		return err
	})
	return ids, wrap("upsert modules", err)
}

func upsertModules(ctx context.Context, tx pgx.Tx, modules []v1.Module) (map[string]uuid.UUID, error) {
	if len(modules) == 0 {
		return map[string]uuid.UUID{}, nil
	}
	sorted := slices.Clone(modules)
	slices.SortFunc(sorted, func(a, b v1.Module) int { return cmp.Compare(a.Key, b.Key) })
	n := len(sorted)
	keys, bases, kinds := make([]string, n), make([]string, n), make([]string, n)
	sources, paths, refs := make([]string, n), make([]string, n), make([]string, n)
	families := map[string]string{}
	for i, m := range sorted {
		if m.Key == "" {
			return nil, fmt.Errorf("module with empty key: %w", ErrInvalid)
		}
		if i > 0 && sorted[i-1].Key == m.Key {
			return nil, fmt.Errorf("duplicate module key %q: %w", m.Key, ErrInvalid)
		}
		base, ref := splitModuleKey(m)
		keys[i], bases[i], kinds[i] = m.Key, base, string(m.Kind)
		sources[i], paths[i], refs[i] = m.Source, m.Path, ref
		if base != m.Key {
			families[base] = string(m.Kind)
		}
	}
	if len(families) > 0 {
		famKeys := make([]string, 0, len(families))
		for k := range families {
			famKeys = append(famKeys, k)
		}
		slices.Sort(famKeys)
		famKinds := make([]string, len(famKeys))
		for i, k := range famKeys {
			famKinds[i] = families[k]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO modules (source_key, base_key, kind)
			SELECT t.key, t.key, t.kind FROM unnest($1::text[], $2::text[]) AS t(key, kind)
			ORDER BY t.key
			ON CONFLICT (source_key) DO NOTHING`, famKeys, famKinds); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO modules (source_key, base_key, kind, source, path, ref)
		SELECT t.key, t.base, t.kind, t.source, t.path, t.ref
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[])
		     AS t(key, base, kind, source, path, ref)
		ORDER BY t.key
		ON CONFLICT (source_key) DO UPDATE SET
			base_key = EXCLUDED.base_key,
			kind = EXCLUDED.kind,
			source = CASE WHEN EXCLUDED.source = '' THEN modules.source ELSE EXCLUDED.source END,
			path = EXCLUDED.path,
			ref = EXCLUDED.ref
		RETURNING source_key, id`,
		keys, bases, kinds, sources, paths, refs)
	if err != nil {
		return nil, err
	}
	return collectKeyIDs(rows)
}

func splitModuleKey(m v1.Module) (base, ref string) {
	if m.Kind == v1.ModuleLocal {
		return m.Key, ""
	}
	if m.Ref != "" && strings.HasSuffix(m.Key, "@"+m.Ref) {
		return strings.TrimSuffix(m.Key, "@"+m.Ref), m.Ref
	}
	if i := strings.LastIndex(m.Key, "@"); i > 0 {
		return m.Key[:i], m.Key[i+1:]
	}
	return m.Key, m.Ref
}

// GetModule returns one module by id.
func (s *Store) GetModule(ctx context.Context, id uuid.UUID) (Module, error) {
	out, err := queryOne[Module](ctx, s.db, `SELECT `+moduleColumns+` FROM modules WHERE id = $1`, id)
	return out, wrap("get module", err)
}

// GetModuleByKey returns one module by its api/v1 key.
func (s *Store) GetModuleByKey(ctx context.Context, key string) (Module, error) {
	out, err := queryOne[Module](ctx, s.db, `SELECT `+moduleColumns+` FROM modules WHERE source_key = $1`, key)
	return out, wrap("get module by key", err)
}

// ListModules returns modules ordered by key.
func (s *Store) ListModules(ctx context.Context, f ModuleFilter) ([]Module, error) {
	out, err := queryAll[Module](ctx, s.db, `
		SELECT `+moduleColumns+` FROM modules
		WHERE ($1::text = '' OR kind = $1::text)
		  AND ($2::text = '' OR starts_with(source_key, $2::text))
		  AND (NOT $3::boolean OR source_key = base_key)
		ORDER BY source_key`, string(f.Kind), f.Prefix, f.FamiliesOnly)
	return out, wrap("list modules", err)
}

// RecordModuleVersion records a released version on the family of the
// given module, creating the family row if needed. A zero taggedAt means
// now. Recording a version again updates its SHA and time.
func (s *Store) RecordModuleVersion(ctx context.Context, moduleID uuid.UUID, version, sha string, taggedAt time.Time) error {
	const op = "record module version"
	if version == "" {
		return invalid(op, "version is required")
	}
	var at *time.Time
	if !taggedAt.IsZero() {
		at = &taggedAt
	}
	tag, err := s.db.Exec(ctx, `
		WITH fam AS (
			INSERT INTO modules (source_key, base_key, kind, path)
			SELECT m.base_key, m.base_key, m.kind, CASE WHEN m.kind = 'local' THEN m.path ELSE '' END
			FROM modules m WHERE m.id = $1
			ON CONFLICT (source_key) DO UPDATE SET base_key = EXCLUDED.base_key
			RETURNING id
		)
		INSERT INTO module_versions (module_id, version, sha, tagged_at)
		SELECT fam.id, $2, $3, COALESCE($4::timestamptz, now()) FROM fam
		ON CONFLICT (module_id, version) DO UPDATE SET sha = EXCLUDED.sha, tagged_at = EXCLUDED.tagged_at`,
		moduleID, version, sha, at)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return notFound(op)
	}
	return nil
}

// ListModuleVersions returns the released versions of the module's family,
// newest first.
func (s *Store) ListModuleVersions(ctx context.Context, moduleID uuid.UUID) ([]ModuleVersion, error) {
	out, err := queryAll[ModuleVersion](ctx, s.db, `
		SELECT v.module_id, v.version, v.sha, v.tagged_at
		FROM modules m
		JOIN modules f ON f.source_key = m.base_key
		JOIN module_versions v ON v.module_id = f.id
		WHERE m.id = $1
		ORDER BY v.tagged_at DESC, v.version DESC`, moduleID)
	return out, wrap("list module versions", err)
}

const consumerSelect = `
	SELECT c.stack_id, c.repo_id, c.repo, c.stack_key, c.module_id, c.module_key, c.ref,
	       COALESCE((
	           SELECT v.version FROM module_versions v JOIN modules f ON f.id = v.module_id
	           WHERE f.source_key = c.base_key
	           ORDER BY v.tagged_at DESC, v.version DESC LIMIT 1), '') AS latest,
	       (SELECT count(*) FROM module_versions v JOIN modules f ON f.id = v.module_id
	        WHERE f.source_key = c.base_key
	          AND v.tagged_at > (
	              SELECT pv.tagged_at FROM module_versions pv
	              WHERE pv.module_id = f.id AND pv.version = c.ref)) AS behind
	FROM c
	ORDER BY c.repo, c.stack_key, c.module_key, c.ref`

// ModuleConsumers lists the stacks that use a module, directly or through
// other modules, in the current graph of every repository: its
// default-branch graph when one is recorded, else its latest graph. For a
// family row the consumers of every pinned ref are included.
func (s *Store) ModuleConsumers(ctx context.Context, moduleID uuid.UUID) ([]ModuleConsumer, error) {
	out, err := queryAll[ModuleConsumer](ctx, s.db, `
		WITH RECURSIVE
		target AS (
			SELECT t.id, t.source_key, t.ref
			FROM modules m
			JOIN modules t ON t.id = m.id OR (m.source_key = m.base_key AND t.base_key = m.base_key)
			WHERE m.id = $1
		),
		latest AS (
			SELECT id, repo_id FROM (
				SELECT r.id AS repo_id, COALESCE(r.default_graph_id, (
					SELECT g.id FROM graphs g WHERE g.repo_id = r.id
					ORDER BY g.created_at DESC, g.id DESC LIMIT 1)) AS id
				FROM repos r
			) current WHERE id IS NOT NULL
		),
		reach AS (
			SELECT e.graph_id, e.from_kind, e.from_key, tg.id AS module_id,
			       COALESCE(NULLIF(e.meta->>'ref', ''), tg.ref) AS ref
			FROM latest l
			JOIN edges e ON e.graph_id = l.id
			JOIN target tg ON tg.source_key = e.to_key
			WHERE e.type = 'uses_module' AND e.to_kind = 'module'
			UNION
			SELECT e.graph_id, e.from_kind, e.from_key, r.module_id, r.ref
			FROM reach r
			JOIN edges e ON e.graph_id = r.graph_id AND e.to_kind = 'module' AND e.to_key = r.from_key
			WHERE r.from_kind = 'module' AND e.type = 'uses_module'
		),
		c AS (
			SELECT DISTINCT s.id AS stack_id, s.repo_id, p.full_name AS repo, s.key AS stack_key,
			       tm.id AS module_id, tm.source_key AS module_key, tm.base_key, r.ref
			FROM reach r
			JOIN latest l ON l.id = r.graph_id
			JOIN stacks s ON s.repo_id = l.repo_id AND s.key = r.from_key
			JOIN repos p ON p.id = s.repo_id
			JOIN modules tm ON tm.id = r.module_id
			WHERE r.from_kind = 'stack'
		)`+consumerSelect, moduleID)
	return out, wrap("module consumers", err)
}

// StackModules lists the modules a stack uses, directly or through other
// modules, in the current graph of its repository (see ModuleConsumers),
// with the ref it pins.
func (s *Store) StackModules(ctx context.Context, stackID uuid.UUID) ([]ModuleConsumer, error) {
	out, err := queryAll[ModuleConsumer](ctx, s.db, `
		WITH RECURSIVE
		st AS (
			SELECT s.id, s.repo_id, s.key, p.full_name FROM stacks s JOIN repos p ON p.id = s.repo_id
			WHERE s.id = $1
		),
		lg AS (
			SELECT COALESCE(p.default_graph_id, (
				SELECT g.id FROM graphs g WHERE g.repo_id = p.id
				ORDER BY g.created_at DESC, g.id DESC LIMIT 1)) AS id
			FROM st JOIN repos p ON p.id = st.repo_id
		),
		down AS (
			SELECT e.graph_id, e.to_key, COALESCE(e.meta->>'ref', '') AS ref
			FROM edges e
			JOIN lg ON lg.id = e.graph_id
			JOIN st ON e.from_kind = 'stack' AND e.from_key = st.key
			WHERE e.type = 'uses_module' AND e.to_kind = 'module'
			UNION
			SELECT e.graph_id, e.to_key, COALESCE(e.meta->>'ref', '')
			FROM down d
			JOIN edges e ON e.graph_id = d.graph_id AND e.from_kind = 'module' AND e.from_key = d.to_key
			WHERE e.type = 'uses_module' AND e.to_kind = 'module'
		),
		c AS (
			SELECT DISTINCT st.id AS stack_id, st.repo_id, st.full_name AS repo, st.key AS stack_key,
			       m.id AS module_id, m.source_key AS module_key, m.base_key,
			       COALESCE(NULLIF(d.ref, ''), m.ref) AS ref
			FROM down d
			JOIN modules m ON m.source_key = d.to_key
			CROSS JOIN st
		)`+consumerSelect, stackID)
	return out, wrap("stack modules", err)
}
