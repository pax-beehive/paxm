package sessions

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Turn struct {
	ID        string
	Sequence  int64
	Agent     string
	SessionID string
	Workspace string
	User      string
	Assistant string
	CreatedAt time.Time
}

// TimeRange selects turns at or after After and strictly before Before.
// A zero boundary is unbounded.
type TimeRange struct {
	After  time.Time
	Before time.Time
}

func (r TimeRange) Includes(value time.Time) bool {
	if !r.After.IsZero() && value.Before(r.After) {
		return false
	}
	return r.Before.IsZero() || value.Before(r.Before)
}

type File struct {
	Path string
	Size int64
}

type record struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	ID          string          `json:"id"`
	ParentID    string          `json:"parentId"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	Payload     json.RawMessage `json:"payload"`
	Message     json.RawMessage `json:"message"`
}

type codexPayload struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	CWD     string `json:"cwd"`
	Message string `json:"message"`
	Phase   string `json:"phase"`
}

type messagePayload struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func Discover(agent string) ([]File, error) {
	root, err := Root(agent)
	if err != nil {
		return nil, err
	}
	var files []File
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, File{Path: path, Size: info.Size()})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func Root(agent string) (string, error) {
	home, _ := os.UserHomeDir()
	switch normalizeAgent(agent) {
	case "codex":
		if root := os.Getenv("PAXM_CODEX_SESSIONS"); root != "" {
			return root, nil
		}
		if root := os.Getenv("CODEX_HOME"); root != "" {
			return filepath.Join(root, "sessions"), nil
		}
		return filepath.Join(home, ".codex", "sessions"), nil
	case "claude":
		if root := os.Getenv("PAXM_CLAUDE_SESSIONS"); root != "" {
			return root, nil
		}
		if root := os.Getenv("CLAUDE_CONFIG_DIR"); root != "" {
			return filepath.Join(root, "projects"), nil
		}
		return filepath.Join(home, ".claude", "projects"), nil
	case "pi":
		if root := os.Getenv("PAXM_PI_SESSIONS"); root != "" {
			return root, nil
		}
		if root := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); root != "" {
			return root, nil
		}
		if root := os.Getenv("PI_CODING_AGENT_DIR"); root != "" {
			return filepath.Join(root, "sessions"), nil
		}
		return filepath.Join(home, ".pi", "agent", "sessions"), nil
	default:
		return "", fmt.Errorf("unsupported session agent %q", agent)
	}
}

func ReadFile(agent, path string, window TimeRange) ([]Turn, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var records []record
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		var entry record
		if json.Unmarshal(line, &entry) == nil {
			if compact, ok := compactRecord(normalizeAgent(agent), entry); ok {
				records = append(records, compact)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read session %s: %w", path, readErr)
		}
	}

	info, _ := file.Stat()
	fallback := time.Time{}
	if info != nil {
		fallback = info.ModTime().UTC()
	}
	switch normalizeAgent(agent) {
	case "codex":
		return readCodex(records, window, fallback), nil
	case "claude":
		return readMessages("claude", records, window, fallback), nil
	case "pi":
		return readMessages("pi", records, window, fallback), nil
	default:
		return nil, fmt.Errorf("unsupported session agent %q", agent)
	}
}

func compactRecord(agent string, entry record) (record, bool) {
	if agent == "codex" {
		if entry.Type != "session_meta" && entry.Type != "event_msg" {
			return record{}, false
		}
		var payload codexPayload
		if json.Unmarshal(entry.Payload, &payload) != nil {
			return record{}, false
		}
		if entry.Type == "event_msg" && payload.Type != "user_message" && payload.Type != "agent_message" {
			return record{}, false
		}
		entry.Payload, _ = json.Marshal(payload)
		return entry, true
	}
	if agent == "pi" && entry.Type == "session" {
		entry.Message = nil
		return entry, true
	}
	if entry.IsSidechain || (entry.Type != "user" && entry.Type != "assistant" && entry.Type != "message") {
		return record{}, false
	}
	var message messagePayload
	if json.Unmarshal(entry.Message, &message) != nil {
		return record{}, false
	}
	text := extractText(message.Content)
	if text == "" || (message.Role != "user" && message.Role != "assistant") {
		return record{}, false
	}
	message.Content, _ = json.Marshal(text)
	entry.Message, _ = json.Marshal(message)
	return entry, true
}

func readCodex(records []record, window TimeRange, fallback time.Time) []Turn {
	var sessionID, workspace string
	var pending *Turn
	var turns []Turn
	sequences := map[string]int64{}
	for _, entry := range records {
		var payload codexPayload
		if len(entry.Payload) == 0 || json.Unmarshal(entry.Payload, &payload) != nil {
			continue
		}
		if entry.Type == "session_meta" {
			sessionID = payload.ID
			workspace = payload.CWD
			continue
		}
		if entry.Type != "event_msg" {
			continue
		}
		switch payload.Type {
		case "user_message":
			if strings.TrimSpace(payload.Message) == "" {
				continue
			}
			pending = &Turn{Agent: "codex", SessionID: sessionID, Workspace: workspace, User: strings.TrimSpace(payload.Message), CreatedAt: parseTime(entry.Timestamp, fallback)}
		case "agent_message":
			if pending == nil || payload.Phase == "commentary" || strings.TrimSpace(payload.Message) == "" {
				continue
			}
			pending.Assistant = strings.TrimSpace(payload.Message)
			appendTurn(&turns, *pending, window, sequences)
			pending = nil
		}
	}
	return turns
}

func readMessages(agent string, records []record, window TimeRange, fallback time.Time) []Turn {
	reader := messageReader{agent: agent, window: window, fallback: fallback, sequences: map[string]int64{}}
	for _, entry := range records {
		reader.observe(entry)
	}
	return reader.finish()
}

type messageReader struct {
	agent     string
	sessionID string
	workspace string
	pending   *Turn
	turns     []Turn
	window    TimeRange
	fallback  time.Time
	sequences map[string]int64
}

func (r *messageReader) observe(entry record) {
	if r.observeSession(entry) {
		return
	}
	if entry.IsSidechain || (entry.Type != "user" && entry.Type != "assistant" && entry.Type != "message") {
		return
	}
	var message messagePayload
	if json.Unmarshal(entry.Message, &message) != nil {
		return
	}
	text := extractText(message.Content)
	if text == "" {
		return
	}
	r.updateContext(entry)
	switch message.Role {
	case "user":
		r.startUserTurn(text, entry.Timestamp)
	case "assistant":
		r.appendAssistant(text)
	}
}

func (r *messageReader) observeSession(entry record) bool {
	if r.agent != "pi" || entry.Type != "session" {
		return false
	}
	r.sessionID = entry.ID
	r.workspace = entry.CWD
	return true
}

func (r *messageReader) updateContext(entry record) {
	if entry.SessionID != "" {
		r.sessionID = entry.SessionID
	}
	if entry.CWD != "" {
		r.workspace = entry.CWD
	}
}

func (r *messageReader) startUserTurn(text, timestamp string) {
	if r.pending != nil {
		appendTurn(&r.turns, *r.pending, r.window, r.sequences)
	}
	r.pending = &Turn{Agent: r.agent, SessionID: r.sessionID, Workspace: r.workspace, User: text, CreatedAt: parseTime(timestamp, r.fallback)}
}

func (r *messageReader) appendAssistant(text string) {
	if r.pending == nil {
		return
	}
	if r.pending.Assistant == "" {
		r.pending.Assistant = text
		return
	}
	r.pending.Assistant += "\n\n" + text
}

func (r *messageReader) finish() []Turn {
	if r.pending != nil {
		appendTurn(&r.turns, *r.pending, r.window, r.sequences)
	}
	return r.turns
}

func extractText(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return strings.TrimSpace(plain)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func appendTurn(turns *[]Turn, turn Turn, window TimeRange, sequences map[string]int64) {
	if strings.TrimSpace(turn.User) == "" || strings.TrimSpace(turn.Assistant) == "" {
		return
	}
	sequences[turn.SessionID]++
	turn.Sequence = sequences[turn.SessionID]
	if !window.Includes(turn.CreatedAt) {
		return
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{turn.Agent, turn.SessionID, turn.CreatedAt.UTC().Format(time.RFC3339Nano), turn.User, turn.Assistant}, "\x00")))
	turn.ID = "backfill-" + hex.EncodeToString(hash[:16])
	*turns = append(*turns, turn)
}

func parseTime(value string, fallback time.Time) time.Time {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	return fallback.UTC()
}

func normalizeAgent(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "claude-code", "claude_code", "claudecode":
		return "claude"
	default:
		return strings.ToLower(strings.TrimSpace(agent))
	}
}
