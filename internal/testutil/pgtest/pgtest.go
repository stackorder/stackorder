// Package pgtest gives tests their own Postgres database.
//
// When TEST_DATABASE_URL is set it points at a server the tests may create
// databases on. Otherwise one postgres:17-alpine container is started per
// test binary with testcontainers, and tests fail when Docker is not
// available. Either way every call creates a uniquely named database that is
// dropped when the test ends.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/stackorder/stackorder/internal/store"
)

// EnvDSN names the variable holding an existing server to use instead of a
// container.
const EnvDSN = "TEST_DATABASE_URL"

// Image is the Postgres image started when EnvDSN is unset.
const Image = "postgres:17-alpine"

const opTimeout = 2 * time.Minute

var (
	once      sync.Once
	adminDSN  string
	startErr  error
	container *postgres.PostgresContainer
)

// New returns a migrated store on a fresh database. The store is closed and
// the database dropped when the test ends.
func New(t testing.TB) *store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	s, err := store.Open(ctx, DSN(t))
	if err != nil {
		t.Fatalf("pgtest: open store: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("pgtest: migrate: %v", err)
	}
	return s
}

// DSN creates an empty, unmigrated database and returns its connection
// string. The database is dropped when the test ends.
func DSN(t testing.TB) string {
	t.Helper()
	admin := server(t)
	name := "stackorder_test_" + randomSuffix(t)
	ident := pgx.Identifier{name}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := adminExec(ctx, admin, "CREATE DATABASE "+ident); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		if err := adminExec(ctx, admin, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("pgtest: drop database: %v", err)
		}
	})
	dsn, err := withDatabase(admin, name)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	return dsn
}

// Main runs the tests of a package and then stops the container, if one
// was started. Call it from TestMain.
func Main(m *testing.M) int {
	code := m.Run()
	if container != nil {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "pgtest: terminate container: %v\n", err)
		}
	}
	return code
}

func server(t testing.TB) string {
	t.Helper()
	if dsn := os.Getenv(EnvDSN); dsn != "" {
		return dsn
	}
	once.Do(start)
	if startErr != nil {
		t.Fatalf("pgtest: start %s: %v", Image, startErr)
	}
	return adminDSN
}

func start() {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := dockerHealthy(ctx); err != nil {
		startErr = fmt.Errorf("docker is unavailable and %s is unset: %w", EnvDSN, err)
		return
	}
	c, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("stackorder"),
		postgres.WithUsername("stackorder"),
		postgres.WithPassword("stackorder"),
		testcontainers.WithCmd("postgres",
			"-c", "fsync=off",
			"-c", "synchronous_commit=off",
			"-c", "full_page_writes=off",
			"-c", "max_connections=300"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		if c != nil {
			_ = testcontainers.TerminateContainer(c)
		}
		startErr = err
		return
	}
	container = c
	adminDSN, startErr = c.ConnectionString(ctx, "sslmode=disable")
}

func dockerHealthy(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("docker provider panicked: %v", r)
		}
	}()
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return err
	}
	return p.Health(ctx)
}

func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

func withDatabase(dsn, name string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse %s: %w", EnvDSN, err)
		}
		u.Path = "/" + name
		u.RawPath = ""
		return u.String(), nil
	}
	return dsn + " dbname=" + name, nil
}

func randomSuffix(t testing.TB) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("pgtest: random database name: %v", err)
	}
	return hex.EncodeToString(b)
}
