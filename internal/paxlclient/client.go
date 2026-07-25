package paxlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type RunFunc func(context.Context, ...string) ([]byte, error)

type Client struct {
	run RunFunc
}

type AgentProvision struct {
	URL          string   `json:"url"`
	APIKey       string   `json:"api_key"`
	AgentID      string   `json:"agent_id"`
	UserID       string   `json:"user_id"`
	CredentialID string   `json:"credential_id"`
	Permissions  []string `json:"permissions,omitempty"`
}

func New(run RunFunc) Client {
	if run == nil {
		run = runPaxl
	}
	return Client{run: run}
}

func (c Client) ProvisionAgent(ctx context.Context, agentID string) (AgentProvision, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return AgentProvision{}, errors.New("paxl device provision requires an agent ID")
	}
	output, err := c.run(ctx, "device", "provision", "--agent", agentID, "--json")
	if err != nil {
		return AgentProvision{}, fmt.Errorf("run paxl device provision: %w", err)
	}
	var provision AgentProvision
	if err := json.Unmarshal(output, &provision); err != nil {
		return AgentProvision{}, fmt.Errorf("decode paxl device provision output: %w", err)
	}
	if strings.TrimSpace(provision.APIKey) == "" ||
		strings.TrimSpace(provision.URL) == "" ||
		strings.TrimSpace(provision.UserID) == "" {
		return AgentProvision{}, errors.New("paxl device provision returned incomplete credentials")
	}
	if strings.TrimSpace(provision.AgentID) == "" {
		provision.AgentID = agentID
	}
	return provision, nil
}

func (c Client) OnPremUserID(ctx context.Context) (string, error) {
	deviceOutput, deviceErr := c.run(ctx, "device", "status", "--format", "jsonl")
	if deviceErr == nil {
		if userID := deviceStatusUserID(deviceOutput); userID != "" {
			return userID, nil
		}
	}
	output, err := c.run(ctx, "channel", "status", "onprem", "--format", "jsonl")
	if err != nil {
		if deviceErr != nil {
			return "", fmt.Errorf("read paxl on-prem identity: device status: %v; channel status: %w", deviceErr, err)
		}
		return "", fmt.Errorf("read paxl on-prem identity: device status has no user ID; channel status: %w", err)
	}
	userID, err := channelStatusUserID(output)
	if err != nil {
		return "", fmt.Errorf("decode paxl on-prem channel status: %w", err)
	}
	if userID == "" {
		return "", errors.New("paxl on-prem channel status has no user ID")
	}
	return userID, nil
}

func deviceStatusUserID(output []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var status struct {
			UserID string `json:"user_id"`
		}
		if err := decoder.Decode(&status); err != nil {
			return ""
		}
		if userID := strings.TrimSpace(status.UserID); userID != "" {
			return userID
		}
	}
}

func channelStatusUserID(output []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var status struct {
			Profile struct {
				UserID string `json:"user_id"`
			} `json:"profile"`
		}
		if err := decoder.Decode(&status); err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", err
		}
		if userID := strings.TrimSpace(status.Profile.UserID); userID != "" {
			return userID, nil
		}
	}
}

func runPaxl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "paxl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message == "" {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %s", err, message)
}
