package agent

import (
	"sync"

	actool "github.com/Autumn-27/norma/tool"
)

type historyReadState struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	cache   map[string]actool.Result
	calls   int
	repeats int
}

type HistoryReadStats struct {
	Calls    int
	Unique   int
	Repeated int
}

func (t *ToolSet) reusePlannerHistoryRead(name, key string) (actool.Result, bool) {
	if t.worker != "planner" {
		return actool.Result{}, false
	}
	state := &t.historyReads
	state.mu.Lock()
	defer state.mu.Unlock()
	state.calls++
	if state.seen == nil {
		state.seen = map[string]struct{}{}
	}
	key = name + ":" + key
	if cached, ok := state.cache[key]; ok {
		state.repeats++
		return cached, true
	}
	// Another call may still be in progress. Never return a synthetic
	// "duplicate" instead of evidence that the caller may no longer have.
	state.seen[key] = struct{}{}
	return actool.Result{}, false
}

// rememberPlannerHistoryRead caches the actual evidence/result, not just a
// "you already read it" placeholder that can cause context loss.
func (t *ToolSet) rememberPlannerHistoryRead(name, key string, result actool.Result) {
	if t.worker != "planner" {
		return
	}
	t.historyReads.mu.Lock()
	if t.historyReads.cache == nil {
		t.historyReads.cache = map[string]actool.Result{}
	}
	t.historyReads.cache[name+":"+key] = result
	t.historyReads.mu.Unlock()
}

func (t *ToolSet) retryPlannerHistoryRead(name, key string) {
	if t.worker != "planner" {
		return
	}
	t.historyReads.mu.Lock()
	delete(t.historyReads.seen, name+":"+key)
	delete(t.historyReads.cache, name+":"+key)
	t.historyReads.mu.Unlock()
}

func (t *ToolSet) HistoryReadStats() HistoryReadStats {
	t.historyReads.mu.Lock()
	defer t.historyReads.mu.Unlock()
	return HistoryReadStats{
		Calls:    t.historyReads.calls,
		Unique:   len(t.historyReads.seen),
		Repeated: t.historyReads.repeats,
	}
}
