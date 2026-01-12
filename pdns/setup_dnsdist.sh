#!/bin/sh -eu
# ===========================================
# dnsdist セットアップスクリプト
# DNS水攻め対策用のdnsdistをインストール・設定
# ===========================================

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
DNSDIST_CONF="${SCRIPT_DIR}/dnsdist.conf"
PDNS_CONF="${SCRIPT_DIR}/pdns.conf"

usage() {
  echo "Usage: $0 {install|configure|start|stop|status|uninstall}"
  echo ""
  echo "Commands:"
  echo "  install    - dnsdistをインストール"
  echo "  configure  - 設定ファイルを配置"
  echo "  start      - dnsdistを起動"
  echo "  stop       - dnsdistを停止"
  echo "  status     - dnsdistの状態を確認"
  echo "  uninstall  - dnsdistをアンインストール"
  exit 1
}

install_dnsdist() {
  echo "=== dnsdist をインストール中 ==="
  
  # Debian/Ubuntu
  if command -v apt-get >/dev/null 2>&1; then
    sudo apt-get update
    sudo apt-get install -y dnsdist
  # RHEL/CentOS
  elif command -v yum >/dev/null 2>&1; then
    sudo yum install -y epel-release
    sudo yum install -y dnsdist
  # Fedora
  elif command -v dnf >/dev/null 2>&1; then
    sudo dnf install -y dnsdist
  else
    echo "エラー: サポートされていないパッケージマネージャです"
    exit 1
  fi
  
  echo "=== dnsdist インストール完了 ==="
}

configure_dnsdist() {
  echo "=== dnsdist 設定を配置中 ==="
  
  # 設定ファイルをバックアップ
  if [ -f /etc/dnsdist/dnsdist.conf ]; then
    sudo cp /etc/dnsdist/dnsdist.conf "/etc/dnsdist/dnsdist.conf.$(date +%Y%m%d%H%M%S).bak"
  fi
  
  # 設定ファイルをコピー
  sudo cp "${DNSDIST_CONF}" /etc/dnsdist/dnsdist.conf
  
  # PowerDNSの設定も更新（ポート5300に変更）
  if [ -f /etc/powerdns/pdns.conf ]; then
    sudo cp /etc/powerdns/pdns.conf "/etc/powerdns/pdns.conf.$(date +%Y%m%d%H%M%S).bak"
  fi
  sudo cp "${PDNS_CONF}" /etc/powerdns/pdns.conf
  
  echo "=== 設定完了 ==="
  echo ""
  echo "構成:"
  echo "  dnsdist   -> 0.0.0.0:53  (フロントエンド)"
  echo "  PowerDNS  -> 127.0.0.1:5300 (バックエンド)"
}

start_dnsdist() {
  echo "=== dnsdist を起動中 ==="
  
  # PowerDNSを再起動（ポート変更を反映）
  sudo systemctl restart pdns
  
  # dnsdistを起動
  sudo systemctl enable dnsdist
  sudo systemctl start dnsdist
  
  echo "=== dnsdist 起動完了 ==="
}

stop_dnsdist() {
  echo "=== dnsdist を停止中 ==="
  sudo systemctl stop dnsdist
  echo "=== dnsdist 停止完了 ==="
}

status_dnsdist() {
  echo "=== dnsdist 状態 ==="
  sudo systemctl status dnsdist --no-pager || true
  echo ""
  echo "=== PowerDNS 状態 ==="
  sudo systemctl status pdns --no-pager || true
}

uninstall_dnsdist() {
  echo "=== dnsdist をアンインストール中 ==="
  
  sudo systemctl stop dnsdist || true
  sudo systemctl disable dnsdist || true
  
  if command -v apt-get >/dev/null 2>&1; then
    sudo apt-get remove -y dnsdist
  elif command -v yum >/dev/null 2>&1; then
    sudo yum remove -y dnsdist
  elif command -v dnf >/dev/null 2>&1; then
    sudo dnf remove -y dnsdist
  fi
  
  # PowerDNSをポート53に戻す
  if [ -f /etc/powerdns/pdns.conf ]; then
    sudo sed -i 's/local-port=5300/local-port=53/' /etc/powerdns/pdns.conf
    sudo systemctl restart pdns
  fi
  
  echo "=== dnsdist アンインストール完了 ==="
}

# メイン処理
if [ $# -lt 1 ]; then
  usage
fi

case "$1" in
  install)
    install_dnsdist
    ;;
  configure)
    configure_dnsdist
    ;;
  start)
    start_dnsdist
    ;;
  stop)
    stop_dnsdist
    ;;
  status)
    status_dnsdist
    ;;
  uninstall)
    uninstall_dnsdist
    ;;
  *)
    usage
    ;;
esac
