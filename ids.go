package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
)

type requestIDs struct {
	Session       string
	Request       string
	Project       string
	ParentSession string
}

func deriveRequestIDs(r *http.Request, body map[string]any) requestIDs {
	// Explicit client session signal must be forwarded verbatim. Upstream
	// uses x-opencode-session for prompt-cache and reasoning-item affinity
	// (rs_* lookups are scoped to the session). Hashing or otherwise
	// rewriting a real client session breaks that affinity and surfaces as
	// "Referenced reasoning item ... was not found or has expired".
	// Only derive-and-hash when the client sent no session at all.
	rawSession := firstString(
		r.Header.Get("x-opencode-session"),
		r.Header.Get("x-session-affinity"),
		r.Header.Get("X-Session-Id"),
		r.Header.Get("x-session-id"),
		r.Header.Get("conversation-id"),
		stringAt(body, "conversation_id"),
		stringAt(body, "metadata", "session_id"),
	)
	var session string
	if rawSession != "" {
		session = strings.TrimSpace(rawSession)
	} else {
		signal := conversationSeed(body)
		if signal == "" {
			signal = stringAt(body, "previous_response_id")
		}
		if signal == "" || signal == `{}` {
			signal = randomID("fallback", 16)
		}
		session = stableID("ses", signal)
	}
	projectSignal := firstString(r.Header.Get("x-opencode-project"), stringAt(body, "metadata", "project_id"))
	if projectSignal == "" {
		projectSignal = "opencode2api:default-project"
	}
	parentSession := firstString(
		r.Header.Get("x-parent-session-id"),
		stringAt(body, "metadata", "parent_session_id"),
	)
	return requestIDs{
		Session:       session,
		Request:       randomID("req", 16),
		Project:       stableID("prj", projectSignal),
		ParentSession: parentSession,
	}
}

func conversationSeed(body map[string]any) string {
	if input, ok := body["input"].(string); ok && input != "" {
		return input
	}
	for _, field := range []string{"messages", "input"} {
		for _, raw := range sliceAt(body, field) {
			item, ok := raw.(map[string]any)
			if !ok || stringAt(item, "role") != "user" {
				continue
			}
			encoded, _ := json.Marshal(item["content"])
			if len(encoded) > 0 && string(encoded) != "null" {
				return string(encoded)
			}
		}
	}
	return ""
}

func stableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

// fabricatedReasoningIDs tracks Responses reasoning item IDs minted by the
// gateway itself for downstream output. Upstream never issued them, so they
// must never be sent back upstream: Responses servers dereference reasoning
// item IDs against their own store and reject unknown ones with
// "Referenced reasoning item ... was not found or has expired".
var fabricatedReasoningIDs = newFabricatedIDSet(32768)

type fabricatedIDSet struct {
	mu    sync.Mutex
	cap   int
	ids   map[string]struct{}
	order []string
}

func newFabricatedIDSet(capacity int) *fabricatedIDSet {
	return &fabricatedIDSet{cap: capacity, ids: make(map[string]struct{})}
}

func (s *fabricatedIDSet) add(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	for len(s.order) > s.cap {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.ids, oldest)
	}
}

func (s *fabricatedIDSet) has(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}

func markFabricatedReasoningID(id string) { fabricatedReasoningIDs.add(id) }

func isFabricatedReasoningID(id string) bool { return fabricatedReasoningIDs.has(id) }

func randomID(prefix string, size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func opencodeUserAgent() string {
	return fmt.Sprintf("opencode/1.18.21 (%s %s; %s)", runtime.GOOS, runtime.GOARCH, runtime.Version())
}
