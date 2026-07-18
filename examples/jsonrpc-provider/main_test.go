package main

import (
	"path/filepath"
	"testing"
)

func TestNextIDSkipsExistingValues(t *testing.T) {
	t.Parallel()

	value := store{Items: map[string]memoryItem{
		"sample-42": {},
		"sample-43": {},
	}}
	if got := nextID(value, 42); got != "sample-44" {
		t.Fatalf("nextID() = %q, want sample-44", got)
	}
}

func TestSearchUsesFiltersButNotRuntimeMetadata(t *testing.T) {
	t.Setenv("PAXM_SAMPLE_PROVIDER_STORE", filepath.Join(t.TempDir(), "store.json"))

	ref, err := put(memoryItem{Text: "deploy runbook", Metadata: map[string]string{"project": "paxm"}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := search(searchQuery{
		Text:     "deploy",
		Metadata: map[string]string{"session_id": "new-session"},
		Filters:  map[string]string{"project": "paxm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != ref.ID {
		t.Fatalf("hits = %#v, want ref %q", hits, ref.ID)
	}

	hits, err = search(searchQuery{Text: "deploy", Filters: map[string]string{"project": "other"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("mismatched explicit filter returned hits: %#v", hits)
	}
}
