#!/bin/bash
# 容器内 pip 环境检查脚本
# 检查 /opt/python/shared/packages 目录是否为空（通过符号链接连接到 /docker-data/python-packages）

# 不使用 set -e，因为我们需要收集所有错误信息
set +e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# 报告文件路径
REPORT_DIR="/docker-data/logs"
REPORT_FILE="$REPORT_DIR/pip_env_check_$(date +%Y%m%d_%H%M%S).txt"

# 确保报告目录存在
mkdir -p "$REPORT_DIR"

# 函数：写入报告
write_report() {
    echo "$1" | tee -a "$REPORT_FILE"
}

# 函数：输出到控制台和报告
log_both() {
    echo -e "${GREEN}$1${NC}"
    write_report "$1"
}

warn_both() {
    echo -e "${YELLOW}$1${NC}"
    write_report "$1"
}

error_both() {
    echo -e "${RED}$1${NC}" >&2
    write_report "$1"
}

info_both() {
    echo -e "${CYAN}$1${NC}"
    write_report "$1"
}

# 开始检查报告
log_both "========================================"
log_both "容器内 PIP 环境检查报告"
log_both "========================================"
log_both "检查时间: $(date '+%Y-%m-%d %H:%M:%S')"
log_both ""

# 定义路径
PACKAGES_TARGET="/opt/python/shared/packages"
PACKAGES_SOURCE="/docker-data/python-packages"

log_both "检查目标:"
log_both "  pip 环境目录: $PACKAGES_TARGET"
log_both "  挂载源目录: $PACKAGES_SOURCE"
log_both ""

has_error=false
has_warning=false

# ============================================
# 1. 检查符号链接是否正确
# ============================================
log_both "1. 检查符号链接..."
if [ ! -e "$PACKAGES_TARGET" ]; then
    error_both "  ✗ 目标目录不存在: $PACKAGES_TARGET"
    error_both "    符号链接未创建或已损坏"
    has_error=true
elif [ -L "$PACKAGES_TARGET" ]; then
    log_both "  ✓ 目标是一个符号链接"
    
    # 解析符号链接
    link_target=$(readlink -f "$PACKAGES_TARGET")
    log_both "  符号链接指向: $link_target"
    
    # 检查是否指向源目录
    if [ -d "$PACKAGES_SOURCE" ]; then
        source_resolved=$(readlink -f "$PACKAGES_SOURCE")
        if [ "$link_target" = "$source_resolved" ]; then
            log_both "  ✓ 符号链接正确指向挂载目录: $PACKAGES_SOURCE"
        else
            error_both "  ✗ 符号链接指向错误！"
            error_both "    期望指向: $PACKAGES_SOURCE ($source_resolved)"
            error_both "    实际指向: $link_target"
            has_error=true
        fi
    else
        warn_both "  ⚠️  源目录不存在，无法验证符号链接指向"
        has_warning=true
    fi
elif [ -d "$PACKAGES_TARGET" ]; then
    warn_both "  ⚠️  目标是一个普通目录，不是符号链接"
    warn_both "    这可能导致环境不一致，应该是指向 $PACKAGES_SOURCE 的符号链接"
    has_warning=true
else
    error_both "  ✗ 目标既不是目录也不是符号链接"
    has_error=true
fi

log_both ""

# ============================================
# 2. 检查 /opt/python/shared/packages 目录内容是否为空
# ============================================
log_both "2. 检查 pip 环境目录内容..."

# 检查目录是否可访问
if [ ! -d "$PACKAGES_TARGET" ] && [ ! -L "$PACKAGES_TARGET" ]; then
    error_both "  ✗ 目录不存在或不可访问: $PACKAGES_TARGET"
    has_error=true
else
    # 统计文件（通过符号链接访问实际目录）
    file_count=$(find "$PACKAGES_TARGET" -type f 2>/dev/null | wc -l)
    dir_count=$(find "$PACKAGES_TARGET" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | wc -l)
    
    log_both "  目录内容统计:"
    log_both "    总文件数: $file_count"
    log_both "    包目录数: $dir_count"
    
    # 计算目录大小
    if command -v du >/dev/null 2>&1; then
        total_size=$(du -sb "$PACKAGES_TARGET" 2>/dev/null | cut -f1)
        if [ -n "$total_size" ] && [ "$total_size" -gt 0 ]; then
            size_mb=$(echo "scale=2; $total_size / 1024 / 1024" | bc 2>/dev/null || echo "N/A")
            size_gb=$(echo "scale=2; $total_size / 1024 / 1024 / 1024" | bc 2>/dev/null || echo "N/A")
            log_both "    总大小: $size_mb MB ($size_gb GB)"
        else
            log_both "    总大小: 0 MB"
        fi
    fi
    
    # 检查是否为空（这是关键检查）
    if [ "$file_count" -eq 0 ] && [ "$dir_count" -eq 0 ]; then
        error_both ""
        error_both "  ✗✗✗ 目录为空！没有找到任何文件或包 ✗✗✗"
        error_both "    这表示 pip 环境包配置不正确"
        error_both ""
        error_both "    问题分析:"
        error_both "      - /opt/python/shared/packages 目录为空"
        error_both "      - 符号链接可能指向了空的挂载目录"
        error_both "      - 宿主机目录 E:\\case2025\\VibeVoice.git\\index-tts-main\\docker-data\\python-packages 可能为空"
        error_both ""
        error_both "    解决方案:"
        error_both "      1. 检查宿主机目录是否有内容"
        error_both "      2. 运行: pip install --target \"$PACKAGES_SOURCE\" -r /docker-data/requirements.txt"
        error_both "      3. 确保 Docker 卷挂载配置正确"
        has_error=true
    elif [ "$dir_count" -eq 0 ]; then
        warn_both "  ⚠️  目录中没有找到 Python 包目录，但存在 $file_count 个文件"
        warn_both "    这可能不是标准的 pip 安装目录结构"
        has_warning=true
    else
        log_both "  ✓ 目录包含 $dir_count 个包目录和 $file_count 个文件"
        log_both "  ✓ pip 环境包配置正确"
        
        # 显示示例包
        log_both ""
        log_both "  示例包列表 (前10个):"
        count=0
        find "$PACKAGES_TARGET" -mindepth 1 -maxdepth 1 -type d | head -10 | while read pkg; do
            count=$((count + 1))
            pkg_name=$(basename "$pkg")
            pkg_files=$(find "$pkg" -type f 2>/dev/null | wc -l)
            
            if command -v du >/dev/null 2>&1; then
                pkg_size=$(du -sb "$pkg" 2>/dev/null | cut -f1)
                if [ -n "$pkg_size" ] && [ "$pkg_size" -gt 0 ]; then
                    pkg_size_mb=$(echo "scale=2; $pkg_size / 1024 / 1024" | bc 2>/dev/null || echo "N/A")
                    log_both "    $count. $pkg_name ($pkg_files 个文件, $pkg_size_mb MB)"
                else
                    log_both "    $count. $pkg_name ($pkg_files 个文件)"
                fi
            else
                log_both "    $count. $pkg_name ($pkg_files 个文件)"
            fi
        done
        
        if [ "$dir_count" -gt 10 ]; then
            log_both "    ... 还有 $((dir_count - 10)) 个包未显示"
        fi
    fi
fi

log_both ""

# ============================================
# 3. 检查关键包
# ============================================
log_both "3. 检查关键 Python 包..."
critical_packages=("torch" "numpy" "transformers" "librosa" "soundfile" "pika" "omegaconf")

for pkg in "${critical_packages[@]}"; do
    pkg_path="$PACKAGES_TARGET/$pkg"
    if [ -d "$pkg_path" ]; then
        # 检查包的主要文件
        if [ -f "$pkg_path/__init__.py" ] || [ -f "$pkg_path/__init__.pyc" ] || find "$pkg_path" -name "*.py" -o -name "*.so" 2>/dev/null | head -1 | grep -q .; then
            log_both "  ✓ $pkg - 已安装且包含文件"
        else
            warn_both "  ⚠️  $pkg - 目录存在但可能不完整"
            has_warning=true
        fi
    else
        error_both "  ✗ $pkg - 未安装"
        has_error=true
    fi
done

log_both ""

# ============================================
# 4. 验证 Python 可以导入（如果目录不为空）
# ============================================
if [ "$has_error" = false ] && [ "$dir_count" -gt 0 ]; then
    log_both "4. 验证 Python 导入..."
    
    if command -v python3 >/dev/null 2>&1; then
        # 测试导入关键包
        test_packages=("torch" "numpy")
        for pkg in "${test_packages[@]}"; do
            if python3 -c "import sys; sys.path.insert(0, '$PACKAGES_TARGET'); import $pkg" 2>/dev/null; then
                pkg_version=$(python3 -c "import sys; sys.path.insert(0, '$PACKAGES_TARGET'); import $pkg; print($pkg.__version__)" 2>/dev/null || echo "unknown")
                log_both "  ✓ $pkg - 可以导入 (版本: $pkg_version)"
            else
                warn_both "  ⚠️  $pkg - 无法导入（可能路径配置问题）"
                has_warning=true
            fi
        done
    else
        warn_both "  ⚠️  Python3 未找到，跳过导入测试"
        has_warning=true
    fi
    
    log_both ""
fi

# ============================================
# 总结
# ============================================
log_both "========================================"
if [ "$has_error" = true ]; then
    error_both "检查结果: [失败] pip 环境目录为空或配置错误"
    error_both ""
    error_both "关键问题: /opt/python/shared/packages 目录为空"
    error_both "这表示环境包配置不正确，需要安装 Python 包"
    error_both ""
    error_both "建议操作:"
    error_both "  1. 检查宿主机目录: E:\\case2025\\VibeVoice.git\\index-tts-main\\docker-data\\python-packages"
    error_both "  2. 在容器内运行: pip install --target \"$PACKAGES_TARGET\" -r /docker-data/requirements.txt"
    error_both "  3. 或确保 Docker 卷正确挂载了包含包的目录"
    exit_code=1
elif [ "$has_warning" = true ]; then
    warn_both "检查结果: [警告] 环境可用，但存在警告"
    exit_code=0
else
    log_both "检查结果: [通过] pip 环境检查完全通过"
    log_both "  /opt/python/shared/packages 目录包含 $dir_count 个包"
    exit_code=0
fi
log_both "========================================"
log_both ""
log_both "报告已保存到: $REPORT_FILE"

exit $exit_code
