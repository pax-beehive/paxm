package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/term"
)

var errPromptCancelled = errors.New("prompt cancelled")

type setupPrompter struct {
	input       io.Reader
	output      io.Writer
	reader      *bufio.Reader
	interactive bool
}

func newSetupPrompter(input io.Reader, output io.Writer) *setupPrompter {
	return &setupPrompter{
		input:       input,
		output:      output,
		reader:      bufio.NewReader(input),
		interactive: terminalPromptAvailable(input, output),
	}
}

func terminalPromptAvailable(input io.Reader, output io.Writer) bool {
	in, inOK := input.(*os.File)
	out, outOK := output.(*os.File)
	if !inOK || !outOK || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

func (p *setupPrompter) multiSelect(question string, options []setupOption, defaults map[string]bool) (map[string]bool, error) {
	if !p.interactive {
		return promptMultiSelect(p.reader, p.output, question, options, defaults)
	}
	selected := make([]string, 0, len(options))
	huhOptions := make([]huh.Option[string], 0, len(options))
	for _, option := range options {
		huhOptions = append(huhOptions, huh.NewOption(option.Label, option.ID).Selected(defaults[option.ID]))
	}
	field := huh.NewMultiSelect[string]().
		Title(question).
		Description("Up/down to move, space to toggle, enter to confirm").
		Options(huhOptions...).
		Height(minInt(len(options)+3, 10)).
		Value(&selected)
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(p.input).WithOutput(p.output).Run(); err != nil {
		return nil, normalizePromptError(err)
	}
	result := make(map[string]bool, len(options))
	for _, option := range options {
		result[option.ID] = false
	}
	for _, id := range selected {
		result[id] = true
	}
	return result, nil
}

func (p *setupPrompter) selectOne(question string, options []setupOption, defaultID string) (string, error) {
	if !p.interactive {
		return promptSingleSelect(p.reader, p.output, question, options, defaultID)
	}
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", question)
	}
	value := defaultID
	if optionIndex(options, value) == -1 {
		value = options[0].ID
	}
	huhOptions := make([]huh.Option[string], 0, len(options))
	for _, option := range options {
		huhOptions = append(huhOptions, huh.NewOption(option.Label, option.ID))
	}
	field := huh.NewSelect[string]().
		Title(question).
		Options(huhOptions...).
		Height(minInt(len(options)+3, 10)).
		Value(&value)
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(p.input).WithOutput(p.output).Run(); err != nil {
		return "", normalizePromptError(err)
	}
	return value, nil
}

func (p *setupPrompter) confirm(question string, defaultValue bool) (bool, error) {
	if !p.interactive {
		return promptBool(p.reader, p.output, question, defaultValue)
	}
	value := defaultValue
	field := huh.NewConfirm().
		Title(question).
		Affirmative("Yes").
		Negative("No").
		Value(&value)
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(p.input).WithOutput(p.output).Run(); err != nil {
		return false, normalizePromptError(err)
	}
	return value, nil
}

func normalizePromptError(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errPromptCancelled
	}
	return err
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
