package config

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Config
		wantErrSubstr []string
	}{
		{
			name: "valid config",
			cfg: Config{
				Telegram: TelegramConfig{Token: "token"},
				LLM:      LLMConfig{APIKey: "apikey"},
				Git:      GitConfig{RepoURL: "git@github.com:org/repo.git"},
			},
		},
		{
			name: "missing telegram token",
			cfg: Config{
				LLM: LLMConfig{APIKey: "apikey"},
				Git: GitConfig{RepoURL: "git@github.com:org/repo.git"},
			},
			wantErrSubstr: []string{"TELEGRAM_BOT_TOKEN"},
		},
		{
			name: "missing llm api key",
			cfg: Config{
				Telegram: TelegramConfig{Token: "token"},
				Git:      GitConfig{RepoURL: "git@github.com:org/repo.git"},
			},
			wantErrSubstr: []string{"LLM_API_KEY"},
		},
		{
			name: "missing git repo",
			cfg: Config{
				Telegram: TelegramConfig{Token: "token"},
				LLM:      LLMConfig{APIKey: "apikey"},
			},
			wantErrSubstr: []string{"git.repo_url"},
		},
		{
			name: "multiple missing fields",
			cfg:  Config{},
			wantErrSubstr: []string{
				"TELEGRAM_BOT_TOKEN",
				"LLM_API_KEY",
				"git.repo_url",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.cfg.Validate()

			if len(tc.wantErrSubstr) == 0 {
				if len(errs) != 0 {
					t.Fatalf("expected no errors, got: %v", errs)
				}
				return
			}

			joined := strings.Join(errs, "\n")
			for _, substr := range tc.wantErrSubstr {
				if !strings.Contains(joined, substr) {
					t.Fatalf("expected error containing %q, got: %v", substr, errs)
				}
			}
		})
	}
}

func TestGetAbsPath(t *testing.T) {
	cfg := &Config{projectDir: "/tmp/project"}

	t.Run("absolute path unchanged", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix absolute paths have different semantics on Windows")
		}
		got := cfg.GetAbsPath("/var/log/app.log")
		if got != "/var/log/app.log" {
			t.Fatalf("unexpected path: got %q", got)
		}
	})

	t.Run("relative path joined", func(t *testing.T) {
		got := cfg.GetAbsPath("logs/app.log")
		want := filepath.Join("/tmp/project", "logs/app.log")
		if got != want {
			t.Fatalf("unexpected path: got %q want %q", got, want)
		}
	})
}
