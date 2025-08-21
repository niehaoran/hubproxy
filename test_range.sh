#!/bin/bash

echo "=== 断点续传功能测试 ==="

# 假设你的服务运行在localhost:8080
BASE_URL="http://localhost:8080"

echo "1. 测试GitHub文件的Range请求支持..."

# 测试获取文件的前1024字节
echo "  - 请求前1024字节"
curl -H "Range: bytes=0-1023" \
     -I "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -v

echo ""
echo "  - 请求中间部分 (1024-2047字节)"
curl -H "Range: bytes=1024-2047" \
     -I "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -v

echo ""
echo "  - 请求最后500字节"
curl -H "Range: bytes=-500" \
     -I "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -v

echo ""
echo "2. 测试实际下载断点续传..."

# 创建测试下载
TEST_FILE="test_download.sh"
PART1_FILE="part1.tmp"
PART2_FILE="part2.tmp"

echo "  - 下载前半部分"
curl -H "Range: bytes=0-1023" \
     "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -o "$PART1_FILE" \
     -v

echo "  - 下载后半部分"
curl -H "Range: bytes=1024-" \
     "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -o "$PART2_FILE" \
     -v

echo "  - 合并文件"
cat "$PART1_FILE" "$PART2_FILE" > "$TEST_FILE"

echo "  - 验证完整性"
curl "${BASE_URL}/https://github.com/docker/docker-install/raw/master/install.sh" \
     -o "complete.sh" \
     -v

if cmp -s "$TEST_FILE" "complete.sh"; then
    echo "✅ 断点续传测试成功！文件完整性验证通过"
else
    echo "❌ 断点续传测试失败！文件不完整"
fi

# 清理临时文件
rm -f "$PART1_FILE" "$PART2_FILE" "$TEST_FILE" "complete.sh"

echo ""
echo "3. 测试Docker blob的Range请求..."
echo "  注意：需要先有一个有效的Docker镜像digest来测试"
echo "  可以通过以下命令获取："
echo "  curl ${BASE_URL}/v2/library/alpine/manifests/latest"

echo ""
echo "=== 测试完成 ===" 