// Package apikeys provides handlers for managing API keys.
package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
)

const (
	maxKeysPerUser = 10
	keyPrefix      = "jrag_"
	keyRandomBytes = 16
)

// ApiKeyRow is the shape returned to API consumers (no key_hash).
type ApiKeyRow struct {
	ID         string     `json:"id"         db:"id"`
	Name       string     `json:"name"       db:"name"`
	KeyPrefix  string     `json:"keyPrefix"  db:"key_prefix"`
	LastUsedAt *time.Time `json:"lastUsedAt" db:"last_used_at"`
	ExpiresAt  *time.Time `json:"expiresAt"  db:"expires_at"`
	CreatedAt  time.Time  `json:"createdAt"  db:"created_at"`
}

// ApiKeyCreate holds the data required to persist a new API key.
type ApiKeyCreate struct {
	UserID    string
	Name      string
	KeyHash   string
	KeyPrefix string
	ExpiresAt *time.Time
}

// Store is the database interface required by Handler.
type Store interface {
	CreateApiKey(ctx context.Context, data ApiKeyCreate) (*ApiKeyRow, error)
	GetApiKeysByUser(ctx context.Context, userID string) ([]ApiKeyRow, error)
	CountApiKeysByUser(ctx context.Context, userID string) (int, error)
	DeleteApiKey(ctx context.Context, id, userID string) (bool, error)
}

// Handler holds the Store dependency for the API key endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a Handler using the given Store implementation.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// CreateApiKey implements POST /api/api-keys. Name presence/length is enforced
// by the spec middleware.
func (h *Handler) CreateApiKey(ctx context.Context, request api.CreateApiKeyRequestObject) (api.CreateApiKeyResponseObject, error) {
	user := auth.UserFromContext(ctx)
	if user == nil {
		return api.CreateApiKey401JSONResponse{
			UnauthorizedJSONResponse: api.UnauthorizedJSONResponse{Error: "authentication required"},
		}, nil
	}

	name := strings.TrimSpace(request.Body.Name)
	if name == "" {
		return api.CreateApiKey400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{Error: "name is required and must be 1-100 characters"},
		}, nil
	}

	count, err := h.store.CountApiKeysByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if count >= maxKeysPerUser {
		return api.CreateApiKey400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{Error: "maximum number of API keys (10) reached"},
		}, nil
	}

	// Generate key: "jrag_" + 32 hex chars (16 random bytes).
	rawBytes := make([]byte, keyRandomBytes)
	if _, err := rand.Read(rawBytes); err != nil {
		return nil, err
	}
	plaintext := keyPrefix + hex.EncodeToString(rawBytes)
	prefix := plaintext[:13]

	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), 10)
	if err != nil {
		return nil, err
	}

	row, err := h.store.CreateApiKey(ctx, ApiKeyCreate{
		UserID:    user.ID,
		Name:      name,
		KeyHash:   string(hash),
		KeyPrefix: prefix,
		ExpiresAt: request.Body.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}

	return api.CreateApiKey201JSONResponse{
		Id:         row.ID,
		Name:       row.Name,
		Key:        plaintext,
		KeyPrefix:  row.KeyPrefix,
		LastUsedAt: row.LastUsedAt,
		ExpiresAt:  row.ExpiresAt,
		CreatedAt:  row.CreatedAt,
	}, nil
}

// ListApiKeys implements GET /api/api-keys.
func (h *Handler) ListApiKeys(ctx context.Context, _ api.ListApiKeysRequestObject) (api.ListApiKeysResponseObject, error) {
	user := auth.UserFromContext(ctx)
	if user == nil {
		return api.ListApiKeys401JSONResponse{
			UnauthorizedJSONResponse: api.UnauthorizedJSONResponse{Error: "authentication required"},
		}, nil
	}

	rows, err := h.store.GetApiKeysByUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	keys := make([]api.ApiKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, api.ApiKey{
			Id:         row.ID,
			Name:       row.Name,
			KeyPrefix:  row.KeyPrefix,
			LastUsedAt: row.LastUsedAt,
			ExpiresAt:  row.ExpiresAt,
			CreatedAt:  row.CreatedAt,
		})
	}
	return api.ListApiKeys200JSONResponse(keys), nil
}

// DeleteApiKey implements DELETE /api/api-keys/{id}.
func (h *Handler) DeleteApiKey(ctx context.Context, request api.DeleteApiKeyRequestObject) (api.DeleteApiKeyResponseObject, error) {
	user := auth.UserFromContext(ctx)
	if user == nil {
		return api.DeleteApiKey401JSONResponse{
			UnauthorizedJSONResponse: api.UnauthorizedJSONResponse{Error: "authentication required"},
		}, nil
	}

	deleted, err := h.store.DeleteApiKey(ctx, request.Id, user.ID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return api.DeleteApiKey404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{Error: "API key not found"},
		}, nil
	}
	return api.DeleteApiKey204Response{}, nil
}
