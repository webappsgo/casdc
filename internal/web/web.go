// Package web provides the web interface for CASDC
package web

import (
	"context"
	"embed"
	"fmt"
	"net/http"

	"github.com/casapps/casdc/internal/api"
	"github.com/casapps/casdc/internal/auth"
	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/pkg/logger"
)

// Server represents the web server
type Server struct {
	assets   embed.FS
	api      *api.Server
	auth     *auth.Service
	config   *config.Config
	logger   *logger.Logger
	httpSrv  *http.Server
	httpsSrv *http.Server
}

// NewServer creates a new web server with embedded assets
func NewServer(
	assets embed.FS,
	apiServer *api.Server,
	authService *auth.Service,
	cfg *config.Config,
	log *logger.Logger,
) *Server {
	return &Server{
		assets: assets,
		api:    apiServer,
		auth:   authService,
		config: cfg,
		logger: log,
	}
}

// Start starts the web server on specified ports
func (s *Server) Start(ctx context.Context, httpPort, httpsPort int) error {
	// Configure HTTP server for redirect to HTTPS
	s.httpSrv = &http.Server{
		Addr:    fmt.Sprintf(":%d", httpPort),
		Handler: http.HandlerFunc(s.redirectToHTTPS),
	}

	// Configure HTTPS server
	mux := http.NewServeMux()
	s.setupRoutes(mux)

	s.httpsSrv = &http.Server{
		Addr:    fmt.Sprintf(":%d", httpsPort),
		Handler: mux,
	}

	// Start HTTP server in goroutine
	go func() {
		if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTP server error: %v", err)
		}
	}()

	// Start HTTPS server
	go func() {
		// For development, use self-signed certificate
		// In production, use Let's Encrypt or provided certificates
		if err := s.httpsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			s.logger.Error("HTTPS server error: %v", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the web server
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down web servers")

	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			return err
		}
	}

	if s.httpsSrv != nil {
		if err := s.httpsSrv.Shutdown(ctx); err != nil {
			return err
		}
	}

	return nil
}

// setupRoutes configures all HTTP routes
func (s *Server) setupRoutes(mux *http.ServeMux) {
	// Use the comprehensive routing system
	s.setupComprehensiveRoutes(mux)
}

// redirectToHTTPS redirects HTTP requests to HTTPS
func (s *Server) redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	target := fmt.Sprintf("https://%s%s", r.Host, r.URL.Path)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

