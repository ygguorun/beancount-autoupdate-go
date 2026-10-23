package ledgercli

import "testing"

func TestBQLDateCond(t *testing.T) {
	tests := []struct {
		op, v, want string
		ok          bool
	}{
		{"since", "2026-10", "date >= 2026-10-01", true},
		{"since", "2026-10-16", "date >= 2026-10-16", true},
		{"until", "2026-10", "date < 2026-11-01", true},    // 含整月（旧实现多放一天 11-01）
		{"until", "2026-10-23", "date < 2026-10-24", true}, // 含当天
		{"until", "2026-02-28", "date < 2026-03-01", true},
		{"until", "2024-02-29", "date < 2024-03-01", true}, // 闰日合法
		{"since", "2026/10", "", false},
		{"since", "2026-13-01", "", false}, // 归一化会漂到次年 1 月，必须拒
		{"until", "2026-02-30", "", false},
		{"until", "abc", "", false},
	}
	for _, tt := range tests {
		got, ok := bqlDateCond(tt.op, tt.v)
		if ok != tt.ok || got != tt.want {
			t.Errorf("bqlDateCond(%q, %q) = %q, %v; want %q, %v", tt.op, tt.v, got, ok, tt.want, tt.ok)
		}
	}
}
