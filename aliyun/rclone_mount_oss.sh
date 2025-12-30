#!/bin/bash
set -e
# Guard set -o pipefail to avoid "Illegal option -o pipefail" when executed under /bin/sh (dash).
if [ -n "${BASH_VERSION:-}" ]; then
    set -o pipefail
fi

# 终端颜色
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
CYAN="\033[36m"
RESET="\033[0m"

# 错误处理函数
error_exit() {
    echo -e "${RED}❌ 错误: $1${RESET}" >&2
    exit 1
}

# 尝试自动安装 rclone（在 rclone 未安装时调用）
install_rclone() {
    echo "尝试自动安装 rclone..."
    ARCH=$(uname -m)
    [ "$ARCH" = "x86_64" ] && ARCH="amd64" || ARCH="arm64"
    if [ -f /etc/debian_version ]; then
        apt-get update -qq || true
        if apt-get install -y rclone 2>&1; then
            echo "通过 apt 安装 rclone 成功"
            return 0
        fi
        apt-get install -y fuse wget unzip || true
        cd /tmp || return 1
        wget -q "https://github.com/rclone/rclone/releases/download/v1.66.0/rclone-v1.66.0-linux-${ARCH}.zip" -O r.zip || \
        curl -L "https://github.com/rclone/rclone/releases/download/v1.66.0/rclone-v1.66.0-linux-${ARCH}.zip" -o r.zip || \
        return 1
        unzip -q r.zip || return 1
        cp rclone-v1.66.0-linux-${ARCH}/rclone /usr/local/bin/ || return 1
        chmod +x /usr/local/bin/rclone || return 1
        rm -rf r.zip rclone-v1.66.0-linux-${ARCH}
        return 0
    elif [ -f /etc/redhat-release ]; then
        yum install -y fuse fuse-libs wget unzip || true
        cd /tmp || return 1
        wget -q "https://github.com/rclone/rclone/releases/download/v1.66.0/rclone-v1.66.0-linux-${ARCH}.zip" -O r.zip || \
        curl -L "https://github.com/rclone/rclone/releases/download/v1.66.0/rclone-v1.66.0-linux-${ARCH}.zip" -o r.zip || \
        return 1
        unzip -q r.zip || return 1
        cp rclone-v1.66.0-linux-${ARCH}/rclone /usr/local/bin/ || return 1
        chmod +x /usr/local/bin/rclone || return 1
        rm -rf r.zip rclone-v1.66.0-linux-${ARCH}
        return 0
    else
        echo "不支持的系统类型，无法自动安装 rclone"
        return 1
    fi
}

# 参数（这些值在创建命令时已被替换为实际值）
OSS_BUCKET="${OSS_BUCKET}"
OSS_ENDPOINT="${OSS_ENDPOINT}"
AK_ID="${AK_ID}"
AK_SECRET="${AK_SECRET}"
MOUNT_DIR="${MOUNT_DIR:-/mnt/oss}"

# 注意：如果看到这个错误，说明参数替换失败
if [ -z "${OSS_BUCKET}" ] || [ -z "${OSS_ENDPOINT}" ] || [ -z "${AK_ID}" ] || [ -z "${AK_SECRET}" ]; then
    echo -e "${RED}❌ 错误: 参数未正确替换，当前值:${RESET}"
    echo "  OSS_BUCKET=${OSS_BUCKET}"
    echo "  OSS_ENDPOINT=${OSS_ENDPOINT}"
    echo "  AK_ID=${AK_ID}"
    echo "  AK_SECRET=${AK_SECRET}"
    error_exit "缺少必需的参数: OSS_BUCKET, OSS_ENDPOINT, AK_ID, AK_SECRET"
fi

        # 初始化步骤状态（便于外部解析）
STEP1_1="pending"   # rclone remote 配置
STEP1_2="pending"   # OSS 连接测试
STEP1_3="pending"   # 挂载
STEP1_4="pending"   # 同步到 /opt

# Helper: 仅在交互式终端时提示
is_interactive() {
    [ -t 0 ]
}

# 可选：若设置此变量且本机已有镜像标志，则跳过全部 rclone 步骤
if [ "${SKIP_IF_DOCKER_IMAGE_PRESENT:-0}" = "1" ]; then
    if command -v docker >/dev/null 2>&1 && docker images --format '{{.Repository}}:{{.Tag}}' | grep -qE '(^|/)?indextts-deplyment-img(:|$)'; then
        echo "STEP_ALL=skipped_by_image"
        exit 0
    fi
fi

        echo "========================================"
echo "第一步：rclone 安装和 OSS 挂载准备"
        echo "========================================"
echo "Bucket: ${OSS_BUCKET}"
echo "Endpoint: ${OSS_ENDPOINT}"
        echo "挂载点: ${MOUNT_DIR}"
echo ""

# 2. Pre-create rclone config (目录与文件) 并提示用户确认（带序号）
# 2.1 确定 rclone.conf 文件目录并创建
RCLONE_CONFIG_DIR="/opt/.config/rclone"
echo "2.1 创建 rclone 配置目录: ${RCLONE_CONFIG_DIR}"
mkdir -p "${RCLONE_CONFIG_DIR}" || error_exit "无法创建目录: ${RCLONE_CONFIG_DIR}"
RCLONE_CONFIG_FILE="${RCLONE_CONFIG_DIR}/rclone.conf"

# 2.2 写入 rclone 配置到文件（使用 remote 名称 oss_<bucket>）
RCLONE_REMOTE_NAME="oss_${OSS_BUCKET}"
echo "2.2 将 rclone remote 写入到: ${RCLONE_CONFIG_FILE} (remote=${RCLONE_REMOTE_NAME})"
cat > "${RCLONE_CONFIG_FILE}" <<EOF
[${RCLONE_REMOTE_NAME}]
type = s3
provider = Alibaba
access_key_id = ${AK_ID}
secret_access_key = ${AK_SECRET}
endpoint = ${OSS_ENDPOINT}
EOF
chmod 600 "${RCLONE_CONFIG_FILE}" 2>/dev/null || true
sync || true

echo "2.3 rclone 配置文件已写入: ${RCLONE_CONFIG_FILE} (已设置 600 权限)"
echo "    - Remote 名称: ${RCLONE_REMOTE_NAME}"
echo "    - 请核对并确认配置正确（不会在此显示密钥）"
read -p "2.4 确认配置无误后按回车继续（交互式运行时有效）..." _ || true

# 9. 可选：同步应用数据到统一数据目录（/opt/indextts2bucket），并设置 .sync_done 标志位
echo "9. 同步应用目录（如需要）..."
# Ensure remote name is defined (may be set later elsewhere); build from OSS_BUCKET to avoid empty remote
UNIFIED_DATA_DIR="/opt/indextts2bucket"
RCLONE_REMOTE_NAME_LOCAL="oss_${OSS_BUCKET}"
RCLONE_PATH_ON_REMOTE="${RCLONE_REMOTE_NAME_LOCAL}:indextts2bucket"
# 使用时间戳命名同步日志，格式: YYYYMMDDhhmmss
TS="$(date +%Y%m%d%H%M%S)"
SYNC_LOG="/tmp/rclone_sync_app_${TS}.log"
# 创建固定名软链，便于外部工具访问最新日志
ln -sf "${SYNC_LOG}" /tmp/rclone_sync_app.log

if [ -d "${UNIFIED_DATA_DIR}" ] && [ "$(ls -A ${UNIFIED_DATA_DIR} 2>/dev/null)" ] && [ -f "${UNIFIED_DATA_DIR}/.sync_done" ]; then
    echo -e "${GREEN}✅ 已检测到 ${UNIFIED_DATA_DIR}/.sync_done，跳过数据同步${RESET}"
    STEP1_4="done"
else
    echo "准备同步： ${RCLONE_PATH_ON_REMOTE} -> ${UNIFIED_DATA_DIR}"
    mkdir -p "${UNIFIED_DATA_DIR}" || true
    rm -f "${SYNC_LOG}" 2>/dev/null || true
    touch "${SYNC_LOG}" 2>/dev/null || true
    echo "开始执行 rclone sync（日志: ${SYNC_LOG}）..."
    if ! command -v rclone >/dev/null 2>&1; then
        echo "rclone 未安装，尝试自动安装..."
        if install_rclone; then
            echo -e "${GREEN}✅ rclone 已安装${RESET}"
        else
            echo -e "${RED}❌ rclone 自动安装失败，无法执行数据同步${RESET}"
            STEP1_4="failed"
        fi
    fi
    if [ "${STEP1_4}" != "failed" ]; then
        # 构建 rclone 命令，only add --config if config file exists
        RCLONE_CMD="rclone sync \"${RCLONE_PATH_ON_REMOTE}\" \"${UNIFIED_DATA_DIR}\""
        if [ -n "${RCLONE_CONFIG_FILE}" ] && [ -f "${RCLONE_CONFIG_FILE}" ]; then
            RCLONE_CMD="${RCLONE_CMD} --config \"${RCLONE_CONFIG_FILE}\""
        fi
        RCLONE_CMD="${RCLONE_CMD} --transfers 16 --checkers 32 --multi-thread-streams 8 --multi-thread-cutoff 1G --s3-chunk-size 256M --buffer-size 256M --no-check-certificate --ignore-checksum --size-only --progress --log-file \"${SYNC_LOG}\" --log-level INFO --stats 10s --stats-log-level NOTICE"
        eval "${RCLONE_CMD}" &
        RCLONE_SYNC_PID=$!
        wait ${RCLONE_SYNC_PID}
        RC=$?
        if [ ${RC} -eq 0 ]; then
            echo -e "${GREEN}✅ 数据同步完成${RESET}"
            touch "${UNIFIED_DATA_DIR}/.sync_done" 2>/dev/null || true
            STEP1_4="done"
        else
            echo -e "${RED}❌ 数据同步失败（rclone 返回码: ${RC}）${RESET}"
            STEP1_4="failed"
            echo "查看同步日志: tail -n 50 ${SYNC_LOG}"
        fi
    fi
fi

# 最终打印各步骤状态，便于外部解析
echo ""
echo "STEP1_1=${STEP1_1}"
echo "STEP1_2=${STEP1_2}"
echo "STEP1_3=${STEP1_3}"
echo "STEP1_4=${STEP1_4}"

# 1. 检查 rclone 是否已安装（若未安装，会发出警告但尽量继续）
echo "1. 检查 rclone 是否已安装..."
if ! command -v rclone >/dev/null 2>&1; then
    echo -e "${YELLOW}⚠️ rclone 未安装: 后续挂载/同步可能失败，建议先安装 rclone${RESET}"
else
    RCLONE_VERSION=$(rclone version | head -n 1)
    echo -e "${GREEN}✅ rclone 已安装: ${RCLONE_VERSION}${RESET}"
fi
echo ""

# 2. Pre-create rclone config (目录与文件) 并提示用户确认（带序号）
# 2.1 确定 rclone.conf 文件目录并创建
RCLONE_CONFIG_DIR="/opt/.config/rclone"
echo "2.1 创建 rclone 配置目录: ${RCLONE_CONFIG_DIR}"
mkdir -p "${RCLONE_CONFIG_DIR}" || error_exit "无法创建目录: ${RCLONE_CONFIG_DIR}"
RCLONE_CONFIG_FILE="${RCLONE_CONFIG_DIR}/rclone.conf"

# 2.2 写入 rclone 配置到文件（使用 remote 名称 oss_<bucket>）
RCLONE_REMOTE_NAME="oss_${OSS_BUCKET}"
echo "2.2 将 rclone remote 写入到: ${RCLONE_CONFIG_FILE} (remote=${RCLONE_REMOTE_NAME})"
cat > "${RCLONE_CONFIG_FILE}" <<EOF
[${RCLONE_REMOTE_NAME}]
type = s3
provider = Alibaba
access_key_id = ${AK_ID}
secret_access_key = ${AK_SECRET}
endpoint = ${OSS_ENDPOINT}
EOF
chmod 600 "${RCLONE_CONFIG_FILE}" 2>/dev/null || true
sync || true

echo "2.3 rclone 配置文件已写入: ${RCLONE_CONFIG_FILE} (已设置 600 权限)"
echo "    - Remote 名称: ${RCLONE_REMOTE_NAME}"
echo "    - 请核对并确认配置正确（不会在此显示密钥）"
read -p "2.4 确认配置无误后按回车继续（交互式运行时有效）..." _ || true

# 2. 确定 rclone 配置目录
echo "2. 确定 rclone 配置目录..."
RCLONE_REMOTE_NAME="oss_${OSS_BUCKET}"
RCLONE_PATH=$(which rclone 2>/dev/null || echo "")
if [ -n "${RCLONE_PATH}" ] && echo "${RCLONE_PATH}" | grep -q "/snap/"; then
    SNAP_VERSION=$(echo "${RCLONE_PATH}" | sed -n 's|.*/snap/rclone/\([0-9]*\)/.*|\1|p')
    [ -n "${SNAP_VERSION}" ] && RCLONE_CONFIG_DIR="/root/snap/rclone/${SNAP_VERSION}/.config/rclone" || \
    RCLONE_CONFIG_DIR=$(ls -td /root/snap/rclone/* 2>/dev/null | head -1)/.config/rclone
else
RCLONE_CONFIG_DIR="/opt/.config/rclone"
fi
mkdir -p "${RCLONE_CONFIG_DIR}"
RCLONE_CONFIG_FILE="${RCLONE_CONFIG_DIR}/rclone.conf"
echo "配置目录: ${RCLONE_CONFIG_DIR}"
echo "配置文件: ${RCLONE_CONFIG_FILE}"
echo ""

# 3. 配置 rclone remote
echo "3. 配置 rclone remote..."
# 规范化 Endpoint（移除协议前缀和尾部斜杠）
ENDPOINT_HOST="${OSS_ENDPOINT#https://}"
ENDPOINT_HOST="${ENDPOINT_HOST#http://}"
ENDPOINT_HOST="${ENDPOINT_HOST%/}"

# 自动转换为内网 endpoint（如果当前不是内网）
if echo "${ENDPOINT_HOST}" | grep -q -- "-internal.aliyuncs.com"; then
    ENDPOINT_INTERNAL="${ENDPOINT_HOST}"
    echo "✓ 检测到内网 Endpoint: ${ENDPOINT_INTERNAL}"
else
    # 转换为内网 endpoint
    ENDPOINT_INTERNAL="${ENDPOINT_HOST%.aliyuncs.com}-internal.aliyuncs.com"
    echo "✓ 自动转换为内网 Endpoint: ${ENDPOINT_INTERNAL}"
fi

    if ! rclone listremotes --config "${RCLONE_CONFIG_FILE}" 2>/dev/null | grep -q "^${RCLONE_REMOTE_NAME}:$"; then
    echo "创建 rclone remote: ${RCLONE_REMOTE_NAME}"
    if rclone config create "${RCLONE_REMOTE_NAME}" s3 \
        provider Alibaba \
        access_key_id "${AK_ID}" \
        secret_access_key "${AK_SECRET}" \
        endpoint "${ENDPOINT_INTERNAL}" \
        --config "${RCLONE_CONFIG_FILE}" \
        --non-interactive; then
        echo -e "${GREEN}✅ rclone remote 配置成功（使用内网模式）${RESET}"
        STEP1_1="done"
    else
        echo -e "${RED}❌ rclone 配置失败${RESET}"
        STEP1_1="failed"
        error_exit "rclone 配置失败"
    fi
else
    echo -e "${GREEN}✅ rclone remote ${RCLONE_REMOTE_NAME} 已存在${RESET}"
    STEP1_1="done"
fi
echo ""
echo ""

# 4. 测试连接（仅在 remote 已配置时执行）
echo "4. 测试 OSS 连接..."
if [ "${STEP1_1}" = "done" ]; then
    if rclone lsd "${RCLONE_REMOTE_NAME}:" --config "${RCLONE_CONFIG_FILE}" >/dev/null 2>&1; then
        echo -e "${GREEN}✅ OSS 连接测试成功${RESET}"
        STEP1_2="done"
    else
        echo -e "${YELLOW}⚠️ OSS 连接测试失败，尝试重建 remote 并重试...${RESET}"
        rclone config delete "${RCLONE_REMOTE_NAME}" --config "${RCLONE_CONFIG_FILE}" 2>/dev/null || true
        if rclone config create "${RCLONE_REMOTE_NAME}" s3 provider Alibaba access_key_id "${AK_ID}" secret_access_key "${AK_SECRET}" endpoint "${ENDPOINT_INTERNAL}" --config "${RCLONE_CONFIG_FILE}" --non-interactive && rclone lsd "${RCLONE_REMOTE_NAME}:" --config "${RCLONE_CONFIG_FILE}" >/dev/null 2>&1; then
            echo -e "${GREEN}✅ OSS 连接测试成功（重建 remote 后）${RESET}"
            STEP1_2="done"
        else
            echo -e "${RED}❌ OSS 连接测试失败，请检查配置${RESET}"
            STEP1_2="failed"
            error_exit "OSS 连接测试失败，请检查配置"
        fi
    fi
else
    echo -e "${YELLOW}⚠️ 跳过 OSS 连接测试（remote 未配置成功）${RESET}"
    STEP1_2="skipped"
fi
echo ""

# 5. 创建挂载点
echo "5. 创建挂载点..."
mkdir -p "${MOUNT_DIR}"
echo "✓ 挂载点已创建"
echo ""

# 6. 检查旧挂载（包含 indextts2bucket 目录验证）
echo "6. 检查是否已挂载 OSS，并验证 indextts2bucket 目录..."
if mountpoint -q "${MOUNT_DIR}" 2>/dev/null && [ -d "${MOUNT_DIR}/indextts2bucket" ]; then
    echo -e "${GREEN}✅ 检测到 OSS 已挂载到: ${MOUNT_DIR}${RESET}"
    echo -e "${GREEN}✅ 检测到目录存在: ${MOUNT_DIR}/indextts2bucket${RESET}"
    echo ""
    mount | grep "${MOUNT_DIR}" || true
    echo ""
    echo -e "${GREEN}✅ 已确认 OSS 挂载成功，跳过重新挂载步骤${RESET}"
    echo ""
else
    if mountpoint -q "${MOUNT_DIR}" 2>/dev/null; then
        echo -e "${YELLOW}⚠️ 检测到 ${MOUNT_DIR} 已挂载，但未发现 indextts2bucket 目录，尝试重新挂载...${RESET}"
    else
        echo -e "${CYAN}未检测到已有挂载，准备执行挂载...${RESET}"
    fi
    echo ""
    # 7. 执行挂载（使用内网模式和缓存）
    echo "7. 执行挂载（内网模式 + 缓存优化）..."
    if ! command -v rclone >/dev/null 2>&1; then
        echo "rclone 未安装，尝试自动安装..."
        if install_rclone; then
            echo -e "${GREEN}✅ rclone 安装成功，继续挂载${RESET}"
        else
            echo -e "${RED}❌ rclone 自动安装失败，无法执行挂载${RESET}"
            error_exit "rclone 未安装，挂载中止"
        fi
    fi
    # Build mount command and only include --config when config file exists
    RCLONE_MOUNT_CMD="rclone mount \"${RCLONE_REMOTE_NAME_LOCAL}:\" \"${MOUNT_DIR}\""
    if [ -n "${RCLONE_CONFIG_FILE}" ] && [ -f "${RCLONE_CONFIG_FILE}" ]; then
        RCLONE_MOUNT_CMD="${RCLONE_MOUNT_CMD} --config \"${RCLONE_CONFIG_FILE}\""
    fi
    RCLONE_MOUNT_CMD="${RCLONE_MOUNT_CMD} --vfs-cache-mode full --vfs-cache-max-size 10G --vfs-cache-max-age 168h --vfs-read-ahead 256M --vfs-read-chunk-size 64M --vfs-read-chunk-size-limit 512M --buffer-size 64M --daemon --log-file /tmp/rclone.log --log-level INFO"
    echo "DEBUG: rclone mount cmd: ${RCLONE_MOUNT_CMD}"
    # Ensure remote exists in config before mounting. Try checking configured file first, then default.
    REMOTE_NAME="${RCLONE_REMOTE_NAME_LOCAL}:"
    REMOTE_EXISTS=0
    if [ -n "${RCLONE_CONFIG_FILE}" ] && [ -f "${RCLONE_CONFIG_FILE}" ]; then
        if rclone listremotes --config "${RCLONE_CONFIG_FILE}" 2>/dev/null | grep -q "^${RCLONE_REMOTE_NAME_LOCAL}:$"; then
            REMOTE_EXISTS=1
        fi
    fi
    if [ "${REMOTE_EXISTS}" -eq 0 ]; then
        if rclone listremotes 2>/dev/null | grep -q "^${RCLONE_REMOTE_NAME_LOCAL}:$"; then
            REMOTE_EXISTS=1
        fi
    fi
    if [ "${REMOTE_EXISTS}" -eq 0 ]; then
        echo "rclone remote ${RCLONE_REMOTE_NAME_LOCAL} 未在配置中找到，尝试创建 remote（non-interactive）..."
        if rclone config create "${RCLONE_REMOTE_NAME_LOCAL}" s3 provider Alibaba access_key_id "${AK_ID}" secret_access_key "${AK_SECRET}" endpoint "${ENDPOINT_INTERNAL}" --non-interactive; then
            echo "✅ 已创建 rclone remote: ${RCLONE_REMOTE_NAME_LOCAL}"
            REMOTE_EXISTS=1
        else
            echo -e "${YELLOW}⚠️ 无法自动创建 remote，请手动运行以下命令，并确认 /root/.config/rclone/rclone.conf 中包含 ${RCLONE_REMOTE_NAME_LOCAL}:${RESET}"
            echo ""
            echo "rclone config create ${RCLONE_REMOTE_NAME_LOCAL} s3 provider Alibaba access_key_id \"${AK_ID}\" secret_access_key \"${AK_SECRET}\" endpoint \"${ENDPOINT_INTERNAL}\" --non-interactive"
            echo ""
        fi
    fi
    # Fallback: if remote still missing, write a minimal rclone.conf section directly
    if [ "${REMOTE_EXISTS}" -eq 0 ]; then
        echo "尝试通过 rclone config create 写入 remote 到 ${RCLONE_CONFIG_FILE} ..."
        mkdir -p "$(dirname "${RCLONE_CONFIG_FILE}")" 2>/dev/null || true
        # Use rclone to create the remote in the target config file
        if rclone config create "${RCLONE_REMOTE_NAME_LOCAL}" s3 provider Alibaba access_key_id "${AK_ID}" secret_access_key "${AK_SECRET}" endpoint "${ENDPOINT_INTERNAL}" --config "${RCLONE_CONFIG_FILE}" --non-interactive; then
            echo "✅ 已在 ${RCLONE_CONFIG_FILE} 中创建 remote: ${RCLONE_REMOTE_NAME_LOCAL}"
            chmod 600 "${RCLONE_CONFIG_FILE}" 2>/dev/null || true
            REMOTE_EXISTS=1
        else
            echo -e "${YELLOW}⚠️ rclone config create 写入失败，请手动运行下列命令或检查权限:${RESET}"
            echo ""
            echo "rclone config create ${RCLONE_REMOTE_NAME_LOCAL} s3 provider Alibaba access_key_id \"${AK_ID}\" secret_access_key \"${AK_SECRET}\" endpoint \"${ENDPOINT_INTERNAL}\" --config \"${RCLONE_CONFIG_FILE}\" --non-interactive"
            echo ""
            # as a last resort, append minimal section (may be less compatible)
            echo "回退：追加最小配置片段到 ${RCLONE_CONFIG_FILE} ..."
            {
                echo ""
                echo "[${RCLONE_REMOTE_NAME_LOCAL}]"
                echo "type = s3"
                echo "provider = Alibaba"
                echo "access_key_id = ${AK_ID}"
                echo "secret_access_key = ${AK_SECRET}"
                echo "endpoint = ${ENDPOINT_INTERNAL}"
            } >> "${RCLONE_CONFIG_FILE}"
            sync || true
            chmod 600 "${RCLONE_CONFIG_FILE}" 2>/dev/null || true
            if rclone listremotes --config "${RCLONE_CONFIG_FILE}" 2>/dev/null | grep -q "^${RCLONE_REMOTE_NAME_LOCAL}:$"; then
                echo "✅ 已在 ${RCLONE_CONFIG_FILE} 中写入 remote: ${RCLONE_REMOTE_NAME_LOCAL}"
                REMOTE_EXISTS=1
            else
                echo -e "${YELLOW}⚠️ 写入后仍未检测到 remote，请手动检查 ${RCLONE_CONFIG_FILE}${RESET}"
            fi
        fi
    fi
    # If running interactively, prompt the user to confirm config before mounting
    if [ "${REMOTE_EXISTS}" -eq 1 ] && is_interactive; then
        read -p "rclone remote 已创建/存在于 ${RCLONE_CONFIG_FILE}，请确认无误后按回车继续挂载..." _ || true
    fi
    eval "${RCLONE_MOUNT_CMD}" || {
        echo -e "${RED}❌ rclone 挂载失败${RESET}"
        error_exit "rclone 挂载失败"
    }

    sleep 3
    echo ""
fi

# 8. 验证挂载
echo "8. 验证挂载..."
if mountpoint -q "${MOUNT_DIR}" 2>/dev/null; then
    echo -e "${GREEN}✅✅✅ OSS 挂载成功（rclone，内网模式 + 缓存）✅✅✅${RESET}"
    echo ""
    mount | grep rclone || true
    echo ""
    echo "挂载点内容（前10项）:"
    ls -lh "${MOUNT_DIR}" | head -10 || true
    echo ""
    echo "========================================"
    echo "挂载信息:"
    echo "  挂载点: ${MOUNT_DIR}"
    echo "  Remote: ${RCLONE_REMOTE_NAME}"
    echo "  Bucket: ${OSS_BUCKET}"
    echo "  Endpoint: ${ENDPOINT_INTERNAL} (内网)"
    echo "  缓存模式: full (完整缓存)"
    echo "  缓存大小: 10GB"
    echo "  日志文件: /tmp/rclone.log"
    echo "  查看日志: tail -f /tmp/rclone.log"
    echo "卸载命令: fusermount -u ${MOUNT_DIR}"
    echo "========================================"
else
    echo -e "${RED}❌ 挂载失败${RESET}"
    echo ""
    echo "查看日志: tail -20 /tmp/rclone.log"
    error_exit "挂载验证失败，请检查日志: /tmp/rclone.log"
fi

echo ""


