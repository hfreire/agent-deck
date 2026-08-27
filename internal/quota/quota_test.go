package quota

import (
	"testing"
	"time"
)

func TestParseResetTime(t *testing.T) {
	iso := time.Date(2026, 8, 27, 16, 29, 59, 0, time.UTC)
	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{"iso", `"2026-08-27T16:29:59Z"`, iso},
		{"iso_offset", `"2026-08-27T16:29:59+00:00"`, iso},
		{"unix_seconds", `1787852999`, time.Unix(1787852999, 0)},
		{"unix_millis", `1787852999000`, time.Unix(1787852999, 0)},
		{"numeric_string", `"1787852999"`, time.Unix(1787852999, 0)},
		{"null", `null`, time.Time{}},
		{"empty_string", `""`, time.Time{}},
		{"garbage", `"soon"`, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResetTime([]byte(tc.raw))
			if !got.Equal(tc.want) {
				t.Fatalf("parseResetTime(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestWindowResetsIn(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	w := Window{ResetsAt: now.Add(90 * time.Minute)}
	if got := w.ResetsIn(now); got != 90*time.Minute {
		t.Fatalf("ResetsIn = %v, want 90m", got)
	}
	if got := (Window{}).ResetsIn(now); got != 0 {
		t.Fatalf("zero ResetsAt should give 0, got %v", got)
	}
	past := Window{ResetsAt: now.Add(-time.Minute)}
	if got := past.ResetsIn(now); got != 0 {
		t.Fatalf("elapsed window should give 0, got %v", got)
	}
}

func TestClampPercent(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{-5, 0}, {0, 0}, {42, 42}, {100, 100}, {140, 100}} {
		if got := clampPercent(tc.in); got != tc.want {
			t.Fatalf("clampPercent(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
