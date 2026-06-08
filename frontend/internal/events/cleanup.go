package events

import "sync"

type CleanupHook func(ev Event) [][]byte

var (
	cleanupHooksMu sync.RWMutex
	cleanupHooks   = map[Kind]CleanupHook{}
)

func RegisterCleanup(kind Kind, fn CleanupHook) {
	cleanupHooksMu.Lock()
	defer cleanupHooksMu.Unlock()
	cleanupHooks[kind] = fn
}

func cascadeKeysFor(ev Event) [][]byte {
	cleanupHooksMu.RLock()
	fn, ok := cleanupHooks[ev.Kind]
	cleanupHooksMu.RUnlock()
	if !ok {
		return nil
	}
	return fn(ev)
}
