package engine

import "time"

// initKBConfig returns the KB-related config fields loaded from environment
// variables. Called from Init() to keep config.go under the 200-line limit.
//
// Phase 2 note: KBPersonID is the person identity for MemDB (user_id in API
// calls). It defaults to KBUser for backwards compatibility when
// DOZOR_KB_PERSON_ID is not set, so existing deployments need no env changes.
func initKBConfig() (server, user, personID, cube, searchTool, saveTool string) {
	server = env("DOZOR_KB_SERVER", "memdb")
	user = env("DOZOR_KB_USER", env("DOZOR_MEMDB_USER", "default"))
	personID = env("DOZOR_KB_PERSON_ID", "")
	cube = env("DOZOR_KB_CUBE", env("DOZOR_MEMDB_CUBE", "default"))
	searchTool = env("DOZOR_KB_SEARCH_TOOL", "search_memories")
	saveTool = env("DOZOR_KB_SAVE_TOOL", "add_memory")
	if personID == "" {
		personID = user
	}
	return
}

// initCBConfig returns circuit-breaker config fields loaded from env.
func initCBConfig() (kbThreshold, llmThreshold int, kbReset, llmReset time.Duration) {
	kbThreshold = envInt("DOZOR_CB_KB_THRESHOLD", defaultCBKBThreshold)
	kbReset = envDurationStr("DOZOR_CB_KB_RESET", defaultCBKBResetMin*time.Minute)
	llmThreshold = envInt("DOZOR_CB_LLM_THRESHOLD", defaultErrorThreshold)
	llmReset = envDurationStr("DOZOR_CB_LLM_RESET", defaultCBLLMResetMin*time.Minute)
	return
}
