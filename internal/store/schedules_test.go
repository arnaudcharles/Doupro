package store

import (
	"testing"
	"time"
)

func TestScheduleCompleted(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		sc   Schedule
		want bool
	}{
		{"once fired", Schedule{Kind: "once", LastRunAt: &now}, true},
		{"once disabled before firing", Schedule{Kind: "once", Enabled: false}, false},
		{"cron with last run", Schedule{Kind: "cron", LastRunAt: &now}, false},
		{"relative with last run", Schedule{Kind: "relative", LastRunAt: &now}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sc.Completed(); got != tt.want {
				t.Fatalf("Completed() = %v, want %v", got, tt.want)
			}
		})
	}
}
