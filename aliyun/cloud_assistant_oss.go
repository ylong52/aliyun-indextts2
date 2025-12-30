//go:build cloud_assistant_oss_main
// +build cloud_assistant_oss_main

package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// 1. 配置与结构体定义
// =============================================================================

// =============================================================================
// 2. 辅助工具函数 (UI、等待、日志流)
// =============================================================================

// waitAndConfirm 显示即将执行的命令，并进行3秒倒计时
func waitAndConfirm(stepName string, cmdDescription string) {
	// 使用 ANSI 颜色提升可读性（如果终端支持）
	colorReset := "\x1b[0m"
	colorCyan := "\x1b[36m"
	colorYellow := "\x1b[33m"
	colorGreen := "\x1b[32m"

	fmt.Printf("\n%s================================================================%s\n", colorCyan, colorReset)
	fmt.Printf("%s准备执行步骤: %s%s\n", colorYellow, stepName, colorReset)
	fmt.Printf("%s执行命令内容: %s%s\n", colorYellow, cmdDescription, colorReset)
	fmt.Printf("%s================================================================%s\n", colorCyan, colorReset)

	for i := 3; i > 0; i-- {
		fmt.Printf("\r%s⏳ 倒计时 %d 秒 (按 Ctrl+C 退出)...%s", colorYellow, i, colorReset)
		time.Sleep(1 * time.Second)
	}
	fmt.Printf("\n\n%s🚀 开始执行 (以下为实时日志)...%s\n", colorGreen, colorReset)
	fmt.Println("----------------------------------------------------------------")
}

// waitForCommandWithStreamOutput 核心功能：实时获取并打印增量日志
func waitForCommandWithStreamOutput(client *ecs.Client, region, invokeID string, timeoutSec int) (string, error) {
	ticker := time.NewTicker(2 * time.Second) // 每2秒轮询一次
	defer ticker.Stop()
	timeout := time.After(time.Duration(timeoutSec) * time.Second)

	var lastOutputLen int // 记录上一次打印的字符长度

	for {
		select {
		case <-timeout:
			return "", fmt.Errorf("执行超时 (%d秒)，请检查云助手或实例网络", timeoutSec)
		case <-ticker.C:
			// 1. 查询执行结果
			req := ecs.CreateDescribeInvocationResultsRequest()
			req.RegionId = region
			req.InvokeId = invokeID
			req.ContentEncoding = "Base64" // 必须显式要求 Base64

			resp, err := client.DescribeInvocationResults(req)
			if err != nil {
				fmt.Printf("[Warning] 获取日志出错: %v (重试中...)\n", err)
				continue
			}
			if len(resp.Invocation.InvocationResults.InvocationResult) == 0 {
				continue
			}

			result := resp.Invocation.InvocationResults.InvocationResult[0]
			status := result.InvocationStatus

			// 2. 解码全量日志
			raw := result.Output
			decodedBytes, err := base64.StdEncoding.DecodeString(raw)
			var currentFullOutput string
			if err != nil {
				currentFullOutput = raw // 解码失败则使用原始值
			} else {
				currentFullOutput = string(decodedBytes)
			}

			// 3. 打印增量部分
			if len(currentFullOutput) > lastOutputLen {
				newLogs := currentFullOutput[lastOutputLen:]
				fmt.Print(newLogs) // 脚本输出自带换行，这里直接 Print
				lastOutputLen = len(currentFullOutput)
			}

			// 4. 判断状态
			if status == "Finished" || status == "Success" {
				return currentFullOutput, nil
			}
			if status == "Failed" || status == "Stopped" || status == "PartialFailed" || status == "Error" {
				return currentFullOutput, fmt.Errorf("任务结束状态异常: %s", status)
			}
		}
	}
}

// =============================================================================
// 3. 阿里云 SDK 封装 (Create/Invoke/List)
// =============================================================================

// =============================================================================
// 4. 自动化部署流程 (Steps)
// =============================================================================

// Step 1: 安装基础依赖 (fuse3, docker)
func step1_InstallDependencies(client *ecs.Client, region, instanceID string) error {
	cmdDesc := "apt-get update, install fuse3/git/docker.io"
	waitAndConfirm("1. 安装系统依赖 & Docker", cmdDesc)

	// 注意：Step 1 不使用 Sprintf 生成脚本，因此 date '+%H:%M:%S' 不需要转义
	script := `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
log() { echo "[$(date '+%H:%M:%S')] $1"; }

log ">>> 执行: apt-get update"
apt-get update || (sleep 3 && apt-get update)

# 安装 fuse3 单独处理，确保 fusermount3 可用
log ">>> 执行: apt-get install -y fuse3"
if apt-get install -y fuse3; then
    log " -> FUSE3_OK: fuse3 安装成功"
else
    log " -> FUSE3 安装失败，尝试安装兼容包 fuse libfuse-dev"
    if apt-get install -y fuse libfuse-dev; then
        log " -> FUSE_LEGACY_OK: 已安装 fuse/libfuse-dev（兼容模式）"
    else
        log " -> FUSE_INSTALL_FAILED: 无法安装 fuse3 或 fuse（请检查源与网络）"
        # 输出 apt 日志供调试
        tail -n 200 /var/log/apt/* 2>/dev/null || true
        exit 1
    fi
fi

# 安装 git（单独执行以便实时输出）
log ">>> 执行: apt-get install -y git"
if apt-get install -y git; then
    log " -> GIT_OK: git 安装成功"
else
    log " -> GIT_INSTALL_FAILED"
    exit 1
fi

# 安装 docker（单独执行）
log ">>> 执行: apt-get install -y docker.io"
if apt-get install -y docker.io; then
    log " -> DOCKER_OK: docker 安装成功"
else
    log " -> DOCKER_INSTALL_FAILED"
    exit 1
fi

if command -v docker >/dev/null; then
    log ">>> 启动 Docker 服务..."
    systemctl start docker || service docker start || true
    systemctl enable docker || true
    log "✅ DEP_DOCKER_OK: Docker 启动/安装完毕"
else
    log "❌ Docker 启动失败（但继续）"
fi
`
	// 超时 30分钟
	cmdID, err := CreateCommand(client, region, "auto_step1_deps", "RunShellScript", script, 1800)
	if err != nil {
		return err
	}
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return err
	}

	output, err := waitForCommandWithStreamOutput(client, region, invID, 1800)
	if err != nil {
		return err
	}
	if !strings.Contains(output, "DEP_DOCKER_OK") {
		return fmt.Errorf("未检测到成功标记")
	}
	return nil
}

// Step 2: 安装 Rclone
func step2_InstallRclone(client *ecs.Client, region, instanceID string) error {
	cmdDesc := "git clone rclone (Gitee Mirror) -> install"
	waitAndConfirm("2. 安装 Rclone", cmdDesc)

	// 注意：Step 2 不使用 Sprintf，不需要转义
	script := `#!/bin/bash
set -e
log() { echo "[$(date '+%H:%M:%S')] $1"; }

log ">>> 清理旧文件..."
rm -rf /tmp/rclone
mkdir -p /tmp
cd /tmp

log ">>> 执行: git clone https://gitee.com/skyqi/rclone.git"
git clone --depth 1 https://gitee.com/skyqi/rclone.git

log ">>> 安装二进制文件..."
if [ -f "/tmp/rclone/rclone" ]; then
    cp -f /tmp/rclone/rclone /usr/local/bin/rclone
    chmod +x /usr/local/bin/rclone
    VER=$(/usr/local/bin/rclone --version | head -n 1)
    log "✅ RCLONE_OK: 安装成功 ($VER)"
else
    log "❌ 失败: 未找到文件"
    exit 1
fi
rm -rf /tmp/rclone
`
	// 超时 10分钟
	cmdID, err := CreateCommand(client, region, "auto_step2_rclone", "RunShellScript", script, 600)
	if err != nil {
		return err
	}
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return err
	}

	output, err := waitForCommandWithStreamOutput(client, region, invID, 600)
	if err != nil {
		return err
	}
	if !strings.Contains(output, "RCLONE_OK") {
		return fmt.Errorf("未检测到 Rclone 成功标记")
	}
	return nil
}

// 已移除：开机自动挂载（systemd/cron）创建逻辑
// 说明：为避免 systemd/cron 在目标环境中因依赖或权限导致服务失败，暂时不再自动创建开机挂载脚本。
// 如果需要在目标实例上恢复此能力，请手动创建相应的 systemd 服务或 cron @reboot 脚本。

// Step 3.1: 将 OSS 上的 checkpoints 同步到 ECS 本地 /checkpoints
func step3_CopyCheckpoints(client *ecs.Client, region, instanceID string) error {
	cmdDesc := "从 OSS 同步 checkpoints 到本地 /checkpoints"
	waitAndConfirm("3.1 同步 checkpoints 到本地", cmdDesc)

	// 读取配置以获得 mountPoint 与 bucket（与 step3_MountOSS 配置一致）
	data, _ := os.ReadFile("config.yml")
	var conf struct {
		OSSConfig struct {
			Bucket     string `yaml:"bucket"`
			MountPoint string `yaml:"mount_point"`
			SubPath    string `yaml:"sub_path"`
		} `yaml:"oss_config"`
	}
	_ = yaml.Unmarshal(data, &conf)

	bucket := conf.OSSConfig.Bucket
	mountPoint := conf.OSSConfig.MountPoint
	if mountPoint == "" {
		mountPoint = "/mnt/oss"
	}

	// 脚本：优先从已挂载目录拷贝；若未挂载则尝试使用 rclone copy（依赖 /mnt/rclone.conf）
	script := fmt.Sprintf(`#!/bin/bash
set -e
log() { echo "[$(date '+%%H:%%M:%%S')] $1"; }

DEST="/checkpoints"
SRC_MOUNT="%s/python_indextts_code/checkpoints"

log ">>> 目标目录: $DEST"
mkdir -p "$DEST"
chmod 755 "$DEST"

if [ -d "$SRC_MOUNT" ]; then
  log ">>> 检测到挂载目录，使用 rsync/cp 拷贝: $SRC_MOUNT -> $DEST"
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete "$SRC_MOUNT/" "$DEST/"
  else
    cp -a "$SRC_MOUNT/." "$DEST/" || true
  fi
else
  log ">>> 未检测到挂载目录，尝试使用 rclone 从 OSS 复制"
  CONF_FILE="/mnt/rclone.conf"
  if [ ! -f "$CONF_FILE" ]; then
    log "❌ rclone 配置文件 $CONF_FILE 不存在，无法从 OSS 复制"
    exit 1
  fi
  log ">>> rclone copy oss_mount:%s/python_indextts_code/checkpoints/ -> $DEST"
  rclone copy "oss_mount:%s/python_indextts_code/checkpoints/" "$DEST" --config "$CONF_FILE" --transfers 8 --checkers 8 --verbose
fi

chown -R 0:0 "$DEST" || true
log ">>> 同步完成，列出部分文件:"
ls -la "$DEST" | head -n 50
`, mountPoint, bucket, bucket)

	// 超时根据 checkpoint 大小设为 1 小时
	cmdID, err := CreateCommand(client, region, "auto_step3_copy_checkpoints", "RunShellScript", script, 3600)
	if err != nil {
		return err
	}
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return err
	}
	output, err := waitForCommandWithStreamOutput(client, region, invID, 3600)
	if err != nil {
		return err
	}
	// 只要脚本执行并输出同步完成相关日志，视为成功
	if strings.Contains(output, "同步完成") || strings.Contains(output, "rclone copy") || strings.Contains(output, "ls -la") {
		return nil
	}
	return nil
}

// Step 3: 挂载 OSS
func step3_MountOSS(client *ecs.Client, region, instanceID string) error {
	// 读取配置
	data, _ := os.ReadFile("config.yml")
	var conf struct {
		OSSConfig struct {
			Bucket              string `yaml:"bucket"`
			Endpoint            string `yaml:"endpoint"`
			MountPoint          string `yaml:"mount_point"`
			SubPath             string `yaml:"sub_path"`
			AccessKeyID         string `yaml:"access_key_id"`
			AccessKeySecret     string `yaml:"access_key_secret"`
			UseInternalEndpoint bool   `yaml:"use_internal_endpoint"`
		} `yaml:"oss_config"`
		AccessKeyID     string `yaml:"access_key_id"`
		AccessKeySecret string `yaml:"access_key_secret"`
	}
	_ = yaml.Unmarshal(data, &conf)

	ak := conf.OSSConfig.AccessKeyID
	if ak == "" {
		ak = conf.AccessKeyID
	}
	sk := conf.OSSConfig.AccessKeySecret
	if sk == "" {
		sk = conf.AccessKeySecret
	}
	bucket := conf.OSSConfig.Bucket
	endpoint := conf.OSSConfig.Endpoint
	mountPoint := conf.OSSConfig.MountPoint
	subPath := conf.OSSConfig.SubPath
	useInternal := conf.OSSConfig.UseInternalEndpoint

	if subPath != "" {
		subPath = "/" + strings.Trim(subPath, "/")
	}

	// 处理内网 endpoint（必须使用内网挂载）
	if useInternal {
		if strings.Contains(endpoint, ".aliyuncs.com") && !strings.Contains(endpoint, "-internal") {
			endpoint = strings.Replace(endpoint, ".aliyuncs.com", "-internal.aliyuncs.com", 1)
			fmt.Printf("🔒 使用内网 endpoint: %s\n", endpoint)
		}
		fmt.Printf("✅ 内网挂载检查: 已启用内网 endpoint 挂载\n")
	} else {
		fmt.Printf("❌ 内网挂载检查: 配置中 use_internal_endpoint 为 false，但 OSS 挂载必须使用内网\n")
		return fmt.Errorf("❌ OSS 挂载必须使用内网 endpoint，但配置中 use_internal_endpoint 为 false")
	}

	cmdDesc := fmt.Sprintf("rclone mount oss://%s%s -> %s", bucket, subPath, mountPoint)
	waitAndConfirm("3. 挂载 OSS", cmdDesc)

	// === 核心修复点 ===
	// 使用 fmt.Sprintf 时，Shell 脚本中的 % 必须写成 %%，否则会被 Go 误解析
	// 导致 date '+%H:%M:%S' 被格式化错误，进而引发脚本语法错误
	script := fmt.Sprintf(`#!/bin/bash
set -e
MOUNT_DIR="%s"
CONF_FILE="/mnt/rclone.conf"
log() { echo "[$(date '+%%H:%%M:%%S')] $1"; }

log ">>> 检查挂载点: $MOUNT_DIR"
if mountpoint -q "$MOUNT_DIR"; then
    log "⚠️  ALREADY_MOUNTED: 目录已挂载"
    exit 0
fi

log ">>> 生成配置..."
mkdir -p $(dirname "$CONF_FILE")
cat > "$CONF_FILE" << EOF
[oss_mount]
type = s3
provider = Alibaba
access_key_id = %s
secret_access_key = %s
endpoint = %s
env_auth = false
EOF
chmod 600 "$CONF_FILE"

mkdir -p "$MOUNT_DIR"

# 构建并打印 rclone 命令（显示所有参数）
RCLONE_CMD="rclone mount oss_mount:%s \"%s\" --config \"$CONF_FILE\" --vfs-cache-mode writes --vfs-cache-max-size 1G --log-file /tmp/rclone.log"
log ">>> 执行命令: $RCLONE_CMD"

# 启动 rclone（调试模式：非 daemon，输出通过 tee 写入 /tmp/rclone.log）
log ">>> 以非-daemon 模式启动 rclone（用于调试）"
eval $RCLONE_CMD 2>&1 | tee /tmp/rclone.log & 
RCLONE_PID=$!
log ">>> rclone PID: $RCLONE_PID"

# 启动实时日志输出（在后台持续输出到主脚本 stdout）
log ">>> 开始实时输出 /tmp/rclone.log"
touch /tmp/rclone.log
tail -n +1 -F /tmp/rclone.log 2>/dev/null & 
TAIL_PID=$!

# 循环检查挂载状态：每5秒检查一次，最多等待2分钟（24次）
for i in {1..24}; do
    if mountpoint -q "$MOUNT_DIR"; then
        log "✅ MOUNT_SUCCESS: 挂载成功 (等待了$((i*5))秒)"
        # 等待 3 秒，确保大文件或 VFS 完成初始化，然后列出挂载目录内容以供验证
        log ">>> 等待 3 秒以确保挂载稳定..."
        sleep 3
        log ">>> 列出挂载目录: $MOUNT_DIR"
        ls -la "$MOUNT_DIR" || log ">>> ls 返回非零状态（可能尚未完全就绪）"
        kill $TAIL_PID || true
        exit 0
    fi
    log ">>> 挂载检查中... ($i/24)"
    sleep 5
done

log "❌ MOUNT_FAILED: 挂载超时 (等待了2分钟)"
# 输出最后 100 行日志供诊断
tail -n 100 /tmp/rclone.log
kill $TAIL_PID || true
exit 1
`, mountPoint, ak, sk, endpoint, subPath, mountPoint)

	// 超时 5分钟
	cmdID, err := CreateCommand(client, region, "auto_step3_oss", "RunShellScript", script, 300)
	if err != nil {
		return err
	}
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return err
	}

	output, err := waitForCommandWithStreamOutput(client, region, invID, 300)
	if err != nil {
		return err
	}
	if strings.Contains(output, "MOUNT_SUCCESS") || strings.Contains(output, "ALREADY_MOUNTED") {
		// 挂载成功，跳过创建开机自动挂载脚本（避免 systemd/cron 在目标环境失败）
		fmt.Printf("✅ MOUNT_SUCCESS: 挂载成功 (%s)。已跳过创建开机自动挂载脚本。\n", mountPoint)
		return nil
	}
	return fmt.Errorf("挂载失败")
}

// Step 4: 从挂载点加载 Docker 镜像并输出镜像列表
func step4_LoadDockerImage(client *ecs.Client, region, instanceID string, mountPoint string) error {
	cmdDesc := fmt.Sprintf("docker load image from %s/indextts-deplyment-img.tar", mountPoint)
	waitAndConfirm("4. 加载 Docker 镜像", cmdDesc)

	// 使用远端脚本在实例上执行 docker load，并在已有镜像时询问是否覆盖（等待3秒）
	script := fmt.Sprintf(`#!/bin/bash
set -e
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
CYAN="\033[36m"
RESET="\033[0m"

IMAGE_PATH="%s/indextts-deplyment-img.tar"
LOGFILE="/tmp/docker_load_$(date +%%Y%%m%%d%%H%%M%%S).log"

# 首先创建 docker-entrypoint.sh 启动脚本
echo -e "${CYAN}>>> 创建 docker-entrypoint.sh 启动脚本${RESET}"
mkdir -p "%s/docker-data"
cat > "%s/docker-data/docker-entrypoint.sh" << 'EOF'
#!/bin/bash
set -e

# 默认 entrypoint：进入代码目录并运行 worker（从 OSS 挂载获取）
cd /python_indextts_code || { echo "ERROR: /python_indextts_code not found"; exec "$@"; }
exec python indextts/app/rabbitmq_worker.py
EOF
chmod +x "%s/docker-data/docker-entrypoint.sh"
echo -e "${GREEN}✅ docker-entrypoint.sh 创建成功${RESET}"

echo -e "${CYAN}>>> 开始准备 Docker 镜像加载: ${IMAGE_PATH}${RESET}"
if [ ! -f "$IMAGE_PATH" ]; then
  echo -e "${RED}❌ 错误: 镜像文件不存在: $IMAGE_PATH${RESET}"
  exit 2
fi

# 检查本地是否已存在目标镜像
if docker images --format '{{.Repository}}:{{.Tag}}' | grep -qE '^indextts-deplyment(:|:latest)?'; then
  echo -e "${YELLOW}⚠️ 检测到镜像 indextts-deplyment 已存在。${RESET}"
  echo -n "是否重新加载该镜像并覆盖已有镜像？输入 Y 确认（等待 3 秒自动忽略）: "
  # 等待 3 秒读取用户输入
  read -t 3 -r ANSWER || ANSWER=""
  echo ""
  if [ -z "$ANSWER" ]; then
    echo ">>> 未收到回复（超时 3s），跳过重新加载镜像。"
    echo "SKIPPED_RELOAD"
    exit 0
  fi
  if [ "$ANSWER" != "Y" ] && [ "$ANSWER" != "y" ]; then
    echo ">>> 用户选择不重新加载，跳过。"
    echo "SKIPPED_RELOAD"
    exit 0
  fi

  echo ">>> 用户确认重新加载，开始删除旧镜像..."
  # 尝试删除相关镜像（忽略失败）
  docker rmi -f indextts-deplyment:latest 2>/dev/null || true
  docker rmi -f indextts-deplyment 2>/dev/null || true
  echo ">>> 已删除旧镜像（若存在）"
fi

rm -f "$LOGFILE" 2>/dev/null || true
touch "$LOGFILE"

# 启动 docker load，并通过 tee 实时写入日志；随后用 tail -F 将日志原样输出到云助手执行流
echo ">>> 启动 docker load 并将输出实时写入 $LOGFILE"
docker load -i "$IMAGE_PATH" 2>&1 | tee -a "$LOGFILE" &
DL_PID=$!

# 实时转发日志（从文件开头开始），在后台持续输出直到 docker load 完成
tail -n +1 -F "$LOGFILE" 2>/dev/null &
TAIL_PID=$!

# 等待 docker load 完成
wait "$DL_PID"
RC=$?
# 停止 tail 后台进程
kill $TAIL_PID 2>/dev/null || true

echo "DONE: rc=$RC"
if [ $RC -ne 0 ]; then
  echo -e "${RED}❌ docker load 失败 (exit=$RC)${RESET}"
  echo "---- 最后 200 行日志 ----"
  tail -n 200 "$LOGFILE" || true
  exit $RC
fi

echo -e "${GREEN}✅ docker load 成功: ${IMAGE_PATH}${RESET}"
echo "---- Docker images (相关条目) ----"
docker images | grep indextts2bucket || docker images | head -n 20 || true
echo "---- end ----"
`, mountPoint, mountPoint, mountPoint, mountPoint)

	// 提交远端命令
	cmdID, err := CreateCommand(client, region, "auto_step4_docker_load", "RunShellScript", script, 7200)
	if err != nil {
		return err
	}
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return err
	}

	// 实时获取并打印增量日志（与前面一致）
	output, err := waitForCommandWithStreamOutput(client, region, invID, 7200)
	if err != nil {
		// 如果返回错误但输出包含 SKIPPED_RELOAD，视为成功（用户选择跳过）
		if strings.Contains(output, "SKIPPED_RELOAD") {
			return nil
		}
		return err
	}
	// 识别成功或跳过标记
	if strings.Contains(output, "✅ docker load 成功") || strings.Contains(output, "DONE: rc=0") || strings.Contains(output, "SKIPPED_RELOAD") {
		return nil
	}
	return fmt.Errorf("docker load 失败或未检测到成功标记")
}

// Step 5: 以后台 (--rm -d) 模式启动容器，并输出进入容器的 exec 命令
func step5_RunDockerDetached(client *ecs.Client, region, instanceID string, mountPoint string) error {
	cmdDesc := fmt.Sprintf("docker run -d indextts-deplyment (mount: %s)", mountPoint)
	waitAndConfirm("5. 运行 Docker 容器（后台 --rm -d）", cmdDesc)

	// #region agent log
	appendDebug := func(hypothesisId, location, message string, data map[string]string) {
		type payload struct {
			SessionId    string            `json:"sessionId"`
			RunId        string            `json:"runId"`
			HypothesisId string            `json:"hypothesisId"`
			Location     string            `json:"location"`
			Message      string            `json:"message"`
			Data         map[string]string `json:"data"`
			Timestamp    int64             `json:"timestamp"`
		}
		p := payload{
			SessionId:    "debug-session",
			RunId:        fmt.Sprintf("run-%d", time.Now().Unix()),
			HypothesisId: hypothesisId,
			Location:     location,
			Message:      message,
			Data:         data,
			Timestamp:    time.Now().UnixNano() / 1e6,
		}
		b, _ := json.Marshal(p)
		f, ferr := os.OpenFile(`e:\case2025\VibeVoice.git\index-tts-main\aliyun\.cursor\debug.log`, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if ferr == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	// log entry with parameters
	appendDebug("A", "step5_RunDockerDetached:entry", "entering step5", map[string]string{"mountPoint": mountPoint})
	// #endregion agent log

	script := fmt.Sprintf(`#!/bin/bash
set -e
RED="\033[31m"
GREEN="\033[32m"
YELLOW="\033[33m"
CYAN="\033[36m"
RESET="\033[0m"

MOUNT_DIR="%s"
IMAGE_NAME="indextts-deplyment:latest"
# 优先使用挂载点根目录的 .env，其次查找 docker-data 下的 .env_dev 或 .env
if [ -f "$MOUNT_DIR/.env" ]; then
  ENV_FILE="$MOUNT_DIR/.env"
elif [ -f "$MOUNT_DIR/docker-data/.env_dev" ]; then
  ENV_FILE="$MOUNT_DIR/docker-data/.env_dev"
elif [ -f "$MOUNT_DIR/docker-data/.env" ]; then
  ENV_FILE="$MOUNT_DIR/docker-data/.env"
else
  ENV_FILE=""
fi

echo -e "${CYAN}>>> 启动容器（后台 --rm -d）：${IMAGE_NAME}${RESET}"
echo "挂载点: $MOUNT_DIR"

# 停止并删除已有容器（如果存在）
if docker ps -a --format '{{.Names}}' | grep -q '^indextts-deplyment$'; then
  echo "检测到已有容器 indextts-deplyment，停止并删除..."
  docker stop indextts-deplyment >/dev/null 2>&1 || true
  docker rm indextts-deplyment >/dev/null 2>&1 || true
fi
# 清理 Docker 日志（便于查看新启动容器的日志）
echo ">>> 清理 Docker 日志..."
rm -f /var/log/docker.log 2>/dev/null || true
if [ -d /var/lib/docker/containers ]; then
  find /var/lib/docker/containers -type f -name "*-json.log" -exec rm -f {} \; 2>/dev/null || true
fi
echo ">>> Docker 日志已清理"

# 运行容器（后台），仅当 ENV_FILE 非空时传递 --env-file 参数
DOCKER_ENV_ARG=""
if [ -n "$ENV_FILE" ]; then
  DOCKER_ENV_ARG="--env-file \"$ENV_FILE\""
  echo ">>> 使用 env 文件: $ENV_FILE"
else
  echo ">>> 未检测到 env 文件，将不传递 --env-file 参数"
fi

DOCKER_CMD="docker run -d --name indextts-deplyment --user 0:0 \
  --add-host=host.docker.internal:host-gateway ${DOCKER_ENV_ARG} \
  -e PYTHONUNBUFFERED=1 \
  -v \"/checkpoints\":/python_indextts_code/checkpoints \
  -v \"$MOUNT_DIR\":/indexttsData \
  -v \"$MOUNT_DIR/docker-data\":/docker-data \
  -v \"$MOUNT_DIR/python_indextts_code\":/python_indextts_code \
  \"$IMAGE_NAME\""

# 使用 eval 启动以展开 DOCKER_ENV_ARG
eval $DOCKER_CMD
RC=$?
if [ $RC -ne 0 ]; then
  echo -e "${RED}❌ docker run 启动失败 (exit=$RC)${RESET}"
  exit $RC
fi

echo -e "${GREEN}✅ 容器启动成功（后台运行）${RESET}"

# 循环检查容器是否在运行（每5秒，最多24次）
for i in {1..24}; do
  if docker ps --format '{{.Names}}' | grep -q '^indextts-deplyment$'; then
    echo "CONTAINER_RUNNING: true (waited $((i*5))s)"
    break
  fi
  echo "等待容器就绪... ($i/24)"
  sleep 5
done

echo "---- Docker 容器列表 ----"
docker ps --filter "name=indextts-deplyment" || true
echo "---- Docker 容器列表 ----"
docker ps --filter "name=indextts-deplyment" || true

# 在容器内配置 pip，使用宿主挂载的 /docker-data/python-packages 作为 find-links 与 cache-dir
echo ">>> 在容器内写入 /etc/pip.conf，使 pip 使用 /docker-data/python-packages 作为本地包源"
docker exec indextts-deplyment bash -c "cat >/etc/pip.conf <<'PIPCONF'
[global]
find-links = /docker-data/python-packages
no-index = true
cache-dir = /docker-data/pip-cache
PIPCONF"
if [ $? -eq 0 ]; then
  echo ">>> 已写入 /etc/pip.conf，内容如下："
  docker exec indextts-deplyment cat /etc/pip.conf || true
else
  echo ">>> 警告：无法写入 /etc/pip.conf（可能权限或容器未就绪）"
fi

echo "---- 进入容器调试命令（仅供复制执行） ----"
echo -e "${YELLOW}docker exec -it indextts-deplyment bash${RESET}"
echo -e "${YELLOW}# 查看容器实时日志（在另一终端运行）:${RESET}"
echo -e "${YELLOW}docker logs -f indextts-deplyment${RESET}"
echo -e "${YELLOW}# 或查看 Docker 服务日志（如使用 journald）:${RESET}"
echo -e "${YELLOW}journalctl -u docker -n 200 --no-pager${RESET}"

`, mountPoint)

	// debug log: creating remote command
	appendDebug("B", "step5_CreateCommand", "creating remote command auto_step5_docker_run", map[string]string{"name": "auto_step5_docker_run"})
	cmdID, err := CreateCommand(client, region, "auto_step5_docker_run", "RunShellScript", script, 1800)
	if err != nil {
		return err
	}
	// debug log: created command
	appendDebug("B", "step5_CreateCommand", "created remote command", map[string]string{"commandID": cmdID})
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	// debug log: invoked command
	appendDebug("B", "step5_InvokeCommand", "invoked remote command", map[string]string{"invokeID": invID})
	if err != nil {
		return err
	}

	output, err := waitForCommandWithStreamOutput(client, region, invID, 1800)
	if err != nil {
		return err
	}
	if strings.Contains(output, "✅ 容器启动成功") || strings.Contains(output, "CONTAINER_RUNNING: true") {
		return nil
	}
	return fmt.Errorf("docker run 未检测到成功标记")
}

// =============================================================================
// 5. 主程序 (Main)
// =============================================================================

func main() {
	// 1. 加载配置
	cfgPath := "config.yml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Printf("❌ 读取配置失败: %v\n", err)
		return
	}
	var cfg ECSConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("❌ 解析配置失败: %v\n", err)
		return
	}

	// 2. 列表选择实例
	instances, err := ListAllInstances(&cfg)
	if err != nil {
		fmt.Printf("获取实例失败: %v\n", err)
		return
	}
	if len(instances) == 0 {
		fmt.Println("没有找到可用实例。")
		return
	}
	DisplayInstances(instances)

	fmt.Print("\n请输入要操作的实例序号 (输入 0 退出): ")
	var idx int
	fmt.Scanln(&idx)
	if idx <= 0 || idx > len(instances) {
		fmt.Println("退出程序。")
		return
	}
	selected := instances[idx-1]
	client, err := NewECSClientWithRegion(&cfg, cfg.RegionID)
	if err != nil {
		fmt.Printf("客户端初始化失败: %v\n", err)
		return
	}

	fmt.Printf("\n🚀 目标实例: [%s] (%s)\n", selected.InstanceID, selected.Instance.InstanceName)

	// 3. 按顺序执行步骤
	// -------------------------------------------------------------------------
	if err := step1_InstallDependencies(client, cfg.RegionID, selected.InstanceID); err != nil {
		fmt.Printf("\n❌ 步骤 1 中断: %v\n", err)
		return
	}

	if err := step2_InstallRclone(client, cfg.RegionID, selected.InstanceID); err != nil {
		fmt.Printf("\n❌ 步骤 2 中断: %v\n", err)
		return
	}

	if err := step3_MountOSS(client, cfg.RegionID, selected.InstanceID); err != nil {
		fmt.Printf("\n❌ 步骤 3 中断: %v\n", err)
		return
	}
	// 3.1 将 OSS 上的 checkpoints 同步到 ECS 本地 /checkpoints（可选失败不阻断）
	if err := step3_CopyCheckpoints(client, cfg.RegionID, selected.InstanceID); err != nil {
		fmt.Printf("\n⚠️ 步骤 3.1 警告（sync checkpoints）: %v\n", err)
		// 继续执行，非致命
	}
	// 3.5 读取配置以获取挂载点（用于后续 docker load）
	var confTmp struct {
		OSSConfig struct {
			MountPoint string `yaml:"mount_point"`
		} `yaml:"oss_config"`
	}
	cfgData, _ := os.ReadFile("config.yml")
	_ = yaml.Unmarshal(cfgData, &confTmp)
	mountPoint := confTmp.OSSConfig.MountPoint
	if mountPoint == "" {
		mountPoint = "/mnt/oss"
	}
	// Step 4: 加载 docker 镜像（可选失败不阻断）
	if err := step4_LoadDockerImage(client, cfg.RegionID, selected.InstanceID, mountPoint); err != nil {
		fmt.Printf("\n⚠️ 步骤 4 警告: %v\n", err)
		// 不直接 return，交由用户决定是否继续
	}
	// Step 5: 启动容器（后台 --rm -d），并打印进入容器的 exec 命令
	if err := step5_RunDockerDetached(client, cfg.RegionID, selected.InstanceID, mountPoint); err != nil {
		fmt.Printf("\n⚠️ 步骤 5 警告: %v\n", err)
	}
	// -------------------------------------------------------------------------

	fmt.Printf("\n\x1b[36m%s\x1b[0m\n", strings.Repeat("=", 80))
	fmt.Printf("\x1b[32m%s\x1b[0m\n", "🚀🚀🚀  所有任务执行完毕 — 环境部署成功！  🎉🎉🎉")
	fmt.Printf("\x1b[36m%s\x1b[0m\n", strings.Repeat("=", 80))

	// 防止窗口立即关闭
	fmt.Println("按回车键退出...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}
