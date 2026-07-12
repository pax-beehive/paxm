package zep

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	zepgo "github.com/getzep/zep-go/v3"
	zepclient "github.com/getzep/zep-go/v3/client"
	"github.com/getzep/zep-go/v3/core"
	"github.com/getzep/zep-go/v3/option"
	"github.com/pax-beehive/paxm/internal/config"
)

type EnsureUserResult struct {
	UserID  string
	Created bool
}

type userClient interface {
	Get(context.Context, string, ...option.RequestOption) (*zepgo.User, error)
	Add(context.Context, *zepgo.CreateUserRequest, ...option.RequestOption) (*zepgo.User, error)
}

func EnsureUser(ctx context.Context, cfg config.ProviderConfig) (EnsureUserResult, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return EnsureUserResult{}, errors.New("zep provider api_key is required")
	}
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		return EnsureUserResult{}, errors.New("zep provider user_id is required")
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(httpClient()),
	}
	if strings.TrimSpace(cfg.BaseURL) != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimSpace(cfg.BaseURL)))
	}
	return ensureUserWithClient(ctx, userID, zepclient.NewClient(opts...).User)
}

func ensureUserWithClient(ctx context.Context, userID string, client userClient) (EnsureUserResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return EnsureUserResult{}, errors.New("zep user_id is required")
	}
	if client == nil {
		return EnsureUserResult{}, errors.New("zep user client is required")
	}
	if _, err := client.Get(ctx, userID); err == nil {
		return EnsureUserResult{UserID: userID}, nil
	} else if !isStatusError(err, http.StatusNotFound) {
		return EnsureUserResult{}, fmt.Errorf("get zep user %q: %w", userID, err)
	}

	if _, err := client.Add(ctx, &zepgo.CreateUserRequest{UserID: userID}); err != nil {
		if isStatusError(err, http.StatusConflict) {
			return EnsureUserResult{UserID: userID}, nil
		}
		return EnsureUserResult{}, fmt.Errorf("create zep user %q: %w", userID, err)
	}
	return EnsureUserResult{UserID: userID, Created: true}, nil
}

func isStatusError(err error, status int) bool {
	var apiErr *core.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

func httpClient() *http.Client {
	return &http.Client{Timeout: defaultTimeout}
}
