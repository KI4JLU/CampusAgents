package authhandler

import (
	"context"
	"os"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

// localAuthEnabled reports whether the username/password form should be offered.
// It is auto-disabled whenever an OIDC provider is active (so a configured IdP
// becomes the only method), and can be force-disabled with DISABLE_LOCAL_AUTH=true.
// When disabled, local password login is rejected for everyone — there is no
// breakglass.
func localAuthEnabled(hasOIDC bool) bool {
	if os.Getenv("DISABLE_LOCAL_AUTH") == "true" {
		return false
	}
	return !hasOIDC
}

// ListAuthProviders implements GET /api/auth/providers (public): the no-secret
// projection of the auth_providers rows the login form needs to decide which
// buttons to render. The response intentionally excludes the config JSONB so
// client_secret and bindCredentials can't leak.
func (h *Handler) ListAuthProviders(ctx context.Context, _ api.ListAuthProvidersRequestObject) (api.ListAuthProvidersResponseObject, error) {
	rows, err := h.store.GetActiveAuthProviders(ctx)
	if err != nil {
		logctx.From(ctx).Error("list public providers", "err", err)
		return api.ListAuthProviders500JSONResponse{
			InternalErrorJSONResponse: api.InternalErrorJSONResponse{Error: "Internal server error"},
		}, nil
	}

	providers := make([]api.PublicProvider, 0, len(rows))
	hasOIDC := false
	for _, p := range rows {
		providers = append(providers, api.PublicProvider{Id: p.ID, Name: p.Name, Type: p.Type})
		if p.Type == OIDCProviderType {
			hasOIDC = true
		}
	}
	return api.ListAuthProviders200JSONResponse{
		Providers:        providers,
		LocalAuthEnabled: localAuthEnabled(hasOIDC),
	}, nil
}
