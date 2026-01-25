#!/bin/bash
#
# Beancount AutoUpdate 服务管理脚本
# 用法: sudo bash service.sh {start|stop|restart|status|enable|disable|logs}
#

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置变量
APP_NAME="beancount-autoupdate"
SERVICE_NAME="beancount-autoupdate"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/beancount-autoupdate"
LOG_DIR="/var/log/beancount-autoupdate"

# 打印信息函数
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_header() {
    echo -e "${BLUE}$1${NC}"
}

# 检查是否为 root 用户
check_root() {
    if [[ $EUID -ne 0 ]]; then
        print_error "此脚本需要 root 权限运行"
        echo "请使用: sudo bash $0"
        exit 1
    fi
}

# 检查是否已安装
check_installed() {
    if [[ ! -f "$INSTALL_DIR/$APP_NAME" ]]; then
        print_error "$APP_NAME 未安装"
        echo "请先运行安装脚本: sudo bash install.sh"
        exit 1
    fi
}

# 启动服务
start_service() {
    print_header "启动服务..."
    systemctl start "$SERVICE_NAME"

    sleep 2

    if systemctl is-active --quiet "$SERVICE_NAME"; then
        print_info "服务启动成功"
        show_status
    else
        print_error "服务启动失败"
        print_info "查看错误日志: journalctl -u $SERVICE_NAME -n 50"
        exit 1
    fi
}

# 停止服务
stop_service() {
    print_header "停止服务..."
    systemctl stop "$SERVICE_NAME"

    sleep 2

    if systemctl is-active --quiet "$SERVICE_NAME"; then
        print_error "服务停止失败"
        exit 1
    else
        print_info "服务已停止"
    fi
}

# 重启服务
restart_service() {
    print_header "重启服务..."
    systemctl restart "$SERVICE_NAME"

    sleep 2

    if systemctl is-active --quiet "$SERVICE_NAME"; then
        print_info "服务重启成功"
        show_status
    else
        print_error "服务重启失败"
        print_info "查看错误日志: journalctl -u $SERVICE_NAME -n 50"
        exit 1
    fi
}

# 显示服务状态
show_status() {
    print_header "服务状态"
    echo ""
    systemctl status "$SERVICE_NAME" --no-pager
    echo ""
}

# 启用开机自启
enable_service() {
    print_header "启用开机自启..."
    systemctl enable "$SERVICE_NAME"
    print_info "已设置开机自启"
}

# 禁用开机自启
disable_service() {
    print_header "禁用开机自启..."
    systemctl disable "$SERVICE_NAME"
    print_info "已禁用开机自启"
}

# 查看日志
show_logs() {
    print_header "查看日志 (按 Ctrl+C 退出)..."
    echo ""
    journalctl -u "$SERVICE_NAME" -f
}

# 查看最近日志
show_recent_logs() {
    print_header "最近 50 条日志"
    echo ""
    journalctl -u "$SERVICE_NAME" -n 50 --no-pager
}

# 重新加载配置
reload_config() {
    print_header "重新加载配置..."
    systemctl restart "$SERVICE_NAME"
    print_info "配置已重新加载"
}

# 显示帮助信息
show_help() {
    echo "Beancount AutoUpdate 服务管理脚本"
    echo ""
    echo "用法: sudo bash $0 {command}"
    echo ""
    echo "可用命令:"
    echo "  start      - 启动服务"
    echo "  stop       - 停止服务"
    echo "  restart    - 重启服务"
    echo "  status     - 查看服务状态"
    echo "  enable     - 启用开机自启"
    echo "  disable    - 禁用开机自启"
    echo "  logs       - 实时查看日志"
    echo "  recent     - 查看最近日志"
    echo "  reload     - 重新加载配置"
    echo "  help       - 显示帮助信息"
    echo ""
    echo "示例:"
    echo "  sudo bash $0 start"
    echo "  sudo bash $0 status"
    echo "  sudo bash $0 logs"
    echo ""
}

# 主函数
main() {
    local command=${1:-"help"}

    check_root
    check_installed

    case $command in
        start)
            start_service
            ;;
        stop)
            stop_service
            ;;
        restart)
            restart_service
            ;;
        status)
            show_status
            ;;
        enable)
            enable_service
            ;;
        disable)
            disable_service
            ;;
        logs)
            show_logs
            ;;
        recent)
            show_recent_logs
            ;;
        reload)
            reload_config
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            print_error "未知命令: $command"
            echo ""
            show_help
            exit 1
            ;;
    esac
}

# 运行主函数
main "$@"