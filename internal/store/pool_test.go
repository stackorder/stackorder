package store

import "testing"

func TestSetsPoolMaxConns(t *testing.T) {
	tests := []struct {
		dsn  string
		want bool
	}{
		{"postgres://u:p@db:5432/so?sslmode=require&pool_max_conns=20", true},
		{"postgresql://db/so?pool_max_conns=3", true},
		{"postgres://u:p@db:5432/so?sslmode=require", false},
		{"postgres://u:pool_max_conns@db/so", false},
		{"host=db dbname=so pool_max_conns=7", true},
		{"host=db pool_max_conns = 7", true},
		{"host=db pool_max_conns =7", true},
		{"host=db dbname=so sslmode=disable", false},
		{"host=db application_name=pool_max_conns", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := setsPoolMaxConns(tt.dsn); got != tt.want {
			t.Errorf("setsPoolMaxConns(%q) = %v, want %v", tt.dsn, got, tt.want)
		}
	}
}
