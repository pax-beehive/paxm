package paxlclient

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestProvisionAgentUsesDocumentedPaxlCommand(t *testing.T) {
	var gotArgs []string
	client := New(func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{
			"url":"https://memory.internal",
			"api_key":"tm_key_agent",
			"agent_id":"paxm-todd",
			"user_id":"usr-1",
			"credential_id":"cred-agent"
		}`), nil
	})

	got, err := client.ProvisionAgent(context.Background(), "paxm-todd")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"device", "provision", "--agent", "paxm-todd", "--json"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got.APIKey != "tm_key_agent" || got.URL != "https://memory.internal" ||
		got.UserID != "usr-1" || got.AgentID != "paxm-todd" {
		t.Fatalf("provision = %#v", got)
	}
}

func TestOnPremUserIDReadsDeviceStatus(t *testing.T) {
	var gotArgs []string
	client := New(func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{
			"url":"https://memory.internal","device_name":"todd-macbook-air","user_id":"usr-1",
			"status":"connected"
		}`), nil
	})

	got, err := client.OnPremUserID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"device", "status", "--format", "jsonl"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got != "usr-1" {
		t.Fatalf("user ID = %q, want usr-1", got)
	}
}

func TestOnPremUserIDFallsBackToChannelStatus(t *testing.T) {
	var calls [][]string
	client := New(func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "device" {
			return nil, errors.New("device status unsupported")
		}
		return []byte(`{
			"profile":{"name":"onprem","url":"https://memory.internal","user_id":"usr-1"},
			"status":"connected"
		}`), nil
	})

	got, err := client.OnPremUserID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := [][]string{
		{"device", "status", "--format", "jsonl"},
		{"channel", "status", "onprem", "--format", "jsonl"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if got != "usr-1" {
		t.Fatalf("user ID = %q, want usr-1", got)
	}
}

func TestOnPremUserIDReadsMultipleDeviceStatusJSONLines(t *testing.T) {
	client := New(func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "device" {
			t.Fatalf("unexpected channel fallback: %#v", args)
		}
		return []byte("{\"status\":\"connecting\"}\n{\"status\":\"connected\",\"user_id\":\"usr_AbC\"}\n"), nil
	})

	got, err := client.OnPremUserID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "usr_AbC" {
		t.Fatalf("user ID = %q, want usr_AbC", got)
	}
}

func TestOnPremUserIDReadsMultipleChannelStatusJSONLines(t *testing.T) {
	client := New(func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "device" {
			return nil, errors.New("device status unsupported")
		}
		return []byte("{\"status\":\"connecting\"}\n{\"status\":\"connected\",\"profile\":{\"user_id\":\"usr_AbC\"}}\n"), nil
	})

	got, err := client.OnPremUserID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "usr_AbC" {
		t.Fatalf("user ID = %q, want usr_AbC", got)
	}
}
