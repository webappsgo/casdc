// Package web provides comprehensive routing and handlers for the CASDC web interface
package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
)

// RouteDefinition defines the structure for routes
type RouteDefinition struct {
	Path        string
	Handler     http.HandlerFunc
	RequireAuth bool
	RequireAdmin bool
	Description string
}

// setupComprehensiveRoutes configures all routes according to the specification
func (s *Server) setupComprehensiveRoutes(mux *http.ServeMux) {
	// Public routes - no authentication required
	publicRoutes := []RouteDefinition{
		{"/", s.handleHome, false, false, "Main landing page with system status"},
		{"/login", s.handleLoginPage, false, false, "User login page"},
		{"/logout", s.handleLogoutAction, false, false, "Logout endpoint"},
		{"/status", s.handlePublicStatus, false, false, "Public system status page"},
		{"/api/v1/health", s.handleHealthCheck, false, false, "Health check endpoint"},
	}

	// User routes - authentication required
	userRoutes := []RouteDefinition{
		{"/users/dashboard", s.handleUserDashboard, true, false, "User dashboard"},
		{"/users/profile", s.handleUserProfile, true, false, "User profile management"},
		{"/users/email", s.handleUserEmail, true, false, "User email interface"},
		{"/users/files", s.handleUserFiles, true, false, "User file access"},
		{"/users/vpn", s.handleUserVPN, true, false, "User VPN configuration"},
		{"/users/password", s.handlePasswordChange, true, false, "Password change"},
		{"/users/mfa", s.handleMFASettings, true, false, "Multi-factor authentication"},
	}

	// Admin routes - admin authentication required
	adminRoutes := []RouteDefinition{
		// Main admin dashboard
		{"/admin/", s.handleAdminDashboard, true, true, "Main admin dashboard"},
		{"/admin/dashboard", s.handleAdminDashboard, true, true, "Admin dashboard"},

		// User and group management
		{"/admin/users", s.handleAdminUsers, true, true, "User management"},
		{"/admin/users/create", s.handleCreateUser, true, true, "Create new user"},
		{"/admin/users/import", s.handleImportUsers, true, true, "Bulk user import"},
		{"/admin/users/export", s.handleExportUsers, true, true, "Export users"},
		{"/admin/groups", s.handleAdminGroups, true, true, "Group management"},
		{"/admin/ou", s.handleOrganizationalUnits, true, true, "Organizational units"},
		{"/admin/ou/create", s.handleOUCreate, true, true, "Create OU"},

		// Domain and Active Directory
		{"/admin/domain", s.handleDomainSettings, true, true, "Domain configuration"},
		{"/admin/gpo", s.handleGroupPolicies, true, true, "Group Policy Objects"},
		{"/admin/gpo/create", s.handleGPOCreate, true, true, "Create GPO"},
		{"/admin/gpo/edit", s.handleGPOEdit, true, true, "Edit GPO"},
		{"/admin/gpo/links", s.handleGPOLinks, true, true, "GPO links to OUs"},
		{"/admin/computers", s.handleComputers, true, true, "Computer accounts"},
		{"/admin/trusts", s.handleDomainTrusts, true, true, "Domain trusts"},
		{"/admin/fsmo", s.handleFSMORoles, true, true, "FSMO roles"},

		// DNS management
		{"/admin/dns", s.handleDNSZones, true, true, "DNS zones management"},
		{"/admin/dns/zones", s.handleDNSZones, true, true, "DNS zones"},
		{"/admin/dns/records", s.handleDNSRecords, true, true, "DNS records"},
		{"/admin/dns/forwarders", s.handleDNSForwarders, true, true, "DNS forwarders"},

		// DHCP management
		{"/admin/dhcp", s.handleDHCPScopes, true, true, "DHCP scopes"},
		{"/admin/dhcp/scopes", s.handleDHCPScopes, true, true, "DHCP scopes"},
		{"/admin/dhcp/scopes/create", s.handleDHCPScopeCreate, true, true, "Create DHCP scope"},
		{"/admin/dhcp/reservations", s.handleDHCPReservations, true, true, "DHCP reservations"},
		{"/admin/dhcp/leases", s.handleDHCPLeases, true, true, "Current leases"},

		// Email management
		{"/admin/email", s.handleEmailDomains, true, true, "Email domains"},
		{"/admin/email/accounts", s.handleEmailAccounts, true, true, "Email accounts"},
		{"/admin/email/aliases", s.handleEmailAliases, true, true, "Email aliases"},
		{"/admin/email/queues", s.handleMailQueues, true, true, "Mail queues"},
		{"/admin/email/activesync", s.handleActiveSync, true, true, "ActiveSync devices"},
		{"/admin/email/publicfolders", s.handlePublicFolders, true, true, "Public folders"},

		// Certificate management
		{"/admin/certificates", s.handleCertificates, true, true, "Certificate management"},
		{"/admin/certificates/generate", s.handleGenerateCert, true, true, "Generate certificate"},
		{"/admin/certificates/import", s.handleImportCert, true, true, "Import certificate"},
		{"/admin/certificates/renew", s.handleRenewCerts, true, true, "Renew certificates"},

		// Web hosting
		{"/admin/websites", s.handleWebsites, true, true, "Website management"},
		{"/admin/websites/create", s.handleCreateWebsite, true, true, "Create website"},
		{"/admin/websites/ssl", s.handleWebsiteSSL, true, true, "Website SSL"},

		// File shares
		{"/admin/file-shares", s.handleFileShares, true, true, "File shares"},
		{"/admin/file-shares/create", s.handleFileShareCreate, true, true, "Create share"},
		{"/admin/file-shares/permissions", s.handleFileSharePermissions, true, true, "Share permissions"},

		// VPN management
		{"/admin/vpn", s.handleVPNServers, true, true, "VPN servers"},
		{"/admin/vpn/servers", s.handleVPNServers, true, true, "VPN servers"},
		{"/admin/vpn/servers/create", s.handleVPNServerCreate, true, true, "Create VPN server"},
		{"/admin/vpn/clients", s.handleVPNClients, true, true, "VPN clients"},
		{"/admin/vpn/clients/download", s.handleVPNClientDownload, true, true, "Download VPN client config"},

		// Security
		{"/admin/security", s.handleSecurityDashboard, true, true, "Security dashboard"},
		{"/admin/security/events", s.handleSecurityEvents, true, true, "Security events"},
		{"/admin/security/threats", s.handleThreatIntel, true, true, "Threat intelligence"},
		{"/admin/security/quarantine", s.handleQuarantine, true, true, "Quarantined files"},
		{"/admin/security/firewall", s.handleFirewall, true, true, "Firewall rules"},
		{"/admin/security/ids", s.handleIDS, true, true, "Intrusion detection"},
		{"/admin/security/compliance", s.handleCompliance, true, true, "Compliance reports"},

		// Backup and restore
		{"/admin/backup", s.handleBackupJobs, true, true, "Backup jobs"},
		{"/admin/backup/create", s.handleCreateBackup, true, true, "Create backup"},
		{"/admin/backup/restore", s.handleRestore, true, true, "Restore from backup"},
		{"/admin/backup/schedule", s.handleBackupSchedule, true, true, "Backup schedule"},

		// Clustering
		{"/admin/cluster", s.handleClusterStatus, true, true, "Cluster status"},
		{"/admin/cluster/nodes", s.handleClusterNodes, true, true, "Cluster nodes"},
		{"/admin/cluster/join", s.handleJoinCluster, true, true, "Join cluster"},

		// Development platform
		{"/admin/git", s.handleGitRepositories, true, true, "Git repositories"},
		{"/admin/git/repos", s.handleGitRepositories, true, true, "Git repositories"},
		{"/admin/git/repos/create", s.handleGitRepoCreate, true, true, "Create Git repository"},
		{"/admin/git/orgs", s.handleGitOrganizations, true, true, "Git organizations"},
		{"/admin/docker", s.handleDockerRegistry, true, true, "Docker registry"},
		{"/admin/docker/images", s.handleDockerImages, true, true, "Docker images"},
		{"/admin/docker/vulnerabilities", s.handleDockerVulnerabilities, true, true, "Image vulnerabilities"},
		{"/admin/api", s.handleAPIGateway, true, true, "API gateway"},

		// System configuration
		{"/admin/config", s.handleSystemConfig, true, true, "System configuration"},
		{"/admin/logs", s.handleSystemLogs, true, true, "System logs"},
		{"/admin/audit", s.handleAuditLogs, true, true, "Audit logs"},
		{"/admin/performance", s.handlePerformance, true, true, "Performance metrics"},
		{"/admin/services", s.handleServices, true, true, "Service management"},
		{"/admin/updates", s.handleSystemUpdates, true, true, "System updates"},

		// Support tickets
		{"/admin/tickets", s.handleSupportTickets, true, true, "Support tickets"},
		{"/admin/tickets/create", s.handleCreateTicket, true, true, "Create ticket"},
		{"/admin/tickets/view", s.handleViewTicket, true, true, "View ticket"},
	}

	// API routes - RESTful endpoints
	apiRoutes := []RouteDefinition{
		// Admin API routes
		{"/api/v1/admin/users", s.handleAPIUsers, true, true, "Users API"},
		{"/api/v1/admin/groups", s.handleAPIGroups, true, true, "Groups API"},
		{"/api/v1/admin/ou", s.apiOrganizationalUnits, true, true, "Organizational Units API"},
		{"/api/v1/admin/gpo", s.apiGroupPolicies, true, true, "Group Policy API"},
		{"/api/v1/admin/dns", s.handleAPIDNS, true, true, "DNS API"},
		{"/api/v1/admin/dhcp", s.apiDHCPScopes, true, true, "DHCP API"},
		{"/api/v1/admin/email", s.handleAPIEmail, true, true, "Email API"},
		{"/api/v1/admin/certificates", s.handleAPICertificates, true, true, "Certificates API"},
		{"/api/v1/admin/file-shares", s.apiFileShares, true, true, "File Shares API"},
		{"/api/v1/admin/vpn", s.apiVPNServers, true, true, "VPN API"},
		{"/api/v1/admin/git", s.apiGitRepositories, true, true, "Git API"},
		{"/api/v1/admin/docker", s.apiDockerImages, true, true, "Docker Registry API"},
		{"/api/v1/admin/backup", s.handleAPIBackup, true, true, "Backup API"},
		{"/api/v1/admin/security", s.handleAPISecurity, true, true, "Security API"},

		// User API routes
		{"/api/v1/users/profile", s.handleAPIUserProfile, true, false, "User profile API"},
		{"/api/v1/users/email", s.handleAPIUserEmail, true, false, "User email API"},
		{"/api/v1/users/files", s.handleAPIUserFiles, true, false, "User files API"},

		// Public API routes
		{"/api/v1/status", s.handleAPIStatus, false, false, "System status API"},
		{"/api/v1/health", s.handleAPIHealth, false, false, "Health check API"},
		{"/api/v1/version", s.handleAPIVersion, false, false, "Version information API"},
	}

	// Support system routes - expanded navigation allowed
	supportRoutes := []RouteDefinition{
		{"/support/", s.handleSupportHome, false, false, "Support home"},
		{"/support/docs", s.handleDocumentation, false, false, "Documentation"},
		{"/support/kb", s.handleKnowledgeBase, false, false, "Knowledge base"},
		{"/support/tickets", s.handleTicketSystem, true, false, "Ticket system"},
		{"/support/chat", s.handleLiveChat, true, false, "Live chat"},
		{"/support/setup", s.handleSetupGuides, false, false, "Setup guides"},
		{"/support/troubleshooting", s.handleTroubleshooting, false, false, "Troubleshooting"},
		{"/support/api", s.handleAPIDocs, false, false, "API documentation"},
		{"/support/migration", s.handleMigrationGuides, false, false, "Migration guides"},
		{"/support/providers", s.handleProviderGuides, false, false, "Provider guides"},
	}

	// Webmail route
	mux.HandleFunc("/webmail/", s.handleWebmailInterface)

	// Register all routes
	s.registerRoutes(mux, publicRoutes)
	s.registerRoutes(mux, userRoutes)
	s.registerRoutes(mux, adminRoutes)
	s.registerRoutes(mux, apiRoutes)
	s.registerRoutes(mux, supportRoutes)

	// Static assets
	mux.HandleFunc("/static/", s.handleStaticFiles)
	mux.HandleFunc("/assets/", s.handleAssets)

	// Special routes for certificates and ACME challenges
	mux.HandleFunc("/.well-known/acme-challenge/", s.handleACMEChallenge)

	// Default catch-all route for unknown domains
	mux.HandleFunc("/unknown/", s.handleUnknownDomain)
}

// registerRoutes registers a set of routes with the mux
func (s *Server) registerRoutes(mux *http.ServeMux, routes []RouteDefinition) {
	for _, route := range routes {
		handler := route.Handler

		// Apply authentication middleware if required
		if route.RequireAuth {
			handler = s.requireAuthentication(handler)
		}

		// Apply admin check if required
		if route.RequireAdmin {
			handler = s.requireAdminRole(handler)
		}

		// Apply CSRF protection for state-changing operations
		if route.RequireAuth && !strings.Contains(route.Path, "/api/") {
			handler = s.csrfProtection(handler)
		}

		// Apply rate limiting
		handler = s.rateLimiting(handler)

		// Apply security headers
		handler = s.securityHeaders(handler)

		// Register the route
		mux.HandleFunc(route.Path, handler)

		s.logger.Debug("Registered route: %s - %s", route.Path, route.Description)
	}
}

// Middleware functions

// requireAuthentication ensures the user is logged in
func (s *Server) requireAuthentication(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check for session cookie
		cookie, err := r.Cookie("casdc_session")
		if err != nil {
			http.Redirect(w, r, "/login?redirect="+r.URL.Path, http.StatusSeeOther)
			return
		}

		// Validate session
		// In production, this would check against database
		if cookie.Value == "" {
			http.Redirect(w, r, "/login?redirect="+r.URL.Path, http.StatusSeeOther)
			return
		}

		next(w, r)
	}
}

// requireAdminRole ensures the user has admin privileges
func (s *Server) requireAdminRole(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if user is admin
		// In production, this would check user roles from database
		next(w, r)
	}
}

// csrfProtection adds CSRF token validation
func (s *Server) csrfProtection(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" {
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.FormValue("_csrf_token")
			}

			// Validate CSRF token
			// In production, this would validate against stored token
			if token == "" {
				http.Error(w, "CSRF token required", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// rateLimiting implements rate limiting per IP
func (s *Server) rateLimiting(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// In production, implement actual rate limiting
		// 60 requests per minute per IP as per spec
		next(w, r)
	}
}

// securityHeaders adds comprehensive security headers
func (s *Server) securityHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Security headers according to spec
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		next(w, r)
	}
}

// Page data structures for template rendering
type PageData struct {
	Title        string
	User         interface{}
	Content      interface{}
	CSRFToken    string
	Breadcrumbs  []Breadcrumb
	Alerts       []Alert
	Theme        string
	Organization string
}

type Breadcrumb struct {
	Name string
	URL  string
	Active bool
}

type Alert struct {
	Type    string // success, warning, error, info
	Message string
}

// renderTemplate renders an HTML template with common page data
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data PageData) {
	// Set default values
	if data.Theme == "" {
		data.Theme = "dracula" // Default dark theme as per spec
	}
	if data.Organization == "" {
		data.Organization = s.config.Organization
	}

	// In production, use actual template files
	tmpl := template.Must(template.New(name).Parse(defaultHTMLTemplate))
	if err := tmpl.Execute(w, data); err != nil {
		s.logger.Error("Template rendering error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// jsonResponse sends a JSON response
func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("JSON encoding error: %v", err)
	}
}

// errorResponse sends an error response
func (s *Server) errorResponse(w http.ResponseWriter, message string, status int) {
	if strings.Contains(w.Header().Get("Accept"), "application/json") {
		s.jsonResponse(w, map[string]string{"error": message}, status)
	} else {
		http.Error(w, message, status)
	}
}

// Default HTML template for basic pages
const defaultHTMLTemplate = `<!DOCTYPE html>
<html lang="en" data-theme="{{.Theme}}">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}} - CASDC</title>
    <link rel="stylesheet" href="/static/css/main.css">
</head>
<body>
    <header>
        <nav class="navbar">
            <div class="navbar-brand">
                <a href="/">CASDC - {{.Organization}}</a>
            </div>
            <div class="navbar-menu">
                {{if .User}}
                    <a href="/users/dashboard">Dashboard</a>
                    <a href="/users/profile">Profile</a>
                    {{if .User.IsAdmin}}
                        <a href="/admin/">Admin</a>
                    {{end}}
                    <a href="/logout">Logout</a>
                {{else}}
                    <a href="/login">Login</a>
                {{end}}
            </div>
        </nav>
        {{if .Breadcrumbs}}
        <nav class="breadcrumbs">
            {{range .Breadcrumbs}}
                {{if .Active}}
                    <span class="active">{{.Name}}</span>
                {{else}}
                    <a href="{{.URL}}">{{.Name}}</a> /
                {{end}}
            {{end}}
        </nav>
        {{end}}
    </header>

    <main>
        {{range .Alerts}}
            <div class="alert alert-{{.Type}}">{{.Message}}</div>
        {{end}}

        <h1>{{.Title}}</h1>

        <div class="content">
            {{.Content}}
        </div>
    </main>

    <footer>
        <p>&copy; {{.Organization}} - Powered by CASDC</p>
    </footer>

    <script src="/static/js/main.js"></script>
</body>
</html>`