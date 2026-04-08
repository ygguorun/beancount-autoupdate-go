package telegram

import "testing"

func TestSlashCommands(t *testing.T) {
	commands := slashCommands()

	want := []struct {
		command     string
		description string
	}{
		{command: "start", description: "显示欢迎信息"},
		{command: "help", description: "显示使用帮助"},
		{command: "accounts", description: "查看账户和分类"},
		{command: "pending", description: "查看待处理交易"},
		{command: "cancel", description: "取消当前输入"},
	}

	if len(commands) != len(want) {
		t.Fatalf("unexpected command count: got %d want %d", len(commands), len(want))
	}

	seen := make(map[string]struct{}, len(commands))
	for i, cmd := range commands {
		if cmd.Command != want[i].command {
			t.Fatalf("unexpected command at index %d: got %q want %q", i, cmd.Command, want[i].command)
		}
		if cmd.Description != want[i].description {
			t.Fatalf("unexpected description for %q: got %q want %q", cmd.Command, cmd.Description, want[i].description)
		}
		if _, exists := seen[cmd.Command]; exists {
			t.Fatalf("duplicate command found: %q", cmd.Command)
		}
		seen[cmd.Command] = struct{}{}
	}
}
