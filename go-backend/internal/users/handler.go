package users

import (
	"context"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
)

var uuidRegex = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// UserRow holds a full user record from the database.
type UserRow struct {
	ID           string    `json:"id" db:"id"`
	Username     string    `json:"username" db:"username"`
	PasswordHash string    `json:"-" db:"password_hash"`
	AuthMethod   string    `json:"-" db:"auth_method"`
	FirstName    *string   `json:"firstName,omitempty" db:"first_name"`
	LastName     *string   `json:"lastName,omitempty" db:"last_name"`
	Email        *string   `json:"email,omitempty" db:"email"`
	Role         string    `json:"role" db:"role"`
	ExternalID   *string   `json:"-" db:"external_id"`
	CreatedAt    time.Time `json:"createdAt" db:"created_at"`
}

// UserUpdate carries the fields to update for a user. Only non-nil fields are applied.
type UserUpdate struct {
	FirstName    *string
	LastName     *string
	Email        *string
	PasswordHash *string
}

// UserCreate carries the fields needed to insert a new user.
type UserCreate struct {
	Username     string
	PasswordHash string
	AuthMethod   string
	FirstName    *string
	LastName     *string
	Email        *string
	Role         string
	// ExternalID stores the OIDC `sub` for users provisioned via OIDC.
	// Nil for local / LDAP users.
	ExternalID *string
}

// Store is the persistence interface used by the users handlers.
type Store interface {
	GetUserByID(ctx context.Context, id string) (*UserRow, error)
	GetUserByUsername(ctx context.Context, username string) (*UserRow, error)
	SearchUserByTerm(ctx context.Context, term string) (*UserRow, error)
	UpdateUser(ctx context.Context, id string, data UserUpdate) (*UserRow, error)
}

// Handler holds the dependencies for the users endpoints.
type Handler struct {
	store Store
}

// NewHandler creates a new Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

func isAdmin(claims *auth.Claims) bool {
	return claims != nil && (claims.Role == "admin" || claims.Role == "superadmin")
}

// fullUser projects a row onto the full (own-profile / admin) wire shape.
func fullUser(row *UserRow) api.User {
	return api.User{
		Id:        row.ID,
		Username:  row.Username,
		FirstName: row.FirstName,
		LastName:  row.LastName,
		Email:     row.Email,
		Role:      api.Role(row.Role),
		CreatedAt: row.CreatedAt,
	}
}

// GetUser implements GET /api/users/{id}.
// It resolves the {id} path value as UUID → username → search term.
// Own profile or admin/superadmin receives the full response; others get a limited view.
func (h *Handler) GetUser(ctx context.Context, request api.GetUserRequestObject) (api.GetUserResponseObject, error) {
	id := request.Id

	var found *UserRow
	var err error

	if uuidRegex.MatchString(id) {
		found, err = h.store.GetUserByID(ctx, id)
		if err != nil {
			return nil, err
		}
	}

	if found == nil {
		found, err = h.store.GetUserByUsername(ctx, id)
		if err != nil {
			return nil, err
		}
	}

	if found == nil {
		found, err = h.store.SearchUserByTerm(ctx, id)
		if err != nil {
			return nil, err
		}
	}

	if found == nil {
		return api.GetUser404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{Error: "user not found"},
		}, nil
	}

	var body api.GetUser200JSONResponseBody
	viewer := auth.UserFromContext(ctx)
	if viewer != nil && (viewer.ID == found.ID || isAdmin(viewer)) {
		if err := body.FromUser(fullUser(found)); err != nil {
			return nil, err
		}
	} else {
		if err := body.FromUserLimited(api.UserLimited{
			Id:        found.ID,
			Username:  found.Username,
			FirstName: found.FirstName,
			LastName:  found.LastName,
		}); err != nil {
			return nil, err
		}
	}
	return api.GetUser200JSONResponse(body), nil
}

// UpdateUser implements PATCH /api/users/{id}.
// Only the user themselves or an admin/superadmin may update the profile.
// Length/format bounds (name ≤ 50, password ≥ 8) are enforced by the spec
// middleware; only the checks the schema cannot express live here.
func (h *Handler) UpdateUser(ctx context.Context, request api.UpdateUserRequestObject) (api.UpdateUserResponseObject, error) {
	id := request.Id
	body := request.Body

	viewer := auth.UserFromContext(ctx)
	if viewer == nil {
		return api.UpdateUser401JSONResponse{
			UnauthorizedJSONResponse: api.UnauthorizedJSONResponse{Error: "authentication required"},
		}, nil
	}

	if viewer.ID != id && !isAdmin(viewer) {
		return api.UpdateUser403JSONResponse{
			ForbiddenJSONResponse: api.ForbiddenJSONResponse{Error: "forbidden"},
		}, nil
	}

	existing, err := h.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return api.UpdateUser404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{Error: "user not found"},
		}, nil
	}

	if body.Email != nil && !strings.Contains(*body.Email, "@") {
		return api.UpdateUser400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{Error: "invalid email"},
		}, nil
	}

	update := UserUpdate{
		FirstName: body.FirstName,
		LastName:  body.LastName,
		Email:     body.Email,
	}

	if body.Password != nil {
		// Cost 12 (~250ms) defends user-chosen low-entropy passwords against
		// offline brute-force if the hash table ever leaks. The login path
		// (authhandler) uses CompareHashAndPassword which reads the cost from
		// the stored hash, so older cost-10 hashes keep working.
		hashed, err := bcrypt.GenerateFromPassword([]byte(*body.Password), 12)
		if err != nil {
			return nil, err
		}
		s := string(hashed)
		update.PasswordHash = &s
	}

	updated, err := h.store.UpdateUser(ctx, id, update)
	if err != nil {
		return nil, err
	}

	return api.UpdateUser200JSONResponse(fullUser(updated)), nil
}
