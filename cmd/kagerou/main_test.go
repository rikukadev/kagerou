package main

import (
	"os"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string // 空なら成功を期待
	}{
		{"no args", nil, "subcommand required"},
		{"version", []string{"version"}, ""},
		{"unknown", []string{"bogus"}, `unknown subcommand "bogus"`},
		{"up requires name", []string{"up"}, "environment name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args, os.Stdout)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("run(%v) = %v, want nil", tc.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("run(%v) = %v, want error containing %q", tc.args, err, tc.wantErr)
			}
		})
	}
}
