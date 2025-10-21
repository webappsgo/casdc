// Package sso implements Single Sign-On and forward proxy authentication
// providing Authelia-style forward auth for domain-wide SSO with header injection
package sso

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles SSO and forward authentication operations
// providing centralized authentication with session management
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// SSO configuration
	authDomain        string
	protectedDomains  []string
	sessionTimeout    time.Duration
	cookieName        string
	cookieDomain      string
	cookieSecure      bool

	// Session management
	sessions      map[string]*SSOSession
	sessionsMutex sync.RWMutex

	// Protected services
	services      map[string]*ProtectedService
	servicesMutex sync.RWMutex
}

// SSOSession represents an active SSO session
type SSOSession struct {
	ID           string
	UserID       int64
	Username     string
	Email        string
	Groups       []string
	CreatedAt    time.Time
	LastActivity time.Time
	ExpiresAt    time.Time
	IPAddress    string
	UserAgent    string
	Active       bool

	// Session metadata
	Attributes map[string]string
}

// ProtectedService represents a service protected by SSO
type ProtectedService struct {
	ID                string
	Name              string
	Domain            string
	Path              string // Optional path prefix
	Enabled           bool
	RequireAuth       bool
	RequireGroups     []string
	HeaderInjection   map[string]string // Headers to inject
	LogoutURL         string
	CreatedAt         time.Time
}

// AuthRequest represents a forward auth request
type AuthRequest struct {
	OriginalURL    string
	Method         string
	Headers        map[string]string
	IPAddress      string
	UserAgent      string
}

// AuthResponse represents a forward auth response
type AuthResponse struct {
	Authenticated  bool
	UserID         int64
	Username       string
	Email          string
	Groups         []string
	Headers        map[string]string // Headers to inject
	RedirectURL    string            // Redirect to login if not authenticated
}

// NewService creates a new SSO service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// SSO configuration per SPEC
		authDomain:       fmt.Sprintf("auth.%s", cfg.Domain),
		protectedDomains: make([]string, 0),
		sessionTimeout:   8 * time.Hour, // 8 hour default
		cookieName:       "casdc_sso",
		cookieDomain:     fmt.Sprintf(".%s", cfg.Domain),
		cookieSecure:     true, // HTTPS only

		sessions: make(map[string]*SSOSession),
		services: make(map[string]*ProtectedService),
	}

	// Load protected services from database
	if err := s.loadProtectedServices(); err != nil {
		log.Warn("Failed to load protected services: %v", err)
	}

	log.Info("SSO service initialized for domain %s", s.authDomain)

	return s, nil
}

// loadProtectedServices loads protected services from database
func (s *Service) loadProtectedServices() error {
	query := `SELECT id, name, domain, path, enabled, require_auth, logout_url, created_at
		FROM sso_protected_services WHERE enabled = 1`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query protected services: %w", err)
	}
	defer rows.Close()

	s.servicesMutex.Lock()
	defer s.servicesMutex.Unlock()

	for rows.Next() {
		service := &ProtectedService{
			HeaderInjection: make(map[string]string),
		}

		err := rows.Scan(
			&service.ID,
			&service.Name,
			&service.Domain,
			&service.Path,
			&service.Enabled,
			&service.RequireAuth,
			&service.LogoutURL,
			&service.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan service: %v", err)
			continue
		}

		// Load required groups
		s.loadServiceGroups(service)

		// Load header injection configuration
		s.loadHeaderInjection(service)

		s.services[service.ID] = service
		s.logger.Debug("Loaded protected service: %s (%s)", service.Name, service.Domain)
	}

	return rows.Err()
}

// loadServiceGroups loads required groups for a service
func (s *Service) loadServiceGroups(service *ProtectedService) error {
	query := `SELECT group_name FROM sso_service_groups WHERE service_id = ?`

	rows, err := s.db.Query(query, service.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	service.RequireGroups = make([]string, 0)
	for rows.Next() {
		var groupName string
		if err := rows.Scan(&groupName); err == nil {
			service.RequireGroups = append(service.RequireGroups, groupName)
		}
	}

	return rows.Err()
}

// loadHeaderInjection loads header injection configuration for a service
func (s *Service) loadHeaderInjection(service *ProtectedService) error {
	query := `SELECT header_name, header_value FROM sso_header_injection WHERE service_id = ?`

	rows, err := s.db.Query(query, service.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err == nil {
			service.HeaderInjection[name] = value
		}
	}

	return rows.Err()
}

// HandleForwardAuth handles forward authentication requests (Authelia-style)
func (s *Service) HandleForwardAuth(w http.ResponseWriter, r *http.Request) {
	authReq := &AuthRequest{
		OriginalURL: r.Header.Get("X-Original-URL"),
		Method:      r.Header.Get("X-Original-Method"),
		Headers:     make(map[string]string),
		IPAddress:   r.Header.Get("X-Real-IP"),
		UserAgent:   r.UserAgent(),
	}

	// If no X-Original-URL, this is a direct request
	if authReq.OriginalURL == "" {
		authReq.OriginalURL = r.URL.String()
		authReq.Method = r.Method
		authReq.IPAddress = r.RemoteAddr
	}

	// Get session from cookie
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		// No session cookie, redirect to login
		s.redirectToLogin(w, r, authReq.OriginalURL)
		return
	}

	// Validate session
	session, err := s.ValidateSession(cookie.Value)
	if err != nil {
		s.logger.Debug("Invalid session: %v", err)
		s.redirectToLogin(w, r, authReq.OriginalURL)
		return
	}

	// Check if service requires specific groups
	service := s.findProtectedService(authReq.OriginalURL)
	if service != nil && len(service.RequireGroups) > 0 {
		if !s.userHasRequiredGroups(session, service.RequireGroups) {
			http.Error(w, "Forbidden: insufficient permissions", http.StatusForbidden)
			return
		}
	}

	// Inject authentication headers
	s.injectAuthHeaders(w, session, service)

	// Return 200 OK to allow request
	w.WriteHeader(http.StatusOK)
}

// redirectToLogin redirects user to SSO login page
func (s *Service) redirectToLogin(w http.ResponseWriter, r *http.Request, originalURL string) {
	loginURL := fmt.Sprintf("https://%s/login?redirect=%s",
		s.authDomain,
		base64.URLEncoding.EncodeToString([]byte(originalURL)))

	http.Redirect(w, r, loginURL, http.StatusFound)
}

// injectAuthHeaders injects authentication headers for the proxied service
func (s *Service) injectAuthHeaders(w http.ResponseWriter, session *SSOSession, service *ProtectedService) {
	// Standard authentication headers
	w.Header().Set("Remote-User", session.Username)
	w.Header().Set("Remote-Email", session.Email)
	w.Header().Set("Remote-Name", session.Username)
	w.Header().Set("Remote-Groups", strings.Join(session.Groups, ","))

	// Service-specific header injection
	if service != nil {
		for headerName, headerValue := range service.HeaderInjection {
			// Replace template variables
			value := s.replaceTemplateVars(headerValue, session)
			w.Header().Set(headerName, value)
		}
	}
}

// replaceTemplateVars replaces template variables in header values
func (s *Service) replaceTemplateVars(template string, session *SSOSession) string {
	replacements := map[string]string{
		"{username}": session.Username,
		"{email}":    session.Email,
		"{userid}":   fmt.Sprintf("%d", session.UserID),
		"{groups}":   strings.Join(session.Groups, ","),
	}

	result := template
	for key, value := range replacements {
		result = strings.ReplaceAll(result, key, value)
	}

	return result
}

// findProtectedService finds the protected service for a URL
func (s *Service) findProtectedService(url string) *ProtectedService {
	s.servicesMutex.RLock()
	defer s.servicesMutex.RUnlock()

	for _, service := range s.services {
		if strings.Contains(url, service.Domain) {
			if service.Path == "" || strings.HasPrefix(url, service.Path) {
				return service
			}
		}
	}

	return nil
}

// userHasRequiredGroups checks if user belongs to required groups
func (s *Service) userHasRequiredGroups(session *SSOSession, requiredGroups []string) bool {
	userGroups := make(map[string]bool)
	for _, group := range session.Groups {
		userGroups[group] = true
	}

	for _, required := range requiredGroups {
		if !userGroups[required] {
			return false
		}
	}

	return true
}

// CreateSession creates a new SSO session after successful authentication
func (s *Service) CreateSession(userID int64, username, email string, groups []string, ipAddress, userAgent string) (*SSOSession, error) {
	// Generate session ID
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session ID: %w", err)
	}

	session := &SSOSession{
		ID:           sessionID,
		UserID:       userID,
		Username:     username,
		Email:        email,
		Groups:       groups,
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		ExpiresAt:    time.Now().Add(s.sessionTimeout),
		IPAddress:    ipAddress,
		UserAgent:    userAgent,
		Active:       true,
		Attributes:   make(map[string]string),
	}

	// Store in memory
	s.sessionsMutex.Lock()
	s.sessions[sessionID] = session
	s.sessionsMutex.Unlock()

	// Store in database
	if err := s.storeSession(session); err != nil {
		s.logger.Warn("Failed to store session in database: %v", err)
	}

	s.logger.Info("Created SSO session for user %s from %s", username, ipAddress)

	return session, nil
}

// storeSession stores a session in the database
func (s *Service) storeSession(session *SSOSession) error {
	query := `INSERT INTO sso_sessions
		(session_id, user_id, username, email, created_at, last_activity, expires_at,
		ip_address, user_agent, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		session.ID,
		session.UserID,
		session.Username,
		session.Email,
		session.CreatedAt,
		session.LastActivity,
		session.ExpiresAt,
		session.IPAddress,
		session.UserAgent,
		session.Active,
	)

	return err
}

// ValidateSession validates a session ID and returns the session
func (s *Service) ValidateSession(sessionID string) (*SSOSession, error) {
	s.sessionsMutex.RLock()
	session, exists := s.sessions[sessionID]
	s.sessionsMutex.RUnlock()

	if !exists {
		// Try to load from database
		var err error
		session, err = s.loadSessionFromDatabase(sessionID)
		if err != nil {
			return nil, fmt.Errorf("session not found: %w", err)
		}

		// Cache in memory
		s.sessionsMutex.Lock()
		s.sessions[sessionID] = session
		s.sessionsMutex.Unlock()
	}

	// Check if session is active
	if !session.Active {
		return nil, fmt.Errorf("session is inactive")
	}

	// Check if session is expired
	if time.Now().After(session.ExpiresAt) {
		return nil, fmt.Errorf("session expired")
	}

	// Update last activity
	session.LastActivity = time.Now()

	// Update in database
	s.db.Exec("UPDATE sso_sessions SET last_activity = ? WHERE session_id = ?",
		session.LastActivity, sessionID)

	return session, nil
}

// loadSessionFromDatabase loads a session from database
func (s *Service) loadSessionFromDatabase(sessionID string) (*SSOSession, error) {
	query := `SELECT session_id, user_id, username, email, created_at, last_activity,
		expires_at, ip_address, user_agent, active
		FROM sso_sessions WHERE session_id = ?`

	session := &SSOSession{
		Attributes: make(map[string]string),
	}

	err := s.db.QueryRow(query, sessionID).Scan(
		&session.ID,
		&session.UserID,
		&session.Username,
		&session.Email,
		&session.CreatedAt,
		&session.LastActivity,
		&session.ExpiresAt,
		&session.IPAddress,
		&session.UserAgent,
		&session.Active,
	)

	if err != nil {
		return nil, err
	}

	// Load user groups
	session.Groups = s.loadUserGroups(session.UserID)

	return session, nil
}

// loadUserGroups loads groups for a user
func (s *Service) loadUserGroups(userID int64) []string {
	query := `SELECT g.name FROM groups g
		JOIN user_groups ug ON g.id = ug.group_id
		WHERE ug.user_id = ?`

	rows, err := s.db.Query(query, userID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()

	groups := make([]string, 0)
	for rows.Next() {
		var groupName string
		if err := rows.Scan(&groupName); err == nil {
			groups = append(groups, groupName)
		}
	}

	return groups
}

// InvalidateSession invalidates a session (logout)
func (s *Service) InvalidateSession(sessionID string) error {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()

	if session, exists := s.sessions[sessionID]; exists {
		session.Active = false
		delete(s.sessions, sessionID)
	}

	// Mark inactive in database
	query := `UPDATE sso_sessions SET active = 0 WHERE session_id = ?`
	if _, err := s.db.Exec(query, sessionID); err != nil {
		return fmt.Errorf("failed to invalidate session: %w", err)
	}

	s.logger.Info("Invalidated SSO session: %s", sessionID)

	return nil
}

// RegisterProtectedService registers a new service for SSO protection
func (s *Service) RegisterProtectedService(name, domain, path string, requireAuth bool, requiredGroups []string) (*ProtectedService, error) {
	service := &ProtectedService{
		ID:              generateServiceID(),
		Name:            name,
		Domain:          domain,
		Path:            path,
		Enabled:         true,
		RequireAuth:     requireAuth,
		RequireGroups:   requiredGroups,
		HeaderInjection: make(map[string]string),
		CreatedAt:       time.Now(),
	}

	// Store in database
	query := `INSERT INTO sso_protected_services
		(id, name, domain, path, enabled, require_auth, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	if _, err := s.db.Exec(query,
		service.ID,
		service.Name,
		service.Domain,
		service.Path,
		service.Enabled,
		service.RequireAuth,
		service.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("failed to register service: %w", err)
	}

	// Store required groups
	for _, group := range requiredGroups {
		s.db.Exec("INSERT INTO sso_service_groups (service_id, group_name) VALUES (?, ?)",
			service.ID, group)
	}

	// Cache in memory
	s.servicesMutex.Lock()
	s.services[service.ID] = service
	s.servicesMutex.Unlock()

	s.logger.Info("Registered protected service: %s (%s)", name, domain)

	return service, nil
}

// CleanupExpiredSessions removes expired sessions
func (s *Service) CleanupExpiredSessions(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

// cleanupExpired removes expired sessions from memory and database
func (s *Service) cleanupExpired() {
	now := time.Now()
	expired := make([]string, 0)

	s.sessionsMutex.Lock()
	for sessionID, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			expired = append(expired, sessionID)
			delete(s.sessions, sessionID)
		}
	}
	s.sessionsMutex.Unlock()

	if len(expired) > 0 {
		// Mark expired in database
		query := `UPDATE sso_sessions SET active = 0 WHERE session_id IN (?` + strings.Repeat(",?", len(expired)-1) + `)`
		args := make([]interface{}, len(expired))
		for i, id := range expired {
			args[i] = id
		}
		s.db.Exec(query, args...)

		s.logger.Debug("Cleaned up %d expired SSO sessions", len(expired))
	}
}

// Shutdown gracefully stops the SSO service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down SSO service")
	return nil
}

// generateSessionID generates a cryptographically secure session ID
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// generateServiceID generates a unique service ID
func generateServiceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("svc_%s", base64.URLEncoding.EncodeToString(b))
}
