package cli

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	paxruntime "github.com/pax-beehive/paxm/internal/runtime"
	"github.com/pax-beehive/paxm/internal/tools"
)

func TestCLIHasNoFacadeDependency(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, _ := strconv.Unquote(imported.Path.Value)
			if path == "github.com/pax-beehive/paxm/internal/facade" {
				t.Fatalf("%s imports facade; CLI must depend on tools/capture interfaces", entry.Name())
			}
		}
	}
}

func TestRuntimeDoesNotExposeFacadeEscapeHatch(t *testing.T) {
	if _, ok := reflect.TypeOf(paxruntime.Runtime{}).FieldByName("Service"); ok {
		t.Fatal("runtime exposes facade Service")
	}
	agentType := reflect.TypeOf((*tools.Agent)(nil)).Elem()
	if agentType.NumMethod() != 2 {
		t.Fatalf("agent tool interface has %d methods, want recall and remember only", agentType.NumMethod())
	}
}

func TestTopLevelCommandsHaveExplicitAudience(t *testing.T) {
	want := map[string]commandClass{
		"setup": operatorCommand, "config": operatorCommand, "history": operatorCommand, "logs": operatorCommand, "backfill": operatorCommand, "eval": operatorCommand, "update": operatorCommand, "uninstall": operatorCommand,
		"recall": toolCommand, "remember": toolCommand, "mcp": toolCommand,
		"__hook": internalCommand, "__hook-daemon": internalCommand, "__hook-control": internalCommand,
	}
	for command, expected := range want {
		if got := classifyCommand(command); got != expected {
			t.Errorf("%s class=%q want %q", command, got, expected)
		}
	}
}
