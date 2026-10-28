# N1 Security Test Coverage Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add executable regression coverage for the N1 admin, Gateway Token, cache, migration, and deployment security requirements.

**Architecture:** Keep pure behavior tests next to their Go package. Put tests that require MySQL and Redis behind the `integration` build tag, and use an in-process HTTP router plus an `httptest` Fake Provider to verify the N1 lifecycle without a real upstream service. Keep Compose and Docker checks as shell-level deployment checks.

**Tech Stack:** Go testing, Gin `httptest`, GORM MySQL, Redis client, `httptest.Server`, Docker Compose.

**Spec:** `docs/experiments/results/N1-admin-security.md`

## Global Constraints

- `/admin` requires `Authorization: Bearer <ADMIN_API_KEY>`.
- Gateway Tokens use Bearer parsing, SHA-256 storage, expiry, and revocation.
- Redis and MySQL must not retain the raw Gateway Token.
- Expired, revoked, and disabled-user requests must not call the Provider.
- Integration tests must use test-only MySQL/Redis and an in-process Fake Provider.
- Do not connect tests to a real Provider or production database.

### Task 1: Complete In-Process Admin Route Coverage

**Files:**
- Modify: `backend/internal/httpapi/admin_auth_test.go`

- [x] Add the revoke route to the unauthorized route matrix.
- [x] Run `go test ./internal/httpapi -run 'TestAdmin|TestRegister' -v` and confirm all unauthorized cases return `401`.
- [x] Keep this task limited to route and middleware behavior; successful database operations belong in the integration suite.

### Task 2: Add N1 HTTP Lifecycle Integration Tests

**Files:**
- Create: `backend/internal/httpapi/admin_token_lifecycle_integration_test.go`

**Interfaces:**
- Read `AIGATEWAY_TEST_MYSQL_DSN` for a dedicated MySQL test database.
- Read `AIGATEWAY_TEST_REDIS_ADDR` and optional `AIGATEWAY_TEST_REDIS_DB` for Redis.
- Build a Gin router with `NewServer`, `Register`, and an `httptest.Server` Fake Provider.
- Track Fake Provider calls with an atomic counter.

- [x] Write tests for admin create-user, create-token, disable-user, and revoke-token success paths.
- [x] Write tests for raw-token authentication rejection, expired-token `401`, revoked-token `401`, and disabled-user `403`.
- [x] Assert invalid-token, expired-token, revoked-token, and disabled-user cases do not increment the Fake Provider call counter.
- [x] Assert a valid Bearer Token reaches the Fake Provider and produces the expected HTTP/SSE response.
- [x] Run with `go test -tags=integration -count=1 ./internal/httpapi` against dedicated MySQL 8.0 and Redis 7 test services.

### Task 3: Add Storage and Migration Integration Tests

**Files:**
- Create: `backend/internal/storage/token_migration_integration_test.go`
- Create: `backend/internal/storage/token_cache_integration_test.go`

- [x] Add an integration test that creates a legacy `tokens.token_key` table, starts `OpenDatabase`, and verifies hash/expiry migration and removal of `token_key`.
- [x] Add Redis integration assertions for actual Set/Get/Delete behavior, raw-token absence, and TTL not exceeding `ExpiresAt`.
- [x] Keep the existing unit-level cache serialization test separate from the real Redis test.
- [x] Run with `go test -tags=integration -count=1 ./internal/storage`; Redis cache and MySQL migration tests passed.

### Task 4: Add Deployment Boundary Checks

**Files:**
- Create: `backend/tests/deployment_boundary_checks.sh`

- [x] Verify `docker compose config` fails when `MYSQL_ROOT_PASSWORD` is missing.
- [x] Verify `docker compose config` fails when `ADMIN_API_KEY` is missing.
- [x] Verify a complete test environment renders successfully and binds MySQL/Redis to `127.0.0.1`.
- [x] Verify the Docker build context excludes `config.json`, `.env`, certificates, and private keys.

### Task 5: Verify and Record Results

- [x] Run `go test -count=1 ./...`.
- [x] Run `go vet ./...` and `go test -race -count=1 ./...`.
- [x] Run the tagged HTTP lifecycle, MySQL migration, and Redis cache integration suites.
- [x] Run `git diff --check` and review the final test files against the N1 acceptance criteria.

Integration verification used isolated local Docker services: MySQL 8.0 on `127.0.0.1:3307` with separate HTTP and migration databases, and Redis 7 on `127.0.0.1:6380` with separate test DBs. Both test containers were stopped after verification.

Remaining limitations: no real Provider was called, and runtime Gateway log collection was not performed. The Docker image was built and its final contents and history were scanned.
