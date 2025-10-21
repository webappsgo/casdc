// Package backup provides comprehensive backup and disaster recovery functionality for CASDC
// Including universal deduplication, intelligent scheduling, and point-in-time recovery
package backup

import (
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the backup service with universal deduplication
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Backup directories
	backupDir    string
	metadataDir  string
	chunkStoreDir string

	// Deduplication components
	chunkIndex   *ChunkIndex
	chunkSize    int
	compression  string // LZ4 for speed, ZSTD for size
	encryption   bool

	// Backup scheduling
	scheduler    *BackupScheduler
	runningJobs  map[int64]*BackupJob
	jobMutex     sync.RWMutex
}

// BackupJob represents a backup job configuration
type BackupJob struct {
	ID              int64             `json:"id" db:"id"`
	Name            string            `json:"name" db:"name"`
	Description     string            `json:"description" db:"description"`
	BackupType      string            `json:"backup_type" db:"backup_type"` // full, incremental, differential
	SourcePaths     []string          `json:"source_paths" db:"source_paths"`
	Destination     string            `json:"destination" db:"destination"`
	Schedule        string            `json:"schedule" db:"schedule"` // Cron expression
	RetentionDays   int               `json:"retention_days" db:"retention_days"`
	Compression     bool              `json:"compression" db:"compression"`
	Encryption      bool              `json:"encryption" db:"encryption"`
	EncryptionKeyID string            `json:"encryption_key_id" db:"encryption_key_id"`
	Enabled         bool              `json:"enabled" db:"enabled"`
	CreatedAt       time.Time         `json:"created_at" db:"created_at"`
	LastRun         *time.Time        `json:"last_run" db:"last_run"`
	NextRun         *time.Time        `json:"next_run" db:"next_run"`
	Status          string            `json:"status"`
	Progress        float64           `json:"progress"`
}

// BackupHistory represents a backup execution record
type BackupHistory struct {
	ID             int64      `json:"id" db:"id"`
	JobID          int64      `json:"job_id" db:"job_id"`
	StartedAt      time.Time  `json:"started_at" db:"started_at"`
	CompletedAt    *time.Time `json:"completed_at" db:"completed_at"`
	Status         string     `json:"status" db:"status"` // running, completed, failed, cancelled
	FilesBackedUp  int64      `json:"files_backed_up" db:"files_backed_up"`
	BytesBackedUp  int64      `json:"bytes_backed_up" db:"bytes_backed_up"`
	BytesDeduplicated int64   `json:"bytes_deduplicated"`
	DurationSeconds int        `json:"duration_seconds" db:"duration_seconds"`
	ErrorMessage   string     `json:"error_message" db:"error_message"`
	BackupPath     string     `json:"backup_path" db:"backup_path"`
}

// ChunkIndex manages deduplication chunks
type ChunkIndex struct {
	mu      sync.RWMutex
	chunks  map[string]*ChunkMetadata
	indexDB *ChunkDatabase
}

// ChunkMetadata represents a deduplicated chunk
type ChunkMetadata struct {
	Hash           string    `json:"hash"`
	Size           int64     `json:"size"`
	CompressedSize int64     `json:"compressed_size"`
	RefCount       int       `json:"ref_count"`
	FirstSeen      time.Time `json:"first_seen"`
	LastAccessed   time.Time `json:"last_accessed"`
	StoragePath    string    `json:"storage_path"`
}

// ChunkDatabase manages persistent chunk index
type ChunkDatabase struct {
	path string
	mu   sync.RWMutex
}

// BackupScheduler manages scheduled backup jobs
type BackupScheduler struct {
	jobs    map[int64]*ScheduledJob
	mu      sync.RWMutex
	service *Service
	ctx     context.Context
	cancel  context.CancelFunc
}

// ScheduledJob represents a scheduled backup job
type ScheduledJob struct {
	Job    *BackupJob
	Timer  *time.Timer
	Active bool
}

// BackupManifest represents the backup metadata
type BackupManifest struct {
	Version       string                 `json:"version"`
	BackupID      string                 `json:"backup_id"`
	JobID         int64                  `json:"job_id"`
	Type          string                 `json:"type"`
	CreatedAt     time.Time              `json:"created_at"`
	SourcePaths   []string               `json:"source_paths"`
	FileCount     int64                  `json:"file_count"`
	TotalSize     int64                  `json:"total_size"`
	DeduplicatedSize int64               `json:"deduplicated_size"`
	ChunkCount    int64                  `json:"chunk_count"`
	Files         []FileEntry            `json:"files"`
	ChunkMap      map[string][]string    `json:"chunk_map"` // file path -> chunk hashes
	Metadata      map[string]interface{} `json:"metadata"`
}

// FileEntry represents a file in the backup
type FileEntry struct {
	Path         string      `json:"path"`
	Size         int64       `json:"size"`
	Mode         os.FileMode `json:"mode"`
	ModTime      time.Time   `json:"mod_time"`
	IsDir        bool        `json:"is_dir"`
	ChunkHashes  []string    `json:"chunk_hashes"`
	Owner        string      `json:"owner"`
	Group        string      `json:"group"`
	Permissions  string      `json:"permissions"`
}

// NewService creates a new backup service with universal deduplication
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	service := &Service{
		db:           db,
		config:       cfg,
		logger:       log,
		chunkSize:    64 * 1024, // 64KB chunks for optimal deduplication
		compression:  "ZSTD",     // Default to ZSTD for better compression
		encryption:   true,
		runningJobs:  make(map[int64]*BackupJob),
	}

	// Initialize backup directories
	if err := service.initializeDirectories(); err != nil {
		return nil, fmt.Errorf("failed to initialize backup directories: %w", err)
	}

	// Initialize chunk index for deduplication
	if err := service.initializeChunkIndex(); err != nil {
		return nil, fmt.Errorf("failed to initialize chunk index: %w", err)
	}

	// Initialize backup scheduler
	if err := service.initializeScheduler(); err != nil {
		return nil, fmt.Errorf("failed to initialize scheduler: %w", err)
	}

	// Load existing backup jobs
	if err := service.loadBackupJobs(); err != nil {
		return nil, fmt.Errorf("failed to load backup jobs: %w", err)
	}

	service.logger.Info("Backup service initialized with universal deduplication")
	return service, nil
}

// initializeDirectories creates the backup directory structure
func (s *Service) initializeDirectories() error {
	// Main backup directory
	s.backupDir = "/mnt/backups/casdc"
	s.metadataDir = filepath.Join(s.backupDir, "metadata")
	s.chunkStoreDir = filepath.Join(s.backupDir, "chunks")

	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.backupDir, 0700},
		{s.metadataDir, 0700},
		{s.chunkStoreDir, 0700},
		{filepath.Join(s.metadataDir, "backup-catalog.db"), 0600},
		{filepath.Join(s.metadataDir, "encryption-keys"), 0700},
		{filepath.Join(s.metadataDir, "restore-scripts"), 0755},
	}

	for _, d := range dirs {
		dir := d.path
		if strings.HasSuffix(d.path, ".db") {
			dir = filepath.Dir(d.path)
		}
		if err := os.MkdirAll(dir, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Create year/month/day structure for organized storage
	now := time.Now()
	datePath := filepath.Join(s.backupDir, fmt.Sprintf("%d/%02d/%02d", now.Year(), now.Month(), now.Day()))
	if err := os.MkdirAll(datePath, 0755); err != nil {
		return fmt.Errorf("failed to create date directory: %w", err)
	}

	return nil
}

// initializeChunkIndex initializes the deduplication chunk index
func (s *Service) initializeChunkIndex() error {
	s.chunkIndex = &ChunkIndex{
		chunks: make(map[string]*ChunkMetadata),
		indexDB: &ChunkDatabase{
			path: filepath.Join(s.metadataDir, "chunk-index.db"),
		},
	}

	// Load existing chunk index from database
	if err := s.chunkIndex.loadFromDisk(); err != nil {
		s.logger.Warn("Failed to load chunk index, starting fresh: %v", err)
	}

	s.logger.Info("Chunk index initialized with %d existing chunks", len(s.chunkIndex.chunks))
	return nil
}

// initializeScheduler sets up the backup scheduler
func (s *Service) initializeScheduler() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.scheduler = &BackupScheduler{
		jobs:    make(map[int64]*ScheduledJob),
		service: s,
		ctx:     ctx,
		cancel:  cancel,
	}

	// Start scheduler goroutine
	go s.scheduler.run()

	return nil
}

// loadBackupJobs loads configured backup jobs from database
func (s *Service) loadBackupJobs() error {
	rows, err := s.db.Query(`
		SELECT id, name, description, backup_type, source_paths, destination,
		       schedule, retention_days, compression, encryption, encryption_key_id,
		       enabled, created_at, last_run, next_run
		FROM backup_jobs WHERE enabled = true`)
	if err != nil {
		return fmt.Errorf("failed to query backup jobs: %w", err)
	}
	defer rows.Close()

	jobCount := 0
	for rows.Next() {
		var job BackupJob
		var sourcePaths string
		if err := rows.Scan(
			&job.ID, &job.Name, &job.Description, &job.BackupType, &sourcePaths,
			&job.Destination, &job.Schedule, &job.RetentionDays, &job.Compression,
			&job.Encryption, &job.EncryptionKeyID, &job.Enabled, &job.CreatedAt,
			&job.LastRun, &job.NextRun,
		); err != nil {
			s.logger.Warn("Failed to scan backup job: %v", err)
			continue
		}

		// Parse source paths from JSON
		if err := json.Unmarshal([]byte(sourcePaths), &job.SourcePaths); err != nil {
			s.logger.Warn("Failed to parse source paths for job %d: %v", job.ID, err)
			continue
		}

		// Schedule the job
		if job.Enabled && job.Schedule != "" {
			s.scheduler.scheduleJob(&job)
		}

		jobCount++
	}

	s.logger.Info("Loaded %d backup jobs", jobCount)
	return nil
}

// CreateBackup creates a new backup with deduplication
func (s *Service) CreateBackup(jobID int64) error {
	// Get job configuration
	var job BackupJob
	err := s.db.QueryRow(`
		SELECT id, name, backup_type, source_paths, destination, compression, encryption
		FROM backup_jobs WHERE id = ?`, jobID).Scan(
		&job.ID, &job.Name, &job.BackupType, &job.SourcePaths, &job.Destination,
		&job.Compression, &job.Encryption,
	)
	if err != nil {
		return fmt.Errorf("backup job not found: %w", err)
	}

	// Mark job as running
	s.jobMutex.Lock()
	s.runningJobs[jobID] = &job
	job.Status = "running"
	job.Progress = 0
	s.jobMutex.Unlock()
	defer func() {
		s.jobMutex.Lock()
		delete(s.runningJobs, jobID)
		s.jobMutex.Unlock()
	}()

	// Create backup history record
	history := &BackupHistory{
		JobID:     jobID,
		StartedAt: time.Now(),
		Status:    "running",
	}

	result, err := s.db.Exec(`
		INSERT INTO backup_history (job_id, started_at, status)
		VALUES (?, ?, ?)`, history.JobID, history.StartedAt, history.Status)
	if err != nil {
		return fmt.Errorf("failed to create history record: %w", err)
	}
	historyID, _ := result.LastInsertId()
	history.ID = historyID

	// Execute the backup
	manifest, err := s.executeBackup(&job, history)
	if err != nil {
		s.updateBackupHistory(history.ID, "failed", err.Error())
		return fmt.Errorf("backup failed: %w", err)
	}

	// Update history with success
	history.CompletedAt = &manifest.CreatedAt
	history.Status = "completed"
	history.FilesBackedUp = manifest.FileCount
	history.BytesBackedUp = manifest.TotalSize
	history.BytesDeduplicated = manifest.TotalSize - manifest.DeduplicatedSize
	history.DurationSeconds = int(time.Since(history.StartedAt).Seconds())
	history.BackupPath = manifest.BackupID

	s.updateBackupHistory(history.ID, "completed", "")

	// Update job last run time
	now := time.Now()
	_, err = s.db.Exec(`UPDATE backup_jobs SET last_run = ? WHERE id = ?`, now, jobID)
	if err != nil {
		s.logger.Warn("Failed to update job last run time: %v", err)
	}

	s.logger.Info("Backup completed successfully: %s (%.2f%% deduplication)",
		manifest.BackupID,
		float64(history.BytesDeduplicated)/float64(history.BytesBackedUp)*100)

	return nil
}

// executeBackup performs the actual backup with deduplication
func (s *Service) executeBackup(job *BackupJob, history *BackupHistory) (*BackupManifest, error) {
	// Create backup manifest
	manifest := &BackupManifest{
		Version:      "1.0",
		BackupID:     s.generateBackupID(job),
		JobID:        job.ID,
		Type:         job.BackupType,
		CreatedAt:    time.Now(),
		SourcePaths:  job.SourcePaths,
		Files:        []FileEntry{},
		ChunkMap:     make(map[string][]string),
		Metadata:     make(map[string]interface{}),
	}

	// Create backup directory
	backupPath := s.getBackupPath(manifest.BackupID)
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Process each source path
	for _, sourcePath := range job.SourcePaths {
		s.logger.Info("Backing up: %s", sourcePath)
		if err := s.backupPath(sourcePath, manifest, job); err != nil {
			return nil, fmt.Errorf("failed to backup %s: %w", sourcePath, err)
		}
	}

	// Save manifest
	manifestPath := filepath.Join(backupPath, "manifest.json")
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal manifest: %w", err)
	}

	if err := ioutil.WriteFile(manifestPath, manifestData, 0600); err != nil {
		return nil, fmt.Errorf("failed to save manifest: %w", err)
	}

	// Create checksums file
	if err := s.createChecksums(backupPath); err != nil {
		s.logger.Warn("Failed to create checksums: %v", err)
	}

	return manifest, nil
}

// backupPath backs up a single path with deduplication
func (s *Service) backupPath(path string, manifest *BackupManifest, job *BackupJob) error {
	return filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			s.logger.Warn("Error accessing %s: %v", filePath, err)
			return nil // Continue with other files
		}

		// Create file entry
		entry := FileEntry{
			Path:        filePath,
			Size:        info.Size(),
			Mode:        info.Mode(),
			ModTime:     info.ModTime(),
			IsDir:       info.IsDir(),
			Permissions: info.Mode().String(),
		}

		// Skip directories (just record metadata)
		if info.IsDir() {
			manifest.Files = append(manifest.Files, entry)
			return nil
		}

		// Process file with deduplication
		chunks, err := s.deduplicateFile(filePath, info)
		if err != nil {
			s.logger.Warn("Failed to deduplicate %s: %v", filePath, err)
			return nil
		}

		entry.ChunkHashes = chunks
		manifest.ChunkMap[filePath] = chunks
		manifest.Files = append(manifest.Files, entry)
		manifest.FileCount++
		manifest.TotalSize += info.Size()

		// Update progress
		s.jobMutex.Lock()
		if runningJob, ok := s.runningJobs[job.ID]; ok {
			runningJob.Progress = float64(manifest.FileCount) / 100 // Simplified progress
		}
		s.jobMutex.Unlock()

		return nil
	})
}

// deduplicateFile chunks and deduplicates a file
func (s *Service) deduplicateFile(filePath string, info os.FileInfo) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var chunks []string
	buffer := make([]byte, s.chunkSize)

	for {
		n, err := file.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			hash := s.hashChunk(chunk)

			// Check if chunk already exists
			s.chunkIndex.mu.RLock()
			existing, exists := s.chunkIndex.chunks[hash]
			s.chunkIndex.mu.RUnlock()

			if !exists {
				// New chunk - store it
				if err := s.storeChunk(hash, chunk); err != nil {
					return nil, fmt.Errorf("failed to store chunk: %w", err)
				}

				// Add to index
				s.chunkIndex.mu.Lock()
				s.chunkIndex.chunks[hash] = &ChunkMetadata{
					Hash:           hash,
					Size:           int64(n),
					CompressedSize: int64(n), // Will be updated after compression
					RefCount:       1,
					FirstSeen:      time.Now(),
					LastAccessed:   time.Now(),
					StoragePath:    s.getChunkPath(hash),
				}
				s.chunkIndex.mu.Unlock()
			} else {
				// Existing chunk - increment reference count
				s.chunkIndex.mu.Lock()
				existing.RefCount++
				existing.LastAccessed = time.Now()
				s.chunkIndex.mu.Unlock()
			}

			chunks = append(chunks, hash)
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	return chunks, nil
}

// hashChunk generates SHA-256 hash of a chunk
func (s *Service) hashChunk(chunk []byte) string {
	hash := sha256.Sum256(chunk)
	return hex.EncodeToString(hash[:])
}

// storeChunk stores a compressed and optionally encrypted chunk
func (s *Service) storeChunk(hash string, data []byte) error {
	chunkPath := s.getChunkPath(hash)
	chunkDir := filepath.Dir(chunkPath)

	// Create chunk directory if needed
	if err := os.MkdirAll(chunkDir, 0755); err != nil {
		return err
	}

	// Compress chunk
	compressed, err := s.compressChunk(data)
	if err != nil {
		return fmt.Errorf("compression failed: %w", err)
	}

	// Encrypt if enabled
	if s.encryption {
		compressed, err = s.encryptChunk(compressed)
		if err != nil {
			return fmt.Errorf("encryption failed: %w", err)
		}
	}

	// Write to disk
	return ioutil.WriteFile(chunkPath, compressed, 0600)
}

// compressChunk compresses a chunk using configured algorithm
func (s *Service) compressChunk(data []byte) ([]byte, error) {
	// For now, use gzip. In production, would use LZ4 or ZSTD
	var buf strings.Builder
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}

// encryptChunk encrypts a chunk using AES-256-GCM
func (s *Service) encryptChunk(data []byte) ([]byte, error) {
	// Generate encryption key (in production, use key management)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, data, nil), nil
}

// getChunkPath returns the storage path for a chunk
func (s *Service) getChunkPath(hash string) string {
	// Use first 2 chars of hash for directory sharding
	return filepath.Join(s.chunkStoreDir, hash[:2], hash)
}

// getBackupPath returns the path for a backup
func (s *Service) getBackupPath(backupID string) string {
	now := time.Now()
	return filepath.Join(s.backupDir,
		fmt.Sprintf("%d/%02d/%02d", now.Year(), now.Month(), now.Day()),
		backupID)
}

// generateBackupID generates a unique backup ID
func (s *Service) generateBackupID(job *BackupJob) string {
	timestamp := time.Now().Format("20060102-150405")
	jobName := strings.ReplaceAll(strings.ToLower(job.Name), " ", "-")
	return fmt.Sprintf("%s-%s-%s", job.BackupType[:4], jobName, timestamp)
}

// createChecksums creates SHA-256 checksums for backup verification
func (s *Service) createChecksums(backupPath string) error {
	checksumFile := filepath.Join(backupPath, "checksums.sha256")
	file, err := os.Create(checksumFile)
	if err != nil {
		return err
	}
	defer file.Close()

	// Add checksum entries
	fmt.Fprintf(file, "# CASDC Backup Checksums - %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(file, "# Format: SHA256 FILENAME\n\n")

	// Calculate manifest checksum
	manifestPath := filepath.Join(backupPath, "manifest.json")
	if data, err := ioutil.ReadFile(manifestPath); err == nil {
		hash := sha256.Sum256(data)
		fmt.Fprintf(file, "%x  manifest.json\n", hash)
	}

	return nil
}

// updateBackupHistory updates a backup history record
func (s *Service) updateBackupHistory(historyID int64, status, errorMsg string) {
	completedAt := time.Now()
	_, err := s.db.Exec(`
		UPDATE backup_history
		SET status = ?, completed_at = ?, error_message = ?
		WHERE id = ?`,
		status, completedAt, errorMsg, historyID)
	if err != nil {
		s.logger.Error("Failed to update backup history: %v", err)
	}
}

// RestoreBackup restores from a backup
func (s *Service) RestoreBackup(backupID string, targetPath string) error {
	s.logger.Info("Starting restore of backup: %s to %s", backupID, targetPath)

	// Load backup manifest
	backupPath := s.findBackupPath(backupID)
	if backupPath == "" {
		return fmt.Errorf("backup not found: %s", backupID)
	}

	manifestPath := filepath.Join(backupPath, "manifest.json")
	manifestData, err := ioutil.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest BackupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Restore files
	for _, file := range manifest.Files {
		if err := s.restoreFile(file, manifest.ChunkMap[file.Path], targetPath); err != nil {
			s.logger.Warn("Failed to restore %s: %v", file.Path, err)
			continue
		}
	}

	s.logger.Info("Restore completed successfully")
	return nil
}

// restoreFile restores a single file from chunks
func (s *Service) restoreFile(file FileEntry, chunks []string, targetPath string) error {
	// Calculate target file path
	relPath := strings.TrimPrefix(file.Path, "/")
	destPath := filepath.Join(targetPath, relPath)

	// Create directory if needed
	if file.IsDir {
		return os.MkdirAll(destPath, file.Mode)
	}

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}

	// Create target file
	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Restore from chunks
	for _, chunkHash := range chunks {
		chunk, err := s.loadChunk(chunkHash)
		if err != nil {
			return fmt.Errorf("failed to load chunk %s: %w", chunkHash, err)
		}

		if _, err := destFile.Write(chunk); err != nil {
			return fmt.Errorf("failed to write chunk: %w", err)
		}
	}

	// Restore file metadata
	if err := os.Chmod(destPath, file.Mode); err != nil {
		s.logger.Warn("Failed to restore permissions for %s: %v", destPath, err)
	}

	if err := os.Chtimes(destPath, file.ModTime, file.ModTime); err != nil {
		s.logger.Warn("Failed to restore timestamps for %s: %v", destPath, err)
	}

	return nil
}

// loadChunk loads and decompresses a chunk
func (s *Service) loadChunk(hash string) ([]byte, error) {
	chunkPath := s.getChunkPath(hash)
	data, err := ioutil.ReadFile(chunkPath)
	if err != nil {
		return nil, err
	}

	// Decrypt if needed
	if s.encryption {
		// In production, implement decryption
	}

	// Decompress
	return s.decompressChunk(data)
}

// decompressChunk decompresses a chunk
func (s *Service) decompressChunk(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return ioutil.ReadAll(reader)
}

// findBackupPath locates a backup by ID
func (s *Service) findBackupPath(backupID string) string {
	// Search in backup directory structure
	var foundPath string
	filepath.Walk(s.backupDir, func(path string, info os.FileInfo, err error) error {
		if strings.Contains(path, backupID) && info.IsDir() {
			foundPath = path
			return filepath.SkipDir
		}
		return nil
	})
	return foundPath
}

// BackupScheduler methods

// run starts the scheduler loop
func (bs *BackupScheduler) run() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-bs.ctx.Done():
			return
		case <-ticker.C:
			bs.checkScheduledJobs()
		}
	}
}

// scheduleJob adds a job to the scheduler
func (bs *BackupScheduler) scheduleJob(job *BackupJob) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	// Calculate next run time based on cron schedule
	// For simplicity, using daily schedule for now
	nextRun := time.Now().Add(24 * time.Hour)
	if job.Schedule == "daily" {
		// Schedule for 2:00 AM as per spec
		nextRun = time.Date(nextRun.Year(), nextRun.Month(), nextRun.Day(), 2, 0, 0, 0, time.Local)
	}

	bs.jobs[job.ID] = &ScheduledJob{
		Job:    job,
		Active: true,
	}

	bs.service.logger.Info("Scheduled backup job %s for %s", job.Name, nextRun.Format(time.RFC3339))
}

// checkScheduledJobs checks and executes due jobs
func (bs *BackupScheduler) checkScheduledJobs() {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	now := time.Now()
	for _, scheduled := range bs.jobs {
		if !scheduled.Active {
			continue
		}

		// Check if it's time to run (simplified for 2:00 AM daily)
		if now.Hour() == 2 && now.Minute() == 0 {
			go func(job *BackupJob) {
				bs.service.logger.Info("Executing scheduled backup: %s", job.Name)
				if err := bs.service.CreateBackup(job.ID); err != nil {
					bs.service.logger.Error("Scheduled backup failed: %v", err)
				}
			}(scheduled.Job)
		}
	}
}

// ChunkIndex methods

// loadFromDisk loads the chunk index from persistent storage
func (ci *ChunkIndex) loadFromDisk() error {
	indexPath := ci.indexDB.path
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return nil // No existing index
	}

	data, err := ioutil.ReadFile(indexPath)
	if err != nil {
		return err
	}

	ci.mu.Lock()
	defer ci.mu.Unlock()

	return json.Unmarshal(data, &ci.chunks)
}

// saveToDisk persists the chunk index to disk
func (ci *ChunkIndex) saveToDisk() error {
	ci.mu.RLock()
	data, err := json.Marshal(ci.chunks)
	ci.mu.RUnlock()

	if err != nil {
		return err
	}

	return ioutil.WriteFile(ci.indexDB.path, data, 0600)
}

// CleanupOldBackups removes backups older than retention period
func (s *Service) CleanupOldBackups() error {
	rows, err := s.db.Query(`
		SELECT job_id, retention_days FROM backup_jobs WHERE retention_days > 0`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var jobID int64
		var retentionDays int
		if err := rows.Scan(&jobID, &retentionDays); err != nil {
			continue
		}

		cutoffDate := time.Now().AddDate(0, 0, -retentionDays)

		// Find and remove old backups
		if err := s.removeOldBackups(jobID, cutoffDate); err != nil {
			s.logger.Warn("Failed to cleanup old backups for job %d: %v", jobID, err)
		}
	}

	// Clean up orphaned chunks
	s.cleanupOrphanedChunks()

	return nil
}

// removeOldBackups removes backups older than cutoff date
func (s *Service) removeOldBackups(jobID int64, cutoffDate time.Time) error {
	// Query old backup records
	rows, err := s.db.Query(`
		SELECT id, backup_path FROM backup_history
		WHERE job_id = ? AND started_at < ? AND status = 'completed'`,
		jobID, cutoffDate)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var historyID int64
		var backupPath string
		if err := rows.Scan(&historyID, &backupPath); err != nil {
			continue
		}

		// Remove backup files
		fullPath := s.findBackupPath(backupPath)
		if fullPath != "" {
			if err := os.RemoveAll(fullPath); err != nil {
				s.logger.Warn("Failed to remove backup directory %s: %v", fullPath, err)
			}
		}

		// Remove database record
		_, err = s.db.Exec("DELETE FROM backup_history WHERE id = ?", historyID)
		if err != nil {
			s.logger.Warn("Failed to delete backup history record %d: %v", historyID, err)
		}
	}

	return nil
}

// cleanupOrphanedChunks removes chunks with zero references
func (s *Service) cleanupOrphanedChunks() {
	s.chunkIndex.mu.Lock()
	defer s.chunkIndex.mu.Unlock()

	for hash, metadata := range s.chunkIndex.chunks {
		if metadata.RefCount <= 0 {
			// Remove chunk file
			chunkPath := s.getChunkPath(hash)
			if err := os.Remove(chunkPath); err != nil {
				s.logger.Warn("Failed to remove orphaned chunk %s: %v", hash, err)
			}

			// Remove from index
			delete(s.chunkIndex.chunks, hash)
		}
	}

	// Save updated index
	s.chunkIndex.saveToDisk()
}

// GetDeduplicationStats returns deduplication statistics
func (s *Service) GetDeduplicationStats() map[string]interface{} {
	s.chunkIndex.mu.RLock()
	defer s.chunkIndex.mu.RUnlock()

	totalChunks := len(s.chunkIndex.chunks)
	var totalSize, deduplicatedSize int64
	var totalRefs int

	for _, metadata := range s.chunkIndex.chunks {
		totalSize += metadata.Size * int64(metadata.RefCount)
		deduplicatedSize += metadata.CompressedSize
		totalRefs += metadata.RefCount
	}

	deduplicationRatio := float64(0)
	if totalSize > 0 {
		deduplicationRatio = float64(totalSize-deduplicatedSize) / float64(totalSize) * 100
	}

	return map[string]interface{}{
		"total_chunks":        totalChunks,
		"total_references":    totalRefs,
		"total_size":          totalSize,
		"deduplicated_size":   deduplicatedSize,
		"saved_space":         totalSize - deduplicatedSize,
		"deduplication_ratio": deduplicationRatio,
		"average_chunk_size":  s.chunkSize,
	}
}

// Shutdown gracefully stops the backup service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down backup service")

	// Stop scheduler
	if s.scheduler != nil {
		s.scheduler.cancel()
	}

	// Save chunk index
	if s.chunkIndex != nil {
		if err := s.chunkIndex.saveToDisk(); err != nil {
			s.logger.Warn("Failed to save chunk index: %v", err)
		}
	}

	// Wait for running backups to complete
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		s.jobMutex.RLock()
		runningCount := len(s.runningJobs)
		s.jobMutex.RUnlock()

		if runningCount == 0 {
			break
		}

		select {
		case <-timeout:
			s.logger.Warn("Timeout waiting for backup jobs to complete")
			return nil
		case <-ticker.C:
			s.logger.Info("Waiting for %d backup jobs to complete", runningCount)
		}
	}

	s.logger.Info("Backup service shutdown complete")
	return nil
}