// Package services provides service configuration takeover and management
// Manages external services (nginx, postfix, bind, dovecot, etc.) by generating and deploying configurations
package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/configgen"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Manager manages external service configurations
// Takes control of system services by generating and deploying configurations from database
type Manager struct {
	db        *database.DB
	config    *config.Config
	logger    *logger.Logger
	configGen *configgen.Service

	// Managed services
	services      map[string]*Service
	servicesMutex sync.RWMutex
}

// Service represents a managed external service
type Service struct {
	Name              string
	DisplayName       string
	ConfigPath        string
	ConfigBackupPath  string
	TestCommand       []string // Command to test configuration validity
	ReloadCommand     []string // Command to reload service
	RestartCommand    []string // Command to restart service
	StatusCommand     []string // Command to check service status
	Enabled           bool
	Managed           bool // Whether CASDC manages this service
	LastConfigUpdate  time.Time
	LastReload        time.Time
	Status            string // running, stopped, error
}

// NewManager creates a new service manager
func NewManager(db *database.DB, cfg *config.Config, log *logger.Logger, configGen *configgen.Service) *Manager {
	return &Manager{
		db:        db,
		config:    cfg,
		logger:    log,
		configGen: configGen,
		services:  make(map[string]*Service),
	}
}

// Start initializes the service manager
func (m *Manager) Start() error {
	m.logger.Info("Starting service manager")

	// Register all manageable services
	m.registerServices()

	// Detect which services are installed
	if err := m.detectServices(); err != nil {
		m.logger.Warn("Service detection had errors: %v", err)
	}

	// Take control of managed services
	if err := m.takeControl(); err != nil {
		return fmt.Errorf("failed to take control of services: %w", err)
	}

	m.logger.Info("Service manager started managing %d services", m.getManagedCount())
	return nil
}

// Stop gracefully stops the service manager
func (m *Manager) Stop() error {
	m.logger.Info("Stopping service manager")
	m.logger.Info("Service manager stopped")
	return nil
}

// registerServices registers all services that CASDC can manage
func (m *Manager) registerServices() {
	services := []*Service{
		{
			Name:        "nginx",
			DisplayName: "Nginx Web Server",
			ConfigPath:  "/etc/nginx/nginx.conf",
			ConfigBackupPath: "/etc/nginx/nginx.conf.casdc-backup",
			TestCommand:     []string{"nginx", "-t"},
			ReloadCommand:   []string{"systemctl", "reload", "nginx"},
			RestartCommand:  []string{"systemctl", "restart", "nginx"},
			StatusCommand:   []string{"systemctl", "is-active", "nginx"},
			Enabled:         true,
		},
		{
			Name:        "postfix",
			DisplayName: "Postfix Mail Server",
			ConfigPath:  "/etc/postfix/main.cf",
			ConfigBackupPath: "/etc/postfix/main.cf.casdc-backup",
			TestCommand:     []string{"postfix", "check"},
			ReloadCommand:   []string{"systemctl", "reload", "postfix"},
			RestartCommand:  []string{"systemctl", "restart", "postfix"},
			StatusCommand:   []string{"systemctl", "is-active", "postfix"},
			Enabled:         true,
		},
		{
			Name:        "bind9",
			DisplayName: "BIND DNS Server",
			ConfigPath:  "/etc/bind/named.conf",
			ConfigBackupPath: "/etc/bind/named.conf.casdc-backup",
			TestCommand:     []string{"named-checkconf"},
			ReloadCommand:   []string{"systemctl", "reload", "bind9"},
			RestartCommand:  []string{"systemctl", "restart", "bind9"},
			StatusCommand:   []string{"systemctl", "is-active", "bind9"},
			Enabled:         true,
		},
		{
			Name:        "named",
			DisplayName: "BIND DNS Server (RHEL)",
			ConfigPath:  "/etc/named.conf",
			ConfigBackupPath: "/etc/named.conf.casdc-backup",
			TestCommand:     []string{"named-checkconf"},
			ReloadCommand:   []string{"systemctl", "reload", "named"},
			RestartCommand:  []string{"systemctl", "restart", "named"},
			StatusCommand:   []string{"systemctl", "is-active", "named"},
			Enabled:         true,
		},
		{
			Name:        "dovecot",
			DisplayName: "Dovecot IMAP/POP3 Server",
			ConfigPath:  "/etc/dovecot/dovecot.conf",
			ConfigBackupPath: "/etc/dovecot/dovecot.conf.casdc-backup",
			TestCommand:     []string{"doveconf", "-n"},
			ReloadCommand:   []string{"systemctl", "reload", "dovecot"},
			RestartCommand:  []string{"systemctl", "restart", "dovecot"},
			StatusCommand:   []string{"systemctl", "is-active", "dovecot"},
			Enabled:         true,
		},
		{
			Name:        "isc-dhcp-server",
			DisplayName: "ISC DHCP Server",
			ConfigPath:  "/etc/dhcp/dhcpd.conf",
			ConfigBackupPath: "/etc/dhcp/dhcpd.conf.casdc-backup",
			TestCommand:     []string{"dhcpd", "-t", "-cf", "/etc/dhcp/dhcpd.conf"},
			ReloadCommand:   []string{"systemctl", "restart", "isc-dhcp-server"},
			RestartCommand:  []string{"systemctl", "restart", "isc-dhcp-server"},
			StatusCommand:   []string{"systemctl", "is-active", "isc-dhcp-server"},
			Enabled:         true,
		},
		{
			Name:        "dhcpd",
			DisplayName: "ISC DHCP Server (RHEL)",
			ConfigPath:  "/etc/dhcp/dhcpd.conf",
			ConfigBackupPath: "/etc/dhcp/dhcpd.conf.casdc-backup",
			TestCommand:     []string{"dhcpd", "-t", "-cf", "/etc/dhcp/dhcpd.conf"},
			ReloadCommand:   []string{"systemctl", "restart", "dhcpd"},
			RestartCommand:  []string{"systemctl", "restart", "dhcpd"},
			StatusCommand:   []string{"systemctl", "is-active", "dhcpd"},
			Enabled:         true,
		},
	}

	m.servicesMutex.Lock()
	defer m.servicesMutex.Unlock()

	for _, svc := range services {
		m.services[svc.Name] = svc
		m.logger.Debug("Registered manageable service: %s", svc.DisplayName)
	}
}

// detectServices detects which services are installed and available
func (m *Manager) detectServices() error {
	m.servicesMutex.Lock()
	defer m.servicesMutex.Unlock()

	for name, svc := range m.services {
		// Check if configuration file exists
		if _, err := os.Stat(svc.ConfigPath); err == nil {
			svc.Managed = true
			m.logger.Info("Detected installed service: %s at %s", svc.DisplayName, svc.ConfigPath)
		} else {
			m.logger.Debug("Service not installed: %s (no config at %s)", svc.DisplayName, svc.ConfigPath)
			svc.Managed = false
		}

		// Check service status
		if svc.Managed {
			status := m.checkServiceStatus(svc)
			svc.Status = status
			m.logger.Debug("Service %s status: %s", name, status)
		}
	}

	return nil
}

// takeControl takes control of all managed services
func (m *Manager) takeControl() error {
	m.servicesMutex.RLock()
	managedServices := []*Service{}
	for _, svc := range m.services {
		if svc.Managed && svc.Enabled {
			managedServices = append(managedServices, svc)
		}
	}
	m.servicesMutex.RUnlock()

	for _, svc := range managedServices {
		if err := m.takeControlOfService(svc); err != nil {
			m.logger.Error("Failed to take control of %s: %v", svc.DisplayName, err)
			// Continue with other services
		}
	}

	return nil
}

// takeControlOfService takes control of a specific service
func (m *Manager) takeControlOfService(svc *Service) error {
	m.logger.Info("Taking control of service: %s", svc.DisplayName)

	// Backup existing configuration
	if err := m.backupConfig(svc); err != nil {
		return fmt.Errorf("failed to backup config: %w", err)
	}

	// Generate new configuration
	if err := m.generateAndDeployConfig(svc); err != nil {
		return fmt.Errorf("failed to generate config: %w", err)
	}

	// Test configuration
	if err := m.testConfig(svc); err != nil {
		m.logger.Error("Configuration test failed for %s, restoring backup", svc.DisplayName)
		m.restoreBackup(svc)
		return fmt.Errorf("config test failed: %w", err)
	}

	// Reload service
	if err := m.reloadService(svc); err != nil {
		m.logger.Error("Failed to reload %s, restoring backup", svc.DisplayName)
		m.restoreBackup(svc)
		return fmt.Errorf("reload failed: %w", err)
	}

	svc.LastConfigUpdate = time.Now()
	svc.LastReload = time.Now()

	m.logger.Info("Successfully took control of service: %s", svc.DisplayName)
	return nil
}

// generateAndDeployConfig generates and deploys configuration for a service
func (m *Manager) generateAndDeployConfig(svc *Service) error {
	var configContent string
	var err error

	// Generate configuration based on service
	switch svc.Name {
	case "nginx":
		configContent, err = m.configGen.GenerateNginxConfig()
	case "postfix":
		configContent, err = m.configGen.GeneratePostfixConfig()
	case "bind9", "named":
		configContent, err = m.configGen.GenerateBindConfig()
	case "dovecot":
		configContent, err = m.configGen.GenerateDovecotConfig()
	case "isc-dhcp-server", "dhcpd":
		configContent, err = m.configGen.GenerateDHCPConfig()
	default:
		return fmt.Errorf("no configuration generator for service: %s", svc.Name)
	}

	if err != nil {
		return fmt.Errorf("failed to generate config: %w", err)
	}

	// Write configuration to file
	if err := m.writeConfig(svc.ConfigPath, configContent); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	m.logger.Info("Deployed configuration for %s to %s", svc.DisplayName, svc.ConfigPath)
	return nil
}

// backupConfig backs up the current service configuration
func (m *Manager) backupConfig(svc *Service) error {
	// Check if config exists
	if _, err := os.Stat(svc.ConfigPath); os.IsNotExist(err) {
		m.logger.Debug("No existing config to backup for %s", svc.Name)
		return nil
	}

	// Read existing config
	content, err := os.ReadFile(svc.ConfigPath)
	if err != nil {
		return err
	}

	// Write backup
	if err := os.WriteFile(svc.ConfigBackupPath, content, 0644); err != nil {
		return err
	}

	m.logger.Info("Backed up %s configuration to %s", svc.DisplayName, svc.ConfigBackupPath)
	return nil
}

// restoreBackup restores a service configuration from backup
func (m *Manager) restoreBackup(svc *Service) error {
	// Check if backup exists
	if _, err := os.Stat(svc.ConfigBackupPath); os.IsNotExist(err) {
		m.logger.Warn("No backup to restore for %s", svc.Name)
		return nil
	}

	// Read backup
	content, err := os.ReadFile(svc.ConfigBackupPath)
	if err != nil {
		return err
	}

	// Write back to original location
	if err := os.WriteFile(svc.ConfigPath, content, 0644); err != nil {
		return err
	}

	m.logger.Info("Restored %s configuration from backup", svc.DisplayName)
	return nil
}

// writeConfig writes configuration content to a file
func (m *Manager) writeConfig(path, content string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Write file
	return os.WriteFile(path, []byte(content), 0644)
}

// testConfig tests a service configuration for validity
func (m *Manager) testConfig(svc *Service) error {
	if len(svc.TestCommand) == 0 {
		m.logger.Debug("No test command defined for %s, skipping validation", svc.Name)
		return nil
	}

	m.logger.Debug("Testing configuration for %s: %v", svc.DisplayName, svc.TestCommand)

	cmd := exec.Command(svc.TestCommand[0], svc.TestCommand[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		m.logger.Error("Configuration test failed for %s: %s", svc.DisplayName, string(output))
		return fmt.Errorf("test failed: %w", err)
	}

	m.logger.Info("Configuration test passed for %s", svc.DisplayName)
	return nil
}

// reloadService reloads a service to apply new configuration
func (m *Manager) reloadService(svc *Service) error {
	if len(svc.ReloadCommand) == 0 {
		m.logger.Debug("No reload command defined for %s", svc.Name)
		return nil
	}

	m.logger.Info("Reloading service: %s", svc.DisplayName)

	cmd := exec.Command(svc.ReloadCommand[0], svc.ReloadCommand[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		m.logger.Error("Failed to reload %s: %s", svc.DisplayName, string(output))
		return fmt.Errorf("reload failed: %w", err)
	}

	m.logger.Info("Successfully reloaded service: %s", svc.DisplayName)
	return nil
}

// restartService restarts a service
func (m *Manager) restartService(svc *Service) error {
	if len(svc.RestartCommand) == 0 {
		m.logger.Debug("No restart command defined for %s", svc.Name)
		return nil
	}

	m.logger.Info("Restarting service: %s", svc.DisplayName)

	cmd := exec.Command(svc.RestartCommand[0], svc.RestartCommand[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		m.logger.Error("Failed to restart %s: %s", svc.DisplayName, string(output))
		return fmt.Errorf("restart failed: %w", err)
	}

	m.logger.Info("Successfully restarted service: %s", svc.DisplayName)
	return nil
}

// checkServiceStatus checks if a service is running
func (m *Manager) checkServiceStatus(svc *Service) string {
	if len(svc.StatusCommand) == 0 {
		return "unknown"
	}

	cmd := exec.Command(svc.StatusCommand[0], svc.StatusCommand[1:]...)
	output, err := cmd.CombinedOutput()

	if err != nil {
		return "stopped"
	}

	status := strings.TrimSpace(string(output))
	if status == "active" || status == "running" {
		return "running"
	}

	return status
}

// RegenerateAllConfigs regenerates and deploys all service configurations
func (m *Manager) RegenerateAllConfigs() error {
	m.logger.Info("Regenerating all service configurations")

	m.servicesMutex.RLock()
	managedServices := []*Service{}
	for _, svc := range m.services {
		if svc.Managed && svc.Enabled {
			managedServices = append(managedServices, svc)
		}
	}
	m.servicesMutex.RUnlock()

	errors := []string{}
	for _, svc := range managedServices {
		if err := m.generateAndDeployConfig(svc); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", svc.Name, err))
			continue
		}

		if err := m.testConfig(svc); err != nil {
			errors = append(errors, fmt.Sprintf("%s: config test failed: %v", svc.Name, err))
			continue
		}

		if err := m.reloadService(svc); err != nil {
			errors = append(errors, fmt.Sprintf("%s: reload failed: %v", svc.Name, err))
			continue
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors regenerating configs: %s", strings.Join(errors, "; "))
	}

	m.logger.Info("Successfully regenerated all service configurations")
	return nil
}

// RegenerateConfig regenerates configuration for a specific service
func (m *Manager) RegenerateConfig(serviceName string) error {
	m.servicesMutex.RLock()
	svc, exists := m.services[serviceName]
	m.servicesMutex.RUnlock()

	if !exists {
		return fmt.Errorf("service not found: %s", serviceName)
	}

	if !svc.Managed {
		return fmt.Errorf("service not managed: %s", serviceName)
	}

	m.logger.Info("Regenerating configuration for %s", svc.DisplayName)

	if err := m.backupConfig(svc); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	if err := m.generateAndDeployConfig(svc); err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	if err := m.testConfig(svc); err != nil {
		m.restoreBackup(svc)
		return fmt.Errorf("test failed: %w", err)
	}

	if err := m.reloadService(svc); err != nil {
		m.restoreBackup(svc)
		return fmt.Errorf("reload failed: %w", err)
	}

	svc.LastConfigUpdate = time.Now()
	svc.LastReload = time.Now()

	return nil
}

// GetManagedServices returns all managed services
func (m *Manager) GetManagedServices() []*Service {
	m.servicesMutex.RLock()
	defer m.servicesMutex.RUnlock()

	services := make([]*Service, 0, len(m.services))
	for _, svc := range m.services {
		if svc.Managed {
			services = append(services, svc)
		}
	}

	return services
}

// GetService returns a specific service
func (m *Manager) GetService(name string) (*Service, error) {
	m.servicesMutex.RLock()
	svc, exists := m.services[name]
	m.servicesMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("service not found: %s", name)
	}

	return svc, nil
}

// getManagedCount returns the count of managed services
func (m *Manager) getManagedCount() int {
	count := 0
	for _, svc := range m.services {
		if svc.Managed {
			count++
		}
	}
	return count
}

// RefreshServiceStatus refreshes the status of all managed services
func (m *Manager) RefreshServiceStatus() {
	m.servicesMutex.Lock()
	defer m.servicesMutex.Unlock()

	for _, svc := range m.services {
		if svc.Managed {
			svc.Status = m.checkServiceStatus(svc)
		}
	}
}