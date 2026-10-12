package gitops

import (
	"strings"
	"testing"
	"time"
)

func TestRetention_Expired(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	id := func(ago time.Duration) string { return now.Add(-ago).Format(backupIDLayout) }
	tiers := Retention{KeepAll: 24 * time.Hour, KeepDaily: 14 * 24 * time.Hour}

	tests := []struct {
		name   string
		policy Retention
		ids    []string
		want   []string
	}{
		{
			name:   "everything within keep_all stays",
			policy: tiers,
			ids:    []string{id(time.Hour), id(2 * time.Hour), id(23 * time.Hour)},
		},
		{
			name:   "older backups keep the newest of each day",
			policy: tiers,
			// 2 days ago at 12:00, 11:00 and 10:00 share a day.
			ids:  []string{id(48 * time.Hour), id(49 * time.Hour), id(50 * time.Hour), id(72 * time.Hour)},
			want: []string{id(49 * time.Hour), id(50 * time.Hour)},
		},
		{
			name:   "past keep_daily goes",
			policy: tiers,
			ids:    []string{id(time.Hour), id(15 * 24 * time.Hour)},
			want:   []string{id(15 * 24 * time.Hour)},
		},
		{
			name:   "the newest backup always stays",
			policy: tiers,
			ids:    []string{id(30 * 24 * time.Hour), id(40 * 24 * time.Hour)},
			want:   []string{id(40 * 24 * time.Hour)},
		},
		{
			name:   "max caps what the tiers keep",
			policy: Retention{KeepAll: 24 * time.Hour, Max: 2},
			ids:    []string{id(time.Hour), id(2 * time.Hour), id(3 * time.Hour)},
			want:   []string{id(3 * time.Hour)},
		},
		{
			name:   "IDs that aren't backup times are never touched",
			policy: Retention{},
			ids:    []string{"keep-me", id(time.Hour), id(2 * time.Hour)},
			want:   []string{id(2 * time.Hour)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(tt.policy.expired(tt.ids, now), ",")
			if want := strings.Join(tt.want, ","); got != want {
				t.Errorf("expired = %s\n          want %s", got, want)
			}
		})
	}
}
