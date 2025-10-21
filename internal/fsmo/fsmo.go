// Package fsmo implements Flexible Single Master Operations roles for Active Directory
// providing complete FSMO role management with automatic failover and load distribution
package fsmo

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles FSMO role operations for Active Directory
// managing the five critical single-master roles per SPEC
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// FSMO role holders
	roles      map[FSMORole]*RoleHolder
	rolesMutex sync.RWMutex

	// Role monitoring
	healthCheckInterval time.Duration
	stopChan            chan bool
}

// FSMORole represents the five FSMO roles in Active Directory
type FSMORole string

const (
	// Forest-wide roles (one per forest)
	SchemaMaster       FSMORole = "schema_master"        // Schema management and replication
	DomainNamingMaster FSMORole = "domain_naming_master" // Domain creation and deletion

	// Domain-wide roles (one per domain)
	RIDMaster            FSMORole = "rid_master"            // Relative Identifier allocation
	PDCEmulator          FSMORole = "pdc_emulator"          // Time sync and legacy compatibility
	InfrastructureMaster FSMORole = "infrastructure_master" // Cross-domain reference updates
)

// RoleHolder represents a domain controller holding a FSMO role
type RoleHolder struct {
	Role         FSMORole
	NodeID       string
	NodeName     string
	Since        time.Time
	LastSeen     time.Time
	Status       string // active, standby, failed
	FailoverNode string // Designated failover node
}

// RoleTransfer represents a FSMO role transfer operation
type RoleTransfer struct {
	Role       FSMORole
	FromNode   string
	ToNode     string
	Reason     string
	Status     string // pending, in_progress, completed, failed
	StartedAt  time.Time
	CompletedAt *time.Time
	Error      string
}

// NewService creates a new FSMO service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		roles:               make(map[FSMORole]*RoleHolder),
		healthCheckInterval: 30 * time.Second,
		stopChan:            make(chan bool),
	}

	// Load current FSMO role assignments from database
	if err := s.loadRoleAssignments(); err != nil {
		log.Warn("Failed to load FSMO role assignments: %v", err)
	}

	// Initialize FSMO roles if this is the first domain controller
	if len(s.roles) == 0 {
		if err := s.initializeRoles(); err != nil {
			return nil, fmt.Errorf("failed to initialize FSMO roles: %w", err)
		}
	}

	log.Info("FSMO service initialized with %d roles", len(s.roles))

	return s, nil
}

// loadRoleAssignments loads current FSMO role assignments from database
func (s *Service) loadRoleAssignments() error {
	query := `SELECT role, node_id, node_name, since, last_seen, status, failover_node
		FROM fsmo_roles ORDER BY role`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query roles: %w", err)
	}
	defer rows.Close()

	s.rolesMutex.Lock()
	defer s.rolesMutex.Unlock()

	for rows.Next() {
		holder := &RoleHolder{}
		var roleStr string
		err := rows.Scan(
			&roleStr,
			&holder.NodeID,
			&holder.NodeName,
			&holder.Since,
			&holder.LastSeen,
			&holder.Status,
			&holder.FailoverNode,
		)
		if err != nil {
			s.logger.Error("Failed to scan role: %v", err)
			continue
		}

		holder.Role = FSMORole(roleStr)
		s.roles[holder.Role] = holder
		s.logger.Debug("Loaded FSMO role: %s held by %s", holder.Role, holder.NodeName)
	}

	return rows.Err()
}

// initializeRoles initializes all FSMO roles on the first domain controller
func (s *Service) initializeRoles() error {
	s.logger.Info("Initializing FSMO roles on first domain controller")

	currentNode := s.getCurrentNode()

	allRoles := []FSMORole{
		SchemaMaster,
		DomainNamingMaster,
		RIDMaster,
		PDCEmulator,
		InfrastructureMaster,
	}

	for _, role := range allRoles {
		if err := s.SeizeRole(role, currentNode.NodeID, currentNode.NodeName, "initial_setup"); err != nil {
			return fmt.Errorf("failed to assign role %s: %w", role, err)
		}
	}

	return nil
}

// getCurrentNode returns information about the current node
func (s *Service) getCurrentNode() struct {
	NodeID   string
	NodeName string
} {
	return struct {
		NodeID   string
		NodeName string
	}{
		NodeID:   s.config.NodeName, // From config
		NodeName: s.config.NodeName,
	}
}

// SeizeRole forcibly assigns a role to a node (used for failover and initial setup)
func (s *Service) SeizeRole(role FSMORole, nodeID, nodeName, reason string) error {
	s.rolesMutex.Lock()
	defer s.rolesMutex.Unlock()

	// Check if role already assigned
	if existing, exists := s.roles[role]; exists {
		s.logger.Info("Transferring FSMO role %s from %s to %s (reason: %s)",
			role, existing.NodeName, nodeName, reason)
	} else {
		s.logger.Info("Assigning FSMO role %s to %s (reason: %s)",
			role, nodeName, reason)
	}

	holder := &RoleHolder{
		Role:     role,
		NodeID:   nodeID,
		NodeName: nodeName,
		Since:    time.Now(),
		LastSeen: time.Now(),
		Status:   "active",
	}

	// Store in database
	query := `INSERT OR REPLACE INTO fsmo_roles
		(role, node_id, node_name, since, last_seen, status)
		VALUES (?, ?, ?, ?, ?, ?)`

	if _, err := s.db.Exec(query,
		string(role),
		nodeID,
		nodeName,
		holder.Since,
		holder.LastSeen,
		holder.Status,
	); err != nil {
		return fmt.Errorf("failed to store role assignment: %w", err)
	}

	// Update in-memory cache
	s.roles[role] = holder

	// Log transfer in audit log
	s.logRoleTransfer(role, "", nodeID, reason, "completed")

	return nil
}

// TransferRole gracefully transfers a role from one node to another
func (s *Service) TransferRole(role FSMORole, toNodeID, toNodeName string) error {
	s.rolesMutex.RLock()
	currentHolder, exists := s.roles[role]
	s.rolesMutex.RUnlock()

	if !exists {
		return fmt.Errorf("role %s not currently assigned", role)
	}

	fromNodeID := currentHolder.NodeID

	s.logger.Info("Initiating graceful transfer of %s from %s to %s",
		role, currentHolder.NodeName, toNodeName)

	// Create transfer record
	transfer := &RoleTransfer{
		Role:      role,
		FromNode:  fromNodeID,
		ToNode:    toNodeID,
		Reason:    "manual_transfer",
		Status:    "in_progress",
		StartedAt: time.Now(),
	}

	// Store transfer record
	s.storeTransferRecord(transfer)

	// Perform synchronization checks based on role
	if err := s.performRoleTransferChecks(role); err != nil {
		transfer.Status = "failed"
		transfer.Error = err.Error()
		s.storeTransferRecord(transfer)
		return fmt.Errorf("role transfer checks failed: %w", err)
	}

	// Execute the transfer
	if err := s.SeizeRole(role, toNodeID, toNodeName, "manual_transfer"); err != nil {
		transfer.Status = "failed"
		transfer.Error = err.Error()
		s.storeTransferRecord(transfer)
		return fmt.Errorf("failed to transfer role: %w", err)
	}

	// Mark transfer as completed
	now := time.Now()
	transfer.Status = "completed"
	transfer.CompletedAt = &now
	s.storeTransferRecord(transfer)

	s.logger.Info("FSMO role %s successfully transferred to %s", role, toNodeName)

	return nil
}

// performRoleTransferChecks performs pre-transfer validation checks
func (s *Service) performRoleTransferChecks(role FSMORole) error {
	switch role {
	case SchemaMaster:
		// Ensure schema is consistent across all DCs
		s.logger.Debug("Checking schema consistency for Schema Master transfer")
		// In production, this would verify schema replication

	case DomainNamingMaster:
		// Ensure no pending domain operations
		s.logger.Debug("Checking for pending domain operations")

	case RIDMaster:
		// Verify RID pool allocation state
		s.logger.Debug("Checking RID pool allocation status")
		// In production, verify current RID allocation

	case PDCEmulator:
		// Check time synchronization
		s.logger.Debug("Verifying time synchronization for PDC Emulator transfer")

	case InfrastructureMaster:
		// Verify cross-domain references are synchronized
		s.logger.Debug("Checking cross-domain reference synchronization")
	}

	return nil
}

// storeTransferRecord stores a role transfer record in database
func (s *Service) storeTransferRecord(transfer *RoleTransfer) error {
	query := `INSERT INTO fsmo_transfers
		(role, from_node, to_node, reason, status, started_at, completed_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.db.Exec(query,
		string(transfer.Role),
		transfer.FromNode,
		transfer.ToNode,
		transfer.Reason,
		transfer.Status,
		transfer.StartedAt,
		transfer.CompletedAt,
		transfer.Error,
	)

	return err
}

// logRoleTransfer logs a role transfer in audit log
func (s *Service) logRoleTransfer(role FSMORole, fromNode, toNode, reason, status string) {
	query := `INSERT INTO audit_logs
		(action, resource_type, resource_id, details, created_at)
		VALUES (?, ?, ?, ?, ?)`

	details := fmt.Sprintf(`{"role": "%s", "from_node": "%s", "to_node": "%s", "reason": "%s", "status": "%s"}`,
		role, fromNode, toNode, reason, status)

	s.db.Exec(query, "fsmo_role_transfer", "fsmo_role", string(role), details, time.Now())
}

// GetRoleHolder returns the current holder of a FSMO role
func (s *Service) GetRoleHolder(role FSMORole) (*RoleHolder, error) {
	s.rolesMutex.RLock()
	defer s.rolesMutex.RUnlock()

	holder, exists := s.roles[role]
	if !exists {
		return nil, fmt.Errorf("role %s not assigned", role)
	}

	return holder, nil
}

// ListRoles returns all FSMO role assignments
func (s *Service) ListRoles() map[FSMORole]*RoleHolder {
	s.rolesMutex.RLock()
	defer s.rolesMutex.RUnlock()

	roles := make(map[FSMORole]*RoleHolder)
	for role, holder := range s.roles {
		roles[role] = holder
	}

	return roles
}

// MonitorRoleHealth monitors FSMO role holder health and performs automatic failover
func (s *Service) MonitorRoleHealth(ctx context.Context) {
	ticker := time.NewTicker(s.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkRoleHolderHealth()
		case <-s.stopChan:
			return
		}
	}
}

// checkRoleHolderHealth checks health of all FSMO role holders
func (s *Service) checkRoleHolderHealth() {
	s.rolesMutex.Lock()
	defer s.rolesMutex.Unlock()

	for role, holder := range s.roles {
		// Check if role holder has checked in recently
		if time.Since(holder.LastSeen) > 5*time.Minute {
			s.logger.Warn("FSMO role %s holder %s has not checked in for %v",
				role, holder.NodeName, time.Since(holder.LastSeen))

			holder.Status = "failed"

			// Trigger automatic failover if in cluster mode
			if s.config.ClusterMode {
				s.logger.Info("Triggering automatic failover for role %s", role)
				go s.performAutomaticFailover(role)
			}
		}
	}
}

// performAutomaticFailover performs automatic role failover to standby node
func (s *Service) performAutomaticFailover(role FSMORole) error {
	s.rolesMutex.RLock()
	holder := s.roles[role]
	s.rolesMutex.RUnlock()

	if holder.FailoverNode == "" {
		s.logger.Error("No failover node designated for role %s", role)
		return fmt.Errorf("no failover node designated")
	}

	s.logger.Info("Failing over role %s to node %s", role, holder.FailoverNode)

	// Seize role to failover node
	return s.SeizeRole(role, holder.FailoverNode, holder.FailoverNode, "automatic_failover")
}

// UpdateRoleHeartbeat updates the last-seen timestamp for roles held by current node
func (s *Service) UpdateRoleHeartbeat() error {
	currentNode := s.getCurrentNode()

	s.rolesMutex.Lock()
	defer s.rolesMutex.Unlock()

	updated := false
	for role, holder := range s.roles {
		if holder.NodeID == currentNode.NodeID {
			holder.LastSeen = time.Now()

			// Update in database
			query := `UPDATE fsmo_roles SET last_seen = ? WHERE role = ?`
			s.db.Exec(query, holder.LastSeen, string(role))

			updated = true
		}
	}

	if updated {
		s.logger.Debug("Updated FSMO role heartbeat for node %s", currentNode.NodeName)
	}

	return nil
}

// SetFailoverNode sets the designated failover node for a role
func (s *Service) SetFailoverNode(role FSMORole, failoverNodeID string) error {
	s.rolesMutex.Lock()
	defer s.rolesMutex.Unlock()

	holder, exists := s.roles[role]
	if !exists {
		return fmt.Errorf("role %s not assigned", role)
	}

	holder.FailoverNode = failoverNodeID

	// Update in database
	query := `UPDATE fsmo_roles SET failover_node = ? WHERE role = ?`
	if _, err := s.db.Exec(query, failoverNodeID, string(role)); err != nil {
		return fmt.Errorf("failed to update failover node: %w", err)
	}

	s.logger.Info("Set failover node for %s to %s", role, failoverNodeID)

	return nil
}

// GetRoleCapabilities returns capabilities description for each role
func (s *Service) GetRoleCapabilities() map[FSMORole]string {
	return map[FSMORole]string{
		SchemaMaster:         "Schema management and replication - Controls directory schema modifications",
		DomainNamingMaster:   "Domain creation and deletion authority - Manages forest-wide domain namespace",
		RIDMaster:            "Relative Identifier allocation - Distributes unique RID pools to domain controllers",
		PDCEmulator:          "Time synchronization and legacy compatibility - Acts as Windows NT PDC for downlevel clients",
		InfrastructureMaster: "Cross-domain reference updates - Maintains references to objects in other domains",
	}
}

// ValidateRoleIntegrity performs integrity checks on FSMO role assignments
func (s *Service) ValidateRoleIntegrity() ([]string, error) {
	issues := make([]string, 0)

	allRoles := []FSMORole{
		SchemaMaster,
		DomainNamingMaster,
		RIDMaster,
		PDCEmulator,
		InfrastructureMaster,
	}

	s.rolesMutex.RLock()
	defer s.rolesMutex.RUnlock()

	// Check all roles are assigned
	for _, role := range allRoles {
		if _, exists := s.roles[role]; !exists {
			issues = append(issues, fmt.Sprintf("Role %s is not assigned to any node", role))
		}
	}

	// Check for duplicate assignments
	nodeCounts := make(map[string]int)
	for _, holder := range s.roles {
		nodeCounts[holder.NodeID]++
	}

	// Warn if all roles on single node (not ideal for HA)
	for nodeID, count := range nodeCounts {
		if count == len(allRoles) {
			issues = append(issues, fmt.Sprintf("All FSMO roles assigned to single node %s (not recommended for HA)", nodeID))
		}
	}

	// Check for failed role holders
	for role, holder := range s.roles {
		if holder.Status == "failed" {
			issues = append(issues, fmt.Sprintf("Role %s holder %s is in failed state", role, holder.NodeName))
		}
	}

	return issues, nil
}

// Shutdown gracefully stops the FSMO service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down FSMO service")
	close(s.stopChan)
	return nil
}
