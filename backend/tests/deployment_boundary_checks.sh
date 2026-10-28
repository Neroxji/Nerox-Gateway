#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

if env -u ADMIN_API_KEY MYSQL_ROOT_PASSWORD='n1-test-db-password' docker compose config >"$tmp_dir/missing-admin.out" 2>&1; then
	printf '%s\n' 'expected docker compose config to fail without ADMIN_API_KEY' >&2
	exit 1
fi
grep -Fq 'ADMIN_API_KEY must be set' "$tmp_dir/missing-admin.out"

if env -u MYSQL_ROOT_PASSWORD ADMIN_API_KEY='n1-test-admin-key' docker compose config >"$tmp_dir/missing-db.out" 2>&1; then
	printf '%s\n' 'expected docker compose config to fail without MYSQL_ROOT_PASSWORD' >&2
	exit 1
fi
grep -Fq 'MYSQL_ROOT_PASSWORD must be set' "$tmp_dir/missing-db.out"

MYSQL_ROOT_PASSWORD='n1-test-db-password' ADMIN_API_KEY='n1-test-admin-key' docker compose config >"$tmp_dir/complete.out"
grep -Fq 'host_ip: 127.0.0.1' "$tmp_dir/complete.out"

for required_pattern in 'config.json' '.env' '.env.*' '*.key' '*.pem' '*.crt'; do
	grep -Fxq "$required_pattern" backend/.dockerignore
done

printf '%s\n' 'Deployment boundary checks passed.'
