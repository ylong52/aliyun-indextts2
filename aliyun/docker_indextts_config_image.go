//go:build docker_indextts_config_image_main
// +build docker_indextts_config_image_main

package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"gopkg.in/yaml.v3"
)

// remoteInstallDocker 在远程ECS实例上安装Docker
// 使用云助手命令在指定ECS实例上安装Docker CE
func remoteInstallDocker(client *ecs.Client, region, instanceID string) error {
	fmt.Printf("开始在实例 %s 上安装Docker...\n", instanceID)

	// Docker安装脚本
	installScript := `#!/bin/bash
set -e

# 更新包管理器
echo "更新包管理器..."
apt-get update

# 安装必要的包
echo "安装必要的包..."
apt-get install -y \
    apt-transport-https \
    ca-certificates \
    curl \
    gnupg \
    lsb-release

# 添加Docker的官方GPG密钥
echo "添加Docker GPG密钥..."
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg

# 添加Docker仓库
echo "添加Docker仓库..."
echo \
  "deb [arch=amd64 signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null

# 更新包索引
echo "更新包索引..."
apt-get update

# 安装Docker CE
echo "安装Docker CE..."
apt-get install -y docker-ce docker-ce-cli containerd.io

# 启动Docker服务
echo "启动Docker服务..."
systemctl start docker
systemctl enable docker

# 添加当前用户到docker组（如果用户存在）
if id ubuntu &>/dev/null; then
    usermod -aG docker ubuntu
    echo "已将ubuntu用户添加到docker组"
elif id root &>/dev/null; then
    echo "使用root用户，docker权限已具备"
fi

# 验证安装
echo "验证Docker安装..."
docker --version
docker run --rm hello-world

echo "Docker安装完成！"
`

	// 创建云助手命令
	cmdID, err := CreateCommand(client, region, "install_docker", "RunShellScript", installScript, 1800)
	if err != nil {
		return fmt.Errorf("创建Docker安装命令失败: %w", err)
	}

	// 执行命令
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return fmt.Errorf("执行Docker安装命令失败: %w", err)
	}

	// 等待命令完成
	result, err := WaitForCommandCompletion(client, region, invID, 1800, 10)
	if err != nil {
		return fmt.Errorf("等待Docker安装完成失败: %w", err)
	}

	if result != nil && result.ExitCode != 0 {
		return fmt.Errorf("Docker安装失败，退出码: %d, 输出: %s", result.ExitCode, result.Output)
	}

	fmt.Printf("✓ Docker安装成功完成\n")
	return nil
}

// uploadConfigToECS 将本地config.yml文件上传到ECS实例
// 使用云助手命令将配置文件内容编码后传输到ECS实例
func uploadConfigToECS(client *ecs.Client, region, instanceID, localConfigPath, remoteConfigPath string) error {
	fmt.Printf("开始上传配置文件 %s 到实例 %s...\n", localConfigPath, instanceID)

	// 读取本地配置文件
	configContent, err := os.ReadFile(localConfigPath)
	if err != nil {
		return fmt.Errorf("读取本地配置文件失败: %w", err)
	}

	// Base64编码文件内容
	encodedContent := base64.StdEncoding.EncodeToString(configContent)

	// 生成远程写入脚本
	uploadScript := fmt.Sprintf(`#!/bin/bash
set -e

# 创建远程配置目录
mkdir -p "$(dirname "%s")"

# 解码并写入配置文件
echo "%s" | base64 -d > "%s"

# 设置正确的权限
chmod 644 "%s"

# 验证文件
echo "配置文件已上传到: %s"
echo "文件大小: $(stat -c%%s "%s") bytes"
echo "文件权限: $(stat -c%%a "%s")"

# 显示配置文件的开头几行（不显示敏感信息）
echo "配置文件内容预览:"
head -10 "%s" | sed 's/secret.*$/secret: ***HIDDEN***/g' | sed 's/key_id.*$/key_id: ***HIDDEN***/g'

echo "配置文件上传完成！"
`, remoteConfigPath, encodedContent, remoteConfigPath, remoteConfigPath, remoteConfigPath, remoteConfigPath, remoteConfigPath, remoteConfigPath)

	// 创建云助手命令
	cmdID, err := CreateCommand(client, region, "upload_config", "RunShellScript", uploadScript, 300)
	if err != nil {
		return fmt.Errorf("创建配置文件上传命令失败: %w", err)
	}

	// 执行命令
	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return fmt.Errorf("执行配置文件上传命令失败: %w", err)
	}

	// 等待命令完成
	result, err := WaitForCommandCompletion(client, region, invID, 300, 5)
	if err != nil {
		return fmt.Errorf("等待配置文件上传完成失败: %w", err)
	}

	if result != nil && result.ExitCode != 0 {
		return fmt.Errorf("配置文件上传失败，退出码: %d, 输出: %s", result.ExitCode, result.Output)
	}

	fmt.Printf("✓ 配置文件上传成功完成\n")
	return nil
}

// ECS Docker自动化部署工具
//
// 功能特性：
// 1. 交互式ECS实例管理菜单
// 2. ECS SSH自动登录
// 3. 远程Docker安装
// 4. 配置文件和脚本一键部署
//
// 菜单和函数对应关系：
// 1) 进入 ECS SSH (自动登录)     → handleSSHLogin()
// 2) 在ECS安装 Docker           → remoteInstallDocker()
// 3) 部署配置和脚本到ECS         → deployConfigAndScript()
//                                    (内部调用uploadConfigToECS + uploadDockerRunScript)
//
// 部署流程：
// 1. 选择目标ECS实例
// 2. 可选：进入SSH检查环境
// 3. 可选：在ECS上安装Docker
// 4. 一键部署配置和脚本（配置文件+运行脚本）
// 5. 在ECS上运行生成的脚本进入容器开发环境

// deployConfigAndScript 上传配置文件并生成Docker运行脚本到ECS
// 合并了配置文件上传和Docker脚本生成两个操作
func deployConfigAndScript(client *ecs.Client, region, instanceID string) error {
	fmt.Printf("开始部署配置和脚本到实例 %s...\n", instanceID)

	// 第一步：上传配置文件
	fmt.Println("1. 上传配置文件...")
	if err := uploadConfigToECS(client, region, instanceID, "./config.yml", "/opt/indextts/config.yml"); err != nil {
		return fmt.Errorf("上传配置文件失败: %w", err)
	}

	// 第二步：生成并上传Docker运行脚本
	fmt.Println("2. 生成并上传Docker运行脚本...")
	if err := uploadDockerRunScript(client, region, instanceID); err != nil {
		return fmt.Errorf("上传Docker运行脚本失败: %w", err)
	}

	fmt.Printf("✓ 配置和脚本部署完成\n")
	fmt.Printf("✓ 在ECS上运行: /opt/indextts/run_container.sh\n")
	return nil
}

// uploadDockerRunScript 生成Docker运行脚本（私有函数，由deployConfigAndScript调用）
func uploadDockerRunScript(client *ecs.Client, region, instanceID string) error {
	// 读取本地config.yml
	configContent, err := os.ReadFile("config.yml")
	if err != nil {
		return fmt.Errorf("读取本地config.yml失败: %w", err)
	}

	// 解析配置获取参数
	var conf struct {
		AccessKeyID     string `yaml:"access_key_id"`
		AccessKeySecret string `yaml:"access_key_secret"`
		OSSConfig       struct {
			Bucket   string `yaml:"bucket"`
			Endpoint string `yaml:"endpoint"`
		} `yaml:"oss_config"`
	}

	if err := yaml.Unmarshal(configContent, &conf); err != nil {
		return fmt.Errorf("解析config.yml失败: %w", err)
	}

	// 优先使用OSS配置
	accessKeyID := conf.OSSConfig.AccessKeyID
	accessKeySecret := conf.OSSConfig.AccessKeySecret
	if accessKeyID == "" {
		accessKeyID = conf.AccessKeyID
	}
	if accessKeySecret == "" {
		accessKeySecret = conf.AccessKeySecret
	}

	bucket := conf.OSSConfig.Bucket
	endpoint := conf.OSSConfig.Endpoint

	if bucket == "" {
		bucket = "indextts2bucket"
	}
	if endpoint == "" {
		endpoint = "oss-cn-shenzhen.aliyuncs.com"
	}

	// 生成Docker运行脚本
	dockerRunScript := fmt.Sprintf(`#!/bin/bash
# ECS Docker运行脚本 - 自动生成
# 使用方法: ./run_container.sh

set -e

echo "=== 启动IndexTTS Docker容器 ==="

# 检查Docker是否安装
if ! command -v docker &> /dev/null; then
    echo "错误：Docker未安装，请先运行安装Docker的选项"
    exit 1
fi

# Docker运行参数（请根据需要修改）
DOCKER_RUN_CMD="docker run -it --rm \
    --name indextts-dev \
    --privileged \
    --cap-add SYS_ADMIN \
    --device /dev/fuse \
    --network host \
    -e ALICLOUD_ACCESS_KEY_ID='%s' \
    -e ALICLOUD_ACCESS_KEY_SECRET='%s' \
    -e OSS_BUCKET='%s' \
    -e OSS_ENDPOINT='%s' \
    -e RABBITMQ_URL='amqp://guest:guest@localhost:5672/' \
    -e PYTHONUNBUFFERED=1 \
    -w /workspace \
    indextts-deployment:latest \
    /bin/bash"

echo "执行命令:"
echo "$DOCKER_RUN_CMD"
echo ""

# 执行Docker运行命令
eval "$DOCKER_RUN_CMD"

echo "容器已退出"
`, accessKeyID, accessKeySecret, bucket, endpoint)

	// 上传脚本到ECS
	script := fmt.Sprintf(`#!/bin/bash
# 确保目录存在
mkdir -p /opt/indextts

# 写入运行脚本
cat > /opt/indextts/run_container.sh << 'EOF'
%s
EOF

# 设置执行权限
chmod +x /opt/indextts/run_container.sh

echo "Docker运行脚本已保存到: /opt/indextts/run_container.sh"
`, dockerRunScript)

	cmdID, err := CreateCommand(client, region, "upload_docker_script", "RunShellScript", script, 300)
	if err != nil {
		return fmt.Errorf("创建上传脚本命令失败: %w", err)
	}

	invID, err := InvokeCommand(client, region, instanceID, cmdID, "Once")
	if err != nil {
		return fmt.Errorf("执行上传脚本命令失败: %w", err)
	}

	result, err := WaitForCommandCompletion(client, region, invID, 300, 5)
	if err != nil {
		return fmt.Errorf("等待脚本上传完成失败: %w", err)
	}

	if result != nil && result.ExitCode != 0 {
		return fmt.Errorf("脚本上传失败，退出码: %d", result.ExitCode)
	}

	return nil
}

// interactiveDeployMenu 交互式部署菜单
func interactiveDeployMenu(cfg ECSConfig) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\n查询实例列表...")
		instances, err := ListAllInstances(&ECSConfig{
			AccessKeyID:         cfg.AccessKeyID,
			AccessKeySecret:     cfg.AccessKeySecret,
			RegionID:            cfg.RegionID,
			QueryAllRegions:     cfg.QueryAllRegions,
			ExcludedInstanceIDs: cfg.ExcludedInstanceIDs,
		})
		if err != nil {
			fmt.Printf("ListAllInstances failed: %v\n", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if len(instances) == 0 {
			fmt.Println("未找到实例")
			time.Sleep(2 * time.Second)
			continue
		}
		DisplayInstances(instances)
		fmt.Print("\n请选择要操作的实例序号 (q 退出): ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "0" || strings.ToLower(line) == "q" {
			return
		}
		idx := 0
		_, err = fmt.Sscanf(line, "%d", &idx)
		if err != nil || idx < 1 || idx > len(instances) {
			fmt.Println("无效选择")
			continue
		}
		selected := instances[idx-1]
		client, err := NewECSClientWithRegion(&ECSConfig{
			AccessKeyID:     cfg.AccessKeyID,
			AccessKeySecret: cfg.AccessKeySecret,
			RegionID:        cfg.RegionID,
		}, cfg.RegionID)
		if err != nil {
			fmt.Printf("NewECSClientWithRegion failed: %v\n", err)
			continue
		}

		for {
			fmt.Printf("\n实例: %s (%s)\n", selected.InstanceID, selected.Instance.InstanceName)
			fmt.Println("1) 进入 ECS SSH (自动登录)")
			fmt.Println("2) 在ECS安装 Docker")
			fmt.Println("3) 部署配置和脚本到ECS (配置文件+运行脚本)")
			fmt.Println("q) 返回上一级")

			fmt.Print("选择: ")
			choice, _ := reader.ReadString('\n')
			choice = strings.TrimSpace(choice)
			if choice == "q" {
				break
			}

			switch choice {
			case "1":
				fmt.Println("进入SSH...")
				handleSSHLogin(client, &cfg, selected, reader)
			case "2":
				fmt.Println("开始安装Docker...")
				if err := remoteInstallDocker(client, cfg.RegionID, selected.InstanceID); err != nil {
					fmt.Printf("安装Docker失败: %v\n", err)
				} else {
					fmt.Println("Docker安装成功")
				}
			case "3":
				fmt.Println("开始部署配置和脚本...")
				if err := deployConfigAndScript(client, cfg.RegionID, selected.InstanceID); err != nil {
					fmt.Printf("部署配置和脚本失败: %v\n", err)
				} else {
					fmt.Println("配置和脚本部署成功")
					fmt.Println("在ECS上运行: /opt/indextts/run_container.sh")
				}
			default:
				fmt.Println("无效选择")
			}

			fmt.Print("\n按Enter键继续...")
			reader.ReadString('\n')
		}
	}
}

func main() {
	fmt.Println("=== ECS Docker自动化部署工具 ===")

	cfgPath := "config.yml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		fmt.Printf("读取配置失败: %v\n", err)
		return
	}

	var cfg ECSConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("解析配置失败: %v\n", err)
		return
	}

	interactiveDeployMenu(cfg)
}
