package permit

import (
	"sync"
)

type Evaluator struct {
	mu         sync.Mutex
	generation map[string]int64
	open       map[string]bool
}

func NewEvaluator() *Evaluator {
	return &Evaluator{generation: make(map[string]int64), open: make(map[string]bool)}
}

// GrantProbe captures a single run's grant so a side-effecting evaluation
// (AllowReceive marks the permit open) can be rolled back with RestoreGrant,
// keeping preflight checks free of field-state changes.
type GrantProbe struct {
	RunID   string
	Existed bool
	Open    bool
}

func (e *Evaluator) GrantProbe(runID string) GrantProbe {
	e.mu.Lock()
	defer e.mu.Unlock()
	open, existed := e.open[runID]
	return GrantProbe{RunID: runID, Existed: existed, Open: open}
}

func (e *Evaluator) RestoreGrant(probe GrantProbe) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if probe.Existed {
		e.open[probe.RunID] = probe.Open
	} else {
		delete(e.open, probe.RunID)
	}
}
