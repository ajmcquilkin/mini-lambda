package scheduler

// liveSlots reports the number of live slots. Test-only.
func (e *Engine) liveSlots() int {
	return e.pool.liveTotal()
}
