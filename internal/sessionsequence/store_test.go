package sessionsequence

import (
	"path/filepath"
	"testing"
)

func TestStoreReservesUniqueSequencesAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sequences.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if got, err := first.Next("session", 100); err != nil || got != 100 {
		t.Fatalf("first sequence = %d, err=%v", got, err)
	}
	if got, err := second.Next("session", 100); err != nil || got != 101 {
		t.Fatalf("cross-instance sequence = %d, err=%v", got, err)
	}
	if got, err := first.Next("session", 200); err != nil || got != 200 {
		t.Fatalf("higher floor sequence = %d, err=%v", got, err)
	}
	if got, err := second.Next("other-session", 100); err != nil || got != 100 {
		t.Fatalf("independent session sequence = %d, err=%v", got, err)
	}
}
