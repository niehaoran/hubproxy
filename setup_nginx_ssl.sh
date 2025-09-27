#!/bin/bash

# 一键设置nginx反代理+SSL证书脚本
# 域名: docker.budiuyun.net
# 反代端口: 5000
# 作者: Assistant

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置变量
DOMAIN="docker.budiuyun.net"
PORT="5000"
EMAIL="admin@budiuyun.net"  # 请修改为你的邮箱

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查是否为root用户
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "此脚本需要root权限运行"
        echo "请使用: sudo $0"
        exit 1
    fi
}

# 检测操作系统
detect_os() {
    if [[ -f /etc/debian_version ]]; then
        OS="debian"
        INSTALL_CMD="apt update && apt install -y"
    elif [[ -f /etc/redhat-release ]]; then
        OS="redhat"
        INSTALL_CMD="yum install -y"
    else
        log_error "不支持的操作系统"
        exit 1
    fi
    log_info "检测到操作系统: $OS"
}

# 安装必要软件包
install_packages() {
    log_info "开始安装nginx和certbot..."
    
    if [[ $OS == "debian" ]]; then
        apt update
        apt install -y nginx certbot python3-certbot-nginx curl dnsutils
        # 安装snapd和certbot（推荐方式）
        apt install -y snapd
        snap install core; snap refresh core
        snap install --classic certbot
        ln -sf /snap/bin/certbot /usr/bin/certbot
    else
        yum install -y epel-release
        yum install -y nginx certbot python3-certbot-nginx curl bind-utils
    fi
    
    log_success "软件包安装完成"
}

# 备份原始nginx配置
backup_nginx_config() {
    if [[ -f /etc/nginx/nginx.conf ]]; then
        cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.backup.$(date +%Y%m%d_%H%M%S)
        log_info "已备份原始nginx配置"
    fi
}

# 配置nginx WebSocket支持
configure_websocket_support() {
    log_info "配置nginx WebSocket支持..."
    
    # 检查是否已经存在WebSocket配置
    if ! grep -q "connection_upgrade" /etc/nginx/nginx.conf; then
        # 在http块中添加WebSocket支持的map指令
        sed -i '/http {/a\    # WebSocket upgrade support\n    map $http_upgrade $connection_upgrade {\n        default upgrade;\n        '"'"''"'"' close;\n    }\n' /etc/nginx/nginx.conf
        log_info "已添加WebSocket支持配置"
    else
        log_info "WebSocket支持配置已存在"
    fi
}

# 创建nginx配置
create_nginx_config() {
    log_info "创建nginx配置文件..."
    
    # 确保目录存在
    mkdir -p /etc/nginx/sites-available
    mkdir -p /etc/nginx/sites-enabled
    
    # 添加sites-enabled到主配置（如果不存在）
    if ! grep -q "sites-enabled" /etc/nginx/nginx.conf; then
        sed -i '/http {/a\    include /etc/nginx/sites-enabled/*;' /etc/nginx/nginx.conf
    fi
    
    # 创建配置文件
    cat > /etc/nginx/sites-available/$DOMAIN << 'EOF'
# 初始HTTP配置（用于申请SSL证书）
server {
    listen 80;
    server_name docker.budiuyun.net;
    
    # Let's Encrypt验证路径
    location /.well-known/acme-challenge/ {
        root /var/www/html;
        allow all;
    }
    
    # 临时反代配置
    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # CORS配置
        add_header 'Access-Control-Allow-Origin' $http_origin always;
        add_header 'Access-Control-Allow-Credentials' 'true' always;
        add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
        add_header 'Access-Control-Allow-Headers' '*' always;
        
        # 处理OPTIONS预检请求
        if ($request_method = 'OPTIONS') {
            add_header 'Access-Control-Allow-Origin' $http_origin always;
            add_header 'Access-Control-Allow-Credentials' 'true' always;
            add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
            add_header 'Access-Control-Allow-Headers' '*' always;
            add_header 'Access-Control-Max-Age' 1728000;
            add_header 'Content-Type' 'text/plain charset=UTF-8';
            add_header 'Content-Length' 0;
            return 204;
        }
    }
}
EOF
    
    # 启用站点
    ln -sf /etc/nginx/sites-available/$DOMAIN /etc/nginx/sites-enabled/
    
    # 移除默认站点（如果存在）
    rm -f /etc/nginx/sites-enabled/default
    
    log_success "nginx配置文件创建完成"
}

# 设置防火墙
setup_firewall() {
    log_info "配置防火墙..."
    
    if command -v ufw &> /dev/null; then
        ufw allow 'Nginx Full'
        ufw allow ssh
        log_info "UFW防火墙配置完成"
    elif command -v firewall-cmd &> /dev/null; then
        firewall-cmd --permanent --add-service=http
        firewall-cmd --permanent --add-service=https
        firewall-cmd --permanent --add-service=ssh
        firewall-cmd --reload
        log_info "Firewalld防火墙配置完成"
    else
        log_warning "未检测到防火墙，请手动开放80和443端口"
    fi
}

# 启动nginx
start_nginx() {
    log_info "启动nginx服务..."
    
    # 测试配置
    nginx -t
    
    # 启动服务
    systemctl enable nginx
    systemctl start nginx
    systemctl reload nginx
    
    log_success "nginx服务启动完成"
}

# 检查域名解析
check_domain() {
    log_info "检查域名解析..."
    
    if nslookup $DOMAIN &> /dev/null; then
        log_success "域名解析正常"
        return 0
    else
        log_warning "域名解析可能有问题，但继续执行..."
        return 1
    fi
}

# 申请SSL证书
request_ssl() {
    log_info "申请SSL证书..."
    
    # 确保www目录存在
    mkdir -p /var/www/html
    
    # 申请证书
    certbot --nginx -d $DOMAIN --non-interactive --agree-tos --email $EMAIL --redirect
    
    if [[ $? -eq 0 ]]; then
        log_success "SSL证书申请成功"
    else
        log_error "SSL证书申请失败"
        return 1
    fi
}

# 更新nginx配置为完整版本
update_nginx_config() {
    log_info "更新nginx配置为完整版本..."
    
    cat > /etc/nginx/sites-available/$DOMAIN << 'EOF'
# 创建地理位置映射文件，用于IP白名单
geo $allowed_ip {
    default 0;
    # 添加server.budiuyun.net解析的IP地址
    # 注意：这里需要手动添加或通过脚本动态获取
    # 示例：如果server.budiuyun.net解析到1.2.3.4，则添加：
    # 1.2.3.4 1;
    
    # 本地回环地址（用于测试和维护）
    127.0.0.1 1;
    ::1 1;
}

server {
    listen 80;
    server_name docker.budiuyun.net;
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name docker.budiuyun.net;
    
    # SSL证书配置
    ssl_certificate /etc/letsencrypt/live/docker.budiuyun.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/docker.budiuyun.net/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
    
    # 安全头
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options DENY always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    
    # IP访问限制
    if ($allowed_ip = 0) {
        return 403;
    }
    
    # 反代理配置
    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header REMOTE-HOST $remote_addr;
        
        # WebSocket支持
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        
        # 超时设置
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
        
        # CORS配置
        add_header 'Access-Control-Allow-Origin' $http_origin always;
        add_header 'Access-Control-Allow-Credentials' 'true' always;
        add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
        add_header 'Access-Control-Allow-Headers' '*' always;
        
        # 处理OPTIONS预检请求
        if ($request_method = 'OPTIONS') {
            add_header 'Access-Control-Allow-Origin' $http_origin always;
            add_header 'Access-Control-Allow-Credentials' 'true' always;
            add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
            add_header 'Access-Control-Allow-Headers' '*' always;
            add_header 'Access-Control-Max-Age' 1728000;
            add_header 'Content-Type' 'text/plain charset=UTF-8';
            add_header 'Content-Length' 0;
            return 204;
        }
    }
    
    # 静态文件缓存
    location ~* \.(gif|png|jpg|css|js|woff|woff2|ico|svg)$ {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
    
    # 日志配置
    access_log /var/log/nginx/docker.budiuyun.net.access.log;
    error_log /var/log/nginx/docker.budiuyun.net.error.log;
}
EOF
    
    # 重载nginx
    nginx -t && systemctl reload nginx
    log_success "nginx配置更新完成"
}

# 获取server.budiuyun.net的IP地址并更新白名单
update_ip_whitelist() {
    log_info "获取server.budiuyun.net的IP地址并更新白名单..."
    
    # 获取server.budiuyun.net的所有IP地址
    SERVER_IPS=$(dig +short server.budiuyun.net | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$')
    
    if [[ -z "$SERVER_IPS" ]]; then
        log_warning "无法解析server.budiuyun.net的IP地址，将只允许本地访问"
        SERVER_IPS=""
    else
        log_success "获取到server.budiuyun.net的IP地址: $SERVER_IPS"
    fi
    
    # 重新生成nginx配置文件，包含最新的IP白名单
    cat > /etc/nginx/sites-available/$DOMAIN << EOF
# 创建地理位置映射文件，用于IP白名单
geo \$allowed_ip {
    default 0;
    
    # 本地回环地址（用于测试和维护）
    127.0.0.1 1;
    ::1 1;
    
    # server.budiuyun.net解析的IP地址
$(for ip in $SERVER_IPS; do echo "    $ip 1;"; done)
}

server {
    listen 80;
    server_name docker.budiuyun.net;
    return 301 https://\$server_name\$request_uri;
}

server {
    listen 443 ssl http2;
    server_name docker.budiuyun.net;
    
    # SSL证书配置
    ssl_certificate /etc/letsencrypt/live/docker.budiuyun.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/docker.budiuyun.net/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
    
    # 安全头
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options DENY always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    
    # IP访问限制
    if (\$allowed_ip = 0) {
        return 403;
    }
    
    # 反代理配置
    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header REMOTE-HOST \$remote_addr;
        
        # WebSocket支持
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
        
        # 超时设置
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
        
        # CORS配置
        add_header 'Access-Control-Allow-Origin' \$http_origin always;
        add_header 'Access-Control-Allow-Credentials' 'true' always;
        add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
        add_header 'Access-Control-Allow-Headers' '*' always;
        
        # 处理OPTIONS预检请求
        if (\$request_method = 'OPTIONS') {
            add_header 'Access-Control-Allow-Origin' \$http_origin always;
            add_header 'Access-Control-Allow-Credentials' 'true' always;
            add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
            add_header 'Access-Control-Allow-Headers' '*' always;
            add_header 'Access-Control-Max-Age' 1728000;
            add_header 'Content-Type' 'text/plain charset=UTF-8';
            add_header 'Content-Length' 0;
            return 204;
        }
    }
    
    # 静态文件缓存
    location ~* \.(gif|png|jpg|css|js|woff|woff2|ico|svg)\$ {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
    
    # 日志配置
    access_log /var/log/nginx/docker.budiuyun.net.access.log;
    error_log /var/log/nginx/docker.budiuyun.net.error.log;
}
EOF
    
    # 重载nginx
    nginx -t && systemctl reload nginx
    log_success "IP白名单更新完成"
}

# 设置自动续期
setup_auto_renewal() {
    log_info "设置SSL证书自动续期..."
    
    # 创建续期脚本
    cat > /etc/cron.d/certbot-renew << 'EOF'
# 每天检查两次证书续期
0 12 * * * root /usr/bin/certbot renew --quiet --nginx
0 0 * * * root /usr/bin/certbot renew --quiet --nginx
EOF

    # 创建IP白名单更新脚本
    cat > /usr/local/bin/update-docker-whitelist.sh << 'EOF'
#!/bin/bash
# 更新docker.budiuyun.net的IP白名单

DOMAIN="docker.budiuyun.net"
SERVER_IPS=$(dig +short server.budiuyun.net | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$')

cat > /etc/nginx/sites-available/$DOMAIN << EOF_NGINX
# 创建地理位置映射文件，用于IP白名单
geo \$allowed_ip {
    default 0;
    
    # 本地回环地址（用于测试和维护）
    127.0.0.1 1;
    ::1 1;
    
    # server.budiuyun.net解析的IP地址
$(for ip in $SERVER_IPS; do echo "    $ip 1;"; done)
}

server {
    listen 80;
    server_name docker.budiuyun.net;
    return 301 https://\$server_name\$request_uri;
}

server {
    listen 443 ssl http2;
    server_name docker.budiuyun.net;
    
    # SSL证书配置
    ssl_certificate /etc/letsencrypt/live/docker.budiuyun.net/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/docker.budiuyun.net/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
    
    # 安全头
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options DENY always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    
    # IP访问限制
    if (\$allowed_ip = 0) {
        return 403;
    }
    
    # 反代理配置
    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header REMOTE-HOST \$remote_addr;
        
        # WebSocket支持
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
        
        # 超时设置
        proxy_connect_timeout 60s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
        
        # CORS配置
        add_header 'Access-Control-Allow-Origin' \$http_origin always;
        add_header 'Access-Control-Allow-Credentials' 'true' always;
        add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
        add_header 'Access-Control-Allow-Headers' '*' always;
        
        # 处理OPTIONS预检请求
        if (\$request_method = 'OPTIONS') {
            add_header 'Access-Control-Allow-Origin' \$http_origin always;
            add_header 'Access-Control-Allow-Credentials' 'true' always;
            add_header 'Access-Control-Allow-Methods' 'GET, POST, PUT, DELETE, PATCH, OPTIONS' always;
            add_header 'Access-Control-Allow-Headers' '*' always;
            add_header 'Access-Control-Max-Age' 1728000;
            add_header 'Content-Type' 'text/plain charset=UTF-8';
            add_header 'Content-Length' 0;
            return 204;
        }
    }
    
    # 静态文件缓存
    location ~* \.(gif|png|jpg|css|js|woff|woff2|ico|svg)\$ {
        proxy_pass http://127.0.0.1:5000;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
    
    # 日志配置
    access_log /var/log/nginx/docker.budiuyun.net.access.log;
    error_log /var/log/nginx/docker.budiuyun.net.error.log;
}
EOF_NGINX

# 测试配置并重载nginx
nginx -t && systemctl reload nginx
EOF

    chmod +x /usr/local/bin/update-docker-whitelist.sh
    
    # 添加IP白名单更新的定时任务（每半小时检查一次）
    cat > /etc/cron.d/docker-whitelist-update << 'EOF'
# 每半小时更新一次docker.budiuyun.net的IP白名单
0,30 * * * * root /usr/local/bin/update-docker-whitelist.sh >/dev/null 2>&1
EOF
    
    # 测试续期
    certbot renew --dry-run
    
    log_success "自动续期设置完成"
}

# 验证安装
verify_installation() {
    log_info "验证安装结果..."
    
    # 检查nginx状态
    if systemctl is-active --quiet nginx; then
        log_success "nginx服务运行正常"
    else
        log_error "nginx服务未运行"
    fi
    
    # 检查SSL证书
    if [[ -f /etc/letsencrypt/live/$DOMAIN/fullchain.pem ]]; then
        log_success "SSL证书存在"
    else
        log_error "SSL证书不存在"
    fi
    
    # 检查端口5000是否有服务
    if netstat -tlnp | grep -q ":5000 "; then
        log_success "检测到5000端口有服务运行"
    else
        log_warning "5000端口没有检测到服务，请确保你的应用在5000端口运行"
    fi
    
    echo ""
    log_success "=== 安装完成 ==="
    echo -e "${GREEN}域名访问地址: ${NC}https://$DOMAIN"
    echo -e "${GREEN}nginx配置文件: ${NC}/etc/nginx/sites-available/$DOMAIN"
    echo -e "${GREEN}SSL证书位置: ${NC}/etc/letsencrypt/live/$DOMAIN/"
    echo -e "${GREEN}访问日志: ${NC}/var/log/nginx/$DOMAIN.access.log"
    echo -e "${GREEN}错误日志: ${NC}/var/log/nginx/$DOMAIN.error.log"
    echo ""
    echo -e "${YELLOW}重要提醒:${NC}"
    echo -e "1. 确保你的应用在5000端口正常运行"
    echo -e "2. 域名 $DOMAIN 必须解析到本服务器IP"
    echo -e "3. SSL证书会自动续期，无需手动维护"
    echo -e "4. ${RED}访问限制：${NC}只有server.budiuyun.net解析的IP地址可以访问"
    echo -e "5. IP白名单每半小时自动更新一次"
    echo -e "6. 手动更新IP白名单：/usr/local/bin/update-docker-whitelist.sh"
    echo ""
}

# 主函数
main() {
    echo -e "${BLUE}"
    echo "=================================================="
    echo "      Nginx反代理+SSL证书一键安装脚本"
    echo "=================================================="
    echo -e "${NC}"
    echo "域名: $DOMAIN"
    echo "反代端口: $PORT"
    echo "邮箱: $EMAIL"
    echo ""
    
    read -p "确认继续安装？(y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        log_info "安装已取消"
        exit 0
    fi
    
    check_root
    detect_os
    install_packages
    backup_nginx_config
    configure_websocket_support
    setup_firewall
    create_nginx_config
    start_nginx
    check_domain
    request_ssl
    update_ip_whitelist
    setup_auto_renewal
    verify_installation
}

# 执行主函数
main "$@" 