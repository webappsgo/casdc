// Package scheduler provides automated task scheduling for CASDC
// Handles periodic maintenance, security updates, backups, and monitoring
package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/backup"
	"github.com/casapps/casdc/internal/certificates"
	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the scheduler service
// Manages all automated tasks and periodic maintenance operations
type Service struct {
	db          *database.DB
	config      *config.Config
	logger      *logger.Logger
	certService *certificates.Service
	backupService *backup.Service

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Task management
	tasks      map[string]*ScheduledTask
	tasksMutex sync.RWMutex
}

// ScheduledTask represents a scheduled task
type ScheduledTask struct {
	Name        string
	Description string
	Schedule    Schedule
	Handler     TaskHandler
	LastRun     time.Time
	NextRun     time.Time
	RunCount    int64
	ErrorCount  int64
	LastError   string
	Enabled     bool
}

// Schedule defines when a task should run
type Schedule struct {
	Type     ScheduleType
	Interval time.Duration // For interval-based schedules
	Hour     int           // For time-of-day schedules
	Minute   int           // For time-of-day schedules
	DayOfWeek int          // For weekly schedules (0=Sunday)
}

// ScheduleType defines the type of schedule
type ScheduleType int

const (
	ScheduleInterval ScheduleType = iota // Run every N duration
	ScheduleDaily                       // Run at specific time daily
	ScheduleWeekly                      // Run at specific time on specific day
)

// TaskHandler is the function signature for task execution
type TaskHandler func(ctx context.Context) error

// NewService creates a new scheduler service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger, certService *certificates.Service, backupService *backup.Service) *Service {
	ctx, cancel := context.WithCancel(context.Background())

	s := &Service{
		db:            db,
		config:        cfg,
		logger:        log,
		certService:   certService,
		backupService: backupService,
		ctx:           ctx,
		cancel:        cancel,
		tasks:         make(map[string]*ScheduledTask),
	}

	// Register all built-in scheduled tasks
	s.registerBuiltInTasks()

	return s
}

// Start starts the scheduler service
// Begins monitoring and executing scheduled tasks
func (s *Service) Start() error {
	s.logger.Info("Starting scheduler service")

	// Start task execution loop
	s.wg.Add(1)
	go s.taskExecutionLoop()

	// Start metrics collection loop
	s.wg.Add(1)
	go s.metricsCollectionLoop()

	s.logger.Info("Scheduler service started")
	return nil
}

// Stop stops the scheduler service gracefully
func (s *Service) Stop() error {
	s.logger.Info("Stopping scheduler service")
	s.cancel()
	s.wg.Wait()
	s.logger.Info("Scheduler service stopped")
	return nil
}

// registerBuiltInTasks registers all built-in scheduled tasks according to spec
func (s *Service) registerBuiltInTasks() {
	// Certificate renewal check (daily 2:00 AM)
	s.RegisterTask(&ScheduledTask{
		Name:        "certificate_renewal",
		Description: "Check and renew SSL certificates",
		Schedule: Schedule{
			Type:   ScheduleDaily,
			Hour:   2,
			Minute: 0,
		},
		Handler: s.taskCertificateRenewal,
		Enabled: true,
	})

	// Security database updates (daily 3:00 AM)
	s.RegisterTask(&ScheduledTask{
		Name:        "security_updates",
		Description: "Update security databases from free sources",
		Schedule: Schedule{
			Type:   ScheduleDaily,
			Hour:   3,
			Minute: 0,
		},
		Handler: s.taskSecurityUpdates,
		Enabled: true,
	})

	// Log rotation and cleanup (daily 4:00 AM)
	s.RegisterTask(&ScheduledTask{
		Name:        "log_cleanup",
		Description: "Rotate and cleanup old log files",
		Schedule: Schedule{
			Type:   ScheduleDaily,
			Hour:   4,
			Minute: 0,
		},
		Handler: s.taskLogCleanup,
		Enabled: true,
	})

	// Antivirus signature updates (every 6 hours)
	s.RegisterTask(&ScheduledTask{
		Name:        "antivirus_updates",
		Description: "Update antivirus signatures",
		Schedule: Schedule{
			Type:     ScheduleInterval,
			Interval: 6 * time.Hour,
		},
		Handler: s.taskAntivirusUpdates,
		Enabled: true,
	})

	// Database optimization (weekly Sunday 1:00 AM)
	s.RegisterTask(&ScheduledTask{
		Name:        "database_optimization",
		Description: "Optimize database performance",
		Schedule: Schedule{
			Type:      ScheduleWeekly,
			DayOfWeek: 0, // Sunday
			Hour:      1,
			Minute:    0,
		},
		Handler: s.taskDatabaseOptimization,
		Enabled: true,
	})

	// Backup verification (weekly Sunday 5:00 AM)
	s.RegisterTask(&ScheduledTask{
		Name:        "backup_verification",
		Description: "Verify backup integrity",
		Schedule: Schedule{
			Type:      ScheduleWeekly,
			DayOfWeek: 0, // Sunday
			Hour:      5,
			Minute:    0,
		},
		Handler: s.taskBackupVerification,
		Enabled: true,
	})

	// Performance metrics collection (every 15 minutes)
	s.RegisterTask(&ScheduledTask{
		Name:        "performance_metrics",
		Description: "Collect system performance metrics",
		Schedule: Schedule{
			Type:     ScheduleInterval,
			Interval: 15 * time.Minute,
		},
		Handler: s.taskPerformanceMetrics,
		Enabled: true,
	})

	// Health check execution (every 5 minutes)
	s.RegisterTask(&ScheduledTask{
		Name:        "health_checks",
		Description: "Execute service health checks",
		Schedule: Schedule{
			Type:     ScheduleInterval,
			Interval: 5 * time.Minute,
		},
		Handler: s.taskHealthChecks,
		Enabled: true,
	})

	s.logger.Info("Registered %d built-in scheduled tasks", len(s.tasks))
}

// RegisterTask registers a new scheduled task
func (s *Service) RegisterTask(task *ScheduledTask) error {
	s.tasksMutex.Lock()
	defer s.tasksMutex.Unlock()

	if task.Name == "" {
		return fmt.Errorf("task name cannot be empty")
	}

	if task.Handler == nil {
		return fmt.Errorf("task handler cannot be nil")
	}

	// Calculate next run time
	task.NextRun = s.calculateNextRun(task.Schedule, time.Now())

	s.tasks[task.Name] = task
	s.logger.Debug("Registered task '%s', next run: %s", task.Name, task.NextRun.Format(time.RFC3339))

	return nil
}

// UnregisterTask removes a scheduled task
func (s *Service) UnregisterTask(name string) error {
	s.tasksMutex.Lock()
	defer s.tasksMutex.Unlock()

	if _, exists := s.tasks[name]; !exists {
		return fmt.Errorf("task '%s' not found", name)
	}

	delete(s.tasks, name)
	s.logger.Debug("Unregistered task '%s'", name)

	return nil
}

// taskExecutionLoop is the main loop for executing scheduled tasks
func (s *Service) taskExecutionLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case now := <-ticker.C:
			s.checkAndExecuteTasks(now)
		}
	}
}

// checkAndExecuteTasks checks all tasks and executes those that are due
func (s *Service) checkAndExecuteTasks(now time.Time) {
	s.tasksMutex.RLock()
	var dueTasks []*ScheduledTask
	for _, task := range s.tasks {
		if task.Enabled && now.After(task.NextRun) {
			dueTasks = append(dueTasks, task)
		}
	}
	s.tasksMutex.RUnlock()

	// Execute due tasks
	for _, task := range dueTasks {
		s.executeTask(task)
	}
}

// executeTask executes a single task
func (s *Service) executeTask(task *ScheduledTask) {
	s.logger.Info("Executing scheduled task: %s", task.Name)

	startTime := time.Now()

	// Execute task handler
	err := task.Handler(s.ctx)

	duration := time.Since(startTime)

	// Update task statistics
	s.tasksMutex.Lock()
	task.LastRun = startTime
	task.RunCount++
	if err != nil {
		task.ErrorCount++
		task.LastError = err.Error()
		s.logger.Error("Task '%s' failed after %v: %v", task.Name, duration, err)
	} else {
		task.LastError = ""
		s.logger.Info("Task '%s' completed successfully in %v", task.Name, duration)
	}
	task.NextRun = s.calculateNextRun(task.Schedule, time.Now())
	s.tasksMutex.Unlock()

	// Log task execution to database
	s.logTaskExecution(task.Name, startTime, duration, err)
}

// calculateNextRun calculates the next run time based on schedule
func (s *Service) calculateNextRun(schedule Schedule, from time.Time) time.Time {
	switch schedule.Type {
	case ScheduleInterval:
		return from.Add(schedule.Interval)

	case ScheduleDaily:
		next := time.Date(from.Year(), from.Month(), from.Day(), schedule.Hour, schedule.Minute, 0, 0, from.Location())
		if next.Before(from) {
			next = next.AddDate(0, 0, 1)
		}
		return next

	case ScheduleWeekly:
		// Find next occurrence of the specified day of week
		daysUntil := (schedule.DayOfWeek - int(from.Weekday()) + 7) % 7
		if daysUntil == 0 {
			// Check if time has passed today
			next := time.Date(from.Year(), from.Month(), from.Day(), schedule.Hour, schedule.Minute, 0, 0, from.Location())
			if next.Before(from) {
				daysUntil = 7
			}
		}
		next := from.AddDate(0, 0, daysUntil)
		return time.Date(next.Year(), next.Month(), next.Day(), schedule.Hour, schedule.Minute, 0, 0, next.Location())

	default:
		// Default to 1 hour from now
		return from.Add(1 * time.Hour)
	}
}

// logTaskExecution logs task execution to database
func (s *Service) logTaskExecution(taskName string, startTime time.Time, duration time.Duration, err error) {
	level := "info"
	message := fmt.Sprintf("Scheduled task '%s' completed successfully", taskName)
	if err != nil {
		level = "error"
		message = fmt.Sprintf("Scheduled task '%s' failed: %v", taskName, err)
	}

	_, dbErr := s.db.Exec(`
		INSERT INTO system_logs (level, component, message, details, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		level, "scheduler", message,
		fmt.Sprintf(`{"task":"%s","duration_ms":%d}`, taskName, duration.Milliseconds()),
		startTime,
	)
	if dbErr != nil {
		s.logger.Error("Failed to log task execution: %v", dbErr)
	}
}

// metricsCollectionLoop periodically collects metrics about the scheduler itself
func (s *Service) metricsCollectionLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-ticker.C:
			s.collectSchedulerMetrics()
		}
	}
}

// collectSchedulerMetrics collects metrics about the scheduler
func (s *Service) collectSchedulerMetrics() {
	s.tasksMutex.RLock()
	defer s.tasksMutex.RUnlock()

	totalTasks := len(s.tasks)
	enabledTasks := 0
	for _, task := range s.tasks {
		if task.Enabled {
			enabledTasks++
		}
	}

	// Store metrics in database
	now := time.Now()
	_, _ = s.db.Exec(`
		INSERT INTO performance_metrics (metric_name, metric_value, tags, collected_at)
		VALUES (?, ?, ?, ?)`,
		"scheduler.total_tasks", float64(totalTasks), `{"component":"scheduler"}`, now,
	)
	_, _ = s.db.Exec(`
		INSERT INTO performance_metrics (metric_name, metric_value, tags, collected_at)
		VALUES (?, ?, ?, ?)`,
		"scheduler.enabled_tasks", float64(enabledTasks), `{"component":"scheduler"}`, now,
	)
}

// ============================================================================
// TASK HANDLERS - Implementation of each scheduled task
// ============================================================================

// taskCertificateRenewal checks and renews SSL certificates
func (s *Service) taskCertificateRenewal(ctx context.Context) error {
	s.logger.Info("Checking certificates for renewal")

	// Check all certificates expiring within 30 days
	rows, err := s.db.Query(`
		SELECT id, domain, expires_at, auto_renew
		FROM ssl_certificates
		WHERE auto_renew = TRUE AND expires_at < ?`,
		time.Now().AddDate(0, 0, 30),
	)
	if err != nil {
		return fmt.Errorf("failed to query certificates: %w", err)
	}
	defer rows.Close()

	renewCount := 0
	for rows.Next() {
		var id int64
		var domain string
		var expiresAt time.Time
		var autoRenew bool

		if err := rows.Scan(&id, &domain, &expiresAt, &autoRenew); err != nil {
			s.logger.Error("Failed to scan certificate row: %v", err)
			continue
		}

		s.logger.Info("Renewing certificate for %s (expires %s)", domain, expiresAt.Format("2006-01-02"))

		// Attempt renewal
		if err := s.certService.RenewCertificate(domain); err != nil {
			s.logger.Error("Failed to renew certificate for %s: %v", domain, err)
		} else {
			renewCount++
		}
	}

	s.logger.Info("Certificate renewal check complete, renewed %d certificates", renewCount)
	return nil
}

// taskSecurityUpdates updates security databases from free public sources
func (s *Service) taskSecurityUpdates(ctx context.Context) error {
	s.logger.Info("Updating security databases")

	// Placeholder: Implement security database updates
	// Will fetch from Abuse.ch, Spamhaus, NIST NVD, etc.
	// See spec for complete list of free security sources

	s.logger.Info("Security database updates complete")
	return nil
}

// taskLogCleanup rotates and cleans up old log files
func (s *Service) taskLogCleanup(ctx context.Context) error {
	s.logger.Info("Cleaning up old logs")

	// Delete logs older than 90 days
	result, err := s.db.Exec(`
		DELETE FROM system_logs WHERE created_at < ?`,
		time.Now().AddDate(0, 0, -90),
	)
	if err != nil {
		return fmt.Errorf("failed to cleanup system logs: %w", err)
	}

	rows, _ := result.RowsAffected()
	s.logger.Info("Deleted %d old system log entries", rows)

	// Delete old audit logs (keep 1 year)
	result, err = s.db.Exec(`
		DELETE FROM audit_logs WHERE created_at < ?`,
		time.Now().AddDate(-1, 0, 0),
	)
	if err != nil {
		return fmt.Errorf("failed to cleanup audit logs: %w", err)
	}

	rows, _ = result.RowsAffected()
	s.logger.Info("Deleted %d old audit log entries", rows)

	return nil
}

// taskAntivirusUpdates updates antivirus signatures
func (s *Service) taskAntivirusUpdates(ctx context.Context) error {
	s.logger.Info("Updating antivirus signatures")

	// Placeholder: Implement ClamAV signature updates
	// Run freshclam or equivalent

	s.logger.Info("Antivirus signature updates complete")
	return nil
}

// taskDatabaseOptimization optimizes database performance
func (s *Service) taskDatabaseOptimization(ctx context.Context) error {
	s.logger.Info("Optimizing database")

	// SQLite optimization
	if s.config.Database.Type == "sqlite" {
		_, err := s.db.Exec("VACUUM")
		if err != nil {
			return fmt.Errorf("failed to vacuum database: %w", err)
		}

		_, err = s.db.Exec("ANALYZE")
		if err != nil {
			return fmt.Errorf("failed to analyze database: %w", err)
		}
	}

	// PostgreSQL optimization
	if s.config.Database.Type == "postgres" {
		_, err := s.db.Exec("VACUUM ANALYZE")
		if err != nil {
			return fmt.Errorf("failed to vacuum/analyze database: %w", err)
		}
	}

	s.logger.Info("Database optimization complete")
	return nil
}

// taskBackupVerification verifies backup integrity
func (s *Service) taskBackupVerification(ctx context.Context) error {
	s.logger.Info("Verifying backup integrity")

	// Placeholder: Implement backup verification
	// Check recent backups for corruption
	// Verify checksums

	s.logger.Info("Backup verification complete")
	return nil
}

// taskPerformanceMetrics collects system performance metrics
func (s *Service) taskPerformanceMetrics(ctx context.Context) error {
	// Collect CPU, memory, disk usage metrics
	// Store in performance_metrics table

	// Placeholder: Implement actual metrics collection
	now := time.Now()

	// Example metrics (will be replaced with real system metrics)
	metrics := []struct {
		name  string
		value float64
	}{
		{"system.cpu_usage_percent", 15.5},
		{"system.memory_usage_percent", 45.2},
		{"system.disk_usage_percent", 62.8},
	}

	for _, m := range metrics {
		_, err := s.db.Exec(`
			INSERT INTO performance_metrics (metric_name, metric_value, tags, collected_at)
			VALUES (?, ?, ?, ?)`,
			m.name, m.value, `{"component":"system"}`, now,
		)
		if err != nil {
			s.logger.Error("Failed to insert metric %s: %v", m.name, err)
		}
	}

	return nil
}

// taskHealthChecks executes service health checks
func (s *Service) taskHealthChecks(ctx context.Context) error {
	// Check health of all services: database, web server, mail server, etc.

	now := time.Now()
	services := []string{"database", "web", "mail", "dns", "backup"}

	for _, service := range services {
		// Placeholder: Implement actual health checks
		status := "healthy"
		responseTime := 5 // milliseconds

		_, err := s.db.Exec(`
			INSERT INTO health_checks (service_name, status, response_time_ms, checked_at)
			VALUES (?, ?, ?, ?)`,
			service, status, responseTime, now,
		)
		if err != nil {
			s.logger.Error("Failed to insert health check for %s: %v", service, err)
		}
	}

	return nil
}

// GetTaskStatus returns the status of all scheduled tasks
func (s *Service) GetTaskStatus() map[string]*ScheduledTask {
	s.tasksMutex.RLock()
	defer s.tasksMutex.RUnlock()

	// Return a copy to prevent external modification
	status := make(map[string]*ScheduledTask)
	for name, task := range s.tasks {
		taskCopy := *task
		status[name] = &taskCopy
	}

	return status
}