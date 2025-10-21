// Package activesync implements Microsoft Exchange ActiveSync protocol
// Provides mobile device synchronization for email, calendar, contacts, and tasks
package activesync

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the ActiveSync service
// Implements Microsoft Exchange ActiveSync protocol for mobile device synchronization
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Device management
	devices      map[string]*MobileDevice
	devicesMutex sync.RWMutex

	// Policy management
	policies      map[int64]*DevicePolicy
	policiesMutex sync.RWMutex
}

// MobileDevice represents a mobile device with ActiveSync connection
type MobileDevice struct {
	ID                      int64
	UserID                  int64
	DeviceID                string
	DeviceType              string
	DeviceModel             string
	DeviceOS                string
	DeviceOSVersion         string
	DeviceIMEI              string
	DevicePhoneNumber       string
	FriendlyName            string
	FirstSyncTime           time.Time
	LastSyncAttempt         time.Time
	LastSuccessfulSync      time.Time
	SyncState               string
	DeviceAccessState       string // allowed, blocked, quarantined
	DeviceAccessStateReason string
	PolicyID                int64
	WipeRequested           bool
	WipeRequestedAt         time.Time
	WipeAcknowledged        bool
	WipeAcknowledgedAt      time.Time
	CreatedAt               time.Time
	HeartbeatInterval       int
	ProtocolVersion         string
	UserAgent               string
}

// DevicePolicy represents a mobile device policy for MDM
type DevicePolicy struct {
	ID                                      int64
	Name                                    string
	Description                             string
	AllowSimplePassword                     bool
	AlphanumericPasswordRequired            bool
	PasswordRecoveryEnabled                 bool
	DeviceEncryptionEnabled                 bool
	AttachmentsEnabled                      bool
	MinPasswordLength                       int
	MaxPasswordFailedAttempts               int
	MaxInactivityTimeLock                   int
	MaxAttachmentSize                       int
	AllowStorageCard                        bool
	AllowCamera                             bool
	RequireDeviceEncryption                 bool
	AllowUnsignedApplications               bool
	AllowUnsignedInstallationPackages       bool
	MinPasswordComplexCharacters            int
	MaxCalendarAgeFilter                    int
	MaxEmailAgeFilter                       int
	MaxEmailBodyTruncationSize              int
	MaxEmailHTMLBodyTruncationSize          int
	RequireSignedSMIMEMessages              bool
	RequireEncryptedSMIMEMessages           bool
	RequireSignedSMIMEAlgorithm             string
	RequireEncryptionSMIMEAlgorithm         string
	AllowSMIMEEncryptionAlgorithmNegotiation bool
	AllowSMIMESoftCerts                     bool
	AllowBrowser                            bool
	AllowConsumerEmail                      bool
	AllowRemoteDesktop                      bool
	AllowInternetSharing                    bool
	AllowIrDA                               bool
	AllowWiFi                               bool
	AllowTextMessaging                      bool
	AllowPOPIMAPEmail                       bool
	AllowBluetooth                          bool
	RequireManualSyncWhenRoaming            bool
	CreatedAt                               time.Time
	IsDefault                               bool
}

// ActiveSyncCommand represents an ActiveSync protocol command
type ActiveSyncCommand struct {
	Name    string
	Version string
	DeviceID string
	UserID   int64
}

// NewService creates a new ActiveSync service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) *Service {
	return &Service{
		db:       db,
		config:   cfg,
		logger:   log,
		devices:  make(map[string]*MobileDevice),
		policies: make(map[int64]*DevicePolicy),
	}
}

// Start starts the ActiveSync service
func (s *Service) Start() error {
	s.logger.Info("Starting ActiveSync service")

	// Load device policies from database
	if err := s.loadPolicies(); err != nil {
		return fmt.Errorf("failed to load device policies: %w", err)
	}

	// Load registered devices
	if err := s.loadDevices(); err != nil {
		return fmt.Errorf("failed to load devices: %w", err)
	}

	s.logger.Info("ActiveSync service started with %d policies and %d devices", len(s.policies), len(s.devices))
	return nil
}

// Stop stops the ActiveSync service
func (s *Service) Stop() error {
	s.logger.Info("Stopping ActiveSync service")
	s.logger.Info("ActiveSync service stopped")
	return nil
}

// HandleRequest processes an ActiveSync HTTP request
// Implements the Microsoft Exchange ActiveSync protocol
func (s *Service) HandleRequest(w http.ResponseWriter, r *http.Request) {
	// Extract ActiveSync command from query parameters
	cmd := &ActiveSyncCommand{
		Name:    r.URL.Query().Get("Cmd"),
		Version: r.URL.Query().Get("ProtocolVersion"),
		DeviceID: r.URL.Query().Get("DeviceId"),
	}

	// Get user from authentication context
	// In production, extract from authenticated session
	userID := s.getUserIDFromRequest(r)
	if userID == 0 {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	cmd.UserID = userID

	s.logger.Debug("ActiveSync request: Cmd=%s, DeviceID=%s, Version=%s", cmd.Name, cmd.DeviceID, cmd.Version)

	// Route command to appropriate handler
	switch cmd.Name {
	case "Sync":
		s.handleSync(w, r, cmd)
	case "FolderSync":
		s.handleFolderSync(w, r, cmd)
	case "GetItemEstimate":
		s.handleGetItemEstimate(w, r, cmd)
	case "Provision":
		s.handleProvision(w, r, cmd)
	case "Ping":
		s.handlePing(w, r, cmd)
	case "SendMail":
		s.handleSendMail(w, r, cmd)
	case "SmartForward":
		s.handleSmartForward(w, r, cmd)
	case "SmartReply":
		s.handleSmartReply(w, r, cmd)
	case "GetAttachment":
		s.handleGetAttachment(w, r, cmd)
	case "Search":
		s.handleSearch(w, r, cmd)
	case "Settings":
		s.handleSettings(w, r, cmd)
	case "ItemOperations":
		s.handleItemOperations(w, r, cmd)
	case "MeetingResponse":
		s.handleMeetingResponse(w, r, cmd)
	case "ResolveRecipients":
		s.handleResolveRecipients(w, r, cmd)
	case "ValidateCert":
		s.handleValidateCert(w, r, cmd)
	default:
		s.logger.Warn("Unknown ActiveSync command: %s", cmd.Name)
		http.Error(w, "Unknown command", http.StatusBadRequest)
	}
}

// handleSync handles the Sync command (email/calendar/contacts synchronization)
func (s *Service) handleSync(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling Sync command for device %s", cmd.DeviceID)

	// Register or update device
	device, err := s.registerDevice(cmd)
	if err != nil {
		s.logger.Error("Failed to register device: %v", err)
		http.Error(w, "Device registration failed", http.StatusInternalServerError)
		return
	}

	// Check if device is allowed
	if device.DeviceAccessState == "blocked" {
		s.logger.Warn("Device %s is blocked: %s", cmd.DeviceID, device.DeviceAccessStateReason)
		http.Error(w, "Device blocked", http.StatusForbidden)
		return
	}

	// Parse sync request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("Failed to read sync request: %v", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Process sync (placeholder - full implementation would parse WBXML and sync data)
	s.logger.Debug("Processing sync for device %s, request size: %d bytes", cmd.DeviceID, len(body))

	// Generate sync response
	response := s.generateSyncResponse(device)

	// Set response headers
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.Header().Set("MS-Server-ActiveSync", "16.0")

	// Write response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))

	// Update device last sync time
	s.updateDeviceLastSync(device.ID)
}

// handleFolderSync handles the FolderSync command (folder hierarchy synchronization)
func (s *Service) handleFolderSync(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling FolderSync command for device %s", cmd.DeviceID)

	// Generate folder hierarchy response
	// In production, this would return actual mail folders
	folders := []struct {
		ID   string
		Name string
		Type int
	}{
		{"1", "Inbox", 2},
		{"2", "Drafts", 3},
		{"3", "Sent Items", 5},
		{"4", "Deleted Items", 4},
		{"5", "Calendar", 8},
		{"6", "Contacts", 9},
		{"7", "Tasks", 7},
	}

	// Build XML response (simplified)
	type FolderSyncResponse struct {
		XMLName xml.Name `xml:"FolderSync"`
		Status  int      `xml:"Status"`
		SyncKey string   `xml:"SyncKey"`
	}

	response := FolderSyncResponse{
		Status:  1, // Success
		SyncKey: "1",
	}

	xmlData, _ := xml.Marshal(response)

	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.Header().Set("MS-Server-ActiveSync", "16.0")
	w.WriteHeader(http.StatusOK)
	w.Write(xmlData)

	s.logger.Debug("FolderSync response sent: %d folders", len(folders))
}

// handleProvision handles the Provision command (policy provisioning)
func (s *Service) handleProvision(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling Provision command for device %s", cmd.DeviceID)

	// Get device
	device, err := s.getDevice(cmd.DeviceID)
	if err != nil {
		// Create new device
		device, err = s.registerDevice(cmd)
		if err != nil {
			http.Error(w, "Device registration failed", http.StatusInternalServerError)
			return
		}
	}

	// Get policy for device
	policy, err := s.getPolicy(device.PolicyID)
	if err != nil {
		// Use default policy
		policy, err = s.getDefaultPolicy()
		if err != nil {
			http.Error(w, "No policy available", http.StatusInternalServerError)
			return
		}
	}

	// Generate policy XML response
	policyXML := s.generatePolicyXML(policy)

	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.Header().Set("MS-Server-ActiveSync", "16.0")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(policyXML))

	s.logger.Info("Policy provisioned for device %s: %s", cmd.DeviceID, policy.Name)
}

// handlePing handles the Ping command (push notifications / heartbeat)
func (s *Service) handlePing(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling Ping command for device %s", cmd.DeviceID)

	// Parse ping request to get heartbeat interval and folders to monitor
	// In production, this would maintain an open connection and push notifications

	// For now, return immediate response (no changes)
	type PingResponse struct {
		XMLName xml.Name `xml:"Ping"`
		Status  int      `xml:"Status"`
	}

	response := PingResponse{
		Status: 1, // No changes
	}

	xmlData, _ := xml.Marshal(response)

	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.Header().Set("MS-Server-ActiveSync", "16.0")
	w.WriteHeader(http.StatusOK)
	w.Write(xmlData)
}

// handleSendMail handles the SendMail command
func (s *Service) handleSendMail(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling SendMail command for device %s", cmd.DeviceID)

	// Read email from request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Send email via SMTP (integrate with email service)
	s.logger.Info("Sending email from device %s, size: %d bytes", cmd.DeviceID, len(body))

	// Success response
	w.WriteHeader(http.StatusOK)
}

// handleGetItemEstimate handles the GetItemEstimate command
func (s *Service) handleGetItemEstimate(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {
	s.logger.Debug("Handling GetItemEstimate command for device %s", cmd.DeviceID)

	// Return estimate of items to sync
	type EstimateResponse struct {
		XMLName  xml.Name `xml:"GetItemEstimate"`
		Status   int      `xml:"Status"`
		Estimate int      `xml:"Estimate"`
	}

	response := EstimateResponse{
		Status:   1,
		Estimate: 0, // No new items
	}

	xmlData, _ := xml.Marshal(response)

	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.WriteHeader(http.StatusOK)
	w.Write(xmlData)
}

// Placeholder handlers for other commands
func (s *Service) handleSmartForward(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)     {}
func (s *Service) handleSmartReply(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)       {}
func (s *Service) handleGetAttachment(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)    {}
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)           {}
func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)         {}
func (s *Service) handleItemOperations(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)   {}
func (s *Service) handleMeetingResponse(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)  {}
func (s *Service) handleResolveRecipients(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand) {}
func (s *Service) handleValidateCert(w http.ResponseWriter, r *http.Request, cmd *ActiveSyncCommand)     {}

// registerDevice registers or updates a mobile device
func (s *Service) registerDevice(cmd *ActiveSyncCommand) (*MobileDevice, error) {
	s.devicesMutex.Lock()
	defer s.devicesMutex.Unlock()

	// Check if device exists
	if device, exists := s.devices[cmd.DeviceID]; exists {
		// Update last sync attempt
		device.LastSyncAttempt = time.Now()
		device.ProtocolVersion = cmd.Version
		return device, nil
	}

	// Create new device
	device := &MobileDevice{
		UserID:            cmd.UserID,
		DeviceID:          cmd.DeviceID,
		FirstSyncTime:     time.Now(),
		LastSyncAttempt:   time.Now(),
		DeviceAccessState: "allowed",
		ProtocolVersion:   cmd.Version,
		HeartbeatInterval: 3600,
		CreatedAt:         time.Now(),
	}

	// Insert into database
	result, err := s.db.Exec(`
		INSERT INTO mobile_devices (user_id, device_id, first_sync_time, last_sync_attempt,
		                           device_access_state, protocol_version, heartbeat_interval, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		device.UserID, device.DeviceID, device.FirstSyncTime, device.LastSyncAttempt,
		device.DeviceAccessState, device.ProtocolVersion, device.HeartbeatInterval, device.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	device.ID = id

	s.devices[cmd.DeviceID] = device
	s.logger.Info("Registered new mobile device: %s", cmd.DeviceID)

	return device, nil
}

// getDevice retrieves a device by ID
func (s *Service) getDevice(deviceID string) (*MobileDevice, error) {
	s.devicesMutex.RLock()
	device, exists := s.devices[deviceID]
	s.devicesMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("device not found: %s", deviceID)
	}

	return device, nil
}

// updateDeviceLastSync updates the last successful sync time
func (s *Service) updateDeviceLastSync(deviceID int64) {
	_, err := s.db.Exec(`
		UPDATE mobile_devices
		SET last_successful_sync = ?
		WHERE id = ?`,
		time.Now(), deviceID,
	)
	if err != nil {
		s.logger.Error("Failed to update device last sync: %v", err)
	}
}

// loadDevices loads all registered devices from database
func (s *Service) loadDevices() error {
	rows, err := s.db.Query(`
		SELECT id, user_id, device_id, device_type, device_model, device_os,
		       first_sync_time, last_sync_attempt, last_successful_sync,
		       device_access_state, protocol_version, heartbeat_interval, created_at
		FROM mobile_devices
		WHERE device_access_state != 'blocked'
		ORDER BY last_sync_attempt DESC
		LIMIT 1000
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.devicesMutex.Lock()
	defer s.devicesMutex.Unlock()

	for rows.Next() {
		device := &MobileDevice{}
		err := rows.Scan(
			&device.ID, &device.UserID, &device.DeviceID, &device.DeviceType, &device.DeviceModel,
			&device.DeviceOS, &device.FirstSyncTime, &device.LastSyncAttempt, &device.LastSuccessfulSync,
			&device.DeviceAccessState, &device.ProtocolVersion, &device.HeartbeatInterval, &device.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan device: %v", err)
			continue
		}

		s.devices[device.DeviceID] = device
	}

	return rows.Err()
}

// loadPolicies loads device policies from database
func (s *Service) loadPolicies() error {
	rows, err := s.db.Query(`
		SELECT id, name, description, allow_simple_password, alphanumeric_password_required,
		       min_password_length, max_password_failed_attempts, require_device_encryption,
		       is_default, created_at
		FROM device_policies
		ORDER BY is_default DESC, name
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.policiesMutex.Lock()
	defer s.policiesMutex.Unlock()

	for rows.Next() {
		policy := &DevicePolicy{}
		err := rows.Scan(
			&policy.ID, &policy.Name, &policy.Description, &policy.AllowSimplePassword,
			&policy.AlphanumericPasswordRequired, &policy.MinPasswordLength,
			&policy.MaxPasswordFailedAttempts, &policy.RequireDeviceEncryption,
			&policy.IsDefault, &policy.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan policy: %v", err)
			continue
		}

		s.policies[policy.ID] = policy
	}

	return rows.Err()
}

// getPolicy retrieves a policy by ID
func (s *Service) getPolicy(policyID int64) (*DevicePolicy, error) {
	s.policiesMutex.RLock()
	policy, exists := s.policies[policyID]
	s.policiesMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("policy not found: %d", policyID)
	}

	return policy, nil
}

// getDefaultPolicy retrieves the default device policy
func (s *Service) getDefaultPolicy() (*DevicePolicy, error) {
	s.policiesMutex.RLock()
	defer s.policiesMutex.RUnlock()

	for _, policy := range s.policies {
		if policy.IsDefault {
			return policy, nil
		}
	}

	return nil, fmt.Errorf("no default policy found")
}

// generateSyncResponse generates a sync response (placeholder)
func (s *Service) generateSyncResponse(device *MobileDevice) string {
	// In production, this would generate proper WBXML/XML sync response
	return `<?xml version="1.0" encoding="utf-8"?><Sync><Status>1</Status></Sync>`
}

// generatePolicyXML generates policy XML for provisioning
func (s *Service) generatePolicyXML(policy *DevicePolicy) string {
	// Simplified policy XML
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<Provision>
  <Status>1</Status>
  <Policies>
    <Policy>
      <PolicyType>MS-EAS-Provisioning-WBXML</PolicyType>
      <Status>1</Status>
      <PolicyKey>1</PolicyKey>
      <Data>
        <EASProvisionDoc>
          <DevicePasswordEnabled>%t</DevicePasswordEnabled>
          <AlphanumericDevicePasswordRequired>%t</AlphanumericDevicePasswordRequired>
          <MinDevicePasswordLength>%d</MinDevicePasswordLength>
          <MaxDevicePasswordFailedAttempts>%d</MaxDevicePasswordFailedAttempts>
          <RequireDeviceEncryption>%t</RequireDeviceEncryption>
        </EASProvisionDoc>
      </Data>
    </Policy>
  </Policies>
</Provision>`,
		!policy.AllowSimplePassword,
		policy.AlphanumericPasswordRequired,
		policy.MinPasswordLength,
		policy.MaxPasswordFailedAttempts,
		policy.RequireDeviceEncryption,
	)
}

// getUserIDFromRequest extracts user ID from authenticated request
func (s *Service) getUserIDFromRequest(r *http.Request) int64 {
	// In production, extract from session/JWT token
	// For now, return placeholder
	username := r.Header.Get("X-User")
	if username == "" {
		// Extract from Basic Auth
		user, _, ok := r.BasicAuth()
		if !ok {
			return 0
		}
		username = user
	}

	// Look up user ID from username
	var userID int64
	err := s.db.QueryRow("SELECT id FROM users WHERE username = ?", username).Scan(&userID)
	if err != nil {
		s.logger.Error("Failed to get user ID for %s: %v", username, err)
		return 0
	}

	return userID
}

// RequestRemoteWipe initiates a remote wipe for a device
func (s *Service) RequestRemoteWipe(deviceID string, reason string) error {
	s.logger.Warn("Remote wipe requested for device %s: %s", deviceID, reason)

	_, err := s.db.Exec(`
		UPDATE mobile_devices
		SET wipe_requested = TRUE,
		    wipe_requested_at = ?,
		    device_access_state = 'quarantined',
		    device_access_state_reason = ?
		WHERE device_id = ?`,
		time.Now(), reason, deviceID,
	)

	return err
}

// GetDeviceStatistics returns statistics about registered devices
func (s *Service) GetDeviceStatistics() map[string]interface{} {
	s.devicesMutex.RLock()
	defer s.devicesMutex.RUnlock()

	stats := map[string]interface{}{
		"total_devices": len(s.devices),
		"by_os":         make(map[string]int),
		"by_state":      make(map[string]int),
	}

	for _, device := range s.devices {
		// Count by OS
		if device.DeviceOS != "" {
			osKey := strings.ToLower(device.DeviceOS)
			stats["by_os"].(map[string]int)[osKey]++
		}

		// Count by state
		stats["by_state"].(map[string]int)[device.DeviceAccessState]++
	}

	return stats
}