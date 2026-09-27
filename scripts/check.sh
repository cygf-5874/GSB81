#!/usr/bin/env bash
# 固定验收入口：等价于在仓库根目录跑 go run ./check
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./check "$@"
