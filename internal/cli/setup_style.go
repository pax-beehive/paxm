package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	setupBannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	setupFaintStyle  = lipgloss.NewStyle().Faint(true)
	setupCardStyle   = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("5")).
				Padding(0, 2)
	setupCommandStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// writeSetupBanner prints the setup header in interactive mode.
func writeSetupBanner(w io.Writer) {
	_, _ = fmt.Fprintln(w, setupBannerStyle.Render("paxm setup")+setupFaintStyle.Render("  memory for your agents"))
	_, _ = fmt.Fprintln(w)
}

// renderSetupSummaryCard wraps the plain summary in a bordered card for
// interactive terminals.
func renderSetupSummaryCard(summary string) string {
	return setupCardStyle.Render(strings.TrimRight(summary, "\n"))
}

// writeSetupNextSteps prints the post-install command hints. Interactive
// terminals get a bordered card with highlighted commands.
func writeSetupNextSteps(w io.Writer, interactive bool) {
	commands := []struct{ cmd, hint string }{
		{"paxm remember --text \"...\"", "store a memory"},
		{"paxm recall --query \"...\"", "search memories"},
		{"paxm config doctor", "check provider health"},
	}
	var b strings.Builder
	b.WriteString("Next steps\n")
	for _, command := range commands {
		if interactive {
			b.WriteString("  " + setupCommandStyle.Render(command.cmd) + setupFaintStyle.Render("  # "+command.hint) + "\n")
			continue
		}
		b.WriteString("  " + command.cmd + "  # " + command.hint + "\n")
	}
	if interactive {
		_, _ = fmt.Fprintln(w, renderSetupSummaryCard(b.String()))
		return
	}
	_, _ = fmt.Fprint(w, b.String())
}

// detectInstalledAgents reports which supported agents appear to be installed
// on this machine, by CLI binary or well-known config directory.
func detectInstalledAgents() map[string]bool {
	detected := make(map[string]bool)
	for _, name := range []string{"codex", "claude", "pi", "opencode", "cursor", "kimi", "zcode", "kiro", "cline"} {
		if _, err := exec.LookPath(name); err == nil {
			detected[name] = true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return detected
	}
	for name, dir := range map[string]string{
		"claude":   ".claude",
		"codex":    ".codex",
		"opencode": filepath.Join(".config", "opencode"),
	} {
		if info, err := os.Stat(filepath.Join(home, dir)); err == nil && info.IsDir() {
			detected[name] = true
		}
	}
	return detected
}
