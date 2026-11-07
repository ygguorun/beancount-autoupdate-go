package git

import (
	"errors"
	"os"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
)

func TestIsNonFastForwardError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"non-fast-forward update: refs/heads/main", true},
		{"rejected: refs/heads/main (fetch first)", true},
		{"remote error: STALE INFO / internal server error", true},
		{"authentication required", false},
		{"failed to add files: packfile not found", false},
	}

	for _, c := range cases {
		if got := isNonFastForwardError(errors.New(c.msg)); got != c.want {
			t.Errorf("isNonFastForwardError(%q) = %v, want %v", c.msg, got, c.want)
		}
	}

	if isNonFastForwardError(nil) {
		t.Error("isNonFastForwardError(nil) = true, want false")
	}
}

func TestRunWithRepoReopen(t *testing.T) {
	repoPath := t.TempDir()
	if _, err := gogit.PlainInit(repoPath, false); err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	m := &Manager{repoPath: repoPath, repo: repo}

	cases := []struct {
		name      string
		actionErr error
		wantCalls int
		wantErr   bool
	}{
		{"首次成功不重试", nil, 1, false},
		{"packfile not found 触发重开重试", errors.New("failed to push: packfile not found"), 2, true},
		{"其他错误不重试", errors.New("authentication required"), 1, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			err := m.runWithRepoReopen("test", func() error {
				calls++
				return c.actionErr
			})
			if calls != c.wantCalls {
				t.Errorf("calls = %d, want %d", calls, c.wantCalls)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if c.wantErr && c.actionErr != nil && !errors.Is(err, c.actionErr) {
				t.Errorf("err = %v, want wraps %v", err, c.actionErr)
			}
		})
	}
}

func TestRunWithRepoReopenReopenFailure(t *testing.T) {
	// 仓库目录被删除后重开必然失败，错误应同时包含原始 packfile 错误与重开失败信息
	repoPath := t.TempDir()
	repo, err := gogit.PlainInit(repoPath, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	m := &Manager{repoPath: repoPath, repo: repo}

	if err := os.RemoveAll(repoPath); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	err = m.runWithRepoReopen("test", func() error {
		return errors.New("拉取失败: packfile not found")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "packfile not found") {
		t.Errorf("err = %v, want contains original error", err)
	}
	if !strings.Contains(err.Error(), "reopen failed") {
		t.Errorf("err = %v, want mentions reopen failure", err)
	}
}
