package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMigrateUsageErrors(t *testing.T) {
	dsn := map[string]string{"DATABASE_URL": "postgres://u:p@localhost:5432/db"}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		code int
		err  string
	}{
		{"status with to", []string{"migrate", "--status", "--to", "3"}, dsn, 1, "--status and --to cannot be combined"},
		{"status with force", []string{"migrate", "--status", "--force", "3"}, dsn, 1, "--status and --force cannot be combined"},
		{"to with force", []string{"migrate", "--to", "3", "--force", "2"}, dsn, 1, "--to and --force cannot be combined"},
		{"negative to", []string{"migrate", "--to", "-1"}, dsn, 1, "must not be negative"},
		{"negative force", []string{"migrate", "--force", "-1"}, dsn, 1, "--force must not be negative"},
		{"zero to", []string{"migrate", "--to", "0"}, dsn, 1, "no migration numbered 0; the latest is"},
		{"unexpected argument", []string{"migrate", "extra"}, dsn, 1, `unexpected argument "extra"`},
		{"unknown flag", []string{"migrate", "--nope"}, dsn, 1, "flag provided but not defined"},
		{"missing database url", []string{"migrate"}, nil, 1, "DATABASE_URL is required"},
		{"missing database url status", []string{"migrate", "--status"}, nil, 1, "DATABASE_URL is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			assert.Equal(t, tt.code, run(tt.args, envOf(tt.env), &stdout, &stderr))
			assert.Contains(t, stderr.String(), tt.err)
		})
	}
}

func TestMigrateHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"migrate", "--help"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stderr.String()+stdout.String(), "usage: stackorder-server migrate")
}

func TestMigrateListedInHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	assert.Equal(t, 0, run([]string{"help"}, envOf(nil), &stdout, &stderr))
	assert.Contains(t, stdout.String(), "migrate")
}
