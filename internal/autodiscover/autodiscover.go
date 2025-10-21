// Package autodiscover implements Microsoft Autodiscover protocol
// providing automatic client configuration for Outlook and mobile devices
// with DNS-based service discovery and HTTP/HTTPS endpoints
package autodiscover

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Autodiscover protocol operations for automatic client configuration
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Autodiscover settings
	enabled         bool
	internalURL     string
	externalURL     string
	redirectEnabled bool
	clientAccessServer string
}

// Autodiscover XML structures for request/response handling

// AutodiscoverRequest represents the client's autodiscover request
type AutodiscoverRequest struct {
	XMLName      xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006 Autodiscover"`
	Request      Request  `xml:"Request"`
}

// Request contains the user's email address for configuration lookup
type Request struct {
	EmailAddress      string `xml:"EMailAddress"`
	AcceptableResponseSchema string `xml:"AcceptableResponseSchema"`
}

// AutodiscoverResponse contains the complete client configuration
type AutodiscoverResponse struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response Response `xml:"Response"`
}

// Response contains the configuration details for the client
type Response struct {
	Account Account `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a Account"`
}

// Account contains all protocol configuration details
type Account struct {
	AccountType  string     `xml:"AccountType"`
	Action       string     `xml:"Action"`
	Protocols    []Protocol `xml:"Protocol"`
}

// Protocol defines a specific protocol configuration (IMAP, SMTP, EWS, etc.)
type Protocol struct {
	Type             string `xml:"Type"`
	Server           string `xml:"Server,omitempty"`
	Port             int    `xml:"Port,omitempty"`
	DirectoryPort    int    `xml:"DirectoryPort,omitempty"`
	ReferralPort     int    `xml:"ReferralPort,omitempty"`
	LoginName        string `xml:"LoginName,omitempty"`
	DomainRequired   string `xml:"DomainRequired,omitempty"`
	DomainName       string `xml:"DomainName,omitempty"`
	SPA              string `xml:"SPA,omitempty"`
	SSL              string `xml:"SSL,omitempty"`
	AuthRequired     string `xml:"AuthRequired,omitempty"`
	UsePOPAuth       string `xml:"UsePOPAuth,omitempty"`
	SMTPLast         string `xml:"SMTPLast,omitempty"`
	ServerExclusiveConnect string `xml:"ServerExclusiveConnect,omitempty"`

	// Exchange Web Services specific
	EwsUrl           string `xml:"EwsUrl,omitempty"`
	EmwsUrl          string `xml:"EmwsUrl,omitempty"`
	ASUrl            string `xml:"ASUrl,omitempty"`
	OOFUrl           string `xml:"OOFUrl,omitempty"`
	UMUrl            string `xml:"UMUrl,omitempty"`
	OABUrl           string `xml:"OABUrl,omitempty"`
	PublicFolderServer string `xml:"PublicFolderServer,omitempty"`

	// ActiveSync specific
	ActiveSyncUrl    string `xml:"Url,omitempty"`
	Name             string `xml:"Name,omitempty"`
}

// POXAutodiscoverResponse is the Plain Old XML (POX) autodiscover response format
type POXAutodiscoverResponse struct {
	XMLName  xml.Name `xml:"Autodiscover"`
	Xmlns    string   `xml:"xmlns,attr"`
	Response POXResponse `xml:"Response"`
}

// POXResponse contains POX format account configuration
type POXResponse struct {
	Account POXAccount `xml:"Account"`
}

// POXAccount contains protocol configurations in POX format
type POXAccount struct {
	AccountType string        `xml:"AccountType"`
	Action      string        `xml:"Action"`
	Protocol    []POXProtocol `xml:"Protocol"`
}

// POXProtocol defines protocol settings in POX format
type POXProtocol struct {
	Type   string `xml:"Type"`
	Server string `xml:"Server"`
	Port   string `xml:"Port"`
	SSL    string `xml:"SSL"`
	Encryption string `xml:"Encryption,omitempty"`
	AuthRequired string `xml:"AuthRequired,omitempty"`
	LoginName string `xml:"LoginName,omitempty"`
	DomainRequired string `xml:"DomainRequired,omitempty"`

	// Exchange specific URLs
	ASUrl  string `xml:"ASUrl,omitempty"`
	EwsUrl string `xml:"EwsUrl,omitempty"`
	OABUrl string `xml:"OABUrl,omitempty"`
}

// MobileConfigResponse is for iOS/macOS configuration profiles
type MobileConfigResponse struct {
	PayloadContent []MobilePayload `xml:"PayloadContent"`
	PayloadDisplayName string `xml:"PayloadDisplayName"`
	PayloadIdentifier string `xml:"PayloadIdentifier"`
	PayloadType string `xml:"PayloadType"`
	PayloadUUID string `xml:"PayloadUUID"`
	PayloadVersion int `xml:"PayloadVersion"`
}

// MobilePayload contains individual service configuration for mobile devices
type MobilePayload struct {
	PayloadType string `xml:"PayloadType"`
	PayloadUUID string `xml:"PayloadUUID"`
	PayloadIdentifier string `xml:"PayloadIdentifier"`
	PayloadVersion int `xml:"PayloadVersion"`
	PayloadDisplayName string `xml:"PayloadDisplayName"`

	// Email configuration
	EmailAccountName string `xml:"EmailAccountName,omitempty"`
	EmailAccountType string `xml:"EmailAccountType,omitempty"`
	EmailAddress string `xml:"EmailAddress,omitempty"`
	IncomingMailServerHostName string `xml:"IncomingMailServerHostName,omitempty"`
	IncomingMailServerPortNumber int `xml:"IncomingMailServerPortNumber,omitempty"`
	IncomingMailServerUseSSL bool `xml:"IncomingMailServerUseSSL,omitempty"`
	OutgoingMailServerHostName string `xml:"OutgoingMailServerHostName,omitempty"`
	OutgoingMailServerPortNumber int `xml:"OutgoingMailServerPortNumber,omitempty"`
	OutgoingMailServerUseSSL bool `xml:"OutgoingMailServerUseSSL,omitempty"`
}

// NewService creates a new Autodiscover service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// Default autodiscover settings
		enabled:         true,
		redirectEnabled: true,
	}

	// Load autodiscover settings from database
	if err := s.loadSettings(); err != nil {
		log.Warn("Failed to load autodiscover settings, using defaults: %v", err)
	}

	// Generate URLs from configuration
	s.internalURL = fmt.Sprintf("https://%s/autodiscover/autodiscover.xml", cfg.ServerAddress)
	s.externalURL = fmt.Sprintf("https://%s/autodiscover/autodiscover.xml", cfg.Domain)
	s.clientAccessServer = cfg.ServerAddress

	return s, nil
}

// loadSettings retrieves autodiscover configuration from database
func (s *Service) loadSettings() error {
	query := `SELECT enabled, internal_url, external_url, redirect_enabled, client_access_server
	          FROM autodiscover_settings ORDER BY id DESC LIMIT 1`

	err := s.db.QueryRow(query).Scan(
		&s.enabled,
		&s.internalURL,
		&s.externalURL,
		&s.redirectEnabled,
		&s.clientAccessServer,
	)

	if err != nil {
		return fmt.Errorf("failed to load autodiscover settings: %w", err)
	}

	return nil
}

// HandleRequest processes autodiscover requests from email clients
func (s *Service) HandleRequest(w http.ResponseWriter, r *http.Request) {
	// Check if autodiscover is enabled
	if !s.enabled {
		http.Error(w, "Autodiscover service is disabled", http.StatusServiceUnavailable)
		return
	}

	// Determine request type based on path and method
	path := strings.ToLower(r.URL.Path)

	switch {
	case strings.Contains(path, "/autodiscover/autodiscover.xml"):
		s.handleOutlookAutodiscover(w, r)
	case strings.Contains(path, "/autodiscover/autodiscover.json"):
		s.handleJSONAutodiscover(w, r)
	case strings.Contains(path, "/autodiscover"):
		// Generic autodiscover endpoint
		if r.Method == "GET" {
			s.handleRedirect(w, r)
		} else {
			s.handleOutlookAutodiscover(w, r)
		}
	case strings.Contains(path, "/mobileconfig"):
		s.handleMobileConfig(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleOutlookAutodiscover handles Outlook autodiscover XML requests
func (s *Service) handleOutlookAutodiscover(w http.ResponseWriter, r *http.Request) {
	// Parse autodiscover request
	var req AutodiscoverRequest
	decoder := xml.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		s.logger.Error("Failed to parse autodiscover request: %v", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	email := req.Request.EmailAddress
	if email == "" {
		http.Error(w, "Email address required", http.StatusBadRequest)
		return
	}

	s.logger.Debug("Autodiscover request for: %s", email)

	// Validate email exists in system
	var userID int64
	err := s.db.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
	if err != nil {
		s.logger.Warn("Autodiscover request for unknown user: %s", email)
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Build autodiscover response
	response := s.buildAutodiscoverResponse(email)

	// Send XML response
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(response); err != nil {
		s.logger.Error("Failed to encode autodiscover response: %v", err)
	}
}

// buildAutodiscoverResponse creates the complete configuration response
func (s *Service) buildAutodiscoverResponse(email string) *AutodiscoverResponse {
	_ = s.config.Domain // Available for future use
	serverAddress := s.config.ServerAddress

	return &AutodiscoverResponse{
		Response: Response{
			Account: Account{
				AccountType: "email",
				Action:      "settings",
				Protocols: []Protocol{
					// Exchange Web Services
					{
						Type:   "EXCH",
						Server: serverAddress,
						Port:   443,
						SSL:    "on",
						EwsUrl: fmt.Sprintf("https://%s/EWS/Exchange.asmx", serverAddress),
						ASUrl:  fmt.Sprintf("https://%s/Microsoft-Server-ActiveSync", serverAddress),
						OABUrl: fmt.Sprintf("https://%s/OAB", serverAddress),
					},
					// IMAP
					{
						Type:         "IMAP",
						Server:       serverAddress,
						Port:         993,
						LoginName:    email,
						SSL:          "on",
						AuthRequired: "on",
					},
					// SMTP
					{
						Type:         "SMTP",
						Server:       serverAddress,
						Port:         587,
						LoginName:    email,
						SSL:          "on",
						AuthRequired: "on",
					},
				},
			},
		},
	}
}

// handleRedirect handles GET requests with redirect to autodiscover URL
func (s *Service) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if !s.redirectEnabled {
		http.Error(w, "Redirects are disabled", http.StatusForbidden)
		return
	}

	// Redirect to external autodiscover URL
	http.Redirect(w, r, s.externalURL, http.StatusMovedPermanently)
}

// handleJSONAutodiscover handles modern JSON-based autodiscover requests
func (s *Service) handleJSONAutodiscover(w http.ResponseWriter, r *http.Request) {
	// Extract email from query parameter or header
	email := r.URL.Query().Get("Email")
	if email == "" {
		email = r.Header.Get("X-AnchorMailbox")
	}

	if email == "" {
		http.Error(w, "Email address required", http.StatusBadRequest)
		return
	}

	// Build JSON response
	response := map[string]interface{}{
		"Protocol":     "AutodiscoverV1",
		"Url":          fmt.Sprintf("https://%s/autodiscover/autodiscover.json", s.config.ServerAddress),
		"EwsUrl":       fmt.Sprintf("https://%s/EWS/Exchange.asmx", s.config.ServerAddress),
		"ActiveSyncUrl": fmt.Sprintf("https://%s/Microsoft-Server-ActiveSync", s.config.ServerAddress),
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Simple JSON encoding
	fmt.Fprintf(w, `{"Protocol":"%s","Url":"%s","EwsUrl":"%s","ActiveSyncUrl":"%s"}`,
		response["Protocol"], response["Url"], response["EwsUrl"], response["ActiveSyncUrl"])
}

// handleMobileConfig handles iOS/macOS configuration profile requests
func (s *Service) handleMobileConfig(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "Email address required", http.StatusBadRequest)
		return
	}

	// Generate mobile configuration profile
	profile := s.buildMobileConfig(email)

	w.Header().Set("Content-Type", "application/x-apple-aspen-config; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.mobileconfig"`, s.config.Organization))
	w.WriteHeader(http.StatusOK)

	encoder := xml.NewEncoder(w)
	encoder.Indent("", "  ")
	if err := encoder.Encode(profile); err != nil {
		s.logger.Error("Failed to encode mobile config: %v", err)
	}
}

// buildMobileConfig creates iOS/macOS configuration profile
func (s *Service) buildMobileConfig(email string) *MobileConfigResponse {
	// TODO: Implement complete mobile configuration profile generation
	return &MobileConfigResponse{
		PayloadDisplayName: s.config.Organization + " Email Configuration",
		PayloadIdentifier:  fmt.Sprintf("com.%s.email", s.config.Domain),
		PayloadType:        "Configuration",
		PayloadUUID:        "generated-uuid-here",
		PayloadVersion:     1,
	}
}

// Shutdown gracefully stops the autodiscover service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Autodiscover service")
	return nil
}
