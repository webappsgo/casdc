// Package publicfolders implements Exchange Enterprise public folder functionality
// providing shared mailboxes, calendars, contacts, and discussion forums with ACL permissions
package publicfolders

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles public folder operations for Exchange Enterprise
// providing shared collaboration spaces with hierarchical organization
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Public folder configuration
	basePath          string
	mailEnabled       bool
	defaultQuota      int64

	// Folder cache
	folders      map[int64]*PublicFolder
	foldersMutex sync.RWMutex
}

// PublicFolder represents a public folder with Exchange Enterprise features
type PublicFolder struct {
	ID                    int64
	Name                  string
	Path                  string
	Description           string
	FolderType            string // mail, calendar, contacts, tasks, notes
	Enabled               bool
	MailEnabled           bool
	EmailAddress          string
	ParentFolderID        *int64
	CreatedBy             int64
	CreatedAt             time.Time

	// Quota settings (in bytes)
	QuotaStorageWarning            int64
	QuotaStorageProhibitSend       int64
	QuotaStorageProhibitSendReceive int64

	// Permissions
	Permissions []*FolderPermission

	// Statistics
	ItemCount    int
	TotalSize    int64
	LastAccessed time.Time
}

// FolderPermission represents access permissions for a public folder
type FolderPermission struct {
	ID              int64
	FolderID        int64
	PrincipalType   string // user, group, anonymous, default
	PrincipalID     *int64 // nil for anonymous/default
	PrincipalName   string
	PermissionLevel string // none, readonly, readwrite, owner

	// Granular permissions
	CanCreateItems      bool
	CanReadItems        bool
	CanEditOwnItems     bool
	CanEditAllItems     bool
	CanDeleteOwnItems   bool
	CanDeleteAllItems   bool
	CanCreateSubfolders bool
	IsFolderOwner       bool
	IsFolderContact     bool

	CreatedAt time.Time
}

// FolderItem represents an item in a public folder
type FolderItem struct {
	ID           int64
	FolderID     int64
	ItemType     string // message, calendar_event, contact, task, note
	Subject      string
	Body         string
	Sender       string
	SenderUserID *int64
	Size         int64
	CreatedAt    time.Time
	ModifiedAt   time.Time
	IsRead       bool
	Attachments  []string
}

// NewService creates a new public folders service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// Public folder configuration per SPEC
		basePath:     filepath.Join(cfg.DataDir, "public-folders"),
		mailEnabled:  true,
		defaultQuota: 2 * 1024 * 1024 * 1024, // 2GB default per SPEC

		folders: make(map[int64]*PublicFolder),
	}

	// Load existing public folders from database
	if err := s.loadFolders(); err != nil {
		log.Warn("Failed to load public folders: %v", err)
	}

	// Create default public folder hierarchy if none exists
	if len(s.folders) == 0 {
		if err := s.createDefaultHierarchy(); err != nil {
			return nil, fmt.Errorf("failed to create default folder hierarchy: %w", err)
		}
	}

	log.Info("Public folders service initialized with %d folders", len(s.folders))

	return s, nil
}

// loadFolders loads all public folders from database
func (s *Service) loadFolders() error {
	query := `SELECT id, name, path, description, folder_type, enabled, mail_enabled, email_address,
		quota_storage_warning, quota_storage_prohibit_send, quota_storage_prohibit_send_receive,
		created_at, created_by, parent_folder_id
		FROM public_folders ORDER BY path`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query folders: %w", err)
	}
	defer rows.Close()

	s.foldersMutex.Lock()
	defer s.foldersMutex.Unlock()

	for rows.Next() {
		folder := &PublicFolder{}
		err := rows.Scan(
			&folder.ID,
			&folder.Name,
			&folder.Path,
			&folder.Description,
			&folder.FolderType,
			&folder.Enabled,
			&folder.MailEnabled,
			&folder.EmailAddress,
			&folder.QuotaStorageWarning,
			&folder.QuotaStorageProhibitSend,
			&folder.QuotaStorageProhibitSendReceive,
			&folder.CreatedAt,
			&folder.CreatedBy,
			&folder.ParentFolderID,
		)
		if err != nil {
			s.logger.Error("Failed to scan folder: %v", err)
			continue
		}

		// Load permissions for this folder
		if err := s.loadFolderPermissions(folder); err != nil {
			s.logger.Error("Failed to load permissions for folder %s: %v", folder.Name, err)
		}

		s.folders[folder.ID] = folder
		s.logger.Debug("Loaded public folder: %s (%s)", folder.Name, folder.Path)
	}

	return rows.Err()
}

// loadFolderPermissions loads permissions for a folder
func (s *Service) loadFolderPermissions(folder *PublicFolder) error {
	query := `SELECT id, principal_type, principal_id, permission_level,
		can_create_items, can_read_items, can_edit_own_items, can_edit_all_items,
		can_delete_own_items, can_delete_all_items, can_create_subfolders,
		is_folder_owner, is_folder_contact, created_at
		FROM public_folder_permissions WHERE folder_id = ?`

	rows, err := s.db.Query(query, folder.ID)
	if err != nil {
		return fmt.Errorf("failed to query permissions: %w", err)
	}
	defer rows.Close()

	folder.Permissions = make([]*FolderPermission, 0)

	for rows.Next() {
		perm := &FolderPermission{FolderID: folder.ID}
		err := rows.Scan(
			&perm.ID,
			&perm.PrincipalType,
			&perm.PrincipalID,
			&perm.PermissionLevel,
			&perm.CanCreateItems,
			&perm.CanReadItems,
			&perm.CanEditOwnItems,
			&perm.CanEditAllItems,
			&perm.CanDeleteOwnItems,
			&perm.CanDeleteAllItems,
			&perm.CanCreateSubfolders,
			&perm.IsFolderOwner,
			&perm.IsFolderContact,
			&perm.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan permission: %v", err)
			continue
		}

		// Get principal name
		if perm.PrincipalType == "user" && perm.PrincipalID != nil {
			var username string
			s.db.QueryRow("SELECT username FROM users WHERE id = ?", *perm.PrincipalID).Scan(&username)
			perm.PrincipalName = username
		} else if perm.PrincipalType == "group" && perm.PrincipalID != nil {
			var groupName string
			s.db.QueryRow("SELECT name FROM groups WHERE id = ?", *perm.PrincipalID).Scan(&groupName)
			perm.PrincipalName = groupName
		} else {
			perm.PrincipalName = perm.PrincipalType
		}

		folder.Permissions = append(folder.Permissions, perm)
	}

	return rows.Err()
}

// createDefaultHierarchy creates default public folder hierarchy per SPEC
func (s *Service) createDefaultHierarchy() error {
	s.logger.Info("Creating default public folder hierarchy")

	// System user ID (1) for creation
	systemUserID := int64(1)

	// Root folders
	folders := []struct {
		name        string
		path        string
		folderType  string
		description string
		mailEnabled bool
	}{
		{
			name:        "All Public Folders",
			path:        "/",
			folderType:  "mail",
			description: "Root public folder hierarchy",
			mailEnabled: false,
		},
		{
			name:        "Company Announcements",
			path:        "/Company Announcements",
			folderType:  "mail",
			description: "Company-wide announcements and news",
			mailEnabled: true,
		},
		{
			name:        "Shared Calendar",
			path:        "/Shared Calendar",
			folderType:  "calendar",
			description: "Organization-wide calendar for events",
			mailEnabled: false,
		},
		{
			name:        "Company Contacts",
			path:        "/Company Contacts",
			folderType:  "contacts",
			description: "Shared contact directory",
			mailEnabled: false,
		},
		{
			name:        "Team Documents",
			path:        "/Team Documents",
			folderType:  "mail",
			description: "Shared documents and files",
			mailEnabled: false,
		},
		{
			name:        "Discussion Forums",
			path:        "/Discussion Forums",
			folderType:  "mail",
			description: "Organization discussion boards",
			mailEnabled: false,
		},
	}

	for _, folder := range folders {
		if _, err := s.CreateFolder(
			folder.name,
			folder.path,
			folder.description,
			folder.folderType,
			nil,
			systemUserID,
		); err != nil {
			return fmt.Errorf("failed to create folder %s: %w", folder.name, err)
		}
	}

	return nil
}

// CreateFolder creates a new public folder
func (s *Service) CreateFolder(name, path, description, folderType string, parentID *int64, createdBy int64) (*PublicFolder, error) {
	// Generate email address if mail-enabled
	var emailAddress string
	if s.mailEnabled {
		emailAddress = fmt.Sprintf("%s@%s",
			strings.ToLower(strings.ReplaceAll(name, " ", "-")),
			s.config.Domain)
	}

	// Insert into database
	query := `INSERT INTO public_folders
		(name, path, description, folder_type, enabled, mail_enabled, email_address,
		quota_storage_warning, quota_storage_prohibit_send, quota_storage_prohibit_send_receive,
		created_at, created_by, parent_folder_id)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`

	result, err := s.db.Exec(query,
		name,
		path,
		description,
		folderType,
		s.mailEnabled,
		emailAddress,
		int64(1900)*1024*1024,  // 1.9GB warning
		int64(2000)*1024*1024,  // 2.0GB prohibit send
		int64(2300)*1024*1024,  // 2.3GB prohibit send/receive
		time.Now(),
		createdBy,
		parentID,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create folder: %w", err)
	}

	folderID, _ := result.LastInsertId()

	folder := &PublicFolder{
		ID:                            folderID,
		Name:                          name,
		Path:                          path,
		Description:                   description,
		FolderType:                    folderType,
		Enabled:                       true,
		MailEnabled:                   s.mailEnabled,
		EmailAddress:                  emailAddress,
		ParentFolderID:                parentID,
		CreatedBy:                     createdBy,
		CreatedAt:                     time.Now(),
		QuotaStorageWarning:           int64(1900) * 1024 * 1024,
		QuotaStorageProhibitSend:      int64(2000) * 1024 * 1024,
		QuotaStorageProhibitSendReceive: int64(2300) * 1024 * 1024,
		Permissions:                   make([]*FolderPermission, 0),
	}

	// Add default permissions
	if err := s.addDefaultPermissions(folder); err != nil {
		s.logger.Warn("Failed to add default permissions: %v", err)
	}

	// Store in cache
	s.foldersMutex.Lock()
	s.folders[folderID] = folder
	s.foldersMutex.Unlock()

	s.logger.Info("Created public folder: %s at %s", name, path)

	return folder, nil
}

// addDefaultPermissions adds default permissions for a new folder
func (s *Service) addDefaultPermissions(folder *PublicFolder) error {
	// Default permission: Everyone can read
	return s.AddPermission(folder.ID, "default", nil, "readonly")
}

// AddPermission adds a permission to a folder
func (s *Service) AddPermission(folderID int64, principalType string, principalID *int64, permissionLevel string) error {
	// Map permission level to specific permissions
	canCreate, canRead, canEditOwn, canEditAll := false, false, false, false
	canDeleteOwn, canDeleteAll, canCreateSub := false, false, false
	isOwner, isContact := false, false

	switch permissionLevel {
	case "readonly":
		canRead = true
	case "readwrite":
		canCreate, canRead, canEditOwn, canDeleteOwn = true, true, true, true
	case "owner":
		canCreate, canRead, canEditOwn, canEditAll = true, true, true, true
		canDeleteOwn, canDeleteAll, canCreateSub = true, true, true
		isOwner = true
	}

	query := `INSERT INTO public_folder_permissions
		(folder_id, principal_type, principal_id, permission_level,
		can_create_items, can_read_items, can_edit_own_items, can_edit_all_items,
		can_delete_own_items, can_delete_all_items, can_create_subfolders,
		is_folder_owner, is_folder_contact, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		folderID,
		principalType,
		principalID,
		permissionLevel,
		canCreate,
		canRead,
		canEditOwn,
		canEditAll,
		canDeleteOwn,
		canDeleteAll,
		canCreateSub,
		isOwner,
		isContact,
		time.Now(),
	)

	if err != nil {
		return fmt.Errorf("failed to add permission: %w", err)
	}

	// Reload folder permissions
	s.foldersMutex.Lock()
	if folder, exists := s.folders[folderID]; exists {
		s.loadFolderPermissions(folder)
	}
	s.foldersMutex.Unlock()

	return nil
}

// GetFolder retrieves a folder by ID
func (s *Service) GetFolder(folderID int64) (*PublicFolder, error) {
	s.foldersMutex.RLock()
	folder, exists := s.folders[folderID]
	s.foldersMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("folder not found: %d", folderID)
	}

	return folder, nil
}

// GetFolderByPath retrieves a folder by path
func (s *Service) GetFolderByPath(path string) (*PublicFolder, error) {
	s.foldersMutex.RLock()
	defer s.foldersMutex.RUnlock()

	for _, folder := range s.folders {
		if folder.Path == path {
			return folder, nil
		}
	}

	return nil, fmt.Errorf("folder not found: %s", path)
}

// ListFolders returns all public folders
func (s *Service) ListFolders() []*PublicFolder {
	s.foldersMutex.RLock()
	defer s.foldersMutex.RUnlock()

	folders := make([]*PublicFolder, 0, len(s.folders))
	for _, folder := range s.folders {
		folders = append(folders, folder)
	}

	return folders
}

// GetChildFolders returns child folders of a parent
func (s *Service) GetChildFolders(parentID *int64) []*PublicFolder {
	s.foldersMutex.RLock()
	defer s.foldersMutex.RUnlock()

	children := make([]*PublicFolder, 0)
	for _, folder := range s.folders {
		// Check if parent matches
		if parentID == nil && folder.ParentFolderID == nil {
			children = append(children, folder)
		} else if parentID != nil && folder.ParentFolderID != nil && *folder.ParentFolderID == *parentID {
			children = append(children, folder)
		}
	}

	return children
}

// CheckPermission checks if a user has specific permission on a folder
func (s *Service) CheckPermission(folderID, userID int64, requiredPermission string) (bool, error) {
	folder, err := s.GetFolder(folderID)
	if err != nil {
		return false, err
	}

	// Check user-specific permissions
	for _, perm := range folder.Permissions {
		if perm.PrincipalType == "user" && perm.PrincipalID != nil && *perm.PrincipalID == userID {
			return s.hasPermission(perm, requiredPermission), nil
		}
	}

	// Check group permissions
	// Get user's groups
	query := `SELECT group_id FROM user_groups WHERE user_id = ?`
	rows, err := s.db.Query(query, userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var groupID int64
			if err := rows.Scan(&groupID); err == nil {
				for _, perm := range folder.Permissions {
					if perm.PrincipalType == "group" && perm.PrincipalID != nil && *perm.PrincipalID == groupID {
						return s.hasPermission(perm, requiredPermission), nil
					}
				}
			}
		}
	}

	// Check default permissions
	for _, perm := range folder.Permissions {
		if perm.PrincipalType == "default" {
			return s.hasPermission(perm, requiredPermission), nil
		}
	}

	return false, nil
}

// hasPermission checks if a permission entry grants the required permission
func (s *Service) hasPermission(perm *FolderPermission, required string) bool {
	switch required {
	case "read":
		return perm.CanReadItems
	case "create":
		return perm.CanCreateItems
	case "edit":
		return perm.CanEditOwnItems || perm.CanEditAllItems
	case "delete":
		return perm.CanDeleteOwnItems || perm.CanDeleteAllItems
	case "owner":
		return perm.IsFolderOwner
	default:
		return false
	}
}

// DeleteFolder deletes a public folder
func (s *Service) DeleteFolder(folderID int64) error {
	// Check for child folders
	children := s.GetChildFolders(&folderID)
	if len(children) > 0 {
		return fmt.Errorf("cannot delete folder with %d child folders", len(children))
	}

	// Delete permissions
	s.db.Exec("DELETE FROM public_folder_permissions WHERE folder_id = ?", folderID)

	// Delete folder
	_, err := s.db.Exec("DELETE FROM public_folders WHERE id = ?", folderID)
	if err != nil {
		return fmt.Errorf("failed to delete folder: %w", err)
	}

	// Remove from cache
	s.foldersMutex.Lock()
	delete(s.folders, folderID)
	s.foldersMutex.Unlock()

	s.logger.Info("Deleted public folder %d", folderID)

	return nil
}

// GetFolderStatistics returns statistics for a folder
func (s *Service) GetFolderStatistics(folderID int64) (map[string]interface{}, error) {
	var itemCount int
	var totalSize int64

	// Count items
	s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(size), 0) FROM public_folder_items WHERE folder_id = ?", folderID).Scan(&itemCount, &totalSize)

	stats := map[string]interface{}{
		"item_count":   itemCount,
		"total_size":   totalSize,
		"quota_used":   float64(totalSize) / float64(s.defaultQuota) * 100,
		"last_updated": time.Now(),
	}

	return stats, nil
}

// Shutdown gracefully stops the public folders service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down public folders service")
	return nil
}
