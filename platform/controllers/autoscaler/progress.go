package autoscaler

import (
	"fmt"
	"time"
)

// scaleUpNoProgressAfter is how long a scale-up the provider reported as done
// may go without the cluster gaining the workers it asked for: create, node
// prep and join take ~5 minutes, so three times that is a stall.
const scaleUpNoProgressAfter = 15 * time.Minute

// scaleUpProgress remembers the last scale-up the provider reported as
// SUCCESSFUL and notices when it never turned into nodes.
//
// On 2026-10-22 a DigitalOcean cluster logged `Scaled cluster "adhar" workers
// to 5` every three minutes for hours while it stayed at four nodes, all at
// 99% of their memory: a worker VM whose join had failed was counted as a
// worker, so every scale-up was a no-op that reported success. The provider
// is fixed, but "the cloud said yes and nothing changed" must never again be
// silent — this turns it into a Warning event and the status reason.
//
// In memory on purpose: a controller restart forgets a stall, and the next
// successful scale-up starts the clock again.
type scaleUpProgress struct {
	target int32
	since  time.Time
	warned bool
}

// recordSuccess notes a scale-up to `target` that the provider accepted. A
// stall in progress keeps its original clock: a higher retry is still the same
// unanswered request for capacity.
func (p *scaleUpProgress) recordSuccess(target int32, now time.Time) {
	if p.target == 0 {
		p.since = now
		p.warned = false
	}
	if target > p.target {
		p.target = target
	}
}

// check reports a stall when the workers asked for have not appeared within
// scaleUpNoProgressAfter: msg while it lasts (for the status reason), first
// only on the tick it is detected (for the one Warning event). Reaching the
// target clears it.
func (p *scaleUpProgress) check(workers int32, now time.Time) (msg string, first bool) {
	if p.target == 0 {
		return "", false
	}
	if workers >= p.target {
		*p = scaleUpProgress{}
		return "", false
	}
	if now.Sub(p.since) < scaleUpNoProgressAfter {
		return "", false
	}
	first = !p.warned
	p.warned = true
	return fmt.Sprintf("the provider reported scaling to %d workers %s ago, but the cluster still has %d schedulable worker(s); "+
		"a worker VM that never joined (counted by the provider but not a node) is the usual cause — compare the cloud's worker VMs with `kubectl get nodes`",
		p.target, now.Sub(p.since).Round(time.Minute), workers), first
}
