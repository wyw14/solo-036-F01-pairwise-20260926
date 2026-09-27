package permit

import "github.com/wyw14/solo-036/internal/model"

func (e *Evaluator) AllowRepressurization(proof model.PassageEvidence, durable bool) bool {
	if !model.ValidEvidence(proof) || !durable {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.open[proof.RunID] = true
	e.generation[proof.RunID] = proof.Generation
	return true
}
func (e *Evaluator) AllowReceive(runID string, proof model.CommandReceipt, expected int64) bool {
	if runID == "" || proof.RunID != runID || !proof.Accepted || proof.Epoch != expected {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.open[runID] = true
	return true
}
func (e *Evaluator) IsOpen(runID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.open[runID]
}
