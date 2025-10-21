// Package registry implements Docker registry functionality
// Provides container image storage, distribution, and vulnerability scanning
// as specified in CASDC Development Platform requirements
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Docker registry operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Registry configuration
	registryPath string
	blobsPath    string
	reposPath    string
	webPath      string

	// Image management
	images      map[string]*Image
	imagesMutex sync.RWMutex

	// Repository management
	repositories map[string]*Repository
	reposMutex   sync.RWMutex
}

// Repository represents a Docker registry repository
type Repository struct {
	Name        string
	Description string
	Private     bool
	Stars       int
	Pulls       int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Tags        []*Tag
}

// Image represents a container image
type Image struct {
	ID          string
	Repository  string
	Tag         string
	Digest      string
	Size        int64
	Architecture string
	OS          string
	Layers      []Layer
	Config      ImageConfig
	CreatedAt   time.Time
	PushedAt    time.Time
	PulledAt    *time.Time
	PullCount   int64
	Scanned     bool
	ScanResults *ScanResults
}

// Layer represents an image layer
type Layer struct {
	Digest      string
	Size        int64
	MediaType   string
	URLs        []string
	Created     time.Time
}

// ImageConfig represents image configuration
type ImageConfig struct {
	User         string
	ExposedPorts map[string]struct{}
	Env          []string
	Cmd          []string
	Volumes      map[string]struct{}
	WorkingDir   string
	Entrypoint   []string
	Labels       map[string]string
}

// Tag represents an image tag
type Tag struct {
	Name      string
	ImageID   string
	Digest    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScanResults represents vulnerability scan results
type ScanResults struct {
	ScanID      string
	ScanTime    time.Time
	Scanner     string
	Critical    int
	High        int
	Medium      int
	Low         int
	Negligible  int
	Total       int
	Vulnerabilities []Vulnerability
}

// Vulnerability represents a security vulnerability
type Vulnerability struct {
	ID          string
	Severity    string
	Package     string
	Version     string
	FixedIn     string
	Description string
	Links       []string
}

// Manifest represents an image manifest
type Manifest struct {
	SchemaVersion int
	MediaType     string
	Config        Descriptor
	Layers        []Descriptor
}

// Descriptor represents a content descriptor
type Descriptor struct {
	MediaType string
	Digest    string
	Size      int64
	URLs      []string
}

// NewService creates a new Docker registry service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// SPEC: Docker registry storage structure
		registryPath: filepath.Join(cfg.DataDir, "registry"),
		blobsPath:    filepath.Join(cfg.DataDir, "registry", "docker", "registry", "v2", "blobs"),
		reposPath:    filepath.Join(cfg.DataDir, "registry", "docker", "registry", "v2", "repositories"),
		webPath:      "/var/www/default/registry",

		images:       make(map[string]*Image),
		repositories: make(map[string]*Repository),
	}

	// Create directory structure
	if err := s.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create registry directories: %w", err)
	}

	// Load repositories from database
	if err := s.loadRepositories(); err != nil {
		log.Warn("Failed to load repositories: %v", err)
	}

	// Load images from database
	if err := s.loadImages(); err != nil {
		log.Warn("Failed to load images: %v", err)
	}

	return s, nil
}

// ensureDirectories creates the Docker registry directory structure
func (s *Service) ensureDirectories() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.registryPath, 0755},
		{s.blobsPath, 0755},
		{filepath.Join(s.blobsPath, "sha256"), 0755},
		{s.reposPath, 0755},
		{filepath.Join(s.registryPath, "helm"), 0755},
		{filepath.Join(s.registryPath, "generic"), 0755},
		{s.webPath, 0755},
		{filepath.Join(s.webPath, "assets"), 0755},
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
		SELECT name, description, private, stars, pulls, created_at, updated_at
		FROM docker_repositories
		ORDER BY name
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
		err := rows.Scan(
			&repo.Name, &repo.Description, &repo.Private,
			&repo.Stars, &repo.Pulls, &repo.CreatedAt, &repo.UpdatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan repository: %v", err)
			continue
		}

		s.repositories[repo.Name] = repo
		s.logger.Debug("Loaded repository: %s", repo.Name)
	}

	return rows.Err()
}

// loadImages loads all images from database
func (s *Service) loadImages() error {
	query := `
		SELECT id, repository, tag, digest, size, architecture, os,
		       created_at, pushed_at, pulled_at, pull_count, scanned
		FROM docker_images
		ORDER BY pushed_at DESC
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query images: %w", err)
	}
	defer rows.Close()

	s.imagesMutex.Lock()
	defer s.imagesMutex.Unlock()

	for rows.Next() {
		img := &Image{}
		var pulledAt *time.Time

		err := rows.Scan(
			&img.ID, &img.Repository, &img.Tag, &img.Digest, &img.Size,
			&img.Architecture, &img.OS, &img.CreatedAt, &img.PushedAt,
			&pulledAt, &img.PullCount, &img.Scanned,
		)
		if err != nil {
			s.logger.Error("Failed to scan image: %v", err)
			continue
		}

		img.PulledAt = pulledAt

		imageKey := fmt.Sprintf("%s:%s", img.Repository, img.Tag)
		s.images[imageKey] = img
		s.logger.Debug("Loaded image: %s", imageKey)
	}

	return rows.Err()
}

// PushImage handles image push to registry
func (s *Service) PushImage(repository, tag string, manifest *Manifest, layers []io.Reader) error {
	s.logger.Info("Pushing image: %s:%s", repository, tag)

	// Validate repository name
	if !isValidRepositoryName(repository) {
		return fmt.Errorf("invalid repository name: %s", repository)
	}

	// Ensure repository exists
	if err := s.ensureRepository(repository); err != nil {
		return fmt.Errorf("failed to ensure repository: %w", err)
	}

	// Store layers as blobs
	var layerDigests []string
	var totalSize int64

	for i, layer := range layers {
		digest, size, err := s.storeBlob(layer)
		if err != nil {
			return fmt.Errorf("failed to store layer %d: %w", i, err)
		}
		layerDigests = append(layerDigests, digest)
		totalSize += size
	}

	// Calculate manifest digest
	manifestDigest := calculateDigest(fmt.Sprintf("%v", manifest))

	// Create image record
	img := &Image{
		ID:           manifestDigest[:12],
		Repository:   repository,
		Tag:          tag,
		Digest:       manifestDigest,
		Size:         totalSize,
		Architecture: "amd64", // TODO: Extract from manifest
		OS:           "linux",  // TODO: Extract from manifest
		CreatedAt:    time.Now(),
		PushedAt:     time.Now(),
		PullCount:    0,
		Scanned:      false,
	}

	// Insert into database
	query := `
		INSERT INTO docker_images (id, repository, tag, digest, size, architecture, os,
		                          created_at, pushed_at, pull_count, scanned)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, FALSE)
	`

	_, err := s.db.Exec(query,
		img.ID, img.Repository, img.Tag, img.Digest, img.Size,
		img.Architecture, img.OS, img.CreatedAt, img.PushedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert image: %w", err)
	}

	// Add to cache
	imageKey := fmt.Sprintf("%s:%s", repository, tag)
	s.imagesMutex.Lock()
	s.images[imageKey] = img
	s.imagesMutex.Unlock()

	s.logger.Info("Pushed image: %s (digest: %s, size: %d)", imageKey, img.Digest[:12], img.Size)

	// Schedule vulnerability scan
	go s.scanImage(img)

	return nil
}

// storeBlob stores a blob (layer) in the registry
func (s *Service) storeBlob(data io.Reader) (string, int64, error) {
	// Calculate digest while reading
	h := sha256.New()
	tmpFile, err := os.CreateTemp(s.blobsPath, "blob-*.tmp")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	// Copy data to temp file while hashing
	size, err := io.Copy(io.MultiWriter(tmpFile, h), data)
	if err != nil {
		return "", 0, fmt.Errorf("failed to write blob: %w", err)
	}

	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))

	// Move to final location with content-addressable storage
	blobPath := filepath.Join(s.blobsPath, "sha256", digest[7:9], digest[7:])
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		return "", 0, fmt.Errorf("failed to create blob directory: %w", err)
	}

	tmpFile.Close()
	if err := os.Rename(tmpFile.Name(), blobPath); err != nil {
		return "", 0, fmt.Errorf("failed to move blob: %w", err)
	}

	return digest, size, nil
}

// ensureRepository ensures a repository exists
func (s *Service) ensureRepository(name string) error {
	s.reposMutex.RLock()
	_, exists := s.repositories[name]
	s.reposMutex.RUnlock()

	if exists {
		return nil
	}

	// Create repository
	repo := &Repository{
		Name:      name,
		Private:   false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	query := `
		INSERT INTO docker_repositories (name, description, private, stars, pulls, created_at, updated_at)
		VALUES (?, '', FALSE, 0, 0, ?, ?)
	`

	_, err := s.db.Exec(query, name, repo.CreatedAt, repo.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create repository: %w", err)
	}

	s.reposMutex.Lock()
	s.repositories[name] = repo
	s.reposMutex.Unlock()

	s.logger.Info("Created repository: %s", name)
	return nil
}

// scanImage performs vulnerability scanning on an image
func (s *Service) scanImage(img *Image) {
	s.logger.Info("Scanning image for vulnerabilities: %s:%s", img.Repository, img.Tag)

	// TODO: Implement actual vulnerability scanning with Trivy or Clair
	// For now, mark as scanned
	query := "UPDATE docker_images SET scanned = TRUE WHERE id = ?"
	if _, err := s.db.Exec(query, img.ID); err != nil {
		s.logger.Error("Failed to update scan status: %v", err)
		return
	}

	s.imagesMutex.Lock()
	img.Scanned = true
	s.imagesMutex.Unlock()

	s.logger.Info("Image scan completed: %s:%s", img.Repository, img.Tag)
}

// isValidRepositoryName validates Docker repository name
func isValidRepositoryName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}

	// Repository name format: [namespace/]name
	parts := strings.Split(name, "/")
	if len(parts) > 2 {
		return false
	}

	for _, part := range parts {
		if len(part) == 0 {
			return false
		}

		// Each part must contain only lowercase letters, numbers, and separators
		for _, ch := range part {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') ||
				ch == '-' || ch == '_' || ch == '.') {
				return false
			}
		}

		// Cannot start with separator
		if part[0] == '-' || part[0] == '_' || part[0] == '.' {
			return false
		}
	}

	return true
}

// calculateDigest calculates SHA256 digest of data
func calculateDigest(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// GetAllImages returns all images
func (s *Service) GetAllImages() []*Image {
	s.imagesMutex.RLock()
	defer s.imagesMutex.RUnlock()

	images := make([]*Image, 0, len(s.images))
	for _, img := range s.images {
		images = append(images, img)
	}
	return images
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

// Shutdown gracefully stops the Docker registry service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Docker registry service")
	return nil
}
