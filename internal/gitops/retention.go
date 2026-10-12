package gitops

import (
	"sort"
	"time"
)

// backupIDLayout names backups by UTC creation time; IDs sort chronologically.
const backupIDLayout = "20060102T150405.000Z"

// Retention decides which of a working copy's backups to keep.
type Retention struct {
	KeepAll   time.Duration // keep every backup younger than this
	KeepDaily time.Duration // keep the newest backup of each UTC day younger than this
	Max       int           // then keep at most this many, newest first; 0 means no cap
}

// expired returns the backup IDs r drops at now. The newest backup is always
// kept, and so is any ID that isn't a backup timestamp.
func (r Retention) expired(ids []string, now time.Time) []string {
	type backup struct {
		id string
		at time.Time
	}
	var backups []backup
	for _, id := range ids {
		if at, err := time.Parse(backupIDLayout, id); err == nil {
			backups = append(backups, backup{id, at})
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].at.After(backups[j].at) })

	var drop []string
	keptDays := map[string]bool{}
	kept := 0
	for i, b := range backups {
		age := now.Sub(b.at)
		day := b.at.UTC().Format("20060102")
		keep := i == 0 || age < r.KeepAll || (age < r.KeepDaily && !keptDays[day])
		if keep && r.Max > 0 && kept >= r.Max && i != 0 {
			keep = false
		}
		if !keep {
			drop = append(drop, b.id)
			continue
		}
		kept++
		keptDays[day] = true
	}
	return drop
}
