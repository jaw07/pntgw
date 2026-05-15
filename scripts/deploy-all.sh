#!/bin/bash
# Run the three deploy scripts in order. Each is idempotent.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

"$SCRIPT_DIR/verify.sh"
"$SCRIPT_DIR/bootstrap.sh"
"$SCRIPT_DIR/cloudflared.sh"
"$SCRIPT_DIR/pntgw.sh"

echo
echo "Done. Verify with:"
echo "  ssh $ER_USER@$ER_HOST 'systemctl is-active cloudflared pntgw ntp'"
echo "  curl -sS http://$ER_HOST:8080/api/status"
