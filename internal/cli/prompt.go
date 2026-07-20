package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

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

func defaultSelections(options []setupOption, selected map[string]bool) map[string]bool {
	normalized := make(map[string]bool)
	for _, option := range options {
		normalized[option.ID] = selected[option.ID]
	}
	return normalized
}

func anySelected(selected map[string]bool) bool {
	for _, enabled := range selected {
		if enabled {
			return true
		}
	}
	return false
}

func sortedSelected(selected map[string]bool) []string {
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func promptBool(reader *bufio.Reader, writer io.Writer, question string, defaultValue bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultValue {
		suffix = " [Y/n]: "
	}
	for {
		_, _ = fmt.Fprint(writer, question+suffix)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		value := strings.ToLower(strings.TrimSpace(line))
		if value == "" {
			return defaultValue, nil
		}
		switch value {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(writer, "Please answer yes or no.")
		}
		if errors.Is(err, io.EOF) {
			return defaultValue, nil
		}
	}
}

func promptSingleSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaultID string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("%s has no options", question)
	}
	defaultIndex := optionIndex(options, defaultID)
	if defaultIndex == -1 && len(options) > 0 {
		defaultIndex = 0
		defaultID = options[0].ID
	}
	for {
		_, _ = fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if i == defaultIndex {
				marker = "[x]"
			}
			_, _ = fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		_, _ = fmt.Fprintf(writer, "Choose one [%d]: ", defaultIndex+1)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultID, nil
		}
		index, parseErr := strconv.Atoi(value)
		if parseErr == nil && index >= 1 && index <= len(options) {
			return options[index-1].ID, nil
		}
		for _, option := range options {
			if strings.EqualFold(value, option.ID) {
				return option.ID, nil
			}
		}
		_, _ = fmt.Fprintln(writer, "Please choose one of the listed options.")
		if errors.Is(err, io.EOF) {
			return defaultID, nil
		}
	}
}

func promptMultiSelect(reader *bufio.Reader, writer io.Writer, question string, options []setupOption, defaults map[string]bool) (map[string]bool, error) {
	if len(options) == 0 {
		return map[string]bool{}, nil
	}
	defaultText := defaultSelectionText(options, defaults)
	for {
		_, _ = fmt.Fprintln(writer, question+":")
		for i, option := range options {
			marker := "[ ]"
			if defaults[option.ID] {
				marker = "[x]"
			}
			_, _ = fmt.Fprintf(writer, "  %d) %s %s\n", i+1, marker, option.Label)
		}
		_, _ = fmt.Fprintf(writer, "Choose numbers, comma-separated, or all/none [%s]: ", defaultText)
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		value := strings.TrimSpace(line)
		if value == "" {
			return defaultSelections(options, defaults), nil
		}
		selected, parseErr := parseMultiSelect(value, options)
		if parseErr == nil {
			return selected, nil
		}
		_, _ = fmt.Fprintf(writer, "%s\n", parseErr)
		if errors.Is(err, io.EOF) {
			return defaultSelections(options, defaults), nil
		}
	}
}

func parseMultiSelect(value string, options []setupOption) (map[string]bool, error) {
	selected := make(map[string]bool)
	for _, option := range options {
		selected[option.ID] = false
	}
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "all":
		for _, option := range options {
			selected[option.ID] = true
		}
		return selected, nil
	case "none":
		return selected, nil
	}
	parts := strings.Split(value, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 1 || index > len(options) {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		selected[options[index-1].ID] = true
	}
	return selected, nil
}

func defaultSelectionText(options []setupOption, defaults map[string]bool) string {
	var indexes []string
	for i, option := range options {
		if defaults[option.ID] {
			indexes = append(indexes, strconv.Itoa(i+1))
		}
	}
	if len(indexes) == 0 {
		return "none"
	}
	return strings.Join(indexes, ",")
}

func optionIndex(options []setupOption, id string) int {
	for i, option := range options {
		if option.ID == id {
			return i
		}
	}
	return -1
}

func promptString(reader *bufio.Reader, writer io.Writer, question, defaultValue string) (string, error) {
	_, _ = fmt.Fprintf(writer, "%s [%s]: ", question, defaultValue)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}
