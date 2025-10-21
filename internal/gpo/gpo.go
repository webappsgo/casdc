// Package gpo implements Group Policy Management
// Provides complete Group Policy Object (GPO) functionality including creation, editing,
// linking to OUs, security filtering, WMI filtering, and policy application
// as per CASDC Active Directory replacement specification
package gpo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Group Policy operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// GPO management
	policies      map[int64]*GroupPolicy
	policiesMutex sync.RWMutex
}

// GroupPolicy represents a Group Policy Object
type GroupPolicy struct {
	ID               int64
	Name             string
	DisplayName      string
	Description      string
	VersionDirectory int
	VersionSYSVOL    int
	Enabled          bool
	CreatedAt        time.Time
	ModifiedAt       time.Time
	Settings         *PolicySettings
	Links            []*GPOLink
	SecurityFilters  []*SecurityFilter
	WMIFilters       []*WMIFilter
}

// PolicySettings contains all policy configurations
type PolicySettings struct {
	ComputerConfig *ComputerConfiguration
	UserConfig     *UserConfiguration
}

// ComputerConfiguration contains computer-side policy settings
type ComputerConfiguration struct {
	// Software Installation
	SoftwarePackages []SoftwarePackage

	// Security Settings
	SecurityPolicies SecurityPolicies

	// Administrative Templates
	RegistryPolicies []RegistryPolicy

	// Scripts
	StartupScripts  []Script
	ShutdownScripts []Script

	// Folder Redirection (Computer-side)
	FolderRedirects []FolderRedirect
}

// UserConfiguration contains user-side policy settings
type UserConfiguration struct {
	// Software Installation
	SoftwarePackages []SoftwarePackage

	// Desktop Settings
	DesktopSettings DesktopSettings

	// Administrative Templates
	RegistryPolicies []RegistryPolicy

	// Scripts
	LogonScripts  []Script
	LogoffScripts []Script

	// Folder Redirection
	FolderRedirects []FolderRedirect

	// Software Restrictions
	SoftwareRestrictions []SoftwareRestriction
}

// SoftwarePackage represents a software installation package
type SoftwarePackage struct {
	Name            string
	PackagePath     string
	Version         string
	Publisher       string
	DeploymentType  string // assign, publish
	InstallOnLogon  bool
	UninstallOnRemove bool
}

// SecurityPolicies contains security settings
type SecurityPolicies struct {
	PasswordPolicy       PasswordPolicy
	AccountLockoutPolicy AccountLockoutPolicy
	AuditPolicy          AuditPolicy
	UserRightsAssignment []UserRightAssignment
	SecurityOptions      []SecurityOption
	EventLog             EventLogSettings
	RestrictedGroups     []RestrictedGroup
	SystemServices       []SystemService
	RegistrySecurity     []RegistrySecurityEntry
	FileSystemSecurity   []FileSystemSecurityEntry
}

// PasswordPolicy defines password requirements
type PasswordPolicy struct {
	MinPasswordLength        int
	MaxPasswordAge           int // days
	MinPasswordAge           int // days
	PasswordHistory          int
	ComplexityEnabled        bool
	ReversibleEncryption     bool
}

// AccountLockoutPolicy defines account lockout settings
type AccountLockoutPolicy struct {
	LockoutDuration      int // minutes
	LockoutThreshold     int // attempts
	ResetCounterAfter    int // minutes
}

// AuditPolicy defines audit settings
type AuditPolicy struct {
	AuditSystemEvents          bool
	AuditLogonEvents           bool
	AuditObjectAccess          bool
	AuditPrivilegeUse          bool
	AuditPolicyChange          bool
	AuditAccountManagement     bool
	AuditDirectoryServiceAccess bool
	AuditAccountLogon          bool
}

// UserRightAssignment assigns rights to users/groups
type UserRightAssignment struct {
	Right      string
	Principals []string
}

// SecurityOption represents a security option setting
type SecurityOption struct {
	Name  string
	Value string
}

// EventLogSettings configures event log behavior
type EventLogSettings struct {
	MaxSize           int64
	RetentionDays     int
	RestrictGuestAccess bool
}

// RestrictedGroup restricts group membership
type RestrictedGroup struct {
	GroupName string
	Members   []string
}

// SystemService configures system service settings
type SystemService struct {
	ServiceName string
	StartupMode string // automatic, manual, disabled
	Permissions []string
}

// RegistrySecurityEntry sets registry key security
type RegistrySecurityEntry struct {
	Path        string
	Permissions []ACEEntry
}

// FileSystemSecurityEntry sets file system security
type FileSystemSecurityEntry struct {
	Path        string
	Permissions []ACEEntry
}

// ACEEntry represents an Access Control Entry
type ACEEntry struct {
	Principal  string
	Permission string
	Type       string // allow, deny
}

// DesktopSettings contains desktop customization
type DesktopSettings struct {
	Wallpaper          string
	Theme              string
	ScreenSaver        string
	ScreenSaverTimeout int
	LockScreen         bool
	StartMenu          StartMenuSettings
	Taskbar            TaskbarSettings
}

// StartMenuSettings configures Start menu
type StartMenuSettings struct {
	RemoveRun              bool
	RemoveSearch           bool
	RemoveShutdown         bool
	RemoveRecentDocuments  bool
	CustomPinnedItems      []string
}

// TaskbarSettings configures taskbar
type TaskbarSettings struct {
	HideSystemTray    bool
	HideNotifications bool
	LockTaskbar       bool
	CustomPinnedItems []string
}

// RegistryPolicy represents a registry-based policy
type RegistryPolicy struct {
	KeyPath   string
	ValueName string
	ValueType string
	ValueData string
}

// Script represents a startup/shutdown/logon/logoff script
type Script struct {
	ScriptPath string
	Parameters string
	Order      int
}

// FolderRedirect redirects special folders
type FolderRedirect struct {
	FolderType      string // Desktop, Documents, Pictures, etc.
	TargetPath      string
	GrantExclusive  bool
	MoveContents    bool
}

// SoftwareRestriction restricts software execution
type SoftwareRestriction struct {
	Type  string // path, hash, certificate, zone
	Value string
	Level string // disallowed, unrestricted
}

// GPOLink represents a GPO linked to an OU
type GPOLink struct {
	GPOID     int64
	OUID      int64
	OUName    string
	Enabled   bool
	Enforced  bool
	LinkOrder int
	LinkedAt  time.Time
}

// SecurityFilter filters GPO application by security principal
type SecurityFilter struct {
	PrincipalType string // user, group
	PrincipalID   int64
	PrincipalName string
	Permission    string // read, apply
}

// WMIFilter filters GPO application by WMI query
type WMIFilter struct {
	ID          int64
	Name        string
	Description string
	Query       string
	Namespace   string
}

// NewService creates a new GPO service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:       db,
		config:   cfg,
		logger:   log,
		policies: make(map[int64]*GroupPolicy),
	}

	// Load GPOs from database
	if err := s.loadGroupPolicies(); err != nil {
		return nil, fmt.Errorf("failed to load group policies: %w", err)
	}

	return s, nil
}

// loadGroupPolicies loads all GPOs from database
func (s *Service) loadGroupPolicies() error {
	query := `
		SELECT id, name, display_name, description, version_directory,
		       version_sysvol, enabled, created_at, modified_at, settings_json
		FROM group_policies
		ORDER BY name
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query group policies: %w", err)
	}
	defer rows.Close()

	s.policiesMutex.Lock()
	defer s.policiesMutex.Unlock()

	for rows.Next() {
		gpo := &GroupPolicy{}
		var settingsJSON sql.NullString
		var displayName sql.NullString
		var description sql.NullString

		err := rows.Scan(
			&gpo.ID, &gpo.Name, &displayName, &description,
			&gpo.VersionDirectory, &gpo.VersionSYSVOL, &gpo.Enabled,
			&gpo.CreatedAt, &gpo.ModifiedAt, &settingsJSON,
		)
		if err != nil {
			s.logger.Error("Failed to scan GPO: %v", err)
			continue
		}

		gpo.DisplayName = displayName.String
		gpo.Description = description.String

		// Parse settings JSON
		if settingsJSON.Valid && settingsJSON.String != "" {
			var settings PolicySettings
			if err := json.Unmarshal([]byte(settingsJSON.String), &settings); err != nil {
				s.logger.Error("Failed to parse GPO settings: %v", err)
			} else {
				gpo.Settings = &settings
			}
		}

		// Load GPO links
		if err := s.loadGPOLinks(gpo); err != nil {
			s.logger.Error("Failed to load GPO links for %s: %v", gpo.Name, err)
		}

		s.policies[gpo.ID] = gpo
		s.logger.Debug("Loaded GPO: %s", gpo.Name)
	}

	return rows.Err()
}

// loadGPOLinks loads all OU links for a GPO
func (s *Service) loadGPOLinks(gpo *GroupPolicy) error {
	query := `
		SELECT gpo_id, ou_id, enabled, enforced, link_order, created_at
		FROM gpo_links
		WHERE gpo_id = ?
		ORDER BY link_order
	`

	rows, err := s.db.Query(query, gpo.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		link := &GPOLink{}
		err := rows.Scan(
			&link.GPOID, &link.OUID, &link.Enabled,
			&link.Enforced, &link.LinkOrder, &link.LinkedAt,
		)
		if err != nil {
			return err
		}

		gpo.Links = append(gpo.Links, link)
	}

	return rows.Err()
}

// CreateGPO creates a new Group Policy Object
func (s *Service) CreateGPO(name, displayName, description string) (*GroupPolicy, error) {
	s.logger.Info("Creating Group Policy: %s", name)

	// Validate name
	if name == "" {
		return nil, fmt.Errorf("GPO name cannot be empty")
	}

	// Check for duplicate
	if s.gpoExistsByName(name) {
		return nil, fmt.Errorf("GPO already exists: %s", name)
	}

	// Create default settings
	settings := &PolicySettings{
		ComputerConfig: &ComputerConfiguration{},
		UserConfig:     &UserConfiguration{},
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Insert into database
	query := `
		INSERT INTO group_policies (name, display_name, description,
		                           version_directory, version_sysvol, enabled,
		                           created_at, modified_at, settings_json)
		VALUES (?, ?, ?, 1, 1, TRUE, ?, ?, ?)
	`

	now := time.Now()
	result, err := s.db.Exec(query, name, displayName, description, now, now, string(settingsJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create GPO: %w", err)
	}

	gpoID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get GPO ID: %w", err)
	}

	gpo := &GroupPolicy{
		ID:               gpoID,
		Name:             name,
		DisplayName:      displayName,
		Description:      description,
		VersionDirectory: 1,
		VersionSYSVOL:    1,
		Enabled:          true,
		CreatedAt:        now,
		ModifiedAt:       now,
		Settings:         settings,
	}

	// Add to cache
	s.policiesMutex.Lock()
	s.policies[gpoID] = gpo
	s.policiesMutex.Unlock()

	s.logger.Info("Created GPO: %s (ID: %d)", name, gpoID)
	return gpo, nil
}

// UpdateGPO updates a Group Policy Object
func (s *Service) UpdateGPO(gpoID int64, settings *PolicySettings) error {
	s.logger.Info("Updating Group Policy ID: %d", gpoID)

	s.policiesMutex.RLock()
	gpo, exists := s.policies[gpoID]
	s.policiesMutex.RUnlock()

	if !exists {
		return fmt.Errorf("GPO not found: %d", gpoID)
	}

	// Increment version
	newVersionDir := gpo.VersionDirectory + 1
	newVersionSYSVOL := gpo.VersionSYSVOL + 1

	// Marshal settings
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	// Update database
	query := `
		UPDATE group_policies
		SET settings_json = ?, version_directory = ?, version_sysvol = ?,
		    modified_at = ?
		WHERE id = ?
	`

	now := time.Now()
	_, err = s.db.Exec(query, string(settingsJSON), newVersionDir, newVersionSYSVOL, now, gpoID)
	if err != nil {
		return fmt.Errorf("failed to update GPO: %w", err)
	}

	// Update cache
	s.policiesMutex.Lock()
	gpo.Settings = settings
	gpo.VersionDirectory = newVersionDir
	gpo.VersionSYSVOL = newVersionSYSVOL
	gpo.ModifiedAt = now
	s.policiesMutex.Unlock()

	s.logger.Info("Updated GPO: %s (Version: %d)", gpo.Name, newVersionDir)
	return nil
}

// LinkGPOToOU links a GPO to an organizational unit
func (s *Service) LinkGPOToOU(gpoID, ouID int64, enabled, enforced bool) error {
	s.logger.Info("Linking GPO %d to OU %d", gpoID, ouID)

	// Get next link order
	var maxOrder int
	query := "SELECT COALESCE(MAX(link_order), 0) FROM gpo_links WHERE ou_id = ?"
	s.db.QueryRow(query, ouID).Scan(&maxOrder)

	// Insert link
	query = `
		INSERT INTO gpo_links (gpo_id, ou_id, enabled, enforced, link_order, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	now := time.Now()
	_, err := s.db.Exec(query, gpoID, ouID, enabled, enforced, maxOrder+1, now)
	if err != nil {
		return fmt.Errorf("failed to link GPO: %w", err)
	}

	// Update cache
	link := &GPOLink{
		GPOID:     gpoID,
		OUID:      ouID,
		Enabled:   enabled,
		Enforced:  enforced,
		LinkOrder: maxOrder + 1,
		LinkedAt:  now,
	}

	s.policiesMutex.Lock()
	if gpo, exists := s.policies[gpoID]; exists {
		gpo.Links = append(gpo.Links, link)
	}
	s.policiesMutex.Unlock()

	s.logger.Info("Linked GPO %d to OU %d", gpoID, ouID)
	return nil
}

// UnlinkGPOFromOU removes a GPO link from an OU
func (s *Service) UnlinkGPOFromOU(gpoID, ouID int64) error {
	s.logger.Info("Unlinking GPO %d from OU %d", gpoID, ouID)

	query := "DELETE FROM gpo_links WHERE gpo_id = ? AND ou_id = ?"
	_, err := s.db.Exec(query, gpoID, ouID)
	if err != nil {
		return fmt.Errorf("failed to unlink GPO: %w", err)
	}

	// Update cache
	s.policiesMutex.Lock()
	if gpo, exists := s.policies[gpoID]; exists {
		for i, link := range gpo.Links {
			if link.OUID == ouID {
				gpo.Links = append(gpo.Links[:i], gpo.Links[i+1:]...)
				break
			}
		}
	}
	s.policiesMutex.Unlock()

	s.logger.Info("Unlinked GPO %d from OU %d", gpoID, ouID)
	return nil
}

// gpoExistsByName checks if a GPO with the given name exists
func (s *Service) gpoExistsByName(name string) bool {
	s.policiesMutex.RLock()
	defer s.policiesMutex.RUnlock()

	for _, gpo := range s.policies {
		if gpo.Name == name {
			return true
		}
	}
	return false
}

// GetAllGPOs returns all Group Policy Objects
func (s *Service) GetAllGPOs() []*GroupPolicy {
	s.policiesMutex.RLock()
	defer s.policiesMutex.RUnlock()

	gpos := make([]*GroupPolicy, 0, len(s.policies))
	for _, gpo := range s.policies {
		gpos = append(gpos, gpo)
	}
	return gpos
}

// GetGPOByID retrieves a GPO by ID
func (s *Service) GetGPOByID(gpoID int64) (*GroupPolicy, error) {
	s.policiesMutex.RLock()
	defer s.policiesMutex.RUnlock()

	gpo, exists := s.policies[gpoID]
	if !exists {
		return nil, fmt.Errorf("GPO not found: %d", gpoID)
	}

	return gpo, nil
}

// Shutdown gracefully stops the GPO service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Group Policy service")
	return nil
}
