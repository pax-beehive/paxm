package cli

import "fmt"

func (r runner) runOperatorCommand(command string, args []string) error {
	switch command {
	case "setup":
		return r.runSetup(args)
	case "uninstall":
		return r.runUninstall(args)
	case "history":
		return r.runHistory(args)
	case "logs":
		return r.runLogs(args)
	case "backfill":
		return r.runBackfill(args)
	case "eval":
		return r.runEval(args)
	case "update":
		return r.runUpdate(args)
	case "version":
		_, _ = fmt.Fprintln(r.stdout, r.versionString())
		return nil
	case "config":
		return r.runConfig(args)
	default:
		return fmt.Errorf("unknown operator command %q", command)
	}
}
