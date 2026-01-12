#!/usr/bin/env bash
# ===========================================
# ゾーンファイル初期化スクリプト（BINDバックエンド用）
# ===========================================

set -eux
cd $(dirname $0)

if test -f /home/isucon/env.sh; then
	. /home/isucon/env.sh
fi

ISUCON_SUBDOMAIN_ADDRESS=${ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS:-127.0.0.1}
ZONE_FILE="/home/isucon/webapp/pdns/t.isucon.pw.zone"

# テンプレートからゾーンファイルを生成
sed 's/<ISUCON_SUBDOMAIN_ADDRESS>/'$ISUCON_SUBDOMAIN_ADDRESS'/g' u.isucon.dev.zone > "${ZONE_FILE}"

echo "ゾーンファイルを生成しました: ${ZONE_FILE}"

# PowerDNSを再起動してゾーンをリロード
if systemctl is-active --quiet pdns; then
    echo "PowerDNSを再起動してゾーンをリロードします..."
    sudo systemctl restart pdns
    echo "完了"
fi
