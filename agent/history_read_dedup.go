package agent

import (
	"sync"

	actool "github.com/Autumn-27/norma/tool"
)

type historyReadState struct {
	mu      sync.Mutex
	seen    map[string]struct{}
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
	if _, ok := state.seen[key]; ok {
		state.repeats++
		result, _ := jsonResult(map[string]any{
			"duplicate": true,
			"message":   "本轮已请求过相同内容；直接使用该工具上次的返回，不要重复回源。",
		})
		return result, true
	}
	state.seen[key] = struct{}{}
	return actool.Result{}, false
}

func (t *ToolSet) retryPlannerHistoryRead(name, key string) {
	if t.worker != "planner" {
		return
	}
	t.historyReads.mu.Lock()
	delete(t.historyReads.seen, name+":"+key)
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
