package analysis

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	outputs map[string]string
	errors  map[string]error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if err, ok := r.errors[key]; ok {
		return "", "", err
	}
	if out, ok := r.outputs[key]; ok {
		return out, "", nil
	}
	return "", "", fmt.Errorf("unexpected command: %s", key)
}

type fakeSummarizer struct{}

func (s *fakeSummarizer) Summarize(_ context.Context, _ string) (string, error) {
	return "测试总结", nil
}

func TestDetectSkill(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		skill Skill
		ok    bool
	}{
		{name: "monthly", text: "本月账单分析", skill: SkillMonthlySummary, ok: true},
		{name: "profit", text: "给我看损益表", skill: SkillProfitLoss, ok: true},
		{name: "top expenses", text: "支出排行", skill: SkillTopExpenses, ok: true},
		{name: "unsupported", text: "你好", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skill, ok := DetectSkill(tc.text)
			if ok != tc.ok {
				t.Fatalf("unexpected ok: got %v want %v", ok, tc.ok)
			}
			if skill != tc.skill {
				t.Fatalf("unexpected skill: got %q want %q", skill, tc.skill)
			}
		})
	}
}

func TestAnalyzeSkill(t *testing.T) {
	now := time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)
	begin, end := monthBounds(now)

	runner := &fakeRunner{
		outputs: map[string]string{
			"bean-query /tmp/main.bean SELECT account, sum(position) FROM OPEN ON " + begin + " CLOSE ON " + end + " WHERE account ~ '^(Income|Expenses):' GROUP BY account ORDER BY account":   "Income: 1000\nExpenses: 400\n",
			"bean-query /tmp/main.bean SELECT account, sum(position) FROM OPEN ON " + begin + " CLOSE ON " + end + " WHERE account ~ '^Expenses:' GROUP BY account ORDER BY sum(position) DESC": "Expenses:Food 200\nExpenses:Transport 100\n",
		},
		errors: map[string]error{},
	}

	svc := NewService(Options{
		Enabled:        true,
		BeanQueryBin:   "bean-query",
		LedgerFile:     "/tmp/main.bean",
		Runner:         runner,
		Summarizer:     &fakeSummarizer{},
		Timeout:        5 * time.Second,
		MaxOutputLines: 120,
	})

	result, err := svc.AnalyzeSkill(context.Background(), SkillMonthlySummary, "本月账单分析", now)
	if err != nil {
		t.Fatalf("AnalyzeSkill failed: %v", err)
	}

	if result.Title != "本月账单分析" {
		t.Fatalf("unexpected title: %s", result.Title)
	}

	if result.Summary != "测试总结" {
		t.Fatalf("unexpected summary: %s", result.Summary)
	}

	if len(result.Sections) != 2 {
		t.Fatalf("unexpected sections len: %d", len(result.Sections))
	}

	for _, section := range result.Sections {
		if !strings.HasPrefix(section.Command, "bean-query ") {
			t.Fatalf("unexpected command, want bean-query: %s", section.Command)
		}
		if strings.Contains(section.Command, "bean-report") {
			t.Fatalf("unexpected deprecated command in section: %s", section.Command)
		}
	}
}

func TestParseSkillAlias(t *testing.T) {
	skill, ok := ParseSkillAlias("monthly")
	if !ok || skill != SkillMonthlySummary {
		t.Fatalf("unexpected alias parse result: %v %q", ok, skill)
	}
}
