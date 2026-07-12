package cli

import "fmt"

func (r runner) runInternalCommand(command string, args []string) error {
	switch command {
	case "__hook":
		return r.runInternalHook(args)
	case "__hook-daemon":
		return r.runHookDaemon(args)
	case "__hook-control":
		return r.runHookControl(args)
	default:
		return fmt.Errorf("unknown internal command %q", command)
	}
}
