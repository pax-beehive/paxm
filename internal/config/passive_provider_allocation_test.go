package config

import (
	"os"
	"strings"
	"testing"
)

func TestProviderAllocationFromEnvUnsetEmptyZeroNegativeMeanNoAllocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
	}{
		{name: "unset", set: false},
		{name: "empty", set: true, val: ""},
		{name: "zero", set: true, val: "0"},
		{name: "negative", set: true, val: "-1"},
		{name: "whitespace only", set: true, val: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(PassiveProviderAllocationEnv, tc.val)
			} else {
				t.Setenv(PassiveProviderAllocationEnv, "")
				os.Unsetenv(PassiveProviderAllocationEnv)
			}
			got, err := ProviderAllocationFromEnv()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != 0 {
				t.Fatalf("ProviderAllocationFromEnv() = %d, want 0", got)
			}
		})
	}
}

func TestProviderAllocationFromEnvPositiveValueParses(t *testing.T) {
	t.Setenv(PassiveProviderAllocationEnv, "2")
	got, err := ProviderAllocationFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Fatalf("ProviderAllocationFromEnv() = %d, want 2", got)
	}
}

func TestProviderAllocationFromEnvMalformedValueErrorsNamingVariable(t *testing.T) {
	t.Setenv(PassiveProviderAllocationEnv, "two")
	_, err := ProviderAllocationFromEnv()
	if err == nil {
		t.Fatal("expected an error for a malformed value, got nil")
	}
	if got, want := err.Error(), PassiveProviderAllocationEnv; !strings.Contains(got, want) {
		t.Fatalf("error %q does not name the variable %q", got, want)
	}
	if !strings.Contains(err.Error(), "two") {
		t.Fatalf("error %q does not include the malformed value", err.Error())
	}
}
