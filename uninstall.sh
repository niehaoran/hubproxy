#!/bin/bash

# HubProxy 卸载脚本
set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置 - 对应安装脚本中的路径
SERVICE_NAME="hubproxy"
INSTALL_DIR="/opt/hubproxy"
LOG_DIR="/var/log/hubproxy"
SYSTEMD_SERVICE="/etc/systemd/system/${SERVICE_NAME}.service"

echo -e "${BLUE}HubProxy 卸载脚本${NC}"
echo "================================================="

# 检查是否以root权限运行
if [[ $EUID -ne 0 ]]; then
    echo -e "${RED}此脚本需要root权限运行${NC}"
    echo "请使用: sudo $0"
    exit 1
fi

# 确认卸载
read -p "$(echo -e ${YELLOW}确定要卸载 HubProxy 吗？[y/N]: ${NC})" -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${BLUE}取消卸载${NC}"
    exit 0
fi

echo -e "${YELLOW}开始卸载 HubProxy...${NC}"

# 1. 停止并禁用服务
if systemctl is-active --quiet ${SERVICE_NAME} 2>/dev/null; then
    echo -e "${BLUE}停止服务: ${SERVICE_NAME}${NC}"
    systemctl stop ${SERVICE_NAME}
else
    echo -e "${YELLOW}服务未运行: ${SERVICE_NAME}${NC}"
fi

if systemctl is-enabled --quiet ${SERVICE_NAME} 2>/dev/null; then
    echo -e "${BLUE}禁用服务: ${SERVICE_NAME}${NC}"
    systemctl disable ${SERVICE_NAME}
else
    echo -e "${YELLOW}服务未启用: ${SERVICE_NAME}${NC}"
fi

# 2. 删除systemd服务文件
if [ -f "${SYSTEMD_SERVICE}" ]; then
    echo -e "${BLUE}删除systemd服务文件${NC}"
    rm -f "${SYSTEMD_SERVICE}"
    systemctl daemon-reload
else
    echo -e "${YELLOW}systemd服务文件不存在${NC}"
fi

# 3. 询问是否保留配置文件
KEEP_CONFIG=false
if [ -d "${INSTALL_DIR}" ]; then
    read -p "$(echo -e ${YELLOW}是否保留配置文件？[y/N]: ${NC})" -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        KEEP_CONFIG=true
        echo -e "${BLUE}将保留配置文件在 ${INSTALL_DIR}${NC}"
    fi
fi

# 4. 删除安装目录
if [ -d "${INSTALL_DIR}" ]; then
    if [ "$KEEP_CONFIG" = true ]; then
        echo -e "${BLUE}删除二进制文件，保留配置${NC}"
        rm -f "${INSTALL_DIR}/hubproxy"
        rm -f "${INSTALL_DIR}/hubproxy.service"
        echo -e "${GREEN}配置文件保留在: ${INSTALL_DIR}${NC}"
    else
        echo -e "${BLUE}删除安装目录: ${INSTALL_DIR}${NC}"
        rm -rf "${INSTALL_DIR}"
    fi
else
    echo -e "${YELLOW}安装目录不存在: ${INSTALL_DIR}${NC}"
fi

# 5. 询问是否删除日志文件
if [ -d "${LOG_DIR}" ]; then
    read -p "$(echo -e ${YELLOW}是否删除日志文件？[y/N]: ${NC})" -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${BLUE}删除日志目录: ${LOG_DIR}${NC}"
        rm -rf "${LOG_DIR}"
    else
        echo -e "${GREEN}日志文件保留在: ${LOG_DIR}${NC}"
    fi
else
    echo -e "${YELLOW}日志目录不存在: ${LOG_DIR}${NC}"
fi

# 6. 检查是否有残留的Docker配置
echo -e "${BLUE}检查Docker配置...${NC}"
if command -v docker &> /dev/null; then
    # 检查是否有使用localhost:5000的镜像
    LOCAL_IMAGES=$(docker images localhost:5000/* --format "table {{.Repository}}:{{.Tag}}" 2>/dev/null | tail -n +2 || true)
    if [ ! -z "$LOCAL_IMAGES" ]; then
        echo -e "${YELLOW}发现通过代理拉取的镜像:${NC}"
        echo "$LOCAL_IMAGES"
        read -p "$(echo -e ${YELLOW}是否删除这些镜像？[y/N]: ${NC})" -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            echo -e "${BLUE}删除代理镜像...${NC}"
            docker rmi $(docker images localhost:5000/* -q) 2>/dev/null || true
        fi
    fi
    
    # 检查是否有登录信息
    if docker info 2>/dev/null | grep -q "localhost:5000"; then
        echo -e "${YELLOW}发现Docker登录信息${NC}"
        read -p "$(echo -e ${YELLOW}是否清除localhost:5000的登录信息？[y/N]: ${NC})" -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            docker logout localhost:5000 2>/dev/null || true
        fi
    fi
fi

# 7. 显示卸载结果
echo ""
echo -e "${GREEN}HubProxy 卸载完成！${NC}"
echo ""
echo "已删除的组件："
echo "  ✓ systemd 服务"
echo "  ✓ 二进制文件"
if [ "$KEEP_CONFIG" = false ]; then
    echo "  ✓ 配置文件"
fi

if [ "$KEEP_CONFIG" = true ]; then
    echo ""
    echo -e "${BLUE}保留的文件:${NC}"
    echo "  • 配置文件: ${INSTALL_DIR}"
fi

if [ -d "${LOG_DIR}" ]; then
    echo "  • 日志文件: ${LOG_DIR}"
fi

echo ""
echo -e "${BLUE}如需重新安装，请运行:${NC}"
echo "curl -fsSL https://raw.githubusercontent.com/sky22333/hubproxy/main/install.sh | sudo bash" 