// Package samba implements Windows file sharing services with Samba integration
// providing complete Windows ACL support, home directory mapping, and group shares
// with automatic configuration from database
package samba

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Samba file sharing operations
// providing Windows-compatible file sharing with ACL support
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Samba configuration
	sambaConfPath  string
	shareBasePath  string
	homeBasePath   string

	// Share management
	shares      map[int64]*Share
	sharesMutex sync.RWMutex
}

// Share represents a Samba file share configuration
type Share struct {
	ID          int64
	Name        string
	Path        string
	Description string
	Enabled     bool
	ReadOnly    bool
	Browseable  bool
	GuestOK     bool
	CreateMask  string
	DirMask     string
	ForceUser   string
	ForceGroup  string
	ValidUsers  []string
	WriteList   []string
	AdminUsers  []string
	VetoFiles   []string
}

// SharePermission represents access permissions for a share
type SharePermission struct {
	ShareID       int64
	PrincipalType string // "user" or "group"
	PrincipalID   int64
	Permission    string // "read", "write", "full"
}

// NewService creates a new Samba service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// Samba configuration paths
		sambaConfPath: "/etc/samba/smb.conf",
		shareBasePath: filepath.Join(cfg.DataDir, "shares"),
		homeBasePath:  filepath.Join(cfg.DataDir, "home"),

		shares: make(map[int64]*Share),
	}

	// Ensure share directories exist
	if err := s.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create share directories: %w", err)
	}

	// Load shares from database
	if err := s.loadShares(); err != nil {
		log.Warn("Failed to load shares from database: %v", err)
	}

	// Generate Samba configuration
	if err := s.generateConfiguration(); err != nil {
		return nil, fmt.Errorf("failed to generate Samba configuration: %w", err)
	}

	return s, nil
}

// ensureDirectories creates required base directories for shares and home folders
func (s *Service) ensureDirectories() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.shareBasePath, 0755},
		{s.homeBasePath, 0755},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
	}

	return nil
}

// loadShares retrieves all file shares from database
func (s *Service) loadShares() error {
	query := `SELECT id, name, path, description, enabled, read_only, browseable,
	          guest_ok, create_mask, directory_mask, force_user, force_group
	          FROM file_shares WHERE enabled = 1`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query shares: %w", err)
	}
	defer rows.Close()

	s.sharesMutex.Lock()
	defer s.sharesMutex.Unlock()

	for rows.Next() {
		share := &Share{}
		err := rows.Scan(
			&share.ID,
			&share.Name,
			&share.Path,
			&share.Description,
			&share.Enabled,
			&share.ReadOnly,
			&share.Browseable,
			&share.GuestOK,
			&share.CreateMask,
			&share.DirMask,
			&share.ForceUser,
			&share.ForceGroup,
		)
		if err != nil {
			s.logger.Error("Failed to scan share: %v", err)
			continue
		}

		// Load permissions for this share
		if err := s.loadSharePermissions(share); err != nil {
			s.logger.Error("Failed to load permissions for share %s: %v", share.Name, err)
		}

		s.shares[share.ID] = share
		s.logger.Debug("Loaded share: %s at %s", share.Name, share.Path)
	}

	return rows.Err()
}

// loadSharePermissions loads ACL permissions for a share
func (s *Service) loadSharePermissions(share *Share) error {
	query := `SELECT principal_type, principal_id, permission
	          FROM share_permissions WHERE share_id = ?`

	rows, err := s.db.Query(query, share.ID)
	if err != nil {
		return fmt.Errorf("failed to query permissions: %w", err)
	}
	defer rows.Close()

	var validUsers []string
	var writeList []string
	var adminUsers []string

	for rows.Next() {
		var principalType string
		var principalID int64
		var permission string

		if err := rows.Scan(&principalType, &principalID, &permission); err != nil {
			s.logger.Error("Failed to scan permission: %v", err)
			continue
		}

		// Get principal name
		var name string
		if principalType == "user" {
			err = s.db.QueryRow("SELECT username FROM users WHERE id = ?", principalID).Scan(&name)
		} else {
			err = s.db.QueryRow("SELECT name FROM groups WHERE id = ?", principalID).Scan(&name)
			name = "@" + name // Group names prefixed with @
		}

		if err != nil {
			s.logger.Error("Failed to get principal name: %v", err)
			continue
		}

		// Add to appropriate access list
		validUsers = append(validUsers, name)

		switch permission {
		case "write", "full":
			writeList = append(writeList, name)
		}

		if permission == "full" {
			adminUsers = append(adminUsers, name)
		}
	}

	share.ValidUsers = validUsers
	share.WriteList = writeList
	share.AdminUsers = adminUsers

	return rows.Err()
}

// generateConfiguration creates the complete Samba configuration file
func (s *Service) generateConfiguration() error {
	var config strings.Builder

	// Global section
	config.WriteString("# CASDC Samba Configuration - Generated automatically\n")
	config.WriteString("# DO NOT EDIT - Changes will be overwritten\n\n")

	config.WriteString("[global]\n")
	config.WriteString(fmt.Sprintf("   workgroup = %s\n", s.getWorkgroup()))
	config.WriteString(fmt.Sprintf("   server string = CASDC File Server (%s)\n", s.config.Organization))
	config.WriteString(fmt.Sprintf("   netbios name = %s\n", s.getNetBIOSName()))
	config.WriteString("   security = user\n")
	config.WriteString("   passdb backend = tdbsam\n")
	config.WriteString("   map to guest = bad user\n")
	config.WriteString("   dns proxy = no\n")
	config.WriteString("   load printers = no\n")
	config.WriteString("   printing = bsd\n")
	config.WriteString("   printcap name = /dev/null\n")
	config.WriteString("   disable spoolss = yes\n\n")

	// Performance and compatibility
	config.WriteString("   # Performance tuning\n")
	config.WriteString("   socket options = TCP_NODELAY IPTOS_LOWDELAY SO_RCVBUF=131072 SO_SNDBUF=131072\n")
	config.WriteString("   read raw = yes\n")
	config.WriteString("   write raw = yes\n")
	config.WriteString("   max xmit = 65535\n")
	config.WriteString("   dead time = 15\n")
	config.WriteString("   getwd cache = yes\n\n")

	// Logging
	config.WriteString("   # Logging\n")
	config.WriteString("   log file = /var/log/casdc/samba-%m.log\n")
	config.WriteString("   max log size = 1000\n")
	config.WriteString("   log level = 1\n\n")

	// Windows ACL support
	config.WriteString("   # Windows ACL support\n")
	config.WriteString("   ea support = yes\n")
	config.WriteString("   store dos attributes = yes\n")
	config.WriteString("   map acl inherit = yes\n")
	config.WriteString("   vfs objects = acl_xattr\n\n")

	// Home directories
	config.WriteString("[homes]\n")
	config.WriteString("   comment = Home Directories\n")
	config.WriteString(fmt.Sprintf("   path = %s/%%S\n", s.homeBasePath))
	config.WriteString("   browseable = no\n")
	config.WriteString("   read only = no\n")
	config.WriteString("   create mask = 0700\n")
	config.WriteString("   directory mask = 0700\n")
	config.WriteString("   valid users = %S\n\n")

	// Custom shares
	s.sharesMutex.RLock()
	for _, share := range s.shares {
		if share.Enabled {
			config.WriteString(s.generateShareConfig(share))
		}
	}
	s.sharesMutex.RUnlock()

	// Write configuration file
	if err := os.WriteFile(s.sambaConfPath, []byte(config.String()), 0644); err != nil {
		return fmt.Errorf("failed to write Samba configuration: %w", err)
	}

	s.logger.Info("Generated Samba configuration with %d shares", len(s.shares))

	return nil
}

// generateShareConfig creates configuration for a single share
func (s *Service) generateShareConfig(share *Share) string {
	var config strings.Builder

	config.WriteString(fmt.Sprintf("[%s]\n", share.Name))

	if share.Description != "" {
		config.WriteString(fmt.Sprintf("   comment = %s\n", share.Description))
	}

	config.WriteString(fmt.Sprintf("   path = %s\n", share.Path))

	if share.ReadOnly {
		config.WriteString("   read only = yes\n")
	} else {
		config.WriteString("   read only = no\n")
	}

	if share.Browseable {
		config.WriteString("   browseable = yes\n")
	} else {
		config.WriteString("   browseable = no\n")
	}

	if share.GuestOK {
		config.WriteString("   guest ok = yes\n")
	} else {
		config.WriteString("   guest ok = no\n")
	}

	if share.CreateMask != "" {
		config.WriteString(fmt.Sprintf("   create mask = %s\n", share.CreateMask))
	}

	if share.DirMask != "" {
		config.WriteString(fmt.Sprintf("   directory mask = %s\n", share.DirMask))
	}

	if share.ForceUser != "" {
		config.WriteString(fmt.Sprintf("   force user = %s\n", share.ForceUser))
	}

	if share.ForceGroup != "" {
		config.WriteString(fmt.Sprintf("   force group = %s\n", share.ForceGroup))
	}

	if len(share.ValidUsers) > 0 {
		config.WriteString(fmt.Sprintf("   valid users = %s\n", strings.Join(share.ValidUsers, " ")))
	}

	if len(share.WriteList) > 0 {
		config.WriteString(fmt.Sprintf("   write list = %s\n", strings.Join(share.WriteList, " ")))
	}

	if len(share.AdminUsers) > 0 {
		config.WriteString(fmt.Sprintf("   admin users = %s\n", strings.Join(share.AdminUsers, " ")))
	}

	if len(share.VetoFiles) > 0 {
		config.WriteString(fmt.Sprintf("   veto files = /%s/\n", strings.Join(share.VetoFiles, "/")))
	}

	config.WriteString("\n")

	return config.String()
}

// getWorkgroup derives workgroup name from domain
func (s *Service) getWorkgroup() string {
	// Use first part of domain as workgroup (e.g., example.com -> EXAMPLE)
	parts := strings.Split(s.config.Domain, ".")
	if len(parts) > 0 {
		workgroup := strings.ToUpper(parts[0])
		// Limit to 15 characters for NetBIOS compatibility
		if len(workgroup) > 15 {
			workgroup = workgroup[:15]
		}
		return workgroup
	}
	return "WORKGROUP"
}

// getNetBIOSName derives NetBIOS name from server address
func (s *Service) getNetBIOSName() string {
	// Use first part of server address (e.g., casdc.example.com -> CASDC)
	parts := strings.Split(s.config.ServerAddress, ".")
	if len(parts) > 0 {
		name := strings.ToUpper(parts[0])
		// Limit to 15 characters for NetBIOS compatibility
		if len(name) > 15 {
			name = name[:15]
		}
		return name
	}
	return "CASDC"
}

// CreateShare creates a new file share
func (s *Service) CreateShare(name, path, description string) error {
	// Create share directory if it doesn't exist
	fullPath := filepath.Join(s.shareBasePath, path)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return fmt.Errorf("failed to create share directory: %w", err)
	}

	// Insert into database
	query := `INSERT INTO file_shares (name, path, description, enabled, read_only, browseable, guest_ok, create_mask, directory_mask)
	          VALUES (?, ?, ?, 1, 0, 1, 0, '0755', '0755')`

	result, err := s.db.Exec(query, name, fullPath, description)
	if err != nil {
		return fmt.Errorf("failed to create share in database: %w", err)
	}

	shareID, _ := result.LastInsertId()

	// Add to memory
	share := &Share{
		ID:          shareID,
		Name:        name,
		Path:        fullPath,
		Description: description,
		Enabled:     true,
		ReadOnly:    false,
		Browseable:  true,
		GuestOK:     false,
		CreateMask:  "0755",
		DirMask:     "0755",
	}

	s.sharesMutex.Lock()
	s.shares[shareID] = share
	s.sharesMutex.Unlock()

	// Regenerate configuration
	return s.generateConfiguration()
}

// DeleteShare removes a file share
func (s *Service) DeleteShare(shareID int64) error {
	// Remove from database
	_, err := s.db.Exec("DELETE FROM file_shares WHERE id = ?", shareID)
	if err != nil {
		return fmt.Errorf("failed to delete share from database: %w", err)
	}

	// Remove from memory
	s.sharesMutex.Lock()
	delete(s.shares, shareID)
	s.sharesMutex.Unlock()

	// Regenerate configuration
	return s.generateConfiguration()
}

// ReloadConfiguration reloads shares from database and regenerates config
func (s *Service) ReloadConfiguration() error {
	if err := s.loadShares(); err != nil {
		return fmt.Errorf("failed to reload shares: %w", err)
	}

	if err := s.generateConfiguration(); err != nil {
		return fmt.Errorf("failed to regenerate configuration: %w", err)
	}

	s.logger.Info("Samba configuration reloaded successfully")
	return nil
}

// Shutdown gracefully stops the Samba service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Samba service")
	return nil
}
