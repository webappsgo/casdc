-- Migration 004: Git server and Docker registry tables
-- Adds support for Git repository management and Docker container registry

-- Git Repositories
CREATE TABLE IF NOT EXISTS git_repositories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    full_name TEXT UNIQUE NOT NULL,
    description TEXT,
    org_id INTEGER,
    owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    private BOOLEAN DEFAULT FALSE,
    default_branch TEXT DEFAULT 'main',
    size INTEGER DEFAULT 0,
    stars INTEGER DEFAULT 0,
    forks INTEGER DEFAULT 0,
    open_issues INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    pushed_at DATETIME,
    FOREIGN KEY (owner_id) REFERENCES users(id)
);

-- Git Organizations
CREATE TABLE IF NOT EXISTS git_organizations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    display_name TEXT NOT NULL,
    description TEXT,
    website TEXT,
    email TEXT,
    location TEXT,
    visibility TEXT DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Git Organization Members
CREATE TABLE IF NOT EXISTS git_org_members (
    org_id INTEGER REFERENCES git_organizations(id) ON DELETE CASCADE,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'readonly')),
    joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id),
    FOREIGN KEY (org_id) REFERENCES git_organizations(id),
    FOREIGN KEY (user_id) REFERENCES users(id)
);

-- Git Repository Collaborators
CREATE TABLE IF NOT EXISTS git_collaborators (
    repo_id INTEGER REFERENCES git_repositories(id) ON DELETE CASCADE,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    permission TEXT NOT NULL CHECK (permission IN ('read', 'write', 'admin')),
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, user_id),
    FOREIGN KEY (repo_id) REFERENCES git_repositories(id),
    FOREIGN KEY (user_id) REFERENCES users(id)
);

-- Git Pull Requests
CREATE TABLE IF NOT EXISTS git_pull_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id INTEGER NOT NULL REFERENCES git_repositories(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    state TEXT DEFAULT 'open' CHECK (state IN ('open', 'closed', 'merged')),
    author_id INTEGER NOT NULL REFERENCES users(id),
    source_branch TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    mergeable BOOLEAN DEFAULT TRUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    merged_at DATETIME,
    merged_by INTEGER REFERENCES users(id),
    FOREIGN KEY (repo_id) REFERENCES git_repositories(id),
    FOREIGN KEY (author_id) REFERENCES users(id)
);

-- Git Issues
CREATE TABLE IF NOT EXISTS git_issues (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id INTEGER NOT NULL REFERENCES git_repositories(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    state TEXT DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    author_id INTEGER NOT NULL REFERENCES users(id),
    assignee_id INTEGER REFERENCES users(id),
    labels TEXT, -- JSON array
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    closed_at DATETIME,
    FOREIGN KEY (repo_id) REFERENCES git_repositories(id),
    FOREIGN KEY (author_id) REFERENCES users(id)
);

-- Git Webhooks
CREATE TABLE IF NOT EXISTS git_webhooks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id INTEGER NOT NULL REFERENCES git_repositories(id) ON DELETE CASCADE,
    url TEXT NOT NULL,
    secret TEXT,
    events TEXT NOT NULL, -- JSON array: ['push', 'pull_request', 'issue']
    active BOOLEAN DEFAULT TRUE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (repo_id) REFERENCES git_repositories(id)
);

-- Docker Repositories
CREATE TABLE IF NOT EXISTS docker_repositories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    private BOOLEAN DEFAULT FALSE,
    stars INTEGER DEFAULT 0,
    pulls INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Docker Images
CREATE TABLE IF NOT EXISTS docker_images (
    id TEXT PRIMARY KEY,
    repository TEXT NOT NULL,
    tag TEXT NOT NULL,
    digest TEXT NOT NULL,
    size INTEGER NOT NULL,
    architecture TEXT DEFAULT 'amd64',
    os TEXT DEFAULT 'linux',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    pushed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    pulled_at DATETIME,
    pull_count INTEGER DEFAULT 0,
    scanned BOOLEAN DEFAULT FALSE,
    UNIQUE(repository, tag)
);

-- Docker Image Layers
CREATE TABLE IF NOT EXISTS docker_layers (
    digest TEXT PRIMARY KEY,
    size INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Docker Image Vulnerabilities
CREATE TABLE IF NOT EXISTS docker_vulnerabilities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    image_id TEXT NOT NULL REFERENCES docker_images(id) ON DELETE CASCADE,
    vulnerability_id TEXT NOT NULL,
    severity TEXT CHECK (severity IN ('critical', 'high', 'medium', 'low', 'negligible')),
    package TEXT NOT NULL,
    version TEXT NOT NULL,
    fixed_in TEXT,
    description TEXT,
    links TEXT, -- JSON array
    scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (image_id) REFERENCES docker_images(id)
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_git_repos_owner ON git_repositories(owner_id);
CREATE INDEX IF NOT EXISTS idx_git_repos_org ON git_repositories(org_id);
CREATE INDEX IF NOT EXISTS idx_git_repos_updated ON git_repositories(updated_at);
CREATE INDEX IF NOT EXISTS idx_git_prs_repo ON git_pull_requests(repo_id);
CREATE INDEX IF NOT EXISTS idx_git_prs_state ON git_pull_requests(state);
CREATE INDEX IF NOT EXISTS idx_git_issues_repo ON git_issues(repo_id);
CREATE INDEX IF NOT EXISTS idx_git_issues_state ON git_issues(state);
CREATE INDEX IF NOT EXISTS idx_docker_images_repo ON docker_images(repository);
CREATE INDEX IF NOT EXISTS idx_docker_images_pushed ON docker_images(pushed_at);
CREATE INDEX IF NOT EXISTS idx_docker_vulns_image ON docker_vulnerabilities(image_id);
CREATE INDEX IF NOT EXISTS idx_docker_vulns_severity ON docker_vulnerabilities(severity);
