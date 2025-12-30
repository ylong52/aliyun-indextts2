#!/bin/bash

# 清理脚本 - 删除临时文件和缓存
# 使用方法: chmod +x cleanup.sh && ./cleanup.sh

echo "========================================"
echo "项目清理脚本"
echo "========================================"
echo ""

# 确认操作
read -p "确认要删除临时文件、缓存和测试输出吗？(y/N): " confirm
if [ "$confirm" != "y" ] && [ "$confirm" != "Y" ]; then
    echo "操作已取消"
    exit 0
fi

echo ""
echo "开始清理项目文件..."
echo ""

# 1. 删除Python依赖缓存
if [ -d "python_deps" ]; then
    echo "[1/5] 删除 python_deps/..."
    size=$(du -sh python_deps 2>/dev/null | cut -f1)
    rm -rf python_deps
    echo "      ✓ 已删除 (释放约 $size)"
else
    echo "[1/5] python_deps/ 不存在，跳过"
fi

# 2. 删除测试输出
if [ -d "tests/output" ]; then
    echo "[2/5] 删除测试输出文件..."
    count=$(ls -1 tests/output/*.wav 2>/dev/null | wc -l)
    if [ "$count" -gt 0 ]; then
        rm -f tests/output/*.wav
        echo "      ✓ 已删除 $count 个测试输出文件"
    else
        echo "      ✓ 没有测试输出文件"
    fi
else
    echo "[2/5] tests/output/ 不存在，跳过"
fi

# 3. 删除日志文件
if [ -d "logs" ]; then
    echo "[3/5] 删除日志文件..."
    count=$(ls -1 logs/*.log 2>/dev/null | wc -l)
    if [ "$count" -gt 0 ]; then
        rm -f logs/*.log
        echo "      ✓ 已删除 $count 个日志文件"
    else
        echo "      ✓ 没有日志文件"
    fi
else
    echo "[3/5] logs/ 不存在，跳过"
fi

# 4. 删除编译产物
if [ -f "aliyun/run_ecs.exe" ]; then
    echo "[4/5] 删除编译产物..."
    rm -f aliyun/run_ecs.exe
    echo "      ✓ 已删除 run_ecs.exe"
else
    echo "[4/5] run_ecs.exe 不存在，跳过"
fi

# 5. 删除Python缓存
echo "[5/5] 删除Python缓存..."
cache_dirs=$(find . -type d -name __pycache__ 2>/dev/null | wc -l)
pyc_files=$(find . -type f -name "*.pyc" 2>/dev/null | wc -l)
pyo_files=$(find . -type f -name "*.pyo" 2>/dev/null | wc -l)
total=$((cache_dirs + pyc_files + pyo_files))

if [ "$total" -gt 0 ]; then
    find . -type d -name __pycache__ -exec rm -r {} + 2>/dev/null
    find . -type f -name "*.pyc" -delete 2>/dev/null
    find . -type f -name "*.pyo" -delete 2>/dev/null
    find . -type f -name "*.pyd" -delete 2>/dev/null
    echo "      ✓ 已删除 $total 个缓存项"
else
    echo "      ✓ 没有Python缓存文件"
fi

echo ""
echo "========================================"
echo "清理完成！"
echo "========================================"
echo ""
echo "提示:"
echo "  - 请检查 .gitignore 文件是否包含相应的忽略规则"
echo "  - 删除 python_deps 后，可通过 'uv sync' 重新安装依赖"
echo "  - 测试输出文件可通过运行测试重新生成"
echo ""

