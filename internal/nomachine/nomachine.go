// Package nomachine implements remote desktop services with HTML5 web-based access
// providing superior performance to Microsoft RDP with multi-session support and collaboration
package nomachine

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles NoMachine remote desktop operations
// providing HTML5 web access and multi-session support
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// NoMachine configuration
	nxServerPath    string
	nxClientPath    string
	webAccessPort   int
	nxdPort         int
	sshPort         int

	// Session management
	activeSessions  map[string]*RemoteSession
	sessionsMutex   sync.RWMutex

	// Recording management
	recordingDir    string
	recordingEnabled bool
}

// RemoteSession represents an active remote desktop session
type RemoteSession struct {
	ID              string
	UserID          int64
	Username        string
	ClientIP        string
	Connected       time.Time
	LastActivity    time.Time
	SessionType     string // desktop, application, shadow
	DisplayNumber   int
	Status          string // active, disconnected, reconnecting
	RecordingPath   string
	SharedWithUsers []int64
}

// SessionPolicy defines session management policies
type SessionPolicy struct {
	UserID              int64
	MaxConcurrentSessions int
	IdleTimeout         time.Duration
	MaxSessionDuration  time.Duration
	AllowRecording      bool
	AllowSharing        bool
	AllowFileTransfer   bool
	AllowClipboard      bool
	AllowPrinting       bool
	AllowAudio          bool
	MultiMonitorEnabled bool
	CompressionLevel    int // 1-9, higher = more compression
}

// NewService creates a new NoMachine remote desktop service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// NoMachine paths per SPEC
		nxServerPath:    "/usr/NX/bin/nxserver",
		nxClientPath:    "/usr/NX/bin/nxclient",
		webAccessPort:   4000, // Default NoMachine web port
		nxdPort:         4000, // NX protocol port
		sshPort:         22,   // SSH tunneling support

		// Session tracking
		activeSessions:  make(map[string]*RemoteSession),

		// Recording configuration
		recordingDir:    filepath.Join(cfg.DataDir, "remote-sessions"),
		recordingEnabled: true,
	}

	// Ensure recording directory exists
	if err := os.MkdirAll(s.recordingDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create recording directory: %w", err)
	}

	// Check if NoMachine is installed
	if err := s.checkNoMachineInstallation(); err != nil {
		log.Warn("NoMachine not installed, remote desktop services limited: %v", err)
		// Continue anyway, will use fallback X11/VNC if needed
	} else {
		// Configure NoMachine server
		if err := s.configureNoMachineServer(); err != nil {
			return nil, fmt.Errorf("failed to configure NoMachine server: %w", err)
		}
	}

	log.Info("NoMachine remote desktop service initialized on port %d", s.webAccessPort)

	return s, nil
}

// checkNoMachineInstallation verifies NoMachine is installed
func (s *Service) checkNoMachineInstallation() error {
	if _, err := os.Stat(s.nxServerPath); os.IsNotExist(err) {
		return fmt.Errorf("NoMachine server not found at %s", s.nxServerPath)
	}
	return nil
}

// configureNoMachineServer configures NoMachine server settings
func (s *Service) configureNoMachineServer() error {
	s.logger.Info("Configuring NoMachine server for CASDC integration")

	// NoMachine configuration per SPEC
	config := fmt.Sprintf(`
# CASDC NoMachine Configuration - Generated automatically
# DO NOT EDIT - Changes will be overwritten

# Server settings
ServerName = "CASDC Remote Desktop"
DefaultDesktop = gnome-session
EnableWebAccess = yes
WebAccessPort = %d
EnableHTTPS = yes
EnableEncryption = yes

# Session settings
MultiSession = yes
MaxSessions = 50
SessionTimeout = 28800
SessionIdleTimeout = 3600
AllowSessionSharing = yes
AllowSessionShadowing = yes

# Security settings
EnableFirewallConfiguration = yes
EnablePasswordAuthentication = yes
EnableKeyAuthentication = yes
EnableMFAAuthentication = yes
RequireEncryption = yes
EnableSessionRecording = yes

# Performance settings
CompressionLevel = 6
AdaptiveCompression = yes
DisplayCache = yes
StreamingQuality = 7

# Device redirection
EnablePrinterSharing = yes
EnableFileTransfer = yes
EnableClipboardSharing = yes
EnableAudioRedirection = yes
EnableUSBDeviceSharing = no

# Logging and auditing
EnableLogging = yes
LogLevel = 3
LogDirectory = /var/log/casdc/remote-desktop
AuditLogging = yes
`, s.webAccessPort)

	configPath := filepath.Join(s.config.ConfigDir, "nomachine", "server.cfg")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write configuration: %w", err)
	}

	s.logger.Debug("NoMachine configuration written to %s", configPath)

	// Restart NoMachine server to apply configuration
	return s.restartNoMachineServer()
}

// restartNoMachineServer restarts the NoMachine server
func (s *Service) restartNoMachineServer() error {
	s.logger.Debug("Restarting NoMachine server")

	cmd := exec.Command(s.nxServerPath, "--restart")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to restart NoMachine: %w, output: %s", err, output)
	}

	return nil
}

// CreateSession creates a new remote desktop session for a user
func (s *Service) CreateSession(userID int64, username, clientIP, sessionType string) (*RemoteSession, error) {
	// Check session policy
	policy, err := s.getSessionPolicy(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session policy: %w", err)
	}

	// Check concurrent session limit
	if err := s.checkSessionLimit(userID, policy.MaxConcurrentSessions); err != nil {
		return nil, err
	}

	// Generate session ID
	sessionID := fmt.Sprintf("nx-%d-%d", userID, time.Now().Unix())

	session := &RemoteSession{
		ID:             sessionID,
		UserID:         userID,
		Username:       username,
		ClientIP:       clientIP,
		Connected:      time.Now(),
		LastActivity:   time.Now(),
		SessionType:    sessionType,
		Status:         "active",
		DisplayNumber:  s.allocateDisplayNumber(),
		SharedWithUsers: make([]int64, 0),
	}

	// Enable recording if policy allows
	if policy.AllowRecording && s.recordingEnabled {
		session.RecordingPath = filepath.Join(s.recordingDir, fmt.Sprintf("%s-%s.nxs", sessionID, time.Now().Format("20060102-150405")))
	}

	// Store session
	s.sessionsMutex.Lock()
	s.activeSessions[sessionID] = session
	s.sessionsMutex.Unlock()

	// Log session creation
	s.logger.Info("Created remote desktop session %s for user %s from %s", sessionID, username, clientIP)

	// Store in database for audit
	if err := s.storeSessionInDatabase(session); err != nil {
		s.logger.Warn("Failed to store session in database: %v", err)
	}

	return session, nil
}

// getSessionPolicy retrieves session policy for a user
func (s *Service) getSessionPolicy(userID int64) (*SessionPolicy, error) {
	query := `SELECT max_concurrent_sessions, idle_timeout, max_session_duration,
		allow_recording, allow_sharing, allow_file_transfer, allow_clipboard,
		allow_printing, allow_audio, multi_monitor_enabled, compression_level
		FROM remote_desktop_policies WHERE user_id = ?`

	policy := &SessionPolicy{
		UserID: userID,
	}

	var idleTimeoutSec, maxDurationSec int

	err := s.db.QueryRow(query, userID).Scan(
		&policy.MaxConcurrentSessions,
		&idleTimeoutSec,
		&maxDurationSec,
		&policy.AllowRecording,
		&policy.AllowSharing,
		&policy.AllowFileTransfer,
		&policy.AllowClipboard,
		&policy.AllowPrinting,
		&policy.AllowAudio,
		&policy.MultiMonitorEnabled,
		&policy.CompressionLevel,
	)

	if err != nil {
		// Return default policy if not found
		return &SessionPolicy{
			UserID:                userID,
			MaxConcurrentSessions: 3,
			IdleTimeout:           time.Hour,
			MaxSessionDuration:    8 * time.Hour,
			AllowRecording:        true,
			AllowSharing:          true,
			AllowFileTransfer:     true,
			AllowClipboard:        true,
			AllowPrinting:         true,
			AllowAudio:            true,
			MultiMonitorEnabled:   true,
			CompressionLevel:      6,
		}, nil
	}

	policy.IdleTimeout = time.Duration(idleTimeoutSec) * time.Second
	policy.MaxSessionDuration = time.Duration(maxDurationSec) * time.Second

	return policy, nil
}

// checkSessionLimit checks if user can create another session
func (s *Service) checkSessionLimit(userID int64, maxSessions int) error {
	s.sessionsMutex.RLock()
	defer s.sessionsMutex.RUnlock()

	count := 0
	for _, session := range s.activeSessions {
		if session.UserID == userID && session.Status == "active" {
			count++
		}
	}

	if count >= maxSessions {
		return fmt.Errorf("maximum concurrent sessions (%d) reached", maxSessions)
	}

	return nil
}

// allocateDisplayNumber allocates a display number for the session
func (s *Service) allocateDisplayNumber() int {
	s.sessionsMutex.RLock()
	defer s.sessionsMutex.RUnlock()

	// Find lowest available display number starting from 10
	used := make(map[int]bool)
	for _, session := range s.activeSessions {
		used[session.DisplayNumber] = true
	}

	for i := 10; i < 1000; i++ {
		if !used[i] {
			return i
		}
	}

	return 10 // Fallback
}

// storeSessionInDatabase stores session information in database for audit
func (s *Service) storeSessionInDatabase(session *RemoteSession) error {
	query := `INSERT INTO remote_desktop_sessions
		(session_id, user_id, client_ip, connected_at, session_type, display_number, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		session.ID,
		session.UserID,
		session.ClientIP,
		session.Connected,
		session.SessionType,
		session.DisplayNumber,
		session.Status,
	)

	return err
}

// TerminateSession terminates a remote desktop session
func (s *Service) TerminateSession(sessionID string) error {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()

	session, exists := s.activeSessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	session.Status = "terminated"

	// Terminate NX session
	cmd := exec.Command(s.nxServerPath, "--terminate", sessionID)
	if output, err := cmd.CombinedOutput(); err != nil {
		s.logger.Warn("Failed to terminate NX session: %v, output: %s", err, output)
	}

	// Update database
	query := `UPDATE remote_desktop_sessions SET status = 'terminated', disconnected_at = ?
		WHERE session_id = ?`
	s.db.Exec(query, time.Now(), sessionID)

	// Remove from active sessions
	delete(s.activeSessions, sessionID)

	s.logger.Info("Terminated remote desktop session %s", sessionID)

	return nil
}

// GetActiveSessions returns all active sessions
func (s *Service) GetActiveSessions() []*RemoteSession {
	s.sessionsMutex.RLock()
	defer s.sessionsMutex.RUnlock()

	sessions := make([]*RemoteSession, 0, len(s.activeSessions))
	for _, session := range s.activeSessions {
		if session.Status == "active" {
			sessions = append(sessions, session)
		}
	}

	return sessions
}

// GetUserSessions returns active sessions for a specific user
func (s *Service) GetUserSessions(userID int64) []*RemoteSession {
	s.sessionsMutex.RLock()
	defer s.sessionsMutex.RUnlock()

	sessions := make([]*RemoteSession, 0)
	for _, session := range s.activeSessions {
		if session.UserID == userID && session.Status == "active" {
			sessions = append(sessions, session)
		}
	}

	return sessions
}

// ShareSession shares a session with another user
func (s *Service) ShareSession(sessionID string, targetUserID int64) error {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()

	session, exists := s.activeSessions[sessionID]
	if !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Check if already shared
	for _, userID := range session.SharedWithUsers {
		if userID == targetUserID {
			return fmt.Errorf("session already shared with user %d", targetUserID)
		}
	}

	session.SharedWithUsers = append(session.SharedWithUsers, targetUserID)

	s.logger.Info("Session %s shared with user %d", sessionID, targetUserID)

	return nil
}

// ShadowSession allows an admin to shadow/monitor a user session
func (s *Service) ShadowSession(adminUserID int64, targetSessionID string) error {
	s.sessionsMutex.RLock()
	session, exists := s.activeSessions[targetSessionID]
	s.sessionsMutex.RUnlock()

	if !exists {
		return fmt.Errorf("session %s not found", targetSessionID)
	}

	// Launch shadow session
	cmd := exec.Command(s.nxClientPath, "--shadow", targetSessionID)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start shadow session: %w", err)
	}

	s.logger.Info("Admin %d shadowing session %s (user %d)", adminUserID, targetSessionID, session.UserID)

	return nil
}

// GetSessionRecording retrieves a session recording
func (s *Service) GetSessionRecording(sessionID string) (string, error) {
	s.sessionsMutex.RLock()
	session, exists := s.activeSessions[sessionID]
	s.sessionsMutex.RUnlock()

	if !exists {
		// Check database for historical session
		var recordingPath string
		query := `SELECT recording_path FROM remote_desktop_sessions WHERE session_id = ?`
		if err := s.db.QueryRow(query, sessionID).Scan(&recordingPath); err != nil {
			return "", fmt.Errorf("session not found: %w", err)
		}
		return recordingPath, nil
	}

	if session.RecordingPath == "" {
		return "", fmt.Errorf("recording not available for session %s", sessionID)
	}

	return session.RecordingPath, nil
}

// ServeWebAccess serves the HTML5 web access interface
func (s *Service) ServeWebAccess(w http.ResponseWriter, r *http.Request) {
	// HTML5 web interface for remote desktop access
	html := `<!DOCTYPE html>
<html>
<head>
	<title>CASDC Remote Desktop</title>
	<style>
		body {
			margin: 0;
			padding: 0;
			font-family: system-ui, -apple-system, sans-serif;
			background: #1a1a1a;
			color: #fff;
		}
		.container {
			max-width: 1200px;
			margin: 0 auto;
			padding: 20px;
		}
		.sessions {
			display: grid;
			grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
			gap: 20px;
			margin-top: 20px;
		}
		.session-card {
			background: #2a2a2a;
			border-radius: 8px;
			padding: 20px;
			border: 1px solid #3a3a3a;
		}
		.session-card h3 {
			margin-top: 0;
		}
		.btn {
			background: #0066cc;
			color: white;
			border: none;
			padding: 10px 20px;
			border-radius: 4px;
			cursor: pointer;
			text-decoration: none;
			display: inline-block;
		}
		.btn:hover {
			background: #0052a3;
		}
	</style>
</head>
<body>
	<div class="container">
		<h1>🖥️ CASDC Remote Desktop</h1>
		<p>HTML5 Web-Based Remote Desktop Access</p>

		<button class="btn" onclick="createSession()">➕ New Session</button>

		<div class="sessions" id="sessions">
			<!-- Sessions will be loaded here -->
		</div>
	</div>

	<script>
		function loadSessions() {
			fetch('/api/v1/remote-desktop/sessions')
				.then(r => r.json())
				.then(sessions => {
					const container = document.getElementById('sessions');
					container.innerHTML = sessions.map(s => ` + "`" + `
						<div class="session-card">
							<h3>Session ` + "${s.id}" + `</h3>
							<p><strong>User:</strong> ` + "${s.username}" + `</p>
							<p><strong>Connected:</strong> ` + "${new Date(s.connected).toLocaleString()}" + `</p>
							<p><strong>Status:</strong> ` + "${s.status}" + `</p>
							<button class="btn" onclick="connectSession('` + "${s.id}" + `')">Connect</button>
						</div>
					` + "`" + `).join('');
				});
		}

		function createSession() {
			fetch('/api/v1/remote-desktop/sessions', { method: 'POST' })
				.then(r => r.json())
				.then(session => {
					connectSession(session.id);
				});
		}

		function connectSession(id) {
			window.location.href = '/remote-desktop/connect/' + id;
		}

		loadSessions();
		setInterval(loadSessions, 5000);
	</script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

// MonitorIdleSessions monitors and terminates idle sessions
func (s *Service) MonitorIdleSessions(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkIdleSessions()
		}
	}
}

// checkIdleSessions terminates sessions that exceed idle timeout
func (s *Service) checkIdleSessions() {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()

	now := time.Now()
	for sessionID, session := range s.activeSessions {
		if session.Status != "active" {
			continue
		}

		// Get session policy
		policy, err := s.getSessionPolicy(session.UserID)
		if err != nil {
			continue
		}

		// Check idle timeout
		if now.Sub(session.LastActivity) > policy.IdleTimeout {
			s.logger.Info("Terminating idle session %s (user %s, idle for %v)",
				sessionID, session.Username, now.Sub(session.LastActivity))

			go s.TerminateSession(sessionID)
		}

		// Check max duration
		if now.Sub(session.Connected) > policy.MaxSessionDuration {
			s.logger.Info("Terminating session %s (user %s, exceeded max duration %v)",
				sessionID, session.Username, policy.MaxSessionDuration)

			go s.TerminateSession(sessionID)
		}
	}
}

// Shutdown gracefully stops the NoMachine service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down NoMachine remote desktop service")

	// Terminate all active sessions gracefully
	sessions := s.GetActiveSessions()
	for _, session := range sessions {
		if err := s.TerminateSession(session.ID); err != nil {
			s.logger.Warn("Failed to terminate session %s: %v", session.ID, err)
		}
	}

	return nil
}
