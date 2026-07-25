package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	jsonrpcadapter "github.com/pax-beehive/paxm/internal/adapters/jsonrpc"
	"github.com/pax-beehive/paxm/internal/config"
	"github.com/pax-beehive/paxm/internal/memory"
	"github.com/pax-beehive/paxm/internal/paxlclient"
)

type Provisioner interface {
	ProvisionAgent(context.Context, string) (paxlclient.AgentProvision, error)
}

type typedProvisioner interface {
	ProvisionAgentWithType(context.Context, string, string) (paxlclient.AgentProvision, error)
}

type Dependencies struct {
	Provisioner      Provisioner
	CredentialDir    string
	ProvisionTimeout time.Duration
	AgentType        string
}

type Provider struct {
	mu               sync.RWMutex
	inner            *jsonrpcadapter.Provider
	baseConfig       config.ProviderConfig
	provisioner      Provisioner
	credentialDir    string
	credentialPath   string
	agentID          string
	agentType        string
	automatic        bool
	generation       uint64
	provisionTimeout time.Duration
	refreshing       *refreshAttempt
	lastRefreshGen   uint64
	lastRefreshErr   error
	agentsMu         sync.Mutex
	agents           map[string]*providerEntry
}

type refreshAttempt struct {
	generation uint64
	done       chan struct{}
	err        error
}

type providerEntry struct {
	ready    chan struct{}
	provider *Provider
	err      error
}

type cachedCredential struct {
	URL          string `json:"url"`
	APIKey       string `json:"api_key"`
	AgentID      string `json:"agent_id"`
	UserID       string `json:"user_id"`
	CredentialID string `json:"credential_id,omitempty"`
}

const defaultProvisionTimeout = 10 * time.Second

func New(name string, providerConfig config.ProviderConfig, dependencies Dependencies) (*Provider, error) {
	if explicitTeamAPIKey(providerConfig.Env) != "" {
		inner, err := jsonrpcadapter.New(name, providerConfig)
		if err != nil {
			return nil, err
		}
		return &Provider{inner: inner}, nil
	}
	agentID := firstNonEmpty(providerConfig.Env["PAXM_AGENT_ID"], os.Getenv("PAXM_AGENT_ID"))
	if agentID == "" {
		return nil, errors.New("team provider requires PAXM_AGENT_ID for device provisioning")
	}
	credentialDir := strings.TrimSpace(dependencies.CredentialDir)
	if credentialDir == "" {
		credentialDir = filepath.Join(filepath.Dir(config.DefaultConfigPath()), "credentials")
	}
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		return nil, fmt.Errorf("create team provider credential directory: %w", err)
	}
	if err := os.Chmod(credentialDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure team provider credential directory: %w", err)
	}
	provisioner := dependencies.Provisioner
	if provisioner == nil {
		client := paxlclient.New(nil)
		provisioner = client
	}
	provisionTimeout := dependencies.ProvisionTimeout
	if provisionTimeout <= 0 {
		provisionTimeout = defaultProvisionTimeout
	}
	cacheID := config.SlugID(agentID)
	if cacheID == "" {
		return nil, errors.New("team provider PAXM_AGENT_ID must contain letters or numbers")
	}
	credentialPath := filepath.Join(credentialDir, "team-"+cacheID+".json")
	credential, err := loadCredential(credentialPath)
	if err != nil {
		return nil, err
	}
	if !credential.usableFor(agentID) {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		provisioned, provisionErr := provisionAgent(
			ctx, provisioner, agentID, dependencies.AgentType,
		)
		cancel()
		if provisionErr != nil {
			return nil, fmt.Errorf(
				"team provider credentials unavailable; run `paxl device connect onprem ...` first: %w",
				provisionErr,
			)
		}
		credential = cachedCredential{
			URL: provisioned.URL, APIKey: provisioned.APIKey, AgentID: provisioned.AgentID,
			UserID: provisioned.UserID, CredentialID: provisioned.CredentialID,
		}
		if !credential.usableFor(agentID) {
			return nil, fmt.Errorf(
				"team provider provisioned credential for agent %q, want %q",
				strings.TrimSpace(credential.AgentID), agentID,
			)
		}
		if err := saveCredential(credentialPath, credential); err != nil {
			return nil, fmt.Errorf("cache team provider credential: %w", err)
		}
	}
	baseConfig := providerConfig
	providerConfig.Env = teamEnvironment(providerConfig.Env, credential)
	inner, err := jsonrpcadapter.New(name, providerConfig)
	if err != nil {
		return nil, err
	}
	return &Provider{
		inner: inner, baseConfig: baseConfig, provisioner: provisioner,
		credentialDir: credentialDir, credentialPath: credentialPath,
		agentID: agentID, agentType: strings.TrimSpace(dependencies.AgentType),
		automatic:        true,
		provisionTimeout: provisionTimeout,
	}, nil
}

func (c cachedCredential) usable() bool {
	return strings.TrimSpace(c.URL) != "" &&
		strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.AgentID) != "" &&
		strings.TrimSpace(c.UserID) != ""
}

func (c cachedCredential) usableFor(agentID string) bool {
	return c.usable() && strings.TrimSpace(c.AgentID) == strings.TrimSpace(agentID)
}

func Matches(providerConfig config.ProviderConfig) bool {
	return providerConfig.Type == "team-memory" ||
		filepath.Base(strings.TrimSpace(providerConfig.Command)) == "paxm-team-memory-provider"
}

func explicitTeamAPIKey(env map[string]string) string {
	if value, configured := env["TEAM_MEMORY_API_KEY"]; configured {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv("TEAM_MEMORY_API_KEY"))
}

func (p *Provider) Name() string {
	inner, _ := p.snapshot()
	return inner.Name()
}

func (p *Provider) Search(ctx context.Context, query memory.SearchQuery) ([]memory.MemoryHit, error) {
	inner, generation := p.snapshot()
	hits, err := inner.Search(ctx, query)
	if !p.shouldRefresh(err) {
		return hits, err
	}
	if err := p.refresh(ctx, generation); err != nil {
		return nil, err
	}
	inner, _ = p.snapshot()
	return inner.Search(ctx, query)
}

func (p *Provider) Put(ctx context.Context, item memory.MemoryItem) (memory.MemoryRef, error) {
	inner, generation := p.snapshot()
	ref, err := inner.Put(ctx, item)
	if !p.shouldRefresh(err) {
		return ref, err
	}
	if err := p.refresh(ctx, generation); err != nil {
		return memory.MemoryRef{}, err
	}
	inner, _ = p.snapshot()
	return inner.Put(ctx, item)
}

func (p *Provider) Health(ctx context.Context) error {
	inner, generation := p.snapshot()
	err := inner.Health(ctx)
	if !p.shouldRefresh(err) {
		return err
	}
	if err := p.refresh(ctx, generation); err != nil {
		return err
	}
	inner, _ = p.snapshot()
	return inner.Health(ctx)
}

func (p *Provider) PutBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	if !p.automatic {
		return p.putBatch(ctx, items)
	}
	groups := groupByAgent(items, p.agentID)
	if len(groups) == 1 && groups[0].agentID == p.agentID {
		return p.putBatch(ctx, items)
	}
	refs := make([]memory.MemoryRef, len(items))
	for _, group := range groups {
		provider, err := p.providerForAgent(ctx, group.agentID, group.agentType)
		if err != nil {
			return nil, err
		}
		groupRefs, err := provider.putBatch(ctx, group.items)
		if err != nil {
			return nil, err
		}
		if len(groupRefs) != len(group.indexes) {
			return nil, fmt.Errorf(
				"team provider %s returned %d refs for %d items",
				group.agentID, len(groupRefs), len(group.indexes),
			)
		}
		for i, index := range group.indexes {
			refs[index] = groupRefs[i]
		}
	}
	return refs, nil
}

func (p *Provider) putBatch(ctx context.Context, items []memory.MemoryItem) ([]memory.MemoryRef, error) {
	inner, generation := p.snapshot()
	refs, err := inner.PutBatch(ctx, items)
	if !p.shouldRefresh(err) {
		return refs, err
	}
	if err := p.refresh(ctx, generation); err != nil {
		return nil, err
	}
	inner, _ = p.snapshot()
	return inner.PutBatch(ctx, items)
}

type agentBatch struct {
	agentID   string
	agentType string
	items     []memory.MemoryItem
	indexes   []int
}

func groupByAgent(items []memory.MemoryItem, fallbackAgentID string) []agentBatch {
	groups := make([]agentBatch, 0, 1)
	indexByAgent := make(map[string]int)
	for index, item := range items {
		agentID := fallbackAgentID
		if item.Source == "hook:episode" {
			agentID = strings.TrimSpace(item.Origin.AgentID)
		}
		if agentID == "" || agentID == "unknown" {
			agentID = fallbackAgentID
		}
		agentType := ""
		if agentID != fallbackAgentID && item.Source == "hook:episode" {
			agentType = strings.TrimSpace(item.Metadata["hook_target"])
		}
		groupIndex, ok := indexByAgent[agentID]
		if !ok {
			groupIndex = len(groups)
			indexByAgent[agentID] = groupIndex
			groups = append(groups, agentBatch{agentID: agentID, agentType: agentType})
		} else if groups[groupIndex].agentType == "" {
			groups[groupIndex].agentType = agentType
		}
		groups[groupIndex].items = append(groups[groupIndex].items, item)
		groups[groupIndex].indexes = append(groups[groupIndex].indexes, index)
	}
	return groups
}

func (p *Provider) providerForAgent(
	ctx context.Context,
	agentID string,
	agentType string,
) (*Provider, error) {
	if agentID == p.agentID || strings.TrimSpace(agentID) == "" {
		return p, nil
	}
	p.agentsMu.Lock()
	if entry := p.agents[agentID]; entry != nil {
		p.agentsMu.Unlock()
		select {
		case <-entry.ready:
			return entry.provider, entry.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.agents == nil {
		p.agents = make(map[string]*providerEntry)
	}
	entry := &providerEntry{ready: make(chan struct{})}
	p.agents[agentID] = entry
	p.agentsMu.Unlock()

	providerConfig := p.baseConfig
	providerConfig.Env = cloneEnvironment(providerConfig.Env)
	providerConfig.Env["PAXM_AGENT_ID"] = agentID
	provider, err := New(p.Name(), providerConfig, Dependencies{
		Provisioner: p.provisioner, CredentialDir: p.credentialDir,
		ProvisionTimeout: p.provisionTimeout, AgentType: agentType,
	})

	p.agentsMu.Lock()
	entry.provider = provider
	entry.err = err
	if err != nil {
		delete(p.agents, agentID)
	}
	close(entry.ready)
	p.agentsMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("resolve team provider credential for agent %s: %w", agentID, err)
	}
	return provider, nil
}

func (p *Provider) Delete(ctx context.Context, ref memory.MemoryRef) error {
	inner, generation := p.snapshot()
	err := inner.Delete(ctx, ref)
	if !p.shouldRefresh(err) {
		return err
	}
	if err := p.refresh(ctx, generation); err != nil {
		return err
	}
	inner, _ = p.snapshot()
	return inner.Delete(ctx, ref)
}

func (p *Provider) snapshot() (*jsonrpcadapter.Provider, uint64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.inner, p.generation
}

func (p *Provider) shouldRefresh(err error) bool {
	return p.automatic && isUnauthorized(err)
}

func (p *Provider) refresh(ctx context.Context, observedGeneration uint64) error {
	p.mu.Lock()
	if p.generation != observedGeneration {
		var err error
		if p.lastRefreshGen == observedGeneration {
			err = p.lastRefreshErr
		}
		p.mu.Unlock()
		return err
	}
	if current := p.refreshing; current != nil && current.generation == observedGeneration {
		p.mu.Unlock()
		select {
		case <-current.done:
			return current.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	attempt := &refreshAttempt{generation: observedGeneration, done: make(chan struct{})}
	p.refreshing = attempt
	providerName := p.inner.Name()
	p.mu.Unlock()

	inner, err := p.reprovision(ctx, providerName)

	p.mu.Lock()
	if p.generation == observedGeneration {
		if err == nil {
			p.inner = inner
		}
		p.lastRefreshGen = observedGeneration
		p.lastRefreshErr = err
		p.generation++
	}
	attempt.err = err
	close(attempt.done)
	if p.refreshing == attempt {
		p.refreshing = nil
	}
	p.mu.Unlock()
	return err
}

func (p *Provider) reprovision(ctx context.Context, providerName string) (*jsonrpcadapter.Provider, error) {
	ctx, cancel := context.WithTimeout(ctx, p.provisionTimeout)
	defer cancel()
	provision, err := provisionAgent(ctx, p.provisioner, p.agentID, p.agentType)
	if err != nil {
		return nil, fmt.Errorf(
			"team provider credential rejected and re-provision failed; run `paxl device connect onprem ...` first: %w",
			err,
		)
	}
	credential := cachedCredential{
		URL: provision.URL, APIKey: provision.APIKey, AgentID: provision.AgentID,
		UserID: provision.UserID, CredentialID: provision.CredentialID,
	}
	if !credential.usableFor(p.agentID) {
		return nil, fmt.Errorf(
			"team provider re-provisioned credential for agent %q, want %q",
			strings.TrimSpace(credential.AgentID), p.agentID,
		)
	}
	if err := saveCredential(p.credentialPath, credential); err != nil {
		return nil, fmt.Errorf("cache re-provisioned team credential: %w", err)
	}
	providerConfig := p.baseConfig
	providerConfig.Env = teamEnvironment(providerConfig.Env, credential)
	inner, err := jsonrpcadapter.New(providerName, providerConfig)
	if err != nil {
		return nil, fmt.Errorf("reload team provider credential: %w", err)
	}
	return inner, nil
}

func provisionAgent(
	ctx context.Context,
	provisioner Provisioner,
	agentID string,
	agentType string,
) (paxlclient.AgentProvision, error) {
	if agentType = strings.TrimSpace(agentType); agentType != "" {
		if typed, ok := provisioner.(typedProvisioner); ok {
			return typed.ProvisionAgentWithType(ctx, agentID, agentType)
		}
	}
	return provisioner.ProvisionAgent(ctx, agentID)
}

func isUnauthorized(err error) bool {
	var rpcErr *jsonrpcadapter.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Code == 401 || rpcErrorStatus(rpcErr.Data) == 401 {
		return true
	}
	if jsonrpcadapter.StderrIndicatesUnauthorized(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "returned 401") ||
		strings.Contains(message, "status 401")
}

func rpcErrorStatus(data json.RawMessage) int {
	if len(data) == 0 {
		return 0
	}
	var status struct {
		Status     int `json:"status"`
		StatusCode int `json:"status_code"`
	}
	if json.Unmarshal(data, &status) == nil {
		if status.StatusCode != 0 {
			return status.StatusCode
		}
		return status.Status
	}
	var code int
	_ = json.Unmarshal(data, &code)
	return code
}

func teamEnvironment(existing map[string]string, credential cachedCredential) map[string]string {
	env := make(map[string]string, len(existing)+4)
	for key, value := range existing {
		env[key] = value
	}
	env["TEAM_MEMORY_API_KEY"] = credential.APIKey
	env["TEAM_MEMORY_BASE_URL"] = credential.URL
	env["PAXM_USER_ID"] = credential.UserID
	env["PAXM_AGENT_ID"] = credential.AgentID
	return env
}

func cloneEnvironment(existing map[string]string) map[string]string {
	env := make(map[string]string, len(existing)+1)
	for key, value := range existing {
		env[key] = value
	}
	return env
}

func loadCredential(path string) (cachedCredential, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return cachedCredential{}, nil
	}
	if err != nil {
		return cachedCredential{}, fmt.Errorf("open team provider credential cache: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0o600); err != nil {
		return cachedCredential{}, fmt.Errorf("secure team provider credential cache: %w", err)
	}
	var credential cachedCredential
	if err := json.NewDecoder(file).Decode(&credential); err != nil {
		return cachedCredential{}, fmt.Errorf("decode team provider credential cache: %w", err)
	}
	return credential, nil
}

func saveCredential(path string, credential cachedCredential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".team-credential-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(credential); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceCredentialFile(tempPath, path, os.Rename); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func replaceCredentialFile(tempPath, path string, rename func(string, string) error) error {
	if err := rename(tempPath, path); err == nil {
		return nil
	}
	backup, err := os.CreateTemp(filepath.Dir(path), ".team-credential-backup-*.tmp")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	removeBackup := true
	defer func() {
		if removeBackup {
			_ = os.Remove(backupPath)
		}
	}()
	if err := backup.Close(); err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := rename(path, backupPath); err != nil {
		return fmt.Errorf("preserve existing team credential: %w", err)
	}
	if err := rename(tempPath, path); err != nil {
		if restoreErr := rename(backupPath, path); restoreErr != nil {
			removeBackup = false
			return errors.Join(
				fmt.Errorf("install replacement team credential: %w", err),
				fmt.Errorf("restore existing team credential from %s: %w", backupPath, restoreErr),
			)
		}
		return fmt.Errorf("install replacement team credential: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("remove replaced team credential backup: %w", err)
	}
	removeBackup = false
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
