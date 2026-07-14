package memory

import "testing"

func TestPrepareProviderItemSeparatesOriginAndScope(t *testing.T) {
	item := ApplyProvenance(MemoryItem{
		Text: "remember",
		Turn: &TurnContext{SessionID: "session-7", TurnID: "turn-42"},
	}, Provenance{UserID: "todd", AgentID: "codex", ScopeType: "team", ScopeID: "pax"})

	got := PrepareProviderItem(item)
	wantOrigin := MemoryOrigin{UserID: "todd", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"}
	wantScope := MemoryScope{Type: "team", ID: "pax"}
	if got.Origin != wantOrigin || got.Scope != wantScope {
		t.Fatalf("attribution = origin %#v scope %#v, want origin %#v scope %#v", got.Origin, got.Scope, wantOrigin, wantScope)
	}
	for key, want := range map[string]string{
		MetadataUserID: "todd", MetadataAgentID: "codex", MetadataSessionID: "session-7",
		MetadataTurnID: "turn-42", MetadataScopeType: "team", MetadataScopeID: "pax",
	} {
		if got.Metadata[key] != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got.Metadata[key], want)
		}
	}
}

func TestApplyProvenanceDropsCallerSuppliedAttributionMetadata(t *testing.T) {
	item := ApplyProvenance(MemoryItem{Metadata: map[string]string{
		MetadataUserID: "spoofed-user", MetadataSessionID: "spoofed-session", MetadataTurnID: "spoofed-turn",
	}}, Provenance{UserID: "trusted-user", AgentID: "trusted-agent", ScopeType: "personal", ScopeID: "trusted-user"})
	if item.Metadata[MetadataUserID] != "trusted-user" {
		t.Fatalf("user metadata = %q", item.Metadata[MetadataUserID])
	}
	if item.Metadata[MetadataSessionID] != "" || item.Metadata[MetadataTurnID] != "" {
		t.Fatalf("caller attribution survived: %#v", item.Metadata)
	}
}

func TestApplyHitAttributionPrefersStructuredFieldsAndFallsBackToMetadata(t *testing.T) {
	hit := ApplyHitAttribution(MemoryHit{
		Origin: MemoryOrigin{UserID: "trusted-user"},
		Metadata: map[string]string{
			MetadataUserID: "metadata-user", MetadataAgentID: "codex", MetadataSessionID: "session-7",
			MetadataTurnID: "turn-42", MetadataScopeType: "personal", MetadataScopeID: "trusted-user",
		},
	})
	if hit.Origin != (MemoryOrigin{UserID: "trusted-user", AgentID: "codex", SessionID: "session-7", TurnID: "turn-42"}) {
		t.Fatalf("origin = %#v", hit.Origin)
	}
	if hit.Scope != (MemoryScope{Type: "personal", ID: "trusted-user"}) {
		t.Fatalf("scope = %#v", hit.Scope)
	}
}
