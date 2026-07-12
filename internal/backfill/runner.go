package backfill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pax-beehive/paxm/internal/sessions"
	"github.com/pax-beehive/paxm/internal/tools"
)

const maxItemBytes = 24 * 1024

type Runner struct {
	Store   *Store
	Service interface {
		RememberBatchToProvider(context.Context, string, tools.RememberBatchInput) (tools.RememberResult, error)
	}
	ProcessID func() int
}

type RunOptions struct {
	Scope        string
	RunID        string
	Mode         string
	Agent        string
	Provider     string
	Files        []sessions.File
	Cutoff       time.Time
	RateInterval time.Duration
	Progress     func(Status)
	Started      func(Status)
}

func NewRunID() string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return hex.EncodeToString(bytes[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (r Runner) Run(ctx context.Context, options RunOptions) (Status, error) {
	if r.Store == nil || r.Service == nil {
		return Status{}, errors.New("backfill runner is not configured")
	}
	if options.RunID == "" {
		options.RunID = NewRunID()
	}
	release, err := r.Store.Acquire(options.Scope, options.RunID)
	if err != nil {
		return Status{}, err
	}
	defer release()

	status := r.initialStatus(options)
	if err := r.publish(options, status); err != nil {
		return status, err
	}
	if options.Started != nil {
		options.Started(status)
	}

	operationErrors, err := r.processBackfillFiles(ctx, options, &status)
	if err != nil {
		return status, err
	}
	return r.finishBackfillRun(options, status, operationErrors)
}

func (r Runner) initialStatus(options RunOptions) Status {
	status := Status{
		State:      "running",
		Mode:       firstNonEmpty(options.Mode, "foreground"),
		RunID:      options.RunID,
		PID:        r.processID(),
		Agent:      options.Agent,
		Provider:   options.Provider,
		StartedAt:  time.Now().UTC(),
		TotalFiles: len(options.Files),
	}
	for _, file := range options.Files {
		status.TotalBytes += file.Size
	}
	return status
}

func (r Runner) processID() int {
	if r.ProcessID != nil {
		return r.ProcessID()
	}
	return processID()
}

func (r Runner) processBackfillFiles(ctx context.Context, options RunOptions, status *Status) ([]error, error) {
	var operationErrors []error
	var nextUpload time.Time
	for _, file := range options.Files {
		fileErrors, err := r.processBackfillFile(ctx, options, status, file, &nextUpload)
		operationErrors = append(operationErrors, fileErrors...)
		if err != nil {
			return operationErrors, err
		}
	}
	return operationErrors, nil
}

func (r Runner) processBackfillFile(ctx context.Context, options RunOptions, status *Status, file sessions.File, nextUpload *time.Time) ([]error, error) {
	if err := ctx.Err(); err != nil {
		return nil, r.pauseBackfill(options, status, err)
	}
	turns, readErr := sessions.ReadFile(options.Agent, file.Path, options.Cutoff)
	fileStartBytes := status.ProcessedBytes
	if readErr != nil {
		status.Failed++
		return []error{readErr}, r.finishBackfillFile(options, status, fileStartBytes, file.Size)
	}

	items := ingestItems(turns)
	status.Discovered += len(items)
	_ = r.publish(options, *status)
	operationErrors, err := r.processBackfillItems(ctx, options, status, fileProgress{startBytes: fileStartBytes, size: file.Size, total: len(items)}, items, nextUpload)
	if err != nil {
		return operationErrors, err
	}
	return operationErrors, r.finishBackfillFile(options, status, fileStartBytes, file.Size)
}

func ingestItems(turns []sessions.Turn) []tools.RememberInput {
	var items []tools.RememberInput
	for _, turn := range turns {
		items = append(items, turnItems(turn)...)
	}
	return items
}

type fileProgress struct {
	startBytes int64
	size       int64
	total      int
}

func (r Runner) processBackfillItems(ctx context.Context, options RunOptions, status *Status, progress fileProgress, items []tools.RememberInput, nextUpload *time.Time) ([]error, error) {
	var operationErrors []error
	for index, item := range items {
		operationErr, err := r.processBackfillItem(ctx, options, status, progress, index, item, nextUpload)
		if operationErr != nil {
			operationErrors = append(operationErrors, operationErr)
		}
		if err != nil {
			return operationErrors, err
		}
	}
	return operationErrors, nil
}

func (r Runner) processBackfillItem(ctx context.Context, options RunOptions, status *Status, progress fileProgress, index int, item tools.RememberInput, nextUpload *time.Time) (error, error) {
	done, checkErr := r.Store.Succeeded(options.Scope, item.ID)
	if checkErr != nil {
		return nil, checkErr
	}
	processedBytes := progress.itemBytes(index)
	if done {
		status.Skipped++
		status.ProcessedBytes = processedBytes
		_ = r.publish(options, *status)
		return nil, nil
	}
	if err := waitForNextUpload(ctx, *nextUpload, options.RateInterval); err != nil {
		return nil, r.pauseBackfill(options, status, err)
	}
	result, ingestErr := r.Service.RememberBatchToProvider(ctx, options.Provider, tools.RememberBatchInput{Items: []tools.RememberInput{item}})
	*nextUpload = time.Now().Add(options.RateInterval)
	if ingestErr != nil {
		status.Failed++
		status.ProcessedBytes = processedBytes
		_ = r.publish(options, *status)
		return fmt.Errorf("%s: %w", item.ID, ingestErr), nil
	}
	if err := r.Store.MarkSucceeded(options.Scope, item.ID, firstProviderRef(result)); err != nil {
		return nil, err
	}
	status.Uploaded++
	status.ProcessedBytes = processedBytes
	_ = r.publish(options, *status)
	return nil, nil
}

func (progress fileProgress) itemBytes(index int) int64 {
	if progress.total == 0 {
		return progress.startBytes + progress.size
	}
	return progress.startBytes + progress.size*int64(index+1)/int64(progress.total)
}

func waitForNextUpload(ctx context.Context, nextUpload time.Time, interval time.Duration) error {
	wait := time.Until(nextUpload)
	if interval <= 0 || wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r Runner) finishBackfillFile(options RunOptions, status *Status, fileStartBytes, fileSize int64) error {
	status.ProcessedFiles++
	status.ProcessedBytes = fileStartBytes + fileSize
	updateRates(status)
	return r.publish(options, *status)
}

func (r Runner) pauseBackfill(options RunOptions, status *Status, err error) error {
	status.State = "paused"
	status.FinishedAt = time.Now().UTC()
	_ = r.publish(options, *status)
	return err
}

func firstProviderRef(result tools.RememberResult) string {
	if len(result.Refs) == 0 {
		return ""
	}
	return result.Refs[0].ID
}

func (r Runner) finishBackfillRun(options RunOptions, status Status, operationErrors []error) (Status, error) {
	status.FinishedAt = time.Now().UTC()
	status.State = "completed"
	if len(operationErrors) > 0 {
		status.State = "completed_with_errors"
		status.Error = errors.Join(operationErrors...).Error()
	}
	updateRates(&status)
	if err := r.publish(options, status); err != nil {
		return status, err
	}
	return status, errors.Join(operationErrors...)
}

func (r Runner) publish(options RunOptions, status Status) error {
	updateRates(&status)
	if err := r.Store.WriteStatus(options.Scope, status); err != nil {
		return err
	}
	if options.Progress != nil {
		options.Progress(status)
	}
	return nil
}

func updateRates(status *Status) {
	elapsed := time.Since(status.StartedAt)
	if elapsed <= 0 {
		return
	}
	status.ItemsPerSecond = float64(status.Uploaded+status.Skipped) / elapsed.Seconds()
	status.BytesPerSecond = float64(status.ProcessedBytes) / elapsed.Seconds()
	remaining := status.TotalBytes - status.ProcessedBytes
	if remaining > 0 && status.BytesPerSecond > 0 {
		status.ETASeconds = int64(float64(remaining) / status.BytesPerSecond)
	} else {
		status.ETASeconds = 0
	}
}

func turnItems(turn sessions.Turn) []tools.RememberInput {
	header := fmt.Sprintf("Historical %s agent session turn.\n\nUser:\n%s\n\nAssistant:\n", turn.Agent, turn.User)
	text := header + turn.Assistant
	parts := splitUTF8(text, maxItemBytes)
	items := make([]tools.RememberInput, 0, len(parts))
	for index, part := range parts {
		id := turn.ID
		if len(parts) > 1 {
			id += "-part-" + strconv.Itoa(index+1)
		}
		metadata := map[string]string{
			"backfill":   "true",
			"agent":      turn.Agent,
			"session_id": turn.SessionID,
			"workspace":  turn.Workspace,
		}
		if len(parts) > 1 {
			metadata["part"] = strconv.Itoa(index + 1)
			metadata["parts"] = strconv.Itoa(len(parts))
		}
		items = append(items, tools.RememberInput{
			ID:        id,
			Text:      part,
			Source:    "backfill:" + turn.Agent,
			Metadata:  metadata,
			CreatedAt: turn.CreatedAt,
		})
	}
	return items
}

func splitUTF8(value string, size int) []string {
	if len(value) <= size {
		return []string{value}
	}
	var parts []string
	for len(value) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(value[cut]) {
			cut--
		}
		if cut == 0 {
			cut = size
		}
		parts = append(parts, strings.TrimSpace(value[:cut]))
		value = value[cut:]
	}
	if strings.TrimSpace(value) != "" {
		parts = append(parts, strings.TrimSpace(value))
	}
	return parts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
