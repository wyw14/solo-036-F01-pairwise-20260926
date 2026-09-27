package permit

import "github.com/wyw14/solo-036/internal/model"

func Restore(e *Evaluator, records []model.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range records {
		if r.Kind == "passage" && r.Durable {
			e.open[r.RunID] = true
			e.generation[r.RunID] = r.Epoch
		}
	}
}
func Snapshot(e *Evaluator) map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]bool)
	for k, v := range e.open {
		out[k] = v
	}
	return out
}
