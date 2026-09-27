package route

import (
	"fmt"
	"github.com/wyw14/solo-036/internal/model"
	"time"
)

func (c *Coordinator) Reserve(route, runID string, epoch int64) (model.RouteLease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, ok := c.owners[route]; ok && owner.RunID != runID {
		return model.RouteLease{}, fmt.Errorf("route owned by %s", owner.RunID)
	}
	lease := model.RouteLease{Route: route, RunID: runID, Epoch: epoch, AcquiredAt: time.Now().UTC()}
	c.owners[route] = lease
	return lease, nil
}
func (c *Coordinator) Release(route, runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if owner, ok := c.owners[route]; ok && owner.RunID == runID {
		delete(c.owners, route)
		delete(c.progress, runID)
	}
}

// Owner returns the current lease holder for a route without changing the
// fence. ok is false when the route is unreserved, enabling read-only preflight
// checks against the exact ownership predicate enforced by Reserve.
func (c *Coordinator) Owner(route string) (lease model.RouteLease, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok = c.owners[route]
	return lease, ok
}
