#!/usr/bin/env bash
# 开发约束自检：Controller 不碰数据库、模板无内联样式、无外部 CDN、Ajax 统一入口。
set -u
cd "$(dirname "$0")/.."
fail=0
err() { echo "[违规] $*"; fail=1; }

# 1. controller 层禁止 import database/sql 与 internal/db，禁止出现 SQL
if grep -rnE '"database/sql"|"work-ledger/internal/db"' internal/controller; then err "controller 引用了数据库包"; fi
if grep -rniE '\b(SELECT|INSERT INTO|UPDATE [a-z_]+ SET|DELETE FROM)\b' internal/controller; then err "controller 中出现 SQL 语句"; fi

# 2. 模板禁止 <style> 与 style="..."
if grep -rnE '<style|style="' web/templates; then err "模板中出现内联样式"; fi

# 3. 模板与业务 JS/CSS 禁止引用外部资源（http/https 链接）
if grep -rnE '(src|href)="(https?:)?//' web/templates; then err "模板引用了外部资源"; fi
if grep -rnE 'https?://' web/assets/js web/assets/css; then err "业务 JS/CSS 引用了外部地址"; fi

# 4. 前端 Ajax 只能通过 WL.api / WL.upload（统一 POST + JSON + action）
if grep -rnE 'fetch\(|XMLHttpRequest|\$\.ajax' web/templates; then err "模板中直接发起请求，请使用 WL.api"; fi

# 5. 每个 Ajax action 都必须登记验证器
if grep -rnE '"[a-z_]+": *\{Handle:' internal/controller; then err "存在没有验证器的 action"; fi

if [ "$fail" = 0 ]; then echo "开发约束检查通过。"; fi
exit $fail
