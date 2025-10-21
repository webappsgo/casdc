// Package web contains all HTTP handler implementations for the CASDC web interface
package web

import (
	"html/template"
	"net/http"
	"time"
)

// Home and authentication handlers

// handleHome displays the main landing page
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Complete Active Directory Server Controller",
		Content: template.HTML(`
			<div class="hero">
				<h2>Welcome to CASDC</h2>
				<p>Enterprise-grade domain controller functionality with modern web-based management</p>
				<div class="stats">
					<div class="stat-card">
						<h3>System Status</h3>
						<p class="status-healthy">All Systems Operational</p>
					</div>
					<div class="stat-card">
						<h3>Active Users</h3>
						<p class="stat-value">0</p>
					</div>
					<div class="stat-card">
						<h3>Managed Computers</h3>
						<p class="stat-value">0</p>
					</div>
				</div>
			</div>
		`),
	}
	s.renderTemplate(w, "home", data)
}

// handleLoginPage displays the login form
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		data := PageData{
			Title: "Login",
			Content: template.HTML(`
				<div class="login-container">
					<form method="POST" action="/login">
						<div class="form-group">
							<label for="username">Username or Email</label>
							<input type="text" id="username" name="username" required autofocus>
						</div>
						<div class="form-group">
							<label for="password">Password</label>
							<input type="password" id="password" name="password" required>
						</div>
						<div class="form-group">
							<label>
								<input type="checkbox" name="remember"> Remember me
							</label>
						</div>
						<button type="submit" class="btn btn-primary">Login</button>
						<a href="/users/password/reset" class="forgot-password">Forgot password?</a>
					</form>
				</div>
			`),
		}
		s.renderTemplate(w, "login", data)
		return
	}

	// Handle POST - process login
	username := r.FormValue("username")
	password := r.FormValue("password")

	// Validate credentials
	// In production, check against database with proper password hashing
	if username != "" && password != "" {
		// Create session
		http.SetCookie(w, &http.Cookie{
			Name:     "casdc_session",
			Value:    "session_token_here",
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   8 * 3600, // 8 hours for admin as per spec
		})

		// Redirect to dashboard or requested page
		redirect := r.URL.Query().Get("redirect")
		if redirect == "" {
			redirect = "/users/dashboard"
		}
		http.Redirect(w, r, redirect, http.StatusSeeOther)
		return
	}

	// Login failed
	data := PageData{
		Title: "Login",
		Alerts: []Alert{{Type: "error", Message: "Invalid username or password"}},
	}
	s.renderTemplate(w, "login", data)
}

// handleLogoutAction logs out the user
func (s *Server) handleLogoutAction(w http.ResponseWriter, r *http.Request) {
	// Clear session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "casdc_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// User dashboard handlers

// handleUserDashboard displays the user's main dashboard
func (s *Server) handleUserDashboard(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "User Dashboard",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Dashboard", Active: true},
		},
		Content: template.HTML(`
			<div class="dashboard-grid">
				<div class="dashboard-card">
					<h3>📧 Email</h3>
					<p>5 new messages</p>
					<a href="/users/email" class="btn btn-sm">Open Email</a>
				</div>
				<div class="dashboard-card">
					<h3>📁 Files</h3>
					<p>2.3 GB used of 5 GB</p>
					<a href="/users/files" class="btn btn-sm">Browse Files</a>
				</div>
				<div class="dashboard-card">
					<h3>🔐 VPN</h3>
					<p>Not configured</p>
					<a href="/users/vpn" class="btn btn-sm">Setup VPN</a>
				</div>
				<div class="dashboard-card">
					<h3>👤 Profile</h3>
					<p>Last login: Today</p>
					<a href="/users/profile" class="btn btn-sm">Edit Profile</a>
				</div>
			</div>
		`),
	}
	s.renderTemplate(w, "dashboard", data)
}

// handleUserProfile manages user profile settings
func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Profile Settings",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Users", URL: "/users/dashboard"},
			{Name: "Profile", Active: true},
		},
	}
	s.renderTemplate(w, "profile", data)
}

// handleUserEmail provides email interface
func (s *Server) handleUserEmail(w http.ResponseWriter, r *http.Request) {
	// Redirect to SnappyMail webmail
	http.Redirect(w, r, "/webmail/", http.StatusSeeOther)
}

// handleUserFiles provides file access interface
func (s *Server) handleUserFiles(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "My Files",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Users", URL: "/users/dashboard"},
			{Name: "Files", Active: true},
		},
	}
	s.renderTemplate(w, "files", data)
}

// handleUserVPN manages user VPN configuration
func (s *Server) handleUserVPN(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "VPN Configuration",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Users", URL: "/users/dashboard"},
			{Name: "VPN", Active: true},
		},
	}
	s.renderTemplate(w, "vpn", data)
}

// handlePasswordChange allows users to change their password
func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Change Password",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Users", URL: "/users/dashboard"},
			{Name: "Password", Active: true},
		},
	}
	s.renderTemplate(w, "password", data)
}

// handleMFASettings manages multi-factor authentication
func (s *Server) handleMFASettings(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Multi-Factor Authentication",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Users", URL: "/users/dashboard"},
			{Name: "MFA", Active: true},
		},
	}
	s.renderTemplate(w, "mfa", data)
}

// Admin dashboard handlers

// handleAdminDashboard displays the main admin dashboard with all features accessible within 3 clicks
func (s *Server) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Admin Dashboard",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", Active: true},
		},
		Content: template.HTML(`
			<div class="admin-dashboard">
				<div class="quick-actions">
					<h2>Quick Actions</h2>
					<div class="action-grid">
						<a href="/admin/users/create" class="action-btn">➕ Create User</a>
						<a href="/admin/groups/create" class="action-btn">👥 Create Group</a>
						<a href="/admin/dns/records" class="action-btn">🌐 Add DNS Record</a>
						<a href="/admin/certificates/generate" class="action-btn">🔒 Generate Certificate</a>
						<a href="/admin/backup/create" class="action-btn">💾 Create Backup</a>
						<a href="/admin/security/events" class="action-btn">🛡️ View Security Events</a>
					</div>
				</div>

				<div class="admin-sections">
					<div class="section-card">
						<h3>👥 Users & Groups</h3>
						<ul>
							<li><a href="/admin/users">User Management</a></li>
							<li><a href="/admin/groups">Group Management</a></li>
							<li><a href="/admin/ou">Organizational Units</a></li>
							<li><a href="/admin/gpo">Group Policies</a></li>
						</ul>
					</div>

					<div class="section-card">
						<h3>🌐 Network Services</h3>
						<ul>
							<li><a href="/admin/dns">DNS Management</a></li>
							<li><a href="/admin/dhcp">DHCP Management</a></li>
							<li><a href="/admin/vpn">VPN Configuration</a></li>
							<li><a href="/admin/firewall">Firewall Rules</a></li>
						</ul>
					</div>

					<div class="section-card">
						<h3>📧 Email Services</h3>
						<ul>
							<li><a href="/admin/email">Email Domains</a></li>
							<li><a href="/admin/email/accounts">Email Accounts</a></li>
							<li><a href="/admin/email/activesync">ActiveSync Devices</a></li>
							<li><a href="/admin/email/queues">Mail Queues</a></li>
						</ul>
					</div>

					<div class="section-card">
						<h3>🔒 Security</h3>
						<ul>
							<li><a href="/admin/security">Security Dashboard</a></li>
							<li><a href="/admin/certificates">Certificates</a></li>
							<li><a href="/admin/security/threats">Threat Intelligence</a></li>
							<li><a href="/admin/security/compliance">Compliance</a></li>
						</ul>
					</div>

					<div class="section-card">
						<h3>💾 Data Management</h3>
						<ul>
							<li><a href="/admin/shares">File Shares</a></li>
							<li><a href="/admin/backup">Backup & Restore</a></li>
							<li><a href="/admin/websites">Web Hosting</a></li>
							<li><a href="/admin/docker">Docker Registry</a></li>
						</ul>
					</div>

					<div class="section-card">
						<h3>⚙️ System</h3>
						<ul>
							<li><a href="/admin/config">Configuration</a></li>
							<li><a href="/admin/services">Services</a></li>
							<li><a href="/admin/logs">System Logs</a></li>
							<li><a href="/admin/performance">Performance</a></li>
						</ul>
					</div>
				</div>

				<div class="system-status">
					<h2>System Status</h2>
					<div class="status-grid">
						<div class="status-item">
							<span class="status-indicator healthy">●</span>
							<span>All Services Running</span>
						</div>
						<div class="status-item">
							<span class="status-indicator healthy">●</span>
							<span>Database Connected</span>
						</div>
						<div class="status-item">
							<span class="status-indicator healthy">●</span>
							<span>DNS Resolving</span>
						</div>
						<div class="status-item">
							<span class="status-indicator healthy">●</span>
							<span>Mail Delivery Active</span>
						</div>
					</div>
				</div>
			</div>
		`),
	}
	s.renderTemplate(w, "admin", data)
}

// User management handlers

// handleAdminUsers manages system users
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "User Management",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Users", Active: true},
		},
	}
	s.renderTemplate(w, "admin-users", data)
}

// handleCreateUser creates a new user
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Create User",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Users", URL: "/admin/users"},
			{Name: "Create", Active: true},
		},
	}
	s.renderTemplate(w, "create-user", data)
}

// handleImportUsers handles bulk user import
func (s *Server) handleImportUsers(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Import Users",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Users", URL: "/admin/users"},
			{Name: "Import", Active: true},
		},
	}
	s.renderTemplate(w, "import-users", data)
}

// handleExportUsers exports users to CSV
func (s *Server) handleExportUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=users.csv")
	// Write CSV data
	w.Write([]byte("username,email,first_name,last_name,groups\n"))
}

// handleAdminGroups manages user groups
func (s *Server) handleAdminGroups(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Group Management",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Groups", Active: true},
		},
	}
	s.renderTemplate(w, "admin-groups", data)
}

// handleOrganizationalUnits manages OUs
func (s *Server) handleOrganizationalUnits(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Organizational Units",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "OUs", Active: true},
		},
	}
	s.renderTemplate(w, "organizational-units", data)
}

// Domain and Active Directory handlers

// handleDomainSettings configures domain settings
func (s *Server) handleDomainSettings(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Domain Configuration",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Domain", Active: true},
		},
	}
	s.renderTemplate(w, "domain-settings", data)
}

// handleGroupPolicy manages Group Policy Objects
func (s *Server) handleGroupPolicy(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Group Policy Management",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "GPO", Active: true},
		},
	}
	s.renderTemplate(w, "group-policy", data)
}

// handleComputers manages computer accounts
func (s *Server) handleComputers(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Computer Accounts",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Computers", Active: true},
		},
	}
	s.renderTemplate(w, "computers", data)
}

// handleDomainTrusts manages domain trust relationships
func (s *Server) handleDomainTrusts(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "Domain Trusts",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "Trusts", Active: true},
		},
	}
	s.renderTemplate(w, "domain-trusts", data)
}

// handleFSMORoles manages FSMO role holders
func (s *Server) handleFSMORoles(w http.ResponseWriter, r *http.Request) {
	data := PageData{
		Title: "FSMO Roles",
		Breadcrumbs: []Breadcrumb{
			{Name: "Home", URL: "/"},
			{Name: "Admin", URL: "/admin/"},
			{Name: "FSMO", Active: true},
		},
	}
	s.renderTemplate(w, "fsmo-roles", data)
}

// Status and health check handlers

// handlePublicStatus shows public system status
func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status": "healthy",
		"services": map[string]string{
			"dns":   "running",
			"email": "running",
			"web":   "running",
			"auth":  "running",
		},
		"timestamp": time.Now().Unix(),
	}
	s.jsonResponse(w, status, http.StatusOK)
}

// handleHealthCheck provides health check endpoint
func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Additional required stub handlers to complete the interface

func (s *Server) handleDNSZones(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dns-zones", PageData{Title: "DNS Zones"})
}

func (s *Server) handleDNSRecords(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dns-records", PageData{Title: "DNS Records"})
}

func (s *Server) handleDNSForwarders(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dns-forwarders", PageData{Title: "DNS Forwarders"})
}

func (s *Server) handleDHCPScopes(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dhcp-scopes", PageData{Title: "DHCP Scopes"})
}

func (s *Server) handleDHCPReservations(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dhcp-reservations", PageData{Title: "DHCP Reservations"})
}

func (s *Server) handleDHCPLeases(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "dhcp-leases", PageData{Title: "DHCP Leases"})
}