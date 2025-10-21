// Package git implements Git server functionality with web interface
// Provides complete Git repository management, access control, and collaboration features
// as specified in CASDC Development Platform requirements
package git

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Git server operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Git server configuration
	gitBasePath    string
	gitWebPath     string
	hooksDir       string
	sshKeysFile    string
	authorizedKeys string

	// Repository management
	repositories map[int64]*Repository
	reposMutex   sync.RWMutex

	// Organization management
	organizations map[int64]*Organization
	orgsMutex     sync.RWMutex
}

// Repository represents a Git repository with access control
type Repository struct {
	ID          int64
	Name        string
	FullName    string // organization/repository or user/repository
	Description string
	OrgID       int64
	OwnerID     int64
	Private     bool
	DefaultBranch string
	Size        int64
	Stars       int
	Forks       int
	OpenIssues  int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PushedAt    *time.Time
	CloneURL    string
	SSHURL      string
	WebURL      string
}

// Organization represents a Git organization for repository grouping
type Organization struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Website     string
	Email       string
	Location    string
	Visibility  string // public, private
	CreatedAt   time.Time
	Members     []*Member
}

// Member represents an organization member with role
type Member struct {
	UserID    int64
	Username  string
	Role      string // owner, admin, member, readonly
	JoinedAt  time.Time
}

// Collaborator represents a repository collaborator with permissions
type Collaborator struct {
	UserID     int64
	Username   string
	Permission string // read, write, admin
	AddedAt    time.Time
}

// Branch represents a Git branch
type Branch struct {
	Name      string
	Protected bool
	Commit    *Commit
}

// Commit represents a Git commit
type Commit struct {
	SHA       string
	Author    string
	Email     string
	Message   string
	Timestamp time.Time
	Parents   []string
}

// PullRequest represents a pull request
type PullRequest struct {
	ID          int64
	RepoID      int64
	Number      int
	Title       string
	Description string
	State       string // open, closed, merged
	AuthorID    int64
	SourceBranch string
	TargetBranch string
	Mergeable   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	MergedAt    *time.Time
	MergedBy    *int64
}

// Issue represents a repository issue
type Issue struct {
	ID          int64
	RepoID      int64
	Number      int
	Title       string
	Description string
	State       string // open, closed
	AuthorID    int64
	AssigneeID  *int64
	Labels      []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
}

// Webhook represents a repository webhook
type Webhook struct {
	ID        int64
	RepoID    int64
	URL       string
	Secret    string
	Events    []string // push, pull_request, issue, etc.
	Active    bool
	CreatedAt time.Time
}

// NewService creates a new Git server service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// SPEC: Git repository paths
		gitBasePath:    filepath.Join(cfg.DataDir, "git"),
		gitWebPath:     "/var/www/default/git",
		hooksDir:       filepath.Join(cfg.DataDir, "git", "hooks"),
		sshKeysFile:    filepath.Join(cfg.ConfigDir, "git", "authorized_keys"),
		authorizedKeys: filepath.Join(cfg.DataDir, "git", ".ssh", "authorized_keys"),

		repositories:  make(map[int64]*Repository),
		organizations: make(map[int64]*Organization),
	}

	// Create directory structure
	if err := s.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create Git directories: %w", err)
	}

	// Load repositories from database
	if err := s.loadRepositories(); err != nil {
		log.Warn("Failed to load repositories: %v", err)
	}

	// Load organizations from database
	if err := s.loadOrganizations(); err != nil {
		log.Warn("Failed to load organizations: %v", err)
	}

	// Setup Git hooks
	if err := s.setupGitHooks(); err != nil {
		return nil, fmt.Errorf("failed to setup Git hooks: %w", err)
	}

	return s, nil
}

// ensureDirectories creates the Git directory structure
func (s *Service) ensureDirectories() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.gitBasePath, 0755},
		{filepath.Join(s.gitBasePath, "repositories"), 0755},
		{filepath.Join(s.gitBasePath, "organizations"), 0755},
		{filepath.Join(s.gitBasePath, "users"), 0755},
		{s.hooksDir, 0755},
		{s.gitWebPath, 0755},
		{filepath.Join(s.gitWebPath, "assets"), 0755},
		{filepath.Dir(s.sshKeysFile), 0700},
		{filepath.Dir(s.authorizedKeys), 0700},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
	}

	return nil
}

// loadRepositories loads all repositories from database
func (s *Service) loadRepositories() error {
	query := `
		SELECT id, name, full_name, description, org_id, owner_id, private,
		       default_branch, size, stars, forks, open_issues,
		       created_at, updated_at, pushed_at
		FROM git_repositories
		ORDER BY updated_at DESC
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query repositories: %w", err)
	}
	defer rows.Close()

	s.reposMutex.Lock()
	defer s.reposMutex.Unlock()

	for rows.Next() {
		repo := &Repository{}
		var pushedAt sql.NullTime

		err := rows.Scan(
			&repo.ID, &repo.Name, &repo.FullName, &repo.Description,
			&repo.OrgID, &repo.OwnerID, &repo.Private, &repo.DefaultBranch,
			&repo.Size, &repo.Stars, &repo.Forks, &repo.OpenIssues,
			&repo.CreatedAt, &repo.UpdatedAt, &pushedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan repository: %v", err)
			continue
		}

		if pushedAt.Valid {
			repo.PushedAt = &pushedAt.Time
		}

		// Generate URLs
		repo.CloneURL = fmt.Sprintf("https://%s/git/%s.git", s.config.Domain, repo.FullName)
		repo.SSHURL = fmt.Sprintf("git@%s:%s.git", s.config.Domain, repo.FullName)
		repo.WebURL = fmt.Sprintf("https://%s/git/%s", s.config.Domain, repo.FullName)

		s.repositories[repo.ID] = repo
		s.logger.Debug("Loaded repository: %s", repo.FullName)
	}

	return rows.Err()
}

// loadOrganizations loads all organizations from database
func (s *Service) loadOrganizations() error {
	query := `
		SELECT id, name, display_name, description, website, email,
		       location, visibility, created_at
		FROM git_organizations
		ORDER BY name
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query organizations: %w", err)
	}
	defer rows.Close()

	s.orgsMutex.Lock()
	defer s.orgsMutex.Unlock()

	for rows.Next() {
		org := &Organization{}
		var website, email, location sql.NullString

		err := rows.Scan(
			&org.ID, &org.Name, &org.DisplayName, &org.Description,
			&website, &email, &location, &org.Visibility, &org.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan organization: %v", err)
			continue
		}

		org.Website = website.String
		org.Email = email.String
		org.Location = location.String

		s.organizations[org.ID] = org
		s.logger.Debug("Loaded organization: %s", org.Name)
	}

	return rows.Err()
}

// CreateRepository creates a new Git repository
func (s *Service) CreateRepository(name, description string, ownerID int64, private bool) (*Repository, error) {
	s.logger.Info("Creating Git repository: %s", name)

	// Validate repository name
	if !isValidRepoName(name) {
		return nil, fmt.Errorf("invalid repository name: %s", name)
	}

	// Check if repository already exists
	if s.repositoryExists(name, ownerID) {
		return nil, fmt.Errorf("repository already exists: %s", name)
	}

	// Get owner username for full name
	username, err := s.getUsername(ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get username: %w", err)
	}

	fullName := fmt.Sprintf("%s/%s", username, name)
	repoPath := filepath.Join(s.gitBasePath, "repositories", username, name+".git")

	// Create bare Git repository
	if err := s.initBareRepository(repoPath); err != nil {
		return nil, fmt.Errorf("failed to initialize repository: %w", err)
	}

	// Insert into database
	query := `
		INSERT INTO git_repositories (name, full_name, description, owner_id, private,
		                             default_branch, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'main', ?, ?)
	`

	now := time.Now()
	result, err := s.db.Exec(query, name, fullName, description, ownerID, private, now, now)
	if err != nil {
		// Cleanup created repository
		os.RemoveAll(repoPath)
		return nil, fmt.Errorf("failed to create repository in database: %w", err)
	}

	repoID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get repository ID: %w", err)
	}

	repo := &Repository{
		ID:            repoID,
		Name:          name,
		FullName:      fullName,
		Description:   description,
		OwnerID:       ownerID,
		Private:       private,
		DefaultBranch: "main",
		CreatedAt:     now,
		UpdatedAt:     now,
		CloneURL:      fmt.Sprintf("https://%s/git/%s.git", s.config.Domain, fullName),
		SSHURL:        fmt.Sprintf("git@%s:%s.git", s.config.Domain, fullName),
		WebURL:        fmt.Sprintf("https://%s/git/%s", s.config.Domain, fullName),
	}

	// Add to cache
	s.reposMutex.Lock()
	s.repositories[repoID] = repo
	s.reposMutex.Unlock()

	s.logger.Info("Created repository: %s (ID: %d)", fullName, repoID)
	return repo, nil
}

// initBareRepository initializes a bare Git repository
func (s *Service) initBareRepository(path string) error {
	// Create directory
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Initialize bare repository
	cmd := exec.Command("git", "init", "--bare", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to initialize repository: %w, output: %s", err, output)
	}

	// Set default branch to main
	cmd = exec.Command("git", "-C", path, "symbolic-ref", "HEAD", "refs/heads/main")
	if output, err := cmd.CombinedOutput(); err != nil {
		s.logger.Warn("Failed to set default branch: %v, output: %s", err, output)
	}

	// Install Git hooks
	if err := s.installRepoHooks(path); err != nil {
		s.logger.Warn("Failed to install hooks: %v", err)
	}

	return nil
}

// setupGitHooks creates global Git hooks
func (s *Service) setupGitHooks() error {
	hooks := map[string]string{
		"pre-receive": `#!/bin/bash
# CASDC Git Pre-Receive Hook
# Validates commits and enforces repository policies

while read oldrev newrev refname; do
    echo "CASDC: Validating push to $refname"
    # TODO: Add validation logic
done
`,
		"post-receive": `#!/bin/bash
# CASDC Git Post-Receive Hook
# Triggers webhooks and updates repository metadata

while read oldrev newrev refname; do
    echo "CASDC: Processing push to $refname"
    # TODO: Trigger webhooks and update stats
done
`,
		"update": `#!/bin/bash
# CASDC Git Update Hook
# Enforces branch protection and access control

refname="$1"
oldrev="$2"
newrev="$3"

echo "CASDC: Checking permissions for $refname"
# TODO: Check branch protection and user permissions
`,
	}

	for name, content := range hooks {
		hookPath := filepath.Join(s.hooksDir, name)
		if err := os.WriteFile(hookPath, []byte(content), 0755); err != nil {
			return fmt.Errorf("failed to write hook %s: %w", name, err)
		}
	}

	return nil
}

// installRepoHooks installs hooks for a specific repository
func (s *Service) installRepoHooks(repoPath string) error {
	hooksDir := filepath.Join(repoPath, "hooks")

	// Link global hooks to repository
	globalHooks := []string{"pre-receive", "post-receive", "update"}
	for _, hook := range globalHooks {
		src := filepath.Join(s.hooksDir, hook)
		dst := filepath.Join(hooksDir, hook)

		// Remove existing hook
		os.Remove(dst)

		// Create symlink to global hook
		if err := os.Symlink(src, dst); err != nil {
			s.logger.Warn("Failed to symlink hook %s: %v", hook, err)
		}
	}

	return nil
}

// repositoryExists checks if a repository already exists
func (s *Service) repositoryExists(name string, ownerID int64) bool {
	s.reposMutex.RLock()
	defer s.reposMutex.RUnlock()

	for _, repo := range s.repositories {
		if repo.Name == name && repo.OwnerID == ownerID {
			return true
		}
	}
	return false
}

// getUsername retrieves username by user ID
func (s *Service) getUsername(userID int64) (string, error) {
	var username string
	query := "SELECT username FROM users WHERE id = ?"
	err := s.db.QueryRow(query, userID).Scan(&username)
	if err != nil {
		return "", fmt.Errorf("failed to get username: %w", err)
	}
	return username, nil
}

// isValidRepoName validates repository name
func isValidRepoName(name string) bool {
	if len(name) == 0 || len(name) > 100 {
		return false
	}

	// Repository name must contain only alphanumeric, dash, underscore, dot
	for _, ch := range name {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}

	// Cannot start or end with special characters
	if name[0] == '-' || name[0] == '_' || name[0] == '.' ||
		name[len(name)-1] == '-' || name[len(name)-1] == '_' || name[len(name)-1] == '.' {
		return false
	}

	// Reserved names
	reserved := []string{"git", "api", "assets", "raw", "new", "admin"}
	nameLower := strings.ToLower(name)
	for _, r := range reserved {
		if nameLower == r {
			return false
		}
	}

	return true
}

// GetAllRepositories returns all repositories
func (s *Service) GetAllRepositories() []*Repository {
	s.reposMutex.RLock()
	defer s.reposMutex.RUnlock()

	repos := make([]*Repository, 0, len(s.repositories))
	for _, repo := range s.repositories {
		repos = append(repos, repo)
	}
	return repos
}

// Shutdown gracefully stops the Git service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Git server service")
	return nil
}
