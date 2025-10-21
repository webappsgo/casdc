// CASDC Web Interface JavaScript
// Provides interactive functionality for the web interface

(function() {
    'use strict';

    // Initialize the application when DOM is loaded
    document.addEventListener('DOMContentLoaded', function() {
        initializeApp();
    });

    function initializeApp() {
        console.log('🚀 CASDC Web Interface initialized');

        // Initialize components
        initializeTheme();
        initializeNotifications();
        initializeDashboard();
        initializeWebSocket();
    }

    // Theme management
    function initializeTheme() {
        const savedTheme = localStorage.getItem('casdc-theme') || 'dark';
        document.documentElement.setAttribute('data-theme', savedTheme);

        // Theme toggle functionality
        const themeToggle = document.getElementById('theme-toggle');
        if (themeToggle) {
            themeToggle.addEventListener('click', toggleTheme);
        }
    }

    function toggleTheme() {
        const currentTheme = document.documentElement.getAttribute('data-theme');
        const newTheme = currentTheme === 'dark' ? 'light' : 'dark';

        document.documentElement.setAttribute('data-theme', newTheme);
        localStorage.setItem('casdc-theme', newTheme);

        showNotification(`Switched to ${newTheme} theme`, 'success');
    }

    // Notification system
    function initializeNotifications() {
        // Create notification container if it doesn't exist
        if (!document.getElementById('notifications')) {
            const container = document.createElement('div');
            container.id = 'notifications';
            container.className = 'notifications-container';
            document.body.appendChild(container);
        }
    }

    function showNotification(message, type = 'info', duration = 5000) {
        const container = document.getElementById('notifications');
        if (!container) return;

        const notification = document.createElement('div');
        notification.className = `notification notification-${type}`;
        notification.innerHTML = `
            <span class="notification-message">${message}</span>
            <button class="notification-close">&times;</button>
        `;

        // Add click handler for close button
        notification.querySelector('.notification-close').addEventListener('click', function() {
            removeNotification(notification);
        });

        // Auto-remove after duration
        setTimeout(() => {
            removeNotification(notification);
        }, duration);

        container.appendChild(notification);

        // Animate in
        requestAnimationFrame(() => {
            notification.classList.add('show');
        });
    }

    function removeNotification(notification) {
        notification.classList.remove('show');
        setTimeout(() => {
            if (notification.parentNode) {
                notification.parentNode.removeChild(notification);
            }
        }, 300);
    }

    // Dashboard functionality
    function initializeDashboard() {
        // Update dashboard metrics periodically
        updateDashboardMetrics();
        setInterval(updateDashboardMetrics, 30000); // Every 30 seconds

        // Add click handlers for cards
        const cards = document.querySelectorAll('.card');
        cards.forEach(card => {
            card.addEventListener('click', handleCardClick);
        });
    }

    function updateDashboardMetrics() {
        // Fetch current metrics from API
        fetch('/api/v1/metrics')
            .then(response => response.json())
            .then(data => {
                updateMetricElements(data);
            })
            .catch(error => {
                console.error('Failed to update metrics:', error);
            });
    }

    function updateMetricElements(data) {
        // Update user count
        const userCount = document.querySelector('[data-metric="users"]');
        if (userCount && data.users) {
            userCount.textContent = `${data.users.active} active`;
        }

        // Update threat count
        const threatCount = document.querySelector('[data-metric="threats"]');
        if (threatCount && data.security) {
            threatCount.textContent = `${data.security.threats} indicators`;
        }

        // Update mail count
        const mailCount = document.querySelector('[data-metric="mail"]');
        if (mailCount && data.mail) {
            mailCount.textContent = `${data.mail.messages} messages`;
        }

        // Update system status
        const systemStatus = document.querySelector('.status');
        if (systemStatus && data.system) {
            systemStatus.className = `status ${data.system.status}`;
            systemStatus.textContent = data.system.message;
        }
    }

    function handleCardClick(event) {
        const card = event.currentTarget;
        const title = card.querySelector('h2').textContent.toLowerCase();

        // Navigate to appropriate section
        switch(title) {
            case 'users':
                window.location.href = '/admin/users';
                break;
            case 'security':
                window.location.href = '/admin/security';
                break;
            case 'mail':
                window.location.href = '/webmail/';
                break;
            case 'system status':
                window.location.href = '/admin/system';
                break;
            default:
                console.log('Card clicked:', title);
        }
    }

    // WebSocket for real-time updates
    function initializeWebSocket() {
        if (window.location.protocol === 'https:') {
            const wsUrl = `wss://${window.location.host}/ws`;
            connectWebSocket(wsUrl);
        }
    }

    function connectWebSocket(url) {
        try {
            const ws = new WebSocket(url);

            ws.onopen = function() {
                console.log('WebSocket connected');
                showNotification('Real-time updates connected', 'success', 2000);
            };

            ws.onmessage = function(event) {
                try {
                    const data = JSON.parse(event.data);
                    handleWebSocketMessage(data);
                } catch (error) {
                    console.error('WebSocket message parsing error:', error);
                }
            };

            ws.onclose = function() {
                console.log('WebSocket disconnected');
                // Attempt to reconnect after 5 seconds
                setTimeout(() => connectWebSocket(url), 5000);
            };

            ws.onerror = function(error) {
                console.error('WebSocket error:', error);
            };

        } catch (error) {
            console.error('WebSocket connection failed:', error);
        }
    }

    function handleWebSocketMessage(data) {
        switch(data.type) {
            case 'notification':
                showNotification(data.message, data.level || 'info');
                break;
            case 'metrics':
                updateMetricElements(data.data);
                break;
            case 'alert':
                showNotification(data.message, 'warning');
                break;
            case 'security_event':
                handleSecurityEvent(data);
                break;
            default:
                console.log('Unknown WebSocket message type:', data.type);
        }
    }

    function handleSecurityEvent(data) {
        showNotification(`Security Event: ${data.message}`, 'warning');

        // Update security metrics if on dashboard
        if (window.location.pathname === '/') {
            updateDashboardMetrics();
        }
    }

    // Form handling utilities
    function handleFormSubmit(form, callback) {
        form.addEventListener('submit', function(event) {
            event.preventDefault();

            const formData = new FormData(form);
            const data = Object.fromEntries(formData);

            if (callback) {
                callback(data);
            }
        });
    }

    // API helper functions
    function apiRequest(method, url, data = null) {
        const options = {
            method: method,
            headers: {
                'Content-Type': 'application/json',
            }
        };

        if (data) {
            options.body = JSON.stringify(data);
        }

        return fetch(url, options)
            .then(response => {
                if (!response.ok) {
                    throw new Error(`HTTP error! status: ${response.status}`);
                }
                return response.json();
            });
    }

    // Export functions for global use
    window.CASDC = {
        showNotification: showNotification,
        apiRequest: apiRequest,
        handleFormSubmit: handleFormSubmit,
        toggleTheme: toggleTheme
    };

})();