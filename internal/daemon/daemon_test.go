package daemon

import (
	"testing"
	"time"
)

func TestInSchedule(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 10, 20, h, m, 0, 0, time.Local)
	}
	tests := []struct {
		name     string
		schedule string
		now      time.Time
		want     bool
	}{
		{"always", "always", at(3, 0), true},
		{"empty", "", at(3, 0), true},
		{"inside day", "01:00-08:00", at(3, 0), true},
		{"before day", "01:00-08:00", at(0, 30), false},
		{"after day", "01:00-08:00", at(9, 0), false},
		{"inside overnight", "22:00-06:00", at(23, 0), true},
		{"after midnight", "22:00-06:00", at(2, 0), true},
		{"overnight outside", "22:00-06:00", at(12, 0), false},
		{"malformed fails open", "nonsense", at(12, 0), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InSchedule(tt.schedule, tt.now); got != tt.want {
				t.Fatalf("InSchedule(%q, %s) = %v, want %v", tt.schedule, tt.now, got, tt.want)
			}
		})
	}
}
