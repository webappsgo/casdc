-- PostgreSQL initialization script for CASDC
-- This script is executed when the PostgreSQL container first starts

-- Ensure proper encoding and locale
SET client_encoding = 'UTF8';

-- Create extensions that CASDC might use
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "citext";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";
CREATE EXTENSION IF NOT EXISTS "unaccent";

-- Set timezone to UTC by default
SET timezone = 'UTC';

-- Create additional users for different components if needed
-- (CASDC user is created by the container environment variables)

-- Log initialization complete
\echo 'CASDC PostgreSQL database initialized successfully'