//go:build !release_ecs && !cloud_assistant_oss_main
// +build !release_ecs,!cloud_assistant_oss_main

// 简要说明：
// 本文件为交互式 ECS 管理与抢占购买工具的主要实现。
// 功能包括：
// - 查询并显示 ECS 实例列表（含地域、类型、公/私网 IP、状态、OS 信息）
// - 启动/停止/重启/释放实例
// - 抢占式实例购买（并行查询规格、镜像、价格；创建实例并等待启动）
// - SSH 自动登录（优先使用 Go SSH 库，失败回退到系统 ssh）
// - 在实例上通过云助手或本地命令执行安装/部署（与 cloud_assistant_oss.go 配合使用）
// 关键函数：LoadConfig, NewECSClientWithRegion, ListAllInstances, StartECS/StopECS/RebootECS,
// handlePurchaseInstance, autoSSHLoginWithGoSSH, waitForSSHReady, enrichInstancesWithOSInfo 等。
// 依赖：github.com/aliyun/alibaba-cloud-sdk-go、golang.org/x/crypto/ssh、gopkg.in/yaml.v3
// 注意：文件使用构建标签 `!release_ecs`，用于开发/调试版本。

package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/errors"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// PurchaseConfig 购买实例配置
type PurchaseConfig struct {
	InstanceTypes   []string `yaml:"instance_types"`
	ImageID         string   `yaml:"image_id"`
	SecurityGroupID string   `yaml:"security_group_id"`
	VSwitchID       string   `yaml:"vswitch_id"`
	SpotStrategy    string   `yaml:"spot_strategy"`
	SpotPriceLimit  float64  `yaml:"spot_price_limit"`
	SystemDiskSize  int      `yaml:"system_disk_size"`
	Password        string   `yaml:"password"`
}

// InstanceTypeWithComment 实例型号和备注
type InstanceTypeWithComment struct {
	InstanceType string // 实例型号1
	Comment      string // 中文备注
}

// InstanceTypeInfo 实例型号的完整信息
type InstanceTypeInfo struct {
	Index        int     // 序号
	InstanceType string  // 规格
	Comment      string  // 中文备注
	CPU          int     // vCPU核数
	Memory       float64 // 内存 GiB
	GPUModel     string  // GPU型号
	GPUCount     int     // GPU数量
	ImageID      string  // 镜像ID
	Price        float64 // 抢占价 元/小时
	PriceError   error   // 价格查询错误
	SpecError    error   // 规格查询错误
	ImageError   error   // 镜像查询错误
}

// ECSConfig 对应 config.yml 中的配置结构
type ECSConfig struct {
	AccessKeyID         string         `yaml:"access_key_id"`         // AccessKey ID
	AccessKeySecret     string         `yaml:"access_key_secret"`     // AccessKey Secret
	RegionID            string         `yaml:"region_id"`             // 实例地域ID（如 cn-hangzhou）
	InstanceID          string         `yaml:"instance_id"`           // GPU 实例 ID（可选）
	QueryAllRegions     bool           `yaml:"query_all_regions"`     // 是否查询全部地域
	ExcludedInstanceIDs []string       `yaml:"excluded_instance_ids"` // 排除的实例ID列表（这些实例不会出现在可操作列表中）
	PurchaseConfig      PurchaseConfig `yaml:"purchase_config"`       // 购买配置
}

// LoadConfig 从指定的 YAML 配置文件加载 ECSConfig
func LoadConfig(path string) (*ECSConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg ECSConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 简单校验必填字段（InstanceID 在查看列表时不是必需的）
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" || cfg.RegionID == "" {
		return nil, fmt.Errorf("配置文件字段不完整，请检查 access_key_id / access_key_secret / region_id")
	}

	return &cfg, nil
}

// NewECSClient 初始化 ECS 客户端
func NewECSClient(config *ECSConfig) (*ecs.Client, error) {
	return NewECSClientWithRegion(config, config.RegionID)
}

// NewECSClientWithRegion 使用指定地域初始化 ECS 客户端
func NewECSClientWithRegion(config *ECSConfig, regionID string) (*ecs.Client, error) {
	credential := credentials.NewAccessKeyCredential(config.AccessKeyID, config.AccessKeySecret)

	// 创建默认 SDK 配置
	sdkConfig := sdk.NewConfig()
	sdkConfig.Scheme = "https" // 使用 HTTPS

	if regionID == "" {
		regionID = config.RegionID
	}
	if regionID == "" {
		regionID = "cn-hangzhou"
	}

	client, err := ecs.NewClientWithOptions(regionID, sdkConfig, credential)
	if err != nil {
		return nil, fmt.Errorf("初始化 ECS 客户端失败: %w", err)
	}
	return client, nil
}

// GetECSStatus 查询 ECS 实例状态
func GetECSStatus(client *ecs.Client, instanceID string) (string, error) {
	request := ecs.CreateDescribeInstancesRequest()
	request.Scheme = "https"
	request.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

	response, err := client.DescribeInstances(request)
	if err != nil {
		return "", fmt.Errorf("查询实例状态失败: %w", err)
	}

	if len(response.Instances.Instance) == 0 {
		return "", fmt.Errorf("未找到实例: %s", instanceID)
	}

	status := response.Instances.Instance[0].Status
	log.Printf("实例 %s 当前状态: %s", instanceID, status)
	return status, nil
}

// StartECS 启动 ECS GPU 实例（按需开机）
func StartECS(client *ecs.Client, instanceID string) error {
	status, err := GetECSStatus(client, instanceID)
	if err != nil {
		return err
	}

	if status == "Running" {
		log.Printf("实例 %s 已处于开机状态，无需操作", instanceID)
		return nil
	}

	request := ecs.CreateStartInstanceRequest()
	request.Scheme = "https"
	request.InstanceId = instanceID

	_, err = client.StartInstance(request)
	if err != nil {
		if sdkErr, ok := err.(*errors.ServerError); ok {
			if sdkErr.ErrorCode() == "InvalidInstance.NotStopped" {
				log.Printf("实例 %s 未处于关机状态，启动失败: %s", instanceID, sdkErr.Message())
				return nil
			}
		}
		return fmt.Errorf("启动实例失败: %w", err)
	}

	log.Printf("实例 %s 启动请求已发送，等待状态变为 Running...", instanceID)
	for i := 0; i < 60; i++ { // 最多等待约 2 分钟
		currentStatus, _ := GetECSStatus(client, instanceID)
		if currentStatus == "Running" {
			log.Printf("实例 %s 已成功开机", instanceID)
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("实例 %s 启动超时", instanceID)
}

// StopECS 停止 ECS GPU 实例（按需关机）
func StopECS(client *ecs.Client, instanceID string, forceStop bool) error {
	status, err := GetECSStatus(client, instanceID)
	if err != nil {
		return err
	}

	if status == "Stopped" {
		log.Printf("实例 %s 已处于关机状态，无需操作", instanceID)
		return nil
	}

	request := ecs.CreateStopInstanceRequest()
	request.Scheme = "https"
	request.InstanceId = instanceID
	request.ForceStop = requests.NewBoolean(forceStop)

	_, err = client.StopInstance(request)
	if err != nil {
		if sdkErr, ok := err.(*errors.ServerError); ok {
			if sdkErr.ErrorCode() == "InvalidInstance.NotRunning" {
				log.Printf("实例 %s 未处于开机状态，停止失败: %s", instanceID, sdkErr.Message())
				return nil
			}
		}
		return fmt.Errorf("停止实例失败: %w", err)
	}

	log.Printf("实例 %s 停止请求已发送，等待状态变为 Stopped...", instanceID)
	for i := 0; i < 30; i++ {
		currentStatus, _ := GetECSStatus(client, instanceID)
		if currentStatus == "Stopped" {
			log.Printf("实例 %s 已成功关机", instanceID)
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("实例 %s 停止超时", instanceID)
}

// RebootECS 重启 ECS GPU 实例
func RebootECS(client *ecs.Client, instanceID string, forceReboot bool) error {
	status, err := GetECSStatus(client, instanceID)
	if err != nil {
		return err
	}

	if status == "Stopped" {
		// 如果已停止，先启动
		log.Printf("实例 %s 当前已停止，将先启动实例...", instanceID)
		if err := StartECS(client, instanceID); err != nil {
			return fmt.Errorf("启动实例失败: %w", err)
		}
		// 等待实例完全启动后再重启
		time.Sleep(5 * time.Second)
	}

	request := ecs.CreateRebootInstanceRequest()
	request.Scheme = "https"
	request.InstanceId = instanceID
	request.ForceStop = requests.NewBoolean(forceReboot)

	_, err = client.RebootInstance(request)
	if err != nil {
		if sdkErr, ok := err.(*errors.ServerError); ok {
			if sdkErr.ErrorCode() == "InvalidInstance.NotRunning" {
				log.Printf("实例 %s 未处于运行状态，重启失败: %s", instanceID, sdkErr.Message())
				return nil
			}
		}
		return fmt.Errorf("重启实例失败: %w", err)
	}

	log.Printf("实例 %s 重启请求已发送，等待状态变为 Running...", instanceID)
	for i := 0; i < 60; i++ { // 最多等待约 2 分钟
		currentStatus, _ := GetECSStatus(client, instanceID)
		if currentStatus == "Running" {
			log.Printf("实例 %s 已成功重启", instanceID)
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("实例 %s 重启超时", instanceID)
}

// GetECSStatusSilent 静默查询 ECS 实例状态（不输出日志）
func GetECSStatusSilent(client *ecs.Client, instanceID string) (string, error) {
	request := ecs.CreateDescribeInstancesRequest()
	request.Scheme = "https"
	request.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

	response, err := client.DescribeInstances(request)
	if err != nil {
		return "", fmt.Errorf("查询实例状态失败: %w", err)
	}

	if len(response.Instances.Instance) == 0 {
		return "", fmt.Errorf("未找到实例: %s", instanceID)
	}

	status := response.Instances.Instance[0].Status
	return status, nil
}

// InstanceListItem 实例列表项
type InstanceListItem struct {
	Index      int
	InstanceID string
	Region     string
	Instance   ecs.Instance
	OSInfo     *OSInfo // 操作系统信息（可选，通过镜像ID查询）
}

// OSInfo 操作系统信息
type OSInfo struct {
	OSName    string // 操作系统名称，如 "CentOS", "Ubuntu"
	Platform  string // 平台，如 "CentOS", "Ubuntu"
	OSVersion string // 操作系统版本，如 "7.9", "20.04"
}

// ListAllInstances 列出所有 ECS 实例（返回列表用于选择）
func ListAllInstances(config *ECSConfig) ([]InstanceListItem, error) {
	var regions []string
	if config.QueryAllRegions {
		// 获取所有地域
		baseClient, err := NewECSClient(config)
		if err != nil {
			return nil, fmt.Errorf("初始化客户端失败: %w", err)
		}
		req := ecs.CreateDescribeRegionsRequest()
		req.Scheme = "https"
		resp, err := baseClient.DescribeRegions(req)
		if err != nil {
			return nil, fmt.Errorf("查询地域列表失败: %w", err)
		}
		for _, r := range resp.Regions.Region {
			regions = append(regions, r.RegionId)
		}
	} else {
		regions = []string{config.RegionID}
	}

	// 构建排除实例ID的快速查找映射（提高查找效率）
	// 支持带横线和不带横线的格式匹配
	excludedMap := make(map[string]bool)
	for _, excludedID := range config.ExcludedInstanceIDs {
		excludedID = strings.TrimSpace(excludedID)
		if excludedID == "" {
			continue
		}
		// 同时存储原始格式和标准化格式
		excludedMap[excludedID] = true
		// 标准化格式：移除横线
		normalizedID := strings.ReplaceAll(excludedID, "-", "")
		if normalizedID != excludedID {
			excludedMap[normalizedID] = true
		}
		// 如果是不带横线的格式，也添加带横线的格式
		if !strings.Contains(excludedID, "-") && len(excludedID) > 1 {
			// 假设格式为 iXxx，转换为 i-xxx
			if excludedID[0] == 'i' && len(excludedID) > 1 {
				withDash := "i-" + excludedID[1:]
				excludedMap[withDash] = true
			}
		}
	}

	var allInstances []InstanceListItem
	index := 1
	excludedCount := 0 // 统计被排除的实例数量

	for _, region := range regions {
		client, err := NewECSClientWithRegion(config, region)
		if err != nil {
			log.Printf("初始化地域 %s 客户端失败: %v", region, err)
			continue
		}

		request := ecs.CreateDescribeInstancesRequest()
		request.Scheme = "https"
		request.PageSize = requests.NewInteger(100)
		pageNumber := 1

		for {
			request.PageNumber = requests.NewInteger(pageNumber)
			response, err := client.DescribeInstances(request)
			if err != nil {
				log.Printf("查询地域 %s 实例列表失败: %v", region, err)
				break
			}

			if len(response.Instances.Instance) == 0 {
				break
			}

			for _, instance := range response.Instances.Instance {
				// 检查是否在排除列表中（使用 map 快速查找）
				// 同时检查原始格式和标准化格式（移除横线）
				instanceID := instance.InstanceId
				normalizedInstanceID := strings.ReplaceAll(instanceID, "-", "")
				if excludedMap[instanceID] || excludedMap[normalizedInstanceID] {
					excludedCount++
					continue // 跳过被排除的实例
				}
				// 如果不在排除列表中，才添加到结果中
				allInstances = append(allInstances, InstanceListItem{
					Index:      index,
					InstanceID: instance.InstanceId,
					Region:     region,
					Instance:   instance,
				})
				index++
			}

			if len(response.Instances.Instance) < 100 {
				break
			}
			pageNumber++
		}
	}

	// 如果有实例被排除，输出提示信息
	if excludedCount > 0 {
		log.Printf("已排除 %d 个实例（根据 excluded_instance_ids 配置）", excludedCount)
	}

	return allInstances, nil
}

// DisplayInstances 显示实例列表
func DisplayInstances(instances []InstanceListItem) {
	statusMap := map[string]string{
		"Running":  "运行中",
		"Stopped":  "已停止",
		"Starting": "启动中",
		"Stopping": "停止中",
		"Pending":  "创建中",
	}

	fmt.Println("\n========================================")
	fmt.Println("   可操作的ECS实例")
	fmt.Println("========================================")

	for _, item := range instances {
		instance := item.Instance
		statusCN := statusMap[instance.Status]
		if statusCN == "" {
			statusCN = instance.Status
		}

		// 获取公网IP
		publicIP := "无"
		if len(instance.PublicIpAddress.IpAddress) > 0 {
			publicIP = instance.PublicIpAddress.IpAddress[0]
		}
		if instance.EipAddress.IpAddress != "" {
			if publicIP == "无" {
				publicIP = instance.EipAddress.IpAddress
			} else {
				publicIP += "," + instance.EipAddress.IpAddress
			}
		}

		// 获取内网IP
		privateIP := "无"
		if len(instance.InnerIpAddress.IpAddress) > 0 {
			privateIP = instance.InnerIpAddress.IpAddress[0]
		}

		// 获取实例名称
		instanceName := instance.InstanceName
		if instanceName == "" {
			instanceName = instance.InstanceId
		}

		// 获取操作系统信息
		osInfoStr := "未知"
		if item.OSInfo != nil {
			if item.OSInfo.OSVersion != "" {
				osInfoStr = fmt.Sprintf("%s %s", item.OSInfo.Platform, item.OSInfo.OSVersion)
			} else {
				osInfoStr = item.OSInfo.Platform
			}
			if osInfoStr == "" {
				osInfoStr = item.OSInfo.OSName
			}
			if osInfoStr == "" {
				osInfoStr = "未知"
			}
		}

		fmt.Printf("\n[%d] %s\n", item.Index, instanceName)
		fmt.Printf("    实例ID: %s\n", instance.InstanceId)
		fmt.Printf("    地域: %s\n", item.Region)
		fmt.Printf("    实例类型: %s\n", instance.InstanceType)
		fmt.Printf("    操作系统: %s\n", osInfoStr)
		fmt.Printf("    公网IP: %s\n", publicIP)
		fmt.Printf("    内网IP: %s\n", privateIP)
		fmt.Printf("    可用区: %s\n", instance.ZoneId)
		if instance.CreationTime != "" {
			fmt.Printf("    创建时间: %s\n", instance.CreationTime)
		}
		fmt.Printf("    状态: %s (%s)\n", statusCN, instance.Status)
	}

	fmt.Println("\n========================================")
	fmt.Printf("总计: %d 个实例\n", len(instances))
	fmt.Println("========================================")
}

// DeleteInstance 删除实例
func DeleteInstance(client *ecs.Client, instanceID string) error {
	req := ecs.CreateDeleteInstanceRequest()
	req.Scheme = "https"
	req.InstanceId = instanceID
	req.Force = requests.NewBoolean(true) // 强制释放

	resp, err := client.DeleteInstance(req)
	if err != nil {
		return fmt.Errorf("释放实例失败: %w", err)
	}
	fmt.Printf("释放成功，RequestId: %s, InstanceId: %s\n", resp.RequestId, instanceID)

	// 从管理列表中移除实例ID
	manager := NewInstanceManager("managed_instances.json")
	if err := manager.RemoveInstance(instanceID); err != nil {
		fmt.Printf("⚠️  警告: 从管理列表移除实例ID失败: %v\n", err)
	} else {
		fmt.Printf("✓ 实例ID已从管理列表移除\n")
	}

	return nil
}

// ListAllInstancesOld 列出所有 ECS 实例（旧版本，保持兼容）
func ListAllInstancesOld(client *ecs.Client) error {
	request := ecs.CreateDescribeInstancesRequest()
	request.Scheme = "https"
	// 不设置 InstanceIds，查询所有实例
	request.PageSize = requests.NewInteger(100) // 每页最多100个
	request.PageNumber = requests.NewInteger(1)

	// 状态中文映射
	statusMap := map[string]string{
		"Running":  "运行中",
		"Stopped":  "已停止",
		"Starting": "启动中",
		"Stopping": "停止中",
		"Pending":  "创建中",
	}

	totalCount := 0
	pageNumber := 1

	fmt.Println("\n========================================")
	fmt.Println("   ECS 实例列表")
	fmt.Println("========================================")

	for {
		request.PageNumber = requests.NewInteger(pageNumber)
		response, err := client.DescribeInstances(request)
		if err != nil {
			return fmt.Errorf("查询实例列表失败: %w", err)
		}

		if len(response.Instances.Instance) == 0 {
			break
		}

		for _, instance := range response.Instances.Instance {
			totalCount++
			statusCN := statusMap[instance.Status]
			if statusCN == "" {
				statusCN = instance.Status
			}

			// 获取公网IP
			publicIP := "无"
			if len(instance.PublicIpAddress.IpAddress) > 0 {
				publicIP = instance.PublicIpAddress.IpAddress[0]
			}

			// 获取内网IP
			privateIP := "无"
			if len(instance.InnerIpAddress.IpAddress) > 0 {
				privateIP = instance.InnerIpAddress.IpAddress[0]
			}

			// 获取实例名称（从Tags中查找）
			instanceName := instance.InstanceId
			for _, tag := range instance.Tags.Tag {
				if tag.TagKey == "Name" {
					instanceName = tag.TagValue
					break
				}
			}

			fmt.Printf("\n[%d] %s\n", totalCount, instanceName)
			fmt.Printf("    实例ID: %s\n", instance.InstanceId)
			fmt.Printf("    实例类型: %s\n", instance.InstanceType)
			fmt.Printf("    公网IP: %s\n", publicIP)
			fmt.Printf("    内网IP: %s\n", privateIP)
			fmt.Printf("    可用区: %s\n", instance.ZoneId)
			if instance.CreationTime != "" {
				fmt.Printf("    创建时间: %s\n", instance.CreationTime)
			}
			fmt.Printf("    状态: %s (%s)\n", statusCN, instance.Status)
		}

		// 检查是否还有更多页
		// 如果返回的实例数少于请求的页大小，说明已经是最后一页
		if len(response.Instances.Instance) < 100 {
			break
		}
		pageNumber++
	}

	fmt.Println("\n========================================")
	fmt.Printf("总计: %d 个实例\n", totalCount)
	fmt.Println("========================================")

	return nil
}

// showInteractiveMenu 显示交互式菜单（旧版本，已废弃，使用 showMainMenu 代替）
func showInteractiveMenu(client *ecs.Client, cfg *ECSConfig) {
	// 如果配置了 InstanceID，显示旧版菜单；否则使用新版主菜单
	if cfg.InstanceID != "" {
		// 先显示所有实例列表
		instances, err := ListAllInstances(cfg)
		if err != nil {
			fmt.Printf("⚠️  查询实例列表失败: %v\n", err)
		} else {
			// 查询并填充操作系统信息
			enrichInstancesWithOSInfo(cfg, instances)
			DisplayInstances(instances)
		}

		// 静默查询当前配置实例的状态
		status, err := GetECSStatusSilent(client, cfg.InstanceID)
		if err != nil {
			fmt.Printf("⚠️  查询当前实例状态失败: %v\n", err)
			status = "Unknown"
		}

		// 状态中文映射
		statusMap := map[string]string{
			"Running":  "运行中",
			"Stopped":  "已停止",
			"Starting": "启动中",
			"Stopping": "停止中",
		}
		statusCN := statusMap[status]
		if statusCN == "" {
			statusCN = status
		}

		fmt.Println("\n========================================")
		fmt.Println("   阿里云 ECS GPU 实例管理工具")
		fmt.Println("========================================")
		fmt.Printf("当前配置实例 (%s) 状态：%s (%s)\n", cfg.InstanceID, statusCN, status)
		fmt.Println("========================================")
		fmt.Println("请选择操作：")
		fmt.Println("  1. 开机")
		fmt.Println("  2. 关机")
		fmt.Println("  3. 重启")
		fmt.Println("  0. 退出")
		fmt.Println("========================================")
		fmt.Print("请输入选项 (0-3): ")

		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("读取输入失败: %v", err)
		}

		input = strings.TrimSpace(input)
		low := strings.ToLower(input)
		if low == "q" || low == "quit" || low == "exit" {
			fmt.Println("退出程序")
			os.Exit(0)
		}
		choice, err := strconv.Atoi(input)
		if err != nil {
			log.Fatalf("无效的选项: %s", input)
		}

		switch choice {
		case 1:
			fmt.Println("\n正在启动实例...")
			if err := StartECS(client, cfg.InstanceID); err != nil {
				log.Fatalf("启动实例失败: %v", err)
			}
			fmt.Println("✓ 实例启动成功")
		case 2:
			fmt.Println("\n正在停止实例...")
			if err := StopECS(client, cfg.InstanceID, false); err != nil {
				log.Fatalf("停止实例失败: %v", err)
			}
			fmt.Println("✓ 实例停止成功")
		case 3:
			fmt.Println("\n正在重启实例...")
			fmt.Print("是否强制重启？(y/N): ")
			forceInput, _ := reader.ReadString('\n')
			forceInput = strings.TrimSpace(strings.ToLower(forceInput))
			if forceInput == "q" || forceInput == "quit" || forceInput == "exit" {
				fmt.Println("退出程序")
				os.Exit(0)
			}
			forceReboot := forceInput == "y" || forceInput == "yes"
			if err := RebootECS(client, cfg.InstanceID, forceReboot); err != nil {
				log.Fatalf("重启实例失败: %v", err)
			}
			fmt.Println("✓ 实例重启成功")
		case 0:
			fmt.Println("退出程序")
			os.Exit(0)
		default:
			log.Fatalf("无效的选项: %d，请输入 0-3", choice)
		}
	} else {
		// 没有配置 InstanceID，使用新版主菜单
		showMainMenu(cfg)
	}
}

// showMainMenu 显示主菜单
func showMainMenu(config *ECSConfig) {
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println("\n========================================")
		fmt.Println("   阿里云 ECS 管理工具")
		fmt.Println("========================================")
		fmt.Println("请选择操作：")
		fmt.Println("  1. 查看机器列表")
		fmt.Println("  2. 抢占机器")
		fmt.Println("  q. 退出")
		fmt.Println("========================================")
		fmt.Print("请输入选项 (q 退出): ")

		input, err := reader.ReadString('\n')
		if err != nil {
			log.Fatalf("读取输入失败: %v", err)
		}

		input = strings.TrimSpace(input)
		low := strings.ToLower(input)
		if low == "q" || low == "quit" || low == "exit" {
			fmt.Println("退出程序")
			return
		}
		choice, err := strconv.Atoi(input)
		if err != nil {
			fmt.Println("无效的选项，请重试")
			continue
		}

		switch choice {
		case 1:
			handleListInstances(config, reader)
		case 2:
			handlePurchaseInstance(config)
		case 0:
			fmt.Println("退出程序")
			return
		default:
			fmt.Println("无效的选项，请重试")
		}
	}
}

// handleListInstances 处理查看机器列表
func handleListInstances(config *ECSConfig, reader *bufio.Reader) {
	fmt.Println("\n正在查询实例列表...")
	instances, err := ListAllInstances(config)
	if err != nil {
		fmt.Printf("查询实例列表失败: %v\n", err)
		return
	}

	if len(instances) == 0 {
		fmt.Println("未找到任何实例")
		return
	}

	// 查询并填充操作系统信息
	fmt.Println("正在查询实例操作系统信息...")
	enrichInstancesWithOSInfo(config, instances)

	DisplayInstances(instances)

	// 选择实例进行操作
	fmt.Print("\n请选择要操作的实例序号 (0 返回主菜单): ")
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	low := strings.ToLower(input)
	if low == "q" || low == "quit" || low == "exit" {
		return
	}
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 0 || choice > len(instances) {
		fmt.Println("无效的选择")
		return
	}
	if choice == 0 {
		return
	}

	selected := instances[choice-1]
	handleInstanceOperation(config, selected, reader)
}

// handleInstanceOperation 处理实例操作（开机/关机/释放）
func handleInstanceOperation(config *ECSConfig, instance InstanceListItem, reader *bufio.Reader) {
	client, err := NewECSClientWithRegion(config, instance.Region)
	if err != nil {
		fmt.Printf("初始化客户端失败: %v\n", err)
		return
	}

	statusMap := map[string]string{
		"Running":  "运行中",
		"Stopped":  "已停止",
		"Starting": "启动中",
		"Stopping": "停止中",
	}
	statusCN := statusMap[instance.Instance.Status]
	if statusCN == "" {
		statusCN = instance.Instance.Status
	}

	fmt.Printf("\n选中实例: %s (%s)\n", instance.Instance.InstanceId, statusCN)
	fmt.Println("请选择操作：")
	fmt.Println("  1. 开机")
	fmt.Println("  2. 关机")
	fmt.Println("  3. 重启")
	fmt.Println("  4. 释放")
	fmt.Println("  5. SSH 登录")
	fmt.Println("  0. 返回")
	fmt.Print("请输入选项 (0-5): ")

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	low := strings.ToLower(input)
	if low == "q" || low == "quit" || low == "exit" {
		return
	}
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 0 || choice > 5 {
		fmt.Println("无效的选项")
		return
	}

	switch choice {
	case 1:
		fmt.Println("\n正在启动实例...")
		if err := StartECS(client, instance.InstanceID); err != nil {
			fmt.Printf("启动实例失败: %v\n", err)
		} else {
			fmt.Println("✓ 实例启动成功")
		}
	case 2:
		fmt.Println("\n正在停止实例...")
		if err := StopECS(client, instance.InstanceID, false); err != nil {
			fmt.Printf("停止实例失败: %v\n", err)
		} else {
			fmt.Println("✓ 实例停止成功")
		}
	case 3:
		fmt.Println("\n正在重启实例...")
		if err := RebootECS(client, instance.InstanceID, false); err != nil {
			fmt.Printf("重启实例失败: %v\n", err)
		} else {
			fmt.Println("✓ 实例重启成功")
		}
	case 4:
		fmt.Printf("\n⚠️  警告: 释放实例将永久删除该实例及其数据！\n")
		fmt.Print("确认释放实例吗？(y/n): ")
		confirm, _ := reader.ReadString('\n')
		confirm = strings.TrimSpace(strings.ToLower(confirm))
		if confirm == "q" || confirm == "quit" || confirm == "exit" {
			fmt.Println("已取消释放")
			return
		}
		if confirm == "y" {
			if err := DeleteInstance(client, instance.InstanceID); err != nil {
				fmt.Printf("释放实例失败: %v\n", err)
			} else {
				fmt.Println("✓ 实例释放成功")
			}
		} else {
			fmt.Println("已取消释放")
		}
	case 5:
		handleSSHLogin(client, config, instance, reader)
	case 0:
		return
	}
}

// handleSSHLogin 处理 SSH 登录操作
func handleSSHLogin(client *ecs.Client, config *ECSConfig, instance InstanceListItem, reader *bufio.Reader) {
	fmt.Println("\n========================================")
	fmt.Println("  SSH 登录")
	fmt.Println("========================================")

	// 检查实例状态
	if instance.Instance.Status != "Running" {
		fmt.Printf("⚠️  警告: 实例状态为 %s，需要实例处于运行中状态才能SSH登录\n", instance.Instance.Status)
		fmt.Println("   请先启动实例后再进行SSH登录")
		return
	}

	// 获取实例IP
	fmt.Println("正在获取实例IP地址...")
	publicIP, privateIP, err := getInstanceIPsForPurchase(client, instance.Region, instance.InstanceID)
	if err != nil {
		fmt.Printf("⚠️  获取IP失败: %v\n", err)
		return
	}

	if publicIP == "" {
		fmt.Println("⚠️  注意: 当前实例没有公网IP，无法直接SSH登录")
		if privateIP != "" {
			fmt.Printf("   内网IP: %s\n", privateIP)
			fmt.Println("   提示: 需要通过跳板机或VPN才能访问")
		}
		return
	}

	fmt.Printf("公网IP: %s\n", publicIP)
	if privateIP != "" {
		fmt.Printf("内网IP: %s\n", privateIP)
	}

	var password string
	var username string = "root"

	// 优先使用 config.yml 中的密码
	if config.PurchaseConfig.Password != "" {
		password = config.PurchaseConfig.Password
		fmt.Printf("✓ 使用 config.yml 中的密码\n")
	} else {
		// 如果 config.yml 中没有，尝试从 managed_instances.json 读取密码
		manager := NewInstanceManager("managed_instances.json")
		instances, err := manager.GetInstances()
		if err != nil {
			log.Printf("读取管理实例列表失败: %v", err)
		} else {
			// 查找实例信息
			for _, managedInst := range instances {
				if managedInst.InstanceID == instance.InstanceID {
					if managedInst.Password != "" {
						password = managedInst.Password
						fmt.Printf("✓ 从管理列表中找到密码\n")
					}
					if managedInst.Username != "" {
						username = managedInst.Username
					}
					break
				}
			}
		}
	}

	// 如果还是没有密码，提示错误
	if password == "" {
		fmt.Println("⚠️  错误: 未找到密码")
		fmt.Println("   请在 config.yml 的 purchase_config.password 中配置密码")
		fmt.Println("   或确保实例信息已保存在 managed_instances.json 中")
		return
	}

	fmt.Printf("用户名: %s\n", username)
	fmt.Printf("密码: %s\n", maskString(password, 3))

	// 等待 SSH 服务就绪
	fmt.Println("\n注意: SSH 服务可能需要一些时间才能完全启动")
	if err := waitForSSHReady(publicIP, 5); err != nil {
		fmt.Printf("\n⚠️  警告: %v\n", err)
		fmt.Println("   SSH 服务可能仍在启动中，您可以稍后手动使用以下命令登录")
		fmt.Printf("   手动登录命令: ssh %s@%s\n", username, publicIP)
		if password != "" {
			fmt.Printf("   密码: %s\n", password)
		}
		return
	}

	// 直接执行自动登录
	fmt.Println("\n正在自动登录到实例...")

	// 注意：autoSSHLogin 函数目前只支持 root 用户
	// 如果用户名不是 root，需要提示用户
	if username != "root" {
		fmt.Printf("⚠️  注意: 当前用户名是 %s，但自动登录功能仅支持 root 用户\n", username)
		fmt.Printf("   请手动使用命令登录: ssh %s@%s\n", username, publicIP)
		if password != "" {
			fmt.Printf("   密码: %s\n", password)
		}
		return
	}

	if err := autoSSHLogin(publicIP, password); err != nil {
		fmt.Printf("\n⚠️  自动登录失败: %v\n", err)
		fmt.Printf("   您可以手动使用命令登录: ssh %s@%s\n", username, publicIP)
		if password != "" {
			fmt.Printf("   密码: %s\n", password)
		}
		fmt.Println("\n提示: 如果连接失败，可能是 SSH 服务尚未完全启动，请稍后重试")
	}
}

// parseInstanceTypesWithComments 解析实例型号列表，提取型号和备注
func parseInstanceTypesWithComments(instanceTypes []string) []InstanceTypeWithComment {
	result := make([]InstanceTypeWithComment, 0, len(instanceTypes))
	for _, item := range instanceTypes {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		// 查找 # 符号
		parts := strings.SplitN(item, "#", 2)
		instanceType := strings.TrimSpace(parts[0])
		comment := ""
		if len(parts) > 1 {
			comment = strings.TrimSpace(parts[1])
		}

		if instanceType != "" {
			result = append(result, InstanceTypeWithComment{
				InstanceType: instanceType,
				Comment:      comment,
			})
		}
	}
	return result
}

// getInstanceTypeInfoForPurchase 获取规格的 vCPU/内存/GPU 信息
func getInstanceTypeInfoForPurchase(client *ecs.Client, regionID, instanceType string) (cpu int, mem float64, gpuModel string, gpuCount int, err error) {
	req := ecs.CreateDescribeInstanceTypesRequest()
	req.Scheme = "https"
	req.RegionId = regionID

	resp, e := client.DescribeInstanceTypes(req)
	if e != nil {
		err = fmt.Errorf("查询实例规格失败: %w", e)
		return
	}
	for _, it := range resp.InstanceTypes.InstanceType {
		if it.InstanceTypeId == instanceType {
			cpu = it.CpuCoreCount
			mem = it.MemorySize
			gpuModel = it.GPUSpec
			gpuCount = it.GPUAmount
			return
		}
	}
	err = fmt.Errorf("未找到实例规格: %s", instanceType)
	return
}

// selectFirstLinux64Image 自动选择与实例规格兼容的 Linux 64 位公共镜像（返回第一个，大小<=40GB）
func selectFirstLinux64Image(client *ecs.Client, regionID, instanceType string) (string, error) {
	req := ecs.CreateDescribeImagesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ImageOwnerAlias = "system"
	req.Status = "Available"
	req.InstanceType = instanceType
	req.Architecture = "x86_64"
	req.OSType = "linux"
	req.PageSize = requests.NewInteger(50)

	maxImageSize := 40 // GB

	pageNumber := 1
	for {
		req.PageNumber = requests.NewInteger(pageNumber)
		resp, err := client.DescribeImages(req)
		if err != nil {
			return "", fmt.Errorf("查询镜像失败: %w", err)
		}
		for _, img := range resp.Images.Image {
			if img.Size > 0 && img.Size <= maxImageSize {
				return img.ImageId, nil
			}
		}
		if len(resp.Images.Image) < 50 {
			break
		}
		pageNumber++
	}
	return "", fmt.Errorf("未找到支持规格 %s 的 Linux 64 位公共镜像（大小<=%dGB）", instanceType, maxImageSize)
}

// getLatestSpotPrice 获取最近一次抢占式价格（元/小时）
func getLatestSpotPrice(client *ecs.Client, regionID, zoneID, instanceType string) (float64, error) {
	req := ecs.CreateDescribeSpotPriceHistoryRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ZoneId = zoneID
	req.InstanceType = instanceType
	req.NetworkType = "vpc"
	resp, err := client.DescribeSpotPriceHistory(req)
	if err != nil {
		return 0, fmt.Errorf("查询抢占式价格失败: %w", err)
	}
	if len(resp.SpotPrices.SpotPriceType) == 0 {
		return 0, fmt.Errorf("未获取到抢占式价格")
	}
	return resp.SpotPrices.SpotPriceType[0].SpotPrice, nil
}

// getVSwitchZoneForPurchase 查询交换机所属可用区
func getVSwitchZoneForPurchase(client *ecs.Client, regionID, vswitchID string) (string, error) {
	req := ecs.CreateDescribeVSwitchesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.VSwitchId = vswitchID
	req.PageSize = requests.NewInteger(1)

	resp, err := client.DescribeVSwitches(req)
	if err != nil {
		return "", fmt.Errorf("查询VSwitch失败: %w", err)
	}
	if len(resp.VSwitches.VSwitch) == 0 {
		return "", fmt.Errorf("未找到VSwitch: %s", vswitchID)
	}
	return resp.VSwitches.VSwitch[0].ZoneId, nil
}

// queryInstanceTypeInfoParallel 并行查询多个实例型号的完整信息
func queryInstanceTypeInfoParallel(client *ecs.Client, regionID, zoneID string, instanceTypesWithComments []InstanceTypeWithComment) []InstanceTypeInfo {
	results := make([]InstanceTypeInfo, len(instanceTypesWithComments))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, item := range instanceTypesWithComments {
		wg.Add(1)
		go func(idx int, instTypeWithComment InstanceTypeWithComment) {
			defer wg.Done()
			info := InstanceTypeInfo{
				Index:        idx + 1,
				InstanceType: instTypeWithComment.InstanceType,
				Comment:      instTypeWithComment.Comment,
			}

			// 并行查询规格信息
			cpu, mem, gpuModel, gpuCount, err := getInstanceTypeInfoForPurchase(client, regionID, instTypeWithComment.InstanceType)
			if err != nil {
				info.SpecError = err
			} else {
				info.CPU = cpu
				info.Memory = mem
				info.GPUModel = gpuModel
				info.GPUCount = gpuCount
			}

			// 并行查询镜像
			imgID, err := selectFirstLinux64Image(client, regionID, instTypeWithComment.InstanceType)
			if err != nil {
				info.ImageError = err
			} else {
				info.ImageID = imgID
			}

			// 并行查询价格
			price, err := getLatestSpotPrice(client, regionID, zoneID, instTypeWithComment.InstanceType)
			if err != nil {
				info.PriceError = err
			} else {
				info.Price = price
			}

			mu.Lock()
			results[idx] = info
			mu.Unlock()
		}(i, item)
	}

	wg.Wait()
	return results
}

// runSpotInstanceForPurchase 调用 RunInstances 创建抢占式实例
func runSpotInstanceForPurchase(client *ecs.Client, regionID, instanceType, imageID, securityGroupID, vswitchID, spotStrategy string, spotPriceLimit float64, spotDuration, systemDiskSize int, password string) (*ecs.RunInstancesResponse, error) {
	r := ecs.CreateRunInstancesRequest()
	r.Scheme = "https"
	r.RegionId = regionID
	r.InstanceChargeType = "PostPaid"
	if spotStrategy == "" {
		spotStrategy = "SpotAsPriceGo"
	}
	r.SpotStrategy = spotStrategy
	if spotStrategy == "SpotWithPriceLimit" && spotPriceLimit > 0 {
		r.SpotPriceLimit = requests.NewFloat(spotPriceLimit)
	}
	if spotDuration <= 0 {
		spotDuration = 1
	}
	r.SpotDuration = requests.NewInteger(spotDuration)

	r.InstanceType = instanceType
	r.ImageId = imageID

	// VSwitch 可用区对齐
	zoneID, err := getVSwitchZoneForPurchase(client, regionID, vswitchID)
	if err != nil {
		return nil, err
	}
	r.ZoneId = zoneID
	r.SecurityGroupId = securityGroupID
	r.VSwitchId = vswitchID

	if systemDiskSize <= 0 {
		systemDiskSize = 60
	}
	r.SystemDiskSize = fmt.Sprintf("%d", systemDiskSize)
	r.SystemDiskCategory = "cloud_essd"

	if password != "" {
		r.Password = password
	}

	// 配置公网带宽
	r.InternetChargeType = "PayByTraffic"
	r.InternetMaxBandwidthOut = requests.NewInteger(5)

	return client.RunInstances(r)
}

// waitForInstanceRunningForPurchase 等待实例启动完成
func waitForInstanceRunningForPurchase(client *ecs.Client, regionID, instanceID string, maxWaitMinutes int) error {
	fmt.Printf("\n等待实例启动中... (最多等待 %d 分钟)\n", maxWaitMinutes)
	maxWaitSeconds := maxWaitMinutes * 60
	checkInterval := 5

	for i := 0; i < maxWaitSeconds; i += checkInterval {
		req := ecs.CreateDescribeInstancesRequest()
		req.Scheme = "https"
		req.RegionId = regionID
		req.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

		resp, err := client.DescribeInstances(req)
		if err != nil {
			fmt.Printf("  查询实例状态失败: %v，继续等待...\n", err)
			time.Sleep(time.Duration(checkInterval) * time.Second)
			continue
		}

		if len(resp.Instances.Instance) == 0 {
			fmt.Printf("  实例 %s 未找到，继续等待...\n", instanceID)
			time.Sleep(time.Duration(checkInterval) * time.Second)
			continue
		}

		instance := resp.Instances.Instance[0]
		status := instance.Status
		fmt.Printf("  当前状态: %s", status)

		if status == "Running" {
			fmt.Println(" ✓")
			return nil
		}

		if status == "Stopped" || status == "Stopping" {
			return fmt.Errorf("实例状态异常: %s", status)
		}

		fmt.Println(" (继续等待...)")
		time.Sleep(time.Duration(checkInterval) * time.Second)
	}

	return fmt.Errorf("等待实例启动超时（超过 %d 分钟）", maxWaitMinutes)
}

// getInstanceIPsForPurchase 获取实例的公网IP和内网IP
func getInstanceIPsForPurchase(client *ecs.Client, regionID, instanceID string) (publicIP, privateIP string, err error) {
	req := ecs.CreateDescribeInstancesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

	resp, err := client.DescribeInstances(req)
	if err != nil {
		return "", "", fmt.Errorf("查询实例信息失败: %w", err)
	}

	if len(resp.Instances.Instance) == 0 {
		return "", "", fmt.Errorf("未找到实例: %s", instanceID)
	}

	instance := resp.Instances.Instance[0]

	if len(instance.PublicIpAddress.IpAddress) > 0 && instance.PublicIpAddress.IpAddress[0] != "" {
		publicIP = instance.PublicIpAddress.IpAddress[0]
	}

	if len(instance.InnerIpAddress.IpAddress) > 0 && instance.InnerIpAddress.IpAddress[0] != "" {
		privateIP = instance.InnerIpAddress.IpAddress[0]
	}

	if publicIP == "" && privateIP == "" {
		return "", "", fmt.Errorf("实例暂无IP地址")
	}

	return publicIP, privateIP, nil
}

// maskString 简单掩码
func maskString(s string, visible int) string {
	if len(s) <= visible*2 {
		return s
	}
	return s[:visible] + "..." + s[len(s)-visible:]
}

// checkSSHReady 检测SSH服务是否就绪（22端口是否可连接）
func checkSSHReady(host string, port int, timeout time.Duration) bool {
	address := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitForSSHReady 等待SSH服务就绪
func waitForSSHReady(publicIP string, maxWaitMinutes int) error {
	fmt.Printf("\n等待SSH服务就绪... (最多等待 %d 分钟)\n", maxWaitMinutes)
	maxWaitSeconds := maxWaitMinutes * 60
	checkInterval := 5 // 每5秒检查一次

	for i := 0; i < maxWaitSeconds; i += checkInterval {
		if checkSSHReady(publicIP, 22, 3*time.Second) {
			fmt.Println("  SSH服务已就绪 ✓")
			return nil
		}
		if i%30 == 0 { // 每30秒显示一次进度
			fmt.Printf("  等待中... (%d/%d秒)\n", i, maxWaitSeconds)
		}
		time.Sleep(time.Duration(checkInterval) * time.Second)
	}

	return fmt.Errorf("等待SSH服务就绪超时（超过 %d 分钟）", maxWaitMinutes)
}

// autoSSHLoginWithGoSSH 使用 Go SSH 库自动登录到实例
func autoSSHLoginWithGoSSH(publicIP, password string) error {
	fmt.Println("\n========================================")
	fmt.Println("  正在使用 Go SSH 库自动登录...")
	fmt.Println("========================================")

	// 在连接前再次确认 SSH 服务就绪（等待更长时间，确保服务完全启动）
	fmt.Println("正在确认 SSH 服务完全就绪...")
	for i := 0; i < 6; i++ { // 最多等待 30 秒（6次 × 5秒）
		if checkSSHReady(publicIP, 22, 5*time.Second) {
			// 端口已开放，再等待 2 秒确保服务完全就绪
			time.Sleep(2 * time.Second)
			break
		}
		if i < 5 {
			fmt.Printf("  SSH 服务尚未完全就绪，等待中... (%d/6)\n", i+1)
			time.Sleep(5 * time.Second)
		} else {
			return fmt.Errorf("SSH 服务未就绪，无法连接")
		}
	}

	// 配置 SSH 客户端
	config := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 跳过主机密钥验证（首次连接）
		Timeout:         30 * time.Second,            // 增加超时时间到 30 秒
	}

	// 建立连接（带重试机制）
	address := fmt.Sprintf("%s:22", publicIP)
	fmt.Printf("正在连接到 %s...\n", address)

	var client *ssh.Client
	var err error
	maxRetries := 3
	retryDelay := 5 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		client, err = ssh.Dial("tcp", address, config)
		if err == nil {
			break
		}

		if attempt < maxRetries {
			fmt.Printf("  连接失败 (尝试 %d/%d): %v\n", attempt, maxRetries, err)
			fmt.Printf("  等待 %v 后重试...\n", retryDelay)
			time.Sleep(retryDelay)
		} else {
			return fmt.Errorf("SSH连接失败（已重试 %d 次）: %w", maxRetries, err)
		}
	}

	defer client.Close()

	fmt.Println("✓ 连接成功！")

	// 创建会话
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	defer session.Close()

	// 获取终端文件描述符
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// 如果不是终端，使用简单的命令执行模式
		fmt.Println("⚠️  当前不是终端环境，将使用命令执行模式")
		fmt.Println("   提示: 输入命令后按回车执行，输入 'exit' 退出")

		// 设置标准输入输出
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		session.Stdin = os.Stdin

		// 启动交互式 shell
		if err := session.Shell(); err != nil {
			return fmt.Errorf("启动shell失败: %w", err)
		}

		// 等待会话结束
		return session.Wait()
	}

	// 设置终端为原始模式
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("设置终端模式失败: %w", err)
	}
	defer term.Restore(fd, oldState)

	// 获取终端大小
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 24 // 默认大小
	}

	// 请求伪终端
	if err := session.RequestPty("xterm-256color", height, width, ssh.TerminalModes{
		ssh.ECHO:          1,     // 启用回显
		ssh.TTY_OP_ISPEED: 14400, // 输入速度
		ssh.TTY_OP_OSPEED: 14400, // 输出速度
	}); err != nil {
		return fmt.Errorf("请求伪终端失败: %w", err)
	}

	// 连接标准输入输出
	session.Stdout = os.Stdout
	session.Stdin = os.Stdin
	session.Stderr = os.Stderr

	// 启动交互式 shell
	if err := session.Shell(); err != nil {
		return fmt.Errorf("启动shell失败: %w", err)
	}

	fmt.Println("已进入SSH会话，输入 'exit' 退出")

	// 等待会话结束
	return session.Wait()
}

// autoSSHLoginFallback 回退到系统 ssh 命令（交互式登录）
func autoSSHLoginFallback(publicIP, password string) error {
	fmt.Println("\n========================================")
	fmt.Println("  使用系统 SSH 客户端（交互式登录）")
	fmt.Println("========================================")
	fmt.Printf("⚠️  未找到 Go SSH 库或连接失败，使用系统 SSH 客户端\n")
	fmt.Printf("   密码: %s\n", password)
	fmt.Println("   请在SSH提示时输入密码")

	// 检查是否有 ssh 命令
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("未找到 ssh 命令，请确保已安装 OpenSSH 客户端")
	}

	// 使用标准SSH命令（交互式）
	cmd := exec.Command(sshPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		fmt.Sprintf("root@%s", publicIP))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("正在启动SSH连接...")
	return cmd.Run()
}

// autoSSHLogin 统一的 SSH 登录入口（优先使用 Go SSH，失败则回退）
func autoSSHLogin(publicIP, password string) error {
	if password == "" {
		return fmt.Errorf("密码为空，无法自动登录")
	}

	// 优先尝试使用 Go SSH 库
	err := autoSSHLoginWithGoSSH(publicIP, password)
	if err != nil {
		fmt.Printf("\n⚠️  Go SSH 库登录失败: %v\n", err)
		fmt.Println("正在回退到系统 SSH 客户端...")
		return autoSSHLoginFallback(publicIP, password)
	}

	return nil
}

// getImageOSInfo 根据镜像ID获取操作系统信息
func getImageOSInfo(client *ecs.Client, regionID, imageID string) (*OSInfo, error) {
	if imageID == "" {
		return nil, fmt.Errorf("镜像ID为空")
	}

	req := ecs.CreateDescribeImagesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ImageId = imageID
	req.PageSize = requests.NewInteger(1)

	resp, err := client.DescribeImages(req)
	if err != nil {
		return nil, fmt.Errorf("查询镜像信息失败: %w", err)
	}

	if len(resp.Images.Image) == 0 {
		return nil, fmt.Errorf("未找到镜像: %s", imageID)
	}

	img := resp.Images.Image[0]
	osVersion := ""

	// 从镜像名称中提取版本信息
	if img.ImageName != "" {
		osVersion = extractOSVersionFromImageName(img.ImageName, img.Platform)
	}

	return &OSInfo{
		OSName:    img.OSName,
		Platform:  img.Platform,
		OSVersion: osVersion,
	}, nil
}

// extractOSVersionFromImageName 从镜像名称中提取操作系统版本
func extractOSVersionFromImageName(imageName, platform string) string {
	// 常见的镜像命名模式：
	// - "CentOS_7.9_64" -> "7.9"
	// - "Ubuntu_20.04_64" -> "20.04"
	// - "Aliyun Linux 2.1903" -> "2.1903"

	if strings.Contains(imageName, "CentOS") {
		parts := strings.Fields(imageName)
		for _, part := range parts {
			if strings.Contains(part, ".") && len(part) < 10 {
				return part
			}
		}
	} else if strings.Contains(imageName, "Ubuntu") {
		parts := strings.Fields(imageName)
		for _, part := range parts {
			if strings.Contains(part, ".") && len(part) < 10 {
				return part
			}
		}
	} else if strings.Contains(imageName, "Aliyun") {
		// Aliyun Linux 可能包含版本号
		parts := strings.Fields(imageName)
		for _, part := range parts {
			if strings.Contains(part, ".") || (len(part) > 0 && part[0] >= '0' && part[0] <= '9') {
				return part
			}
		}
	}

	return ""
}

// enrichInstancesWithOSInfo 批量查询并填充实例的操作系统信息
func enrichInstancesWithOSInfo(config *ECSConfig, instances []InstanceListItem) {
	// 按地域分组，避免重复创建客户端
	regionClients := make(map[string]*ecs.Client)

	// 并行查询镜像信息
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := range instances {
		instance := instances[i].Instance
		region := instances[i].Region
		imageID := instance.ImageId

		if imageID == "" {
			continue // 跳过没有镜像ID的实例
		}

		wg.Add(1)
		go func(idx int, reg, imgID string) {
			defer wg.Done()

			// 获取或创建该地域的客户端
			mu.Lock()
			client, exists := regionClients[reg]
			if !exists {
				var err error
				client, err = NewECSClientWithRegion(config, reg)
				if err != nil {
					mu.Unlock()
					log.Printf("初始化地域 %s 客户端失败: %v", reg, err)
					return
				}
				regionClients[reg] = client
			}
			mu.Unlock()

			// 查询镜像信息
			osInfo, err := getImageOSInfo(client, reg, imgID)
			if err != nil {
				// 查询失败不影响显示，只是不显示OS信息
				log.Printf("查询实例 %s 的镜像信息失败: %v", instances[idx].InstanceID, err)
				return
			}

			// 更新实例的OS信息
			mu.Lock()
			instances[idx].OSInfo = osInfo
			mu.Unlock()
		}(i, region, imageID)
	}

	wg.Wait()
}

// handlePurchaseInstance 处理抢占机器
func handlePurchaseInstance(config *ECSConfig) {
	// 验证购买配置
	if len(config.PurchaseConfig.InstanceTypes) == 0 {
		fmt.Println("❌ 配置文件中 purchase_config.instance_types 为空，请至少配置一个实例型号")
		return
	}
	if config.PurchaseConfig.SecurityGroupID == "" {
		fmt.Println("❌ 配置文件中缺少 purchase_config.security_group_id")
		return
	}
	if config.PurchaseConfig.VSwitchID == "" {
		fmt.Println("❌ 配置文件中缺少 purchase_config.vswitch_id")
		return
	}

	region := config.RegionID
	if region == "" {
		region = "cn-shenzhen"
	}

	client, err := NewECSClientWithRegion(config, region)
	if err != nil {
		fmt.Printf("初始化客户端失败: %v\n", err)
		return
	}

	// 获取 VSwitch 可用区
	zoneID, err := getVSwitchZoneForPurchase(client, region, config.PurchaseConfig.VSwitchID)
	if err != nil {
		fmt.Printf("获取VSwitch可用区失败: %v\n", err)
		return
	}

	// 解析实例型号和备注
	instanceTypesWithComments := parseInstanceTypesWithComments(config.PurchaseConfig.InstanceTypes)
	if len(instanceTypesWithComments) == 0 {
		fmt.Println("解析后的实例型号列表为空，请检查配置")
		return
	}

	// 并行查询所有实例型号的信息
	fmt.Printf("\n正在并行查询 %d 个实例型号的信息...\n", len(instanceTypesWithComments))
	instanceInfos := queryInstanceTypeInfoParallel(client, region, zoneID, instanceTypesWithComments)

	// 展示所有实例型号信息
	fmt.Println("\n========================================")
	fmt.Println("  可用实例型号列表")
	fmt.Println("========================================")
	fmt.Printf("%-5s %-30s %-50s %-12s %-12s %-20s %-15s\n", "序号", "实例规格", "备注", "vCPU", "内存(GiB)", "GPU", "价格(元/时)")
	fmt.Println(strings.Repeat("-", 150))

	validInstances := []InstanceTypeInfo{}
	for _, info := range instanceInfos {
		if info.SpecError != nil || info.ImageError != nil {
			fmt.Printf("[%d] %-30s (查询失败: 规格=%v, 镜像=%v)\n",
				info.Index, info.InstanceType, info.SpecError, info.ImageError)
			continue
		}

		validInstances = append(validInstances, info)

		cpuStr := fmt.Sprintf("%d核", info.CPU)
		memStr := fmt.Sprintf("%.1f", info.Memory)
		gpuStr := "无"
		if info.GPUCount > 0 && info.GPUModel != "" {
			gpuStr = fmt.Sprintf("%s x%d", info.GPUModel, info.GPUCount)
		}
		priceStr := "N/A"
		if info.PriceError == nil && info.Price > 0 {
			priceStr = fmt.Sprintf("%.4f", info.Price)
		}

		comment := info.Comment
		if len(comment) > 48 {
			comment = comment[:45] + "..."
		}
		if comment == "" {
			comment = "-"
		}

		fmt.Printf("[%d] %-30s %-50s %-12s %-12s %-20s %-15s\n",
			info.Index, info.InstanceType, comment, cpuStr, memStr, gpuStr, priceStr)
	}

	if len(validInstances) == 0 {
		fmt.Println("没有可用的实例型号，请检查配置")
		return
	}

	// 用户选择
	fmt.Println(strings.Repeat("-", 150))
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("\n请选择要购买的实例型号 (输入序号 1-%d，或 0 取消): ", len(validInstances))
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	low := strings.ToLower(line)
	if low == "q" || low == "quit" || low == "exit" {
		fmt.Println("已取消购买")
		return
	}
	choice, err := strconv.Atoi(line)
	if err != nil || choice < 0 || choice > len(validInstances) {
		fmt.Println("无效的选择，已取消")
		return
	}
	if choice == 0 {
		fmt.Println("已取消购买")
		return
	}

	selectedInfo := validInstances[choice-1]

	// 构建购买请求
	spotStrategy := config.PurchaseConfig.SpotStrategy
	if spotStrategy == "" {
		spotStrategy = "SpotAsPriceGo"
	}
	systemDiskSize := config.PurchaseConfig.SystemDiskSize
	if systemDiskSize <= 0 {
		systemDiskSize = 60
	}

	// 显示选中实例的详细信息
	fmt.Println("\n========================================")
	fmt.Println("  选中实例详细信息")
	fmt.Println("========================================")
	fmt.Printf("  Region: %s\n", region)
	fmt.Printf("  Zone:   %s (来自 VSwitch)\n", zoneID)
	fmt.Printf("  InstanceType: %s\n", selectedInfo.InstanceType)
	if selectedInfo.Comment != "" {
		fmt.Printf("  备注: %s\n", selectedInfo.Comment)
	}
	fmt.Printf("  规格资源: %d 核 (vCPU) %.1f GiB\n", selectedInfo.CPU, selectedInfo.Memory)
	if selectedInfo.GPUCount > 0 && selectedInfo.GPUModel != "" {
		fmt.Printf("  GPU: %s x%d\n", selectedInfo.GPUModel, selectedInfo.GPUCount)
	}
	fmt.Printf("  ImageId: %s\n", selectedInfo.ImageID)
	fmt.Printf("  SecurityGroupId: %s\n", config.PurchaseConfig.SecurityGroupID)
	fmt.Printf("  VSwitchId: %s\n", config.PurchaseConfig.VSwitchID)
	fmt.Printf("  SpotStrategy: %s", spotStrategy)
	if spotStrategy == "SpotWithPriceLimit" && config.PurchaseConfig.SpotPriceLimit > 0 {
		fmt.Printf(" (Limit: %.4f)", config.PurchaseConfig.SpotPriceLimit)
	}
	fmt.Println()
	fmt.Printf("  SpotDuration: 1 小时\n")
	fmt.Printf("  SystemDisk: %d GB, %s\n", systemDiskSize, "cloud_essd")
	if config.PurchaseConfig.Password != "" {
		fmt.Printf("  Password: %s\n", maskString(config.PurchaseConfig.Password, 3))
	}
	priceStr := "N/A"
	if selectedInfo.PriceError == nil && selectedInfo.Price > 0 {
		priceStr = fmt.Sprintf("%.4f 元/小时", selectedInfo.Price)
	}
	fmt.Printf("  当前抢占价: %s\n", priceStr)

	// 最终确认
	fmt.Print("\n确认购买吗？(y/N): ")
	line, _ = reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "q" || line == "quit" || line == "exit" {
		fmt.Println("已取消购买")
		return
	}
	if line != "y" && line != "yes" {
		fmt.Println("已取消购买")
		return
	}

	// 创建实例
	fmt.Println("\n正在创建抢占式实例...")
	resp, err := runSpotInstanceForPurchase(client, region, selectedInfo.InstanceType, selectedInfo.ImageID,
		config.PurchaseConfig.SecurityGroupID, config.PurchaseConfig.VSwitchID,
		spotStrategy, config.PurchaseConfig.SpotPriceLimit, 1, systemDiskSize,
		config.PurchaseConfig.Password)
	if err != nil {
		fmt.Printf("创建抢占式实例失败: %v\n", err)
		return
	}

	fmt.Println("\n========================================")
	fmt.Println("  创建成功")
	fmt.Println("========================================")
	fmt.Printf("RequestId: %s\n", resp.RequestId)
	if len(resp.InstanceIdSets.InstanceIdSet) == 0 {
		fmt.Println("未返回实例ID")
		return
	}

	instanceID := resp.InstanceIdSets.InstanceIdSet[0]
	fmt.Printf("InstanceId: %s\n", instanceID)

	// 等待实例启动
	if err := waitForInstanceRunningForPurchase(client, region, instanceID, 10); err != nil {
		fmt.Printf("\n⚠️  警告: %v\n", err)
		fmt.Printf("实例可能仍在启动中，请稍后在控制台查看状态\n")
		return
	}

	// 获取实例IP
	fmt.Println("\n正在获取实例IP地址...")
	publicIP, privateIP, err := getInstanceIPsForPurchase(client, region, instanceID)
	if err != nil {
		fmt.Printf("⚠️  获取IP失败: %v\n", err)
		return
	}

	fmt.Println("\n========================================")
	fmt.Println("  SSH 登录信息")
	fmt.Println("========================================")
	if privateIP != "" {
		fmt.Printf("内网IP: %s\n", privateIP)
	}
	if publicIP != "" {
		fmt.Printf("公网IP: %s\n", publicIP)
		fmt.Printf("用户名: root\n")
		if config.PurchaseConfig.Password != "" {
			fmt.Printf("密码: %s\n", config.PurchaseConfig.Password)
		}
		fmt.Printf("\nSSH 登录命令: ssh root@%s\n", publicIP)

		// 等待 SSH 服务就绪（增加等待时间，确保服务完全启动）
		fmt.Println("\n注意: SSH 服务可能需要一些时间才能完全启动")
		if err := waitForSSHReady(publicIP, 10); err != nil {
			fmt.Printf("\n⚠️  警告: %v\n", err)
			fmt.Println("   SSH 服务可能仍在启动中，您可以稍后手动使用上述命令登录")
			fmt.Printf("   手动登录命令: ssh root@%s\n", publicIP)
			if config.PurchaseConfig.Password != "" {
				fmt.Printf("   密码: %s\n", config.PurchaseConfig.Password)
			}
			return
		}

		// 询问用户是否自动登录
		if config.PurchaseConfig.Password != "" {
			fmt.Print("\n是否自动登录到实例？(y/N): ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line == "q" || line == "quit" || line == "exit" {
				fmt.Println("已跳过自动登录")
				return
			}

			if line == "y" || line == "yes" {
				// 执行自动登录
				if err := autoSSHLogin(publicIP, config.PurchaseConfig.Password); err != nil {
					fmt.Printf("\n⚠️  自动登录失败: %v\n", err)
					fmt.Printf("   您可以手动使用命令登录: ssh root@%s\n", publicIP)
					fmt.Printf("   密码: %s\n", config.PurchaseConfig.Password)
					fmt.Println("\n提示: 如果连接失败，可能是 SSH 服务尚未完全启动，请稍后重试")
				}
			} else {
				fmt.Println("已跳过自动登录")
			}
		} else {
			fmt.Println("\n⚠️  注意: 未配置密码，无法自动登录")
			fmt.Printf("   请手动使用命令登录: ssh root@%s\n", publicIP)
		}
	} else {
		fmt.Println("⚠️  注意: 当前实例没有公网IP，无法直接SSH登录")
	}
}
