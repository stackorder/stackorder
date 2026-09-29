//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stackorder/stackorder/internal/testutil/pgtest"
)

var (
	buildOnce sync.Once
	buildDir  string
	cliPath   string
	buildErr  error
)

func TestMain(m *testing.M) {
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":         awsAccessKey,
		"AWS_SECRET_ACCESS_KEY":     awsSecretKey,
		"AWS_REGION":                awsRegion,
		"AWS_EC2_METADATA_DISABLED": "true",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: set %s: %v\n", k, err)
			os.Exit(1)
		}
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_SESSION_TOKEN", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3"} {
		_ = os.Unsetenv(k)
	}
	code := pgtest.Main(m)
	stopLocalStack()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func stackorderBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "stackorder-e2e-")
		if buildErr != nil {
			return
		}
		cliPath = filepath.Join(buildDir, "stackorder")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", cliPath, "github.com/stackorder/stackorder/cmd/stackorder")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build ./cmd/stackorder: %w\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatalf("e2e: %v", buildErr)
	}
	return cliPath
}
