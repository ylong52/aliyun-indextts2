#!/bin/bash
# OSS 文件预热脚本
# 在容器启动时执行此脚本，预热关键文件到本地缓存

set -e

echo "=========================================="
echo "OSS 文件预热脚本"
echo "=========================================="

# 检查依赖包路径
PACKAGES_PATH=${EXTERNAL_PACKAGES:-/opt/python/shared/packages}
CHECKPOINTS_PATH=${APP_HOME:-/python_indextts_code}/checkpoints

echo "依赖包路径: $PACKAGES_PATH"
echo "Checkpoints 路径: $CHECKPOINTS_PATH"

# 1. 预热 PyTorch（触发库文件加载）
echo ""
echo "1. 预热 PyTorch..."
if [ -d "$PACKAGES_PATH/torch" ]; then
    timeout 300 python3 <<EOF
import sys
import time
sys.path.insert(0, '$PACKAGES_PATH')
print("  开始导入 torch...")
start = time.time()
import torch
elapsed = time.time() - start
print(f"  ✓ PyTorch {torch.__version__} 已加载 (耗时: {elapsed:.1f}秒)")
EOF
else
    echo "  ⚠️  PyTorch 目录不存在，跳过"
fi

# 2. 预热模型文件（读取前 100MB 触发 OSSFS 缓存）
echo ""
echo "2. 预热模型文件..."

if [ -f "$CHECKPOINTS_PATH/gpt.pth" ]; then
    echo "  预热 gpt.pth (读取前 100MB)..."
    timeout 300 dd if="$CHECKPOINTS_PATH/gpt.pth" of=/dev/null bs=1M count=100 2>&1 | tail -1
    echo "  ✓ gpt.pth 预热完成"
else
    echo "  ⚠️  gpt.pth 不存在，跳过"
fi

if [ -f "$CHECKPOINTS_PATH/s2mel.pth" ]; then
    echo "  预热 s2mel.pth (读取前 100MB)..."
    timeout 300 dd if="$CHECKPOINTS_PATH/s2mel.pth" of=/dev/null bs=1M count=100 2>&1 | tail -1
    echo "  ✓ s2mel.pth 预热完成"
else
    echo "  ⚠️  s2mel.pth 不存在，跳过"
fi

# 3. 预热配置文件
echo ""
echo "3. 预热配置文件..."
if [ -f "$CHECKPOINTS_PATH/config.yaml" ]; then
    cat "$CHECKPOINTS_PATH/config.yaml" > /dev/null
    echo "  ✓ config.yaml 已读取"
fi

echo ""
echo "=========================================="
echo "预热完成"
echo "=========================================="

