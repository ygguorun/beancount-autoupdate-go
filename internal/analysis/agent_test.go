package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunBeanQueryTool(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			"bean-query /tmp/main.bean SELECT account, sum(position) GROUP BY account": "ok",
		},
		errors: map[string]error{},
	}

	svc := NewService(Options{
		Enabled:      true,
		AgentEnabled: true,
		LLMAPIKey:    "test-key",
		LLMModel:     "test-model",
		BeanQueryBin: "bean-query",
		LedgerFile:   "/tmp/main.bean",
		Runner:       runner,
		Timeout:      3 * time.Second,
		MaxToolCalls: 2,
		SessionTTL:   10 * time.Minute,
	})

	output, cmd, err := svc.runBeanQueryTool(context.Background(), beanQueryToolArgs{Query: "SELECT account, sum(position) GROUP BY account"})
	if err != nil {
		t.Fatalf("runBeanQueryTool failed: %v", err)
	}
	if strings.TrimSpace(output) != "ok" {
		t.Fatalf("unexpected output: %q", output)
	}
	if !strings.HasPrefix(cmd, "bean-query ") {
		t.Fatalf("unexpected command: %s", cmd)
	}
}

func TestRunBeanCheckTool(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			"bean-check /tmp/main.bean": "0 errors",
		},
		errors: map[string]error{},
	}

	svc := NewService(Options{
		Enabled:      true,
		AgentEnabled: true,
		LLMAPIKey:    "test-key",
		LLMModel:     "test-model",
		LedgerFile:   "/tmp/main.bean",
		Runner:       runner,
		Timeout:      3 * time.Second,
	})

	output, cmd, err := svc.runBeanCheckTool(context.Background())
	if err != nil {
		t.Fatalf("runBeanCheckTool failed: %v", err)
	}
	if !strings.Contains(output, "0 errors") {
		t.Fatalf("unexpected output: %q", output)
	}
	if cmd != "bean-check /tmp/main.bean" {
		t.Fatalf("unexpected command: %s", cmd)
	}
}

func TestRunQuickAnalysisTool(t *testing.T) {
	tempDir := t.TempDir()
	pythonPath := filepath.Join(tempDir, ".venv", "bin", "python")
	scriptPath := filepath.Join(tempDir, "scripts", "analyze_beancount.py")

	if err := os.MkdirAll(filepath.Dir(pythonPath), 0o755); err != nil {
		t.Fatalf("mkdir python path failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("mkdir script path failed: %v", err)
	}
	if err := os.WriteFile(pythonPath, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatalf("write python stub failed: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("write script stub failed: %v", err)
	}

	runner := &fakeRunner{
		outputs: map[string]string{
			pythonPath + " " + scriptPath + " /tmp/main.bean --all": "analysis ok",
		},
		errors: map[string]error{},
	}

	svc := NewService(Options{
		Enabled:          true,
		AgentEnabled:     true,
		LLMAPIKey:        "test-key",
		LLMModel:         "test-model",
		LedgerFile:       "/tmp/main.bean",
		Runner:           runner,
		Timeout:          3 * time.Second,
		PythonVenvPath:   filepath.Join(tempDir, ".venv"),
		PythonScriptPath: scriptPath,
	})

	output, _, err := svc.runQuickAnalysisTool(context.Background(), quickAnalysisToolArgs{Report: "all"})
	if err != nil {
		t.Fatalf("runQuickAnalysisTool failed: %v", err)
	}
	if !strings.Contains(output, "analysis ok") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestAnalysisSessionLifecycle(t *testing.T) {
	svc := NewService(Options{
		Enabled:      true,
		AgentEnabled: true,
		LLMAPIKey:    "test-key",
		LLMModel:     "test-model",
		LedgerFile:   "/tmp/main.bean",
		SessionTTL:   1 * time.Minute,
	})

	if svc.IsSessionActive(1001) {
		t.Fatalf("session should not be active before update")
	}

	svc.updateSession(1001, "问题", "回答", time.Now())
	if !svc.IsSessionActive(1001) {
		t.Fatalf("session should be active after update")
	}

	svc.ResetSession(1001)
	if svc.IsSessionActive(1001) {
		t.Fatalf("session should be inactive after reset")
	}
}

func TestValidateBQLQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "valid query",
			query:   "SELECT payee, sum(position) AS total WHERE account ~ '^Expenses:Food' AND year = 2026 AND month = 4 GROUP BY payee ORDER BY total DESC LIMIT 5",
			wantErr: false,
		},
		{
			name:    "contains keyword",
			query:   "SELECT payee, SUM(amount) AS total WHERE account CONTAINS 'Expenses'",
			wantErr: true,
		},
		{
			name:    "during keyword",
			query:   "SELECT sum(position) WHERE date DURING 2026-04",
			wantErr: true,
		},
		{
			name:    "quoted date compare",
			query:   "SELECT sum(position) WHERE date >= \"2026-04-01\"",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBQLQuery(tc.query)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildToolFailureSummary(t *testing.T) {
	msg := buildToolFailureSummary("本月餐饮支出最多的是哪些？", []Section{{
		Title:   "BQL 查询",
		Command: "bean-query ...",
		Output:  "tool_error: BQL 语法错误",
	}})

	if !strings.Contains(msg, "失败原因") {
		t.Fatalf("summary should contain failure reason: %s", msg)
	}
	if !strings.Contains(msg, "SELECT payee, sum(position)") {
		t.Fatalf("summary should contain example query: %s", msg)
	}
}
