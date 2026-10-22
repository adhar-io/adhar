package digitalocean

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/digitalocean/godo"

	provider "adhar-io/adhar/platform/providers"
)

// orphanGrace is how long a worker droplet may exist without being a node
// before it is treated as a failed join rather than one still in progress. A
// worker is created, prepared (cloud-init, ~3 min) and joined (~1 min) within
// one scale call; twenty minutes is several times that.
const orphanGrace = 20 * time.Minute

// workerSet is a scale decision's view of the worker droplets, reconciled
// against the nodes the control plane actually has.
type workerSet struct {
	// Members are droplets that are cluster nodes. Only these count, and only
	// these are ever drained and retired.
	Members []godo.Droplet
	// InFlight are droplets too young to judge: counted, never touched.
	InFlight []godo.Droplet
	// Stale are droplets that never became nodes. They are discarded.
	Stale []godo.Droplet
}

// reconcileWorkers splits worker droplets by whether the control plane lists
// them as nodes.
//
// Matching is by ADDRESS, never by name: a failed scale-up on DigitalOcean
// (2026-10-22) created a droplet named adhar-adhar-workers-4 while a node of
// that name already ran on another droplet. Its join failed on the duplicate
// name, the droplet stayed, and from then on every "scale to 5" counted five
// droplets, did nothing and logged success — while four nodes sat at 99% of
// their memory and builds waited for hours. A name match would have called
// that orphan a member, and a scale-down would have drained the REAL node of
// that name while deleting the orphan's droplet.
//
// An empty node list means the control plane could not be asked, never that
// every droplet is an orphan: everything is then a member, as before.
func reconcileWorkers(workers []godo.Droplet, joined provider.JoinedNodes, now time.Time) workerSet {
	var s workerSet
	if len(joined.Names) == 0 {
		s.Members = append(s.Members, workers...)
		return s
	}
	for _, d := range workers {
		priv, _ := d.PrivateIPv4()
		pub, _ := d.PublicIPv4()
		switch {
		case (priv != "" && joined.Addresses[priv]) || (pub != "" && joined.Addresses[pub]):
			s.Members = append(s.Members, d)
		case dropletAge(d, now) < orphanGrace:
			s.InFlight = append(s.InFlight, d)
		default:
			s.Stale = append(s.Stale, d)
		}
	}
	return s
}

// dropletAge is how long ago the droplet was created; an unparseable time is
// treated as brand new, so an unreadable droplet is never discarded.
func dropletAge(d godo.Droplet, now time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339, d.Created)
	if err != nil {
		return 0
	}
	return now.Sub(t)
}

var workerIndex = regexp.MustCompile(`-(\d+)$`)

// nextWorkerNames returns n names for new workers that no droplet and no node
// uses: indices continue after the highest one in use. Numbering from the
// droplet COUNT reused workers-4 when workers-3 was gone, which is how the
// orphan above was made.
func nextWorkerNames(prefix string, inUse []string, n int) []string {
	taken := map[string]bool{}
	highest := 0
	for _, name := range inUse {
		taken[name] = true
		if m := workerIndex.FindStringSubmatch(name); m != nil && len(name) > len(m[0]) && name[:len(name)-len(m[0])] == prefix {
			if i, err := strconv.Atoi(m[1]); err == nil && i > highest {
				highest = i
			}
		}
	}
	var out []string
	for i := highest + 1; len(out) < n; i++ {
		if name := fmt.Sprintf("%s-%d", prefix, i); !taken[name] {
			out = append(out, name)
		}
	}
	return out
}

// sortedByIndex orders workers by their numeric suffix, so scale-down retires
// workers-10 before workers-9 (a string sort would not).
func sortedByIndex(ds []godo.Droplet) []godo.Droplet {
	out := append([]godo.Droplet(nil), ds...)
	idx := func(name string) int {
		if m := workerIndex.FindStringSubmatch(name); m != nil {
			i, _ := strconv.Atoi(m[1])
			return i
		}
		return 0
	}
	sort.SliceStable(out, func(i, j int) bool { return idx(out[i].Name) < idx(out[j].Name) })
	return out
}
