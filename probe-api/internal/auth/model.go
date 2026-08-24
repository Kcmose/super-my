package auth

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Role string

const (
	// RoleViewer is retained only as a legacy wire value so older data and
	// clients can be rejected explicitly. Anonymous guests do not have users,
	// passwords, roles, or sessions.
	RoleViewer Role = "viewer"
	RoleAdmin  Role = "admin"
)

var (
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUnauthorized         = errors.New("valid user session required")
	ErrForbidden            = errors.New("request is forbidden")
	ErrBootstrapUnavailable = errors.New("administrator bootstrap is unavailable")
)

type RateLimitError struct {
	RetryAfter time.Duration
}

func (err *RateLimitError) Error() string {
	return "login rate limit exceeded"
}

type FieldError struct {
	Field string
}

func (err *FieldError) Error() string {
	return err.Field + " is invalid"
}

type User struct {
	ID          string     `json:"user_id"`
	Username    string     `json:"username"`
	Role        Role       `json:"role"`
	Enabled     bool       `json:"enabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (request LoginRequest) Validate() error {
	if !validUsername(request.Username) {
		return errors.New("username is invalid")
	}
	if !validPasswordInput(request.Password) {
		return errors.New("password is invalid")
	}
	return nil
}

type RequestMetadata struct {
	SourceIP  string
	UserAgent string
	RequestID string
}

type LoginInput struct {
	LoginRequest
	Metadata RequestMetadata
}

type AuthResponse struct {
	User      User   `json:"user"`
	CSRFToken string `json:"csrf_token"`
}

type LoginResult struct {
	AuthResponse
	SessionToken string    `json:"-"`
	ExpiresAt    time.Time `json:"-"`
}

type Identity struct {
	SessionID string
	User      User
	ExpiresAt time.Time
}

func (identity Identity) UserID() string {
	return identity.User.ID
}

func (identity Identity) IsAdmin() bool {
	return identity.User.Role == RoleAdmin
}

func validUsername(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validPasswordInput(value string) bool {
	return value != "" && len(value) <= maxPasswordBytes && utf8.ValidString(value)
}

func validPasswordBytes(value []byte) bool {
	return len(value) > 0 && len(value) <= maxPasswordBytes && utf8.Valid(value)
}

func validNewPasswordBytes(value []byte) bool {
	return len(value) >= 12 && len(value) <= maxPasswordBytes && utf8.Valid(value)
}

func validRole(role Role) bool {
	return role == RoleAdmin
}
