// Ordered transitions use retained timestamps as lower bounds so clock changes
// cannot move newly committed lifecycle evidence backwards.

package catalog

import "time"

// TransitionTime chooses a timestamp for new ordered state, not an expiry or
// deadline. Retained timestamps are lower bounds across clock corrections and
// process restarts. Microsecond precision matches persisted audit/domain time.
// It never changes the clock or rewrites an existing record.
func TransitionTime(now time.Time, floors ...time.Time) time.Time {
	at := now.UTC().Truncate(time.Microsecond)
	for _, floor := range floors {
		floor = floor.UTC().Truncate(time.Microsecond)
		if floor.After(at) {
			at = floor
		}
	}
	return at
}
