package scheduler

// liveSlots reports the number of live slots. Test-only.
func (e *Engine) liveSlots() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.total
}
