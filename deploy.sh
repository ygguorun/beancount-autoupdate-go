#!/bin/bash

# Beancount AutoUpdate 部署脚本
# 用于将编译好的二进制文件部署到服务器

set -e

# 配置变量
APP_NAME="beancount-autoupdate"
REMOTE_USER="your_username"
REMOTE_HOST="your_server.com"
REMOTE_PATH="/opt/beancount-autoupdate"
SERVICE_NAME="beancount-autoupdate"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 打印信息
info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查构建文件
check_build() {
    if [ ! -f "./build/${APP_NAME}" ]; then
        error "构建文件不存在，请先运行 'make build'"
        exit 1
    fi
    info "构建文件检查通过"
}

# 上传文件
upload_files() {
    info "上传文件到服务器..."
    scp ./build/${APP_NAME} ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_PATH}/
    scp ./config.toml ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_PATH}/
    scp ./.env ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_PATH}/
    info "文件上传完成"
}

# 重启服务
restart_service() {
    info "重启服务..."
    ssh ${REMOTE_USER}@${REMOTE_HOST} << EOF
        cd ${REMOTE_PATH}
        sudo systemctl restart ${SERVICE_NAME}
        sudo systemctl status ${SERVICE_NAME}
EOF
    info "服务重启完成"
}

# 创建 systemd 服务文件
create_service() {
    info "创建 systemd 服务文件..."
    ssh ${REMOTE_USER}@${REMOTE_HOST} << EOF
        sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null << 'EOT'
[Unit]
Description=Beancount AutoUpdate Service
After=network.target

[Service]
Type=simple
User=${REMOTE_USER}
WorkingDirectory=${REMOTE_PATH}
ExecStart=${REMOTE_PATH}/${APP_NAME}
Restart=always
RestartSec=10
Environment=PATH=/usr/local/bin:/usr/bin:/bin

[Install]
WantedBy=multi-user.target
EOT
        sudo systemctl daemon-reload
        sudo systemctl enable ${SERVICE_NAME}
EOF
    info "服务文件创建完成"
}

# 主函数
main() {
    info "开始部署 ${APP_NAME}..."
    
    # 检查构建文件
    check_build
    
    # 创建服务文件（如果不存在）
    if [ "$1" == "--create-service" ]; then
        create_service
    fi
    
    # 上传文件
    upload_files
    
    # 重启服务
    restart_service
    
    info "部署完成！"
    info "服务状态: sudo systemctl status ${SERVICE_NAME}"
    info "查看日志: sudo journalctl -u ${SERVICE_NAME} -f"
}

# 显示帮助
show_help() {
    cat << EOF
Beancount AutoUpdate 部署脚本

用法: $0 [选项]

选项:
    --create-service    创建 systemd 服务文件
    -h, --help          显示帮助信息

示例:
    $0                  # 部署（假设服务已存在）
    $0 --create-service # 首次部署，创建服务文件

注意:
    请先修改脚本中的配置变量（REMOTE_USER, REMOTE_HOST, REMOTE_PATH）
EOF
}

# 处理参数
if [ "$1" == "-h" ] || [ "$1" == "--help" ]; then
    show_help
    exit 0
fi

# 运行主函数
main "$@"