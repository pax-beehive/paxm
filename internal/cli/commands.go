package cli

import "fmt"

type commandClass string

const (
	operatorCommand commandClass = "operator"
	toolCommand     commandClass = "tool"
	internalCommand commandClass = "internal"
)

var commandClasses = map[string]commandClass{
	"setup": operatorCommand, "uninstall": operatorCommand, "history": operatorCommand, "logs": operatorCommand, "backfill": operatorCommand, "eval": operatorCommand, "update": operatorCommand, "version": operatorCommand, "config": operatorCommand,
	"recall": toolCommand, "remember": toolCommand, "mcp": toolCommand,
	"__hook": internalCommand, "__hook-daemon": internalCommand, "__hook-control": internalCommand,
}

func classifyCommand(command string) commandClass { return commandClasses[command] }

func (r runner) run(args []string) error {
	command, rest := args[0], args[1:]
	switch classifyCommand(command) {
	case operatorCommand:
		return r.runOperatorCommand(command, rest)
	case toolCommand:
		return r.runToolCommand(command, rest)
	case internalCommand:
		return r.runInternalCommand(command, rest)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
