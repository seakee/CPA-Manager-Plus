#!/usr/bin/env bash
set -euo pipefail

# Rebase the provider-alias commits onto a fresh upstream CPA release,
# build both components, and optionally deploy them to yosef-server.
# Usage: reapply-provider-alias.sh [upstream-ref] [--deploy]

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UPSTREAM_REF="${1:-origin/dev}"
DEPLOY=0
[[ "${2:-}" == "--deploy" ]] && DEPLOY=1

PATCH_BASE="${PATCH_BASE:-29d676f8}"
REMOTE_HOST="${CPA_DEPLOY_HOST:-yosef-server}"
REMOTE_ROOT="${CPA_DEPLOY_ROOT:-/data/cpa/CPA-Manager}"

cd "$ROOT_DIR"
command -v git >/dev/null
command -v node >/dev/null
command -v npm >/dev/null
command -v go >/dev/null

if [[ -n "$(git status --porcelain)" ]]; then
  echo "工作区有未提交修改，请先提交或暂存后再运行。" >&2
  exit 2
fi

echo "[1/7] 获取官方最新代码"
git fetch origin

if [[ "${UPSTREAM_REF}" == origin/* ]]; then
  git rev-parse --verify "$UPSTREAM_REF" >/dev/null
fi

echo "[2/7] 将 provider alias 补丁重放到 ${UPSTREAM_REF}"
if ! git merge-base --is-ancestor "$PATCH_BASE" HEAD; then
  echo "当前分支不包含补丁基线 ${PATCH_BASE}。请在 provider alias 分支运行。" >&2
  exit 2
fi
git rebase --onto "$UPSTREAM_REF" "$PATCH_BASE"

echo "[3/7] 运行前端测试"
npm --prefix apps/web test -- --run

echo "[4/7] 构建前端"
npm --prefix apps/web run build

echo "[5/7] 运行服务端测试"
pushd apps/manager-server >/dev/null
go test ./...
popd >/dev/null

echo "[6/7] 构建 Linux 服务端"
mkdir -p dist/provider-alias
pushd apps/manager-server >/dev/null
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$ROOT_DIR/dist/provider-alias/cpa-manager-plus" ./cmd/cpa-manager-plus
popd >/dev/null
cp apps/web/dist/index.html dist/provider-alias/management.html

if [[ "$DEPLOY" != 1 ]]; then
  echo "构建完成：dist/provider-alias/"
  echo "部署时重新运行：$0 $UPSTREAM_REF --deploy"
  exit 0
fi

RELEASE="provider-alias-$(git rev-parse --short HEAD)-$(date +%Y%m%d%H%M%S)"
REMOTE_RELEASE="$REMOTE_ROOT/app/$RELEASE"
REMOTE_PANEL="$REMOTE_ROOT/panel/releases/$RELEASE"

echo "[7/7] 部署到 ${REMOTE_HOST}"
PREVIOUS_APP="$(ssh "$REMOTE_HOST" "readlink -f '$REMOTE_ROOT/app/current' || true")"
PREVIOUS_PANEL="$(ssh "$REMOTE_HOST" "readlink -f '$REMOTE_ROOT/panel/current' || true")"
ssh "$REMOTE_HOST" "set -eu; mkdir -p '$REMOTE_RELEASE' '$REMOTE_PANEL'"
scp dist/provider-alias/cpa-manager-plus "$REMOTE_HOST:$REMOTE_RELEASE/cpa-manager-plus"
scp dist/provider-alias/management.html "$REMOTE_HOST:$REMOTE_PANEL/management.html"
if ! ssh "$REMOTE_HOST" "set -eu; chmod 0755 '$REMOTE_RELEASE/cpa-manager-plus'; ln -sfn '$REMOTE_RELEASE' '$REMOTE_ROOT/app/current'; ln -sfn '$REMOTE_PANEL' '$REMOTE_ROOT/panel/current'; systemctl --user restart cpa-manager.service; for i in \$(seq 1 30); do curl -fsS http://127.0.0.1:18317/health >/dev/null && break; sleep 1; done; curl -fsS http://127.0.0.1:18317/health; echo; key=\$(sed -n 's/^CPA_MANAGER_ADMIN_KEY=//p' '$REMOTE_ROOT/data/manager.env'); test \"\$(curl -sS -o /dev/null -w '%{http_code}' -H \"Authorization: Bearer \$key\" http://127.0.0.1:18317/v0/management/provider-key-aliases)\" = 200; '$REMOTE_RELEASE/cpa-manager-plus' --version"; then
  echo "部署校验失败，恢复上一版。" >&2
  if [[ -n "$PREVIOUS_APP" && -n "$PREVIOUS_PANEL" ]]; then
    ssh "$REMOTE_HOST" "set -eu; ln -sfn '$PREVIOUS_APP' '$REMOTE_ROOT/app/current'; ln -sfn '$PREVIOUS_PANEL' '$REMOTE_ROOT/panel/current'; systemctl --user restart cpa-manager.service"
  fi
  exit 1
fi

echo "部署完成：$RELEASE"
