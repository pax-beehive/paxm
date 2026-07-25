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

type Dependencies struct {
	Provisioner      Provisioner
	CredentialDir    string
	ProvisionTimeout time.Duration
}

type Provider struct {
	mu               sync.RWMutex
	inner            *jsonrpcadapter.Provider
	baseConfig       config.ProviderConfig
	provisioner      Provisioner
	credentialPath   string
	agentID          string
	automatic        bool
	generation       uint64
	provisionTimeout time.Duration
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
	if !credential.usable() {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		provisioned, provisionErr := provisioner.ProvisionAgent(ctx, agentID)
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
		credentialPath: credentialPath, agentID: agentID, automatic: true,
		provisionTimeout: provisionTimeout,
	}, nil
}

func (c cachedCredential) usable() bool {
	return strings.TrimSpace(c.URL) != "" &&
		strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.AgentID) != "" &&
		strings.TrimSpace(c.UserID) != ""
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
	return p.inner.Name()
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
	defer p.mu.Unlock()
	if p.generation != observedGeneration {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.provisionTimeout)
	defer cancel()
	provision, err := p.provisioner.ProvisionAgent(ctx, p.agentID)
	if err != nil {
		return fmt.Errorf(
			"team provider credential rejected and re-provision failed; run `paxl device connect onprem ...` first: %w",
			err,
		)
	}
	credential := cachedCredential{
		URL: provision.URL, APIKey: provision.APIKey, AgentID: provision.AgentID,
		UserID: provision.UserID, CredentialID: provision.CredentialID,
	}
	if err := saveCredential(p.credentialPath, credential); err != nil {
		return fmt.Errorf("cache re-provisioned team credential: %w", err)
	}
	providerConfig := p.baseConfig
	providerConfig.Env = teamEnvironment(providerConfig.Env, credential)
	inner, err := jsonrpcadapter.New(p.inner.Name(), providerConfig)
	if err != nil {
		return fmt.Errorf("reload team provider credential: %w", err)
	}
	p.inner = inner
	p.generation++
	return nil
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
	env["TEAM_MEMORY_BASE_URL"] = firstNonEmpty(env["TEAM_MEMORY_BASE_URL"], credential.URL)
	env["PAXM_USER_ID"] = firstNonEmpty(env["PAXM_USER_ID"], credential.UserID)
	env["PAXM_AGENT_ID"] = firstNonEmpty(env["PAXM_AGENT_ID"], credential.AgentID)
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
