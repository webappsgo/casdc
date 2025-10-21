// Package ntp provides Network Time Protocol services for domain time synchronization
// Critical for Kerberos authentication and Active Directory functionality
package ntp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the NTP time synchronization service
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// NTP service configuration
	ntpConfDir    string
	serviceName   string
	isPrimary     bool // Primary DC is authoritative time source
	upstreamServers []string

	// Status tracking
	isRunning bool
	syncStatus string
	lastSync  time.Time
}

// NTPConfig represents NTP server configuration
type NTPConfig struct {
	ConfigDir       string
	ServiceName     string
	IsPrimary       bool
	UpstreamServers []string
	LocalStratum    int
	Domain          string
	ListenAddress   string
}

// NewService creates a new NTP time synchronization service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,
		upstreamServers: []string{
			"0.pool.ntp.org",
			"1.pool.ntp.org",
			"2.pool.ntp.org",
			"3.pool.ntp.org",
		},
	}

	// Detect NTP service type and paths
	if err := s.detectNTPService(); err != nil {
		return nil, fmt.Errorf("failed to detect NTP service: %w", err)
	}

	// Take control of NTP configuration
	if err := s.takeNTPControl(); err != nil {
		return nil, fmt.Errorf("failed to take NTP control: %w", err)
	}

	// Generate NTP configuration
	if err := s.generateNTPConfig(); err != nil {
		return nil, fmt.Errorf("failed to generate NTP config: %w", err)
	}

	return s, nil
}

// detectNTPService detects which NTP implementation is installed
func (s *Service) detectNTPService() error {
	s.logger.Debug("Detecting NTP service implementation")

	// Check for chrony (preferred for modern systems)
	if _, err := os.Stat("/etc/chrony"); err == nil {
		s.ntpConfDir = "/etc/chrony"
		s.serviceName = "chronyd"
		s.logger.Info("Detected chrony NTP service at %s", s.ntpConfDir)
		return nil
	}

	// Check for chrony alternative path
	if _, err := os.Stat("/etc/chrony.d"); err == nil {
		s.ntpConfDir = "/etc"
		s.serviceName = "chronyd"
		s.logger.Info("Detected chrony NTP service at %s", s.ntpConfDir)
		return nil
	}

	// Check for ntpd (classic NTP daemon)
	if _, err := os.Stat("/etc/ntp.conf"); err == nil {
		s.ntpConfDir = "/etc"
		s.serviceName = "ntpd"
		s.logger.Info("Detected ntpd NTP service at %s", s.ntpConfDir)
		return nil
	}

	// Check for systemd-timesyncd (basic time sync)
	if _, err := os.Stat("/etc/systemd/timesyncd.conf"); err == nil {
		s.ntpConfDir = "/etc/systemd"
		s.serviceName = "systemd-timesyncd"
		s.logger.Info("Detected systemd-timesyncd at %s", s.ntpConfDir)
		return nil
	}

	// Default to chrony (most common on modern Linux)
	s.ntpConfDir = "/etc/chrony"
	s.serviceName = "chronyd"
	s.logger.Warn("NTP service not detected, defaulting to chrony")
	return nil
}

// takeNTPControl takes control of NTP configuration
func (s *Service) takeNTPControl() error {
	s.logger.Info("Taking control of NTP configuration")

	// Create CASDC NTP configuration directory
	casdcConfDir := filepath.Join(s.config.ConfigDir, "ntp")
	if err := os.MkdirAll(casdcConfDir, 0755); err != nil {
		return fmt.Errorf("failed to create NTP config directory: %w", err)
	}

	// Backup original configuration
	var originalConf string
	switch s.serviceName {
	case "chronyd":
		originalConf = filepath.Join(s.ntpConfDir, "chrony.conf")
	case "ntpd":
		originalConf = filepath.Join(s.ntpConfDir, "ntp.conf")
	case "systemd-timesyncd":
		originalConf = filepath.Join(s.ntpConfDir, "timesyncd.conf")
	}

	if originalConf != "" {
		if _, err := os.Stat(originalConf); err == nil {
			backupConf := originalConf + ".casdc-backup"
			if _, err := os.Stat(backupConf); os.IsNotExist(err) {
				if err := s.copyFile(originalConf, backupConf); err != nil {
					s.logger.Warn("Failed to backup original NTP config: %v", err)
				} else {
					s.logger.Info("Backed up original NTP config to %s", backupConf)
				}
			}
		}
	}

	return nil
}

// generateNTPConfig generates NTP configuration based on detected service
func (s *Service) generateNTPConfig() error {
	s.logger.Debug("Generating NTP configuration for %s", s.serviceName)

	config := &NTPConfig{
		ConfigDir:       s.ntpConfDir,
		ServiceName:     s.serviceName,
		IsPrimary:       s.isPrimary,
		UpstreamServers: s.upstreamServers,
		LocalStratum:    10, // Stratum 10 for primary DC, 11 for secondary
		Domain:          s.config.Domain,
		ListenAddress:   s.config.ServerAddress,
	}

	var err error
	switch s.serviceName {
	case "chronyd":
		err = s.generateChronyConfig(config)
	case "ntpd":
		err = s.generateNTPDConfig(config)
	case "systemd-timesyncd":
		err = s.generateTimesyncDConfig(config)
	default:
		return fmt.Errorf("unsupported NTP service: %s", s.serviceName)
	}

	if err != nil {
		return fmt.Errorf("failed to generate %s config: %w", s.serviceName, err)
	}

	s.logger.Info("Generated NTP configuration for %s", s.serviceName)
	return nil
}

// generateChronyConfig generates chrony configuration (preferred)
func (s *Service) generateChronyConfig(config *NTPConfig) error {
	confContent := fmt.Sprintf(`# CASDC Chrony Configuration - Domain Controller Time Synchronization
# Generated automatically - DO NOT EDIT MANUALLY

# Upstream NTP servers for time synchronization
%s

# Allow clients from internal networks to sync with this server
allow 10.0.0.0/8
allow 172.16.0.0/12
allow 192.168.0.0/16
allow 127.0.0.1

# Serve time even if not synchronized to upstream servers (important for primary DC)
local stratum %d

# Record the rate at which the system clock gains/loses time
driftfile /var/lib/chrony/drift

# Save NTS keys and cookies
ntsdumpdir /var/lib/chrony

# Enable kernel synchronization
rtcsync

# Step the system clock if the adjustment is larger than 1 second
makestep 1.0 3

# Enable hardware timestamping if available
#hwtimestamp *

# Log file location
logdir /var/log/casdc

# Select which information is logged
log tracking measurements statistics
`, s.generateServerList(config.UpstreamServers), config.LocalStratum)

	confPath := filepath.Join(config.ConfigDir, "chrony.conf")
	if err := s.writeFile(confPath, confContent); err != nil {
		return fmt.Errorf("failed to write chrony.conf: %w", err)
	}

	return nil
}

// generateNTPDConfig generates classic ntpd configuration
func (s *Service) generateNTPDConfig(config *NTPConfig) error {
	confContent := fmt.Sprintf(`# CASDC NTP Configuration - Domain Controller Time Synchronization
# Generated automatically - DO NOT EDIT MANUALLY

# Upstream NTP servers
%s

# Serve time to local network
restrict default kod nomodify notrap nopeer noquery
restrict -6 default kod nomodify notrap nopeer noquery
restrict 127.0.0.1
restrict ::1

# Allow clients from internal networks
restrict 10.0.0.0 mask 255.0.0.0 nomodify notrap
restrict 172.16.0.0 mask 255.240.0.0 nomodify notrap
restrict 192.168.0.0 mask 255.255.0.0 nomodify notrap

# Drift file location
driftfile /var/lib/ntp/drift

# Log file
logfile /var/log/casdc/ntp.log

# Statistics
statsdir /var/log/casdc/ntp/
statistics loopstats peerstats clockstats
filegen loopstats file loopstats type day enable
filegen peerstats file peerstats type day enable
filegen clockstats file clockstats type day enable
`, s.generateNTPDServerList(config.UpstreamServers))

	confPath := filepath.Join(config.ConfigDir, "ntp.conf")
	if err := s.writeFile(confPath, confContent); err != nil {
		return fmt.Errorf("failed to write ntp.conf: %w", err)
	}

	return nil
}

// generateTimesyncDConfig generates systemd-timesyncd configuration
func (s *Service) generateTimesyncDConfig(config *NTPConfig) error {
	confContent := fmt.Sprintf(`# CASDC systemd-timesyncd Configuration
# Generated automatically - DO NOT EDIT MANUALLY

[Time]
NTP=%s
FallbackNTP=0.pool.ntp.org 1.pool.ntp.org 2.pool.ntp.org 3.pool.ntp.org
`, strings.Join(config.UpstreamServers, " "))

	confPath := filepath.Join(config.ConfigDir, "timesyncd.conf")
	if err := s.writeFile(confPath, confContent); err != nil {
		return fmt.Errorf("failed to write timesyncd.conf: %w", err)
	}

	return nil
}

// generateServerList generates chrony server list
func (s *Service) generateServerList(servers []string) string {
	var serverList strings.Builder
	for _, server := range servers {
		serverList.WriteString(fmt.Sprintf("server %s iburst\n", server))
	}
	return serverList.String()
}

// generateNTPDServerList generates ntpd server list
func (s *Service) generateNTPDServerList(servers []string) string {
	var serverList strings.Builder
	for _, server := range servers {
		serverList.WriteString(fmt.Sprintf("server %s iburst\n", server))
	}
	return serverList.String()
}

// Start starts the NTP service
func (s *Service) Start() error {
	s.logger.Info("Starting NTP service: %s", s.serviceName)

	// Stop the service first to ensure clean start
	if err := s.stopService(); err != nil {
		s.logger.Warn("Failed to stop NTP service before starting: %v", err)
	}

	// Start the service
	cmd := exec.Command("systemctl", "start", s.serviceName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to start %s: %w", s.serviceName, err)
	}

	// Enable service for automatic startup
	cmd = exec.Command("systemctl", "enable", s.serviceName)
	if err := cmd.Run(); err != nil {
		s.logger.Warn("Failed to enable %s: %v", s.serviceName, err)
	}

	// Wait for service to start
	time.Sleep(2 * time.Second)

	// Verify service is running
	if err := s.verifyServiceRunning(); err != nil {
		return fmt.Errorf("NTP service failed to start: %w", err)
	}

	s.isRunning = true
	s.logger.Info("NTP service %s started successfully", s.serviceName)
	return nil
}

// stopService stops the NTP service
func (s *Service) stopService() error {
	cmd := exec.Command("systemctl", "stop", s.serviceName)
	return cmd.Run()
}

// verifyServiceRunning verifies the NTP service is running
func (s *Service) verifyServiceRunning() error {
	cmd := exec.Command("systemctl", "is-active", s.serviceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("service not running: %s", string(output))
	}

	if strings.TrimSpace(string(output)) != "active" {
		return fmt.Errorf("service status: %s", string(output))
	}

	return nil
}

// GetSyncStatus returns the current time synchronization status
func (s *Service) GetSyncStatus() map[string]interface{} {
	status := map[string]interface{}{
		"running":      s.isRunning,
		"service_name": s.serviceName,
		"is_primary":   s.isPrimary,
		"last_sync":    s.lastSync,
		"sync_status":  s.syncStatus,
	}

	// Get current sync status from service
	var cmd *exec.Cmd
	switch s.serviceName {
	case "chronyd":
		cmd = exec.Command("chronyc", "tracking")
	case "ntpd":
		cmd = exec.Command("ntpq", "-p")
	case "systemd-timesyncd":
		cmd = exec.Command("timedatectl", "timesync-status")
	}

	if cmd != nil {
		output, err := cmd.CombinedOutput()
		if err == nil {
			status["sync_details"] = string(output)
		}
	}

	return status
}

// Shutdown gracefully stops the NTP service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down NTP service")

	if err := s.stopService(); err != nil {
		s.logger.Warn("Failed to stop NTP service: %v", err)
	}

	s.isRunning = false
	return nil
}

// Helper functions

// copyFile copies a file from src to dst
func (s *Service) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = sourceFile.WriteTo(destFile)
	return err
}

// writeFile writes content to a file
func (s *Service) writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
