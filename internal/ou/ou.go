// Package ou implements Organizational Units management
// Provides hierarchical OU structure with unlimited nesting, delegation of administrative rights,
// and OU-specific Group Policy application as per CASDC Active Directory replacement specification
package ou

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Organizational Units operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// OU management
	organizationalUnits map[int64]*OrganizationalUnit
	ouMutex             sync.RWMutex
}

// OrganizationalUnit represents an Active Directory organizational unit
type OrganizationalUnit struct {
	ID                     int64
	Name                   string
	Description            string
	DistinguishedName      string
	ParentID               *int64
	CreatedAt              time.Time
	GPOInheritanceBlocked  bool
	ProtectFromDeletion    bool

	// Runtime hierarchy
	Parent                 *OrganizationalUnit
	Children               []*OrganizationalUnit
	Users                  []*OUUser
	Computers              []*OUComputer
	Groups                 []*OUGroup
	LinkedGPOs             []*LinkedGPO
	DelegatedAdmins        []*DelegatedAdmin
}

// OUUser represents a user in an OU
type OUUser struct {
	UserID    int64
	Username  string
	FullName  string
	Email     string
	Enabled   bool
}

// OUComputer represents a computer in an OU
type OUComputer struct {
	ComputerID int64
	Name       string
	DNSHostname string
	IPAddress  string
	Enabled    bool
}

// OUGroup represents a group in an OU
type OUGroup struct {
	GroupID int64
	Name    string
	Type    string // security, distribution
	Scope   string // domain_local, global, universal
}

// LinkedGPO represents a Group Policy linked to an OU
type LinkedGPO struct {
	GPOID      int64
	GPOName    string
	Enabled    bool
	Enforced   bool
	LinkOrder  int
	LinkedAt   time.Time
}

// DelegatedAdmin represents delegated administrative rights for an OU
type DelegatedAdmin struct {
	PrincipalType string // user, group
	PrincipalID   int64
	Permissions   []string // create_users, delete_users, modify_users, reset_passwords, etc.
	GrantedAt     time.Time
	GrantedBy     int64
}

// NewService creates a new OU service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:                  db,
		config:              cfg,
		logger:              log,
		organizationalUnits: make(map[int64]*OrganizationalUnit),
	}

	// Load OUs from database
	if err := s.loadOrganizationalUnits(); err != nil {
		return nil, fmt.Errorf("failed to load organizational units: %w", err)
	}

	// Build OU hierarchy
	s.buildOUHierarchy()

	return s, nil
}

// loadOrganizationalUnits loads all OUs from database
func (s *Service) loadOrganizationalUnits() error {
	query := `
		SELECT id, name, description, distinguished_name, parent_id,
		       created_at, gpo_inheritance_blocked, protect_from_deletion
		FROM organizational_units
		ORDER BY distinguished_name
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query organizational units: %w", err)
	}
	defer rows.Close()

	s.ouMutex.Lock()
	defer s.ouMutex.Unlock()

	for rows.Next() {
		ou := &OrganizationalUnit{}
		var parentID sql.NullInt64

		err := rows.Scan(
			&ou.ID, &ou.Name, &ou.Description, &ou.DistinguishedName,
			&parentID, &ou.CreatedAt, &ou.GPOInheritanceBlocked,
			&ou.ProtectFromDeletion,
		)
		if err != nil {
			s.logger.Error("Failed to scan OU: %v", err)
			continue
		}

		if parentID.Valid {
			ou.ParentID = &parentID.Int64
		}

		s.organizationalUnits[ou.ID] = ou
		s.logger.Debug("Loaded OU: %s", ou.DistinguishedName)
	}

	return rows.Err()
}

// buildOUHierarchy builds parent-child relationships between OUs
func (s *Service) buildOUHierarchy() {
	s.ouMutex.Lock()
	defer s.ouMutex.Unlock()

	// First pass: link parents and children
	for _, ou := range s.organizationalUnits {
		if ou.ParentID != nil {
			if parent, exists := s.organizationalUnits[*ou.ParentID]; exists {
				ou.Parent = parent
				parent.Children = append(parent.Children, ou)
			}
		}
	}
}

// CreateOU creates a new organizational unit
func (s *Service) CreateOU(name, description string, parentID *int64) (*OrganizationalUnit, error) {
	s.logger.Info("Creating organizational unit: %s", name)

	// Validate name
	if !isValidOUName(name) {
		return nil, fmt.Errorf("invalid OU name: %s", name)
	}

	// Build distinguished name
	dn := s.buildDistinguishedName(name, parentID)

	// Check for duplicate DN
	if s.ouExistsByDN(dn) {
		return nil, fmt.Errorf("organizational unit already exists: %s", dn)
	}

	// Insert into database
	query := `
		INSERT INTO organizational_units (name, description, distinguished_name,
		                                  parent_id, created_at, gpo_inheritance_blocked,
		                                  protect_from_deletion)
		VALUES (?, ?, ?, ?, ?, FALSE, FALSE)
	`

	now := time.Now()
	result, err := s.db.Exec(query, name, description, dn, parentID, now)
	if err != nil {
		return nil, fmt.Errorf("failed to create OU: %w", err)
	}

	ouID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get OU ID: %w", err)
	}

	ou := &OrganizationalUnit{
		ID:                    ouID,
		Name:                  name,
		Description:           description,
		DistinguishedName:     dn,
		ParentID:              parentID,
		CreatedAt:             now,
		GPOInheritanceBlocked: false,
		ProtectFromDeletion:   false,
	}

	// Add to cache
	s.ouMutex.Lock()
	s.organizationalUnits[ouID] = ou

	// Link to parent
	if parentID != nil {
		if parent, exists := s.organizationalUnits[*parentID]; exists {
			ou.Parent = parent
			parent.Children = append(parent.Children, ou)
		}
	}
	s.ouMutex.Unlock()

	s.logger.Info("Created OU: %s (ID: %d)", dn, ouID)
	return ou, nil
}

// UpdateOU updates an organizational unit
func (s *Service) UpdateOU(ouID int64, name, description string) error {
	s.logger.Info("Updating organizational unit ID: %d", ouID)

	s.ouMutex.RLock()
	ou, exists := s.organizationalUnits[ouID]
	s.ouMutex.RUnlock()

	if !exists {
		return fmt.Errorf("organizational unit not found: %d", ouID)
	}

	// Build new DN if name changed
	newDN := ou.DistinguishedName
	if name != ou.Name {
		newDN = s.buildDistinguishedName(name, ou.ParentID)
	}

	// Update database
	query := `
		UPDATE organizational_units
		SET name = ?, description = ?, distinguished_name = ?
		WHERE id = ?
	`

	_, err := s.db.Exec(query, name, description, newDN, ouID)
	if err != nil {
		return fmt.Errorf("failed to update OU: %w", err)
	}

	// Update cache
	s.ouMutex.Lock()
	ou.Name = name
	ou.Description = description
	ou.DistinguishedName = newDN
	s.ouMutex.Unlock()

	s.logger.Info("Updated OU: %s", newDN)
	return nil
}

// DeleteOU deletes an organizational unit
func (s *Service) DeleteOU(ouID int64) error {
	s.logger.Info("Deleting organizational unit ID: %d", ouID)

	s.ouMutex.RLock()
	ou, exists := s.organizationalUnits[ouID]
	s.ouMutex.RUnlock()

	if !exists {
		return fmt.Errorf("organizational unit not found: %d", ouID)
	}

	// Check protection
	if ou.ProtectFromDeletion {
		return fmt.Errorf("OU is protected from deletion: %s", ou.DistinguishedName)
	}

	// Check for children
	if len(ou.Children) > 0 {
		return fmt.Errorf("OU has child OUs and cannot be deleted: %s", ou.DistinguishedName)
	}

	// Check for users/computers
	userCount, err := s.countOUUsers(ouID)
	if err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}
	if userCount > 0 {
		return fmt.Errorf("OU contains %d users and cannot be deleted", userCount)
	}

	// Delete from database
	query := "DELETE FROM organizational_units WHERE id = ?"
	_, err = s.db.Exec(query, ouID)
	if err != nil {
		return fmt.Errorf("failed to delete OU: %w", err)
	}

	// Remove from cache
	s.ouMutex.Lock()
	if ou.Parent != nil {
		// Remove from parent's children
		for i, child := range ou.Parent.Children {
			if child.ID == ouID {
				ou.Parent.Children = append(ou.Parent.Children[:i], ou.Parent.Children[i+1:]...)
				break
			}
		}
	}
	delete(s.organizationalUnits, ouID)
	s.ouMutex.Unlock()

	s.logger.Info("Deleted OU: %s", ou.DistinguishedName)
	return nil
}

// MoveOU moves an OU to a different parent
func (s *Service) MoveOU(ouID int64, newParentID *int64) error {
	s.logger.Info("Moving organizational unit ID %d to parent %v", ouID, newParentID)

	s.ouMutex.RLock()
	ou, exists := s.organizationalUnits[ouID]
	s.ouMutex.RUnlock()

	if !exists {
		return fmt.Errorf("organizational unit not found: %d", ouID)
	}

	// Prevent moving to self or descendant
	if newParentID != nil {
		if *newParentID == ouID {
			return fmt.Errorf("cannot move OU to itself")
		}
		if s.isDescendant(ouID, *newParentID) {
			return fmt.Errorf("cannot move OU to its own descendant")
		}
	}

	// Build new DN
	newDN := s.buildDistinguishedName(ou.Name, newParentID)

	// Update database
	query := `
		UPDATE organizational_units
		SET parent_id = ?, distinguished_name = ?
		WHERE id = ?
	`

	_, err := s.db.Exec(query, newParentID, newDN, ouID)
	if err != nil {
		return fmt.Errorf("failed to move OU: %w", err)
	}

	// Update cache and hierarchy
	s.ouMutex.Lock()

	// Remove from old parent
	if ou.Parent != nil {
		for i, child := range ou.Parent.Children {
			if child.ID == ouID {
				ou.Parent.Children = append(ou.Parent.Children[:i], ou.Parent.Children[i+1:]...)
				break
			}
		}
	}

	// Set new parent
	ou.ParentID = newParentID
	ou.DistinguishedName = newDN

	if newParentID != nil {
		if newParent, exists := s.organizationalUnits[*newParentID]; exists {
			ou.Parent = newParent
			newParent.Children = append(newParent.Children, ou)
		}
	} else {
		ou.Parent = nil
	}

	s.ouMutex.Unlock()

	s.logger.Info("Moved OU to: %s", newDN)
	return nil
}

// SetProtection enables or disables deletion protection
func (s *Service) SetProtection(ouID int64, protect bool) error {
	query := "UPDATE organizational_units SET protect_from_deletion = ? WHERE id = ?"
	_, err := s.db.Exec(query, protect, ouID)
	if err != nil {
		return fmt.Errorf("failed to update protection: %w", err)
	}

	s.ouMutex.Lock()
	if ou, exists := s.organizationalUnits[ouID]; exists {
		ou.ProtectFromDeletion = protect
	}
	s.ouMutex.Unlock()

	return nil
}

// SetGPOInheritance sets GPO inheritance blocking
func (s *Service) SetGPOInheritance(ouID int64, block bool) error {
	query := "UPDATE organizational_units SET gpo_inheritance_blocked = ? WHERE id = ?"
	_, err := s.db.Exec(query, block, ouID)
	if err != nil {
		return fmt.Errorf("failed to update GPO inheritance: %w", err)
	}

	s.ouMutex.Lock()
	if ou, exists := s.organizationalUnits[ouID]; exists {
		ou.GPOInheritanceBlocked = block
	}
	s.ouMutex.Unlock()

	return nil
}

// buildDistinguishedName builds LDAP distinguished name for OU
func (s *Service) buildDistinguishedName(name string, parentID *int64) string {
	parts := []string{"OU=" + name}

	if parentID != nil {
		s.ouMutex.RLock()
		if parent, exists := s.organizationalUnits[*parentID]; exists {
			// Parent DN already includes domain components
			parts = append(parts, parent.DistinguishedName)
		}
		s.ouMutex.RUnlock()
	} else {
		// Root OU - add domain components
		domainParts := strings.Split(s.config.Domain, ".")
		for _, part := range domainParts {
			parts = append(parts, "DC="+part)
		}
	}

	return strings.Join(parts, ",")
}

// isDescendant checks if potentialDescendant is a descendant of ancestor
func (s *Service) isDescendant(ancestorID, potentialDescendantID int64) bool {
	s.ouMutex.RLock()
	defer s.ouMutex.RUnlock()

	ou, exists := s.organizationalUnits[potentialDescendantID]
	if !exists {
		return false
	}

	for ou.ParentID != nil {
		if *ou.ParentID == ancestorID {
			return true
		}
		ou, exists = s.organizationalUnits[*ou.ParentID]
		if !exists {
			break
		}
	}

	return false
}

// ouExistsByDN checks if an OU with the given DN exists
func (s *Service) ouExistsByDN(dn string) bool {
	s.ouMutex.RLock()
	defer s.ouMutex.RUnlock()

	for _, ou := range s.organizationalUnits {
		if ou.DistinguishedName == dn {
			return true
		}
	}
	return false
}

// countOUUsers counts users in an OU
func (s *Service) countOUUsers(ouID int64) (int, error) {
	var count int
	query := "SELECT COUNT(*) FROM users WHERE ou_id = ?"
	err := s.db.QueryRow(query, ouID).Scan(&count)
	return count, err
}

// isValidOUName validates OU name
func isValidOUName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}

	// OU name cannot contain special LDAP characters
	invalidChars := []string{",", "=", "+", "<", ">", "#", ";", "\\", "\""}
	for _, char := range invalidChars {
		if strings.Contains(name, char) {
			return false
		}
	}

	return true
}

// GetAllOUs returns all organizational units
func (s *Service) GetAllOUs() []*OrganizationalUnit {
	s.ouMutex.RLock()
	defer s.ouMutex.RUnlock()

	ous := make([]*OrganizationalUnit, 0, len(s.organizationalUnits))
	for _, ou := range s.organizationalUnits {
		ous = append(ous, ou)
	}
	return ous
}

// GetOUByID retrieves an OU by ID
func (s *Service) GetOUByID(ouID int64) (*OrganizationalUnit, error) {
	s.ouMutex.RLock()
	defer s.ouMutex.RUnlock()

	ou, exists := s.organizationalUnits[ouID]
	if !exists {
		return nil, fmt.Errorf("organizational unit not found: %d", ouID)
	}

	return ou, nil
}

// GetRootOUs returns all root-level OUs (those without a parent)
func (s *Service) GetRootOUs() []*OrganizationalUnit {
	s.ouMutex.RLock()
	defer s.ouMutex.RUnlock()

	var roots []*OrganizationalUnit
	for _, ou := range s.organizationalUnits {
		if ou.ParentID == nil {
			roots = append(roots, ou)
		}
	}
	return roots
}

// Shutdown gracefully stops the OU service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Organizational Units service")
	return nil
}
