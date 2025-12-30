//go:build ecs_buy
// +build ecs_buy

// 简要说明：
// 本文件为面向“购买并在实例上挂载 OSS”的交互式工具变体。
// 功能包括：
// - 读取配置并列出 ECS 实例
// - 抢占式购买（选择规格、自动选择镜像、创建实例、等待启动）
// - 在选中实例上触发 OSS 挂载逻辑（占位实现，建议复用 cloud_assistant_oss.go 的实现）
// - 包含与 run_ecs.go 部分重复的查询/展示/购买逻辑（可考虑抽取共享包）
// 关键函数：loadConfig, newECSClient, listAllInstances, runSpotInstance, executeOSSMountOnInstance（占位）
// 依赖：github.com/aliyun/alibaba-cloud-sdk-go、gopkg.in/yaml.v3
// 注意：文件使用构建标签 `!release_ecs`，与 run_ecs.go 属于开发/调试版本。

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"gopkg.in/yaml.v3"
)

// RunSpotInstanceRequest 输入参数
type RunSpotInstanceRequest struct {
	RegionID        string
	InstanceType    string
	ImageID         string
	SecurityGroupID string
	VSwitchID       string
	SpotStrategy    string // SpotAsPriceGo / SpotWithPriceLimit
	SpotPriceLimit  float64
	SpotDuration    int    // 小时，1 表示至少运行 1 小时
	SystemDiskSize  int    // GB
	Password        string // 可选
}

type purchaseConfig struct {
	InstanceTypes   []string `yaml:"instance_types"`
	ImageID         string   `yaml:"image_id"`
	SecurityGroupID string   `yaml:"security_group_id"`
	VSwitchID       string   `yaml:"vswitch_id"`
	SpotStrategy    string   `yaml:"spot_strategy"`
	SpotPriceLimit  float64  `yaml:"spot_price_limit"`
	SystemDiskSize  int      `yaml:"system_disk_size"`
	Password        string   `yaml:"password"`
}

// OSSConfig OSS 配置结构
type OSSConfig struct {
	Bucket          string `yaml:"bucket"`            // OSS Bucket 名称
	Endpoint        string `yaml:"endpoint"`          // OSS Endpoint
	AccessKeyID     string `yaml:"access_key_id"`     // AccessKey ID（可选，如果为空则从主配置读取）
	AccessKeySecret string `yaml:"access_key_secret"` // AccessKey Secret（可选，如果为空则从主配置读取）
	MountPoint      string `yaml:"mount_point"`       // 挂载点路径，默认 /mnt/oss
	AutoRemount     bool   `yaml:"auto_remount"`      // 启动时自动重新挂载，默认 true
}

// CloudAssistantConfig 云助手配置结构
type CloudAssistantConfig struct {
	// OSS 挂载命令配置
	OSSMountCommand struct {
		Name       string `yaml:"name"`        // 命令名称
		Timeout    int    `yaml:"timeout"`     // 命令超时时间（秒）
		AutoCreate bool   `yaml:"auto_create"` // 是否自动创建命令
		Content    string `yaml:"content"`     // 自定义命令内容（可选）
	} `yaml:"oss_mount_command"`

	// 执行配置
	Execution struct {
		RepeatMode        string `yaml:"repeat_mode"`         // 执行模式：Once/EveryReboot
		WaitForCompletion bool   `yaml:"wait_for_completion"` // 是否等待执行完成
		WaitTimeout       int    `yaml:"wait_timeout"`        // 等待超时时间（秒）
		PollInterval      int    `yaml:"poll_interval"`       // 查询结果间隔（秒）
	} `yaml:"execution"`
}

// DockerConfig Docker 配置结构
type DockerConfig struct {
	ImageFile    string `yaml:"image_file"`     // Docker 镜像文件绝对路径（在 OSS 挂载点内）
	AutoLoad     bool   `yaml:"auto_load"`      // 是否自动加载镜像，默认 true
	SkipIfExists bool   `yaml:"skip_if_exists"` // 如果镜像已存在则跳过，默认 true
	ImageName    string `yaml:"image_name"`     // 镜像名称（用于检查是否已存在，可选）
	ImageTag     string `yaml:"image_tag"`      // 镜像标签（用于检查是否已存在，可选）
}

type configFile struct {
	AccessKeyID         string               `yaml:"access_key_id"`
	AccessKeySecret     string               `yaml:"access_key_secret"`
	RegionID            string               `yaml:"region_id"`
	PurchaseConfig      purchaseConfig       `yaml:"purchase_config"`
	QueryAllRegions     bool                 `yaml:"query_all_regions"`     // 是否查询全部地域
	ExcludedInstanceIDs []string             `yaml:"excluded_instance_ids"` // 排除的实例ID列表
	OSSConfig           OSSConfig            `yaml:"oss_config"`            // OSS配置
	CloudAssistant      CloudAssistantConfig `yaml:"cloud_assistant"`       // 云助手配置
	DockerConfig        DockerConfig         `yaml:"docker_config"`         // Docker配置
}

// InstanceTypeWithComment 实例型号和备注
type InstanceTypeWithComment struct {
	InstanceType string // 实例型号，如 "ecs.gn6v-c8g1.2xlarge"
	Comment      string // 中文备注，如 "机型用于低层的tts运行，费用低。"
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

// loadAccessKey 尝试从环境变量获取；若缺失则从 config.yml 读取
func loadAccessKey(defaultRegion string) (ak, sk, region string, err error) {
	ak = os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID")
	sk = os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	region = os.Getenv("ALIBABA_CLOUD_REGION_ID")
	if region == "" {
		region = defaultRegion
	}
	if ak != "" && sk != "" {
		return
	}
	cfgPath := "config.yml"
	data, readErr := os.ReadFile(cfgPath)
	if readErr != nil {
		err = fmt.Errorf("缺少环境变量且读取配置失败: %w", readErr)
		return
	}
	var cfg configFile
	if unmarshalErr := yaml.Unmarshal(data, &cfg); unmarshalErr != nil {
		err = fmt.Errorf("解析配置文件失败: %w", unmarshalErr)
		return
	}
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		err = fmt.Errorf("配置文件缺少 access_key_id / access_key_secret")
		return
	}
	ak = cfg.AccessKeyID
	sk = cfg.AccessKeySecret
	if region == "" {
		region = cfg.RegionID
	}
	if region == "" {
		region = defaultRegion
	}
	return
}

// newECSClient 创建 ECS Client（优先使用传入的 regionID，来自 config.yml）
func newECSClient(regionID string) (*ecs.Client, error) {
	ak, sk, _, err := loadAccessKey(regionID)
	if err != nil {
		return nil, err
	}

	// 优先使用传入的 regionID（确保使用 config.yml 中指定的地域）
	// 如果 regionID 为空，使用默认值
	if regionID == "" {
		regionID = "cn-hangzhou" // 默认值
	}
	cfg := sdk.NewConfig()
	cfg.Timeout = 20 * time.Second
	cred := credentials.NewAccessKeyCredential(ak, sk)
	return ecs.NewClientWithOptions(regionID, cfg, cred)
}

// ImageInfo 镜像信息
type ImageInfo struct {
	ImageID   string
	OSName    string
	Platform  string
	OSVersion string
}

// selectFirstLinux64Image 自动选择与实例规格兼容的 Linux 64 位公共镜像（返回第一个，大小<=40GB）
func selectFirstLinux64Image(cli *ecs.Client, regionID, instanceType string) (string, error) {
	imgInfo, err := selectFirstLinux64ImageWithInfo(cli, regionID, instanceType)
	if err != nil {
		return "", err
	}
	return imgInfo.ImageID, nil
}

// selectFirstLinux64ImageWithInfo 自动选择与实例规格兼容的 Linux 64 位公共镜像（返回镜像详细信息）
func selectFirstLinux64ImageWithInfo(cli *ecs.Client, regionID, instanceType string) (*ImageInfo, error) {
	req := ecs.CreateDescribeImagesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ImageOwnerAlias = "system"
	req.Status = "Available"
	req.InstanceType = instanceType
	req.Architecture = "x86_64"
	req.OSType = "linux"
	req.PageSize = requests.NewInteger(50)

	maxImageSize := 40 // GB，镜像大小限制

	pageNumber := 1
	for {
		req.PageNumber = requests.NewInteger(pageNumber)
		resp, err := cli.DescribeImages(req)
		if err != nil {
			return nil, fmt.Errorf("查询镜像失败: %w", err)
		}
		// 遍历镜像，找到第一个大小<=40GB的镜像
		for _, img := range resp.Images.Image {
			// Size 字段单位是GB，检查是否<=40GB
			if img.Size > 0 && img.Size <= maxImageSize {
				// 解析镜像信息
				osName := img.OSName
				platform := img.Platform
				osVersion := ""

				// 从镜像名称中提取版本信息
				if img.ImageName != "" {
					// 尝试从镜像名称中提取版本，例如 "CentOS_7.9_64" -> "7.9"
					osVersion = extractOSVersionFromImageName(img.ImageName, platform)
				}

				return &ImageInfo{
					ImageID:   img.ImageId,
					OSName:    osName,
					Platform:  platform,
					OSVersion: osVersion,
				}, nil
			}
		}
		if len(resp.Images.Image) < 50 {
			break
		}
		pageNumber++
	}
	return nil, fmt.Errorf("未找到支持规格 %s 的 Linux 64 位公共镜像（大小<=%dGB）", instanceType, maxImageSize)
}

// extractOSVersionFromImageName 从镜像名称中提取操作系统版本
func extractOSVersionFromImageName(imageName, platform string) string {
	// 常见的镜像命名模式：
	// - "CentOS_7.9_64" -> "7.9"
	// - "Ubuntu_20.04_64" -> "20.04"
	// - "Aliyun Linux 2.1903" -> "2.1903"

	// 如果镜像名称包含版本号模式，提取它
	// 这里使用简单的字符串匹配，可以根据实际镜像命名规则调整
	if strings.Contains(imageName, "CentOS") {
		// 查找 "CentOS_X.Y" 模式
		parts := strings.Fields(imageName)
		for _, part := range parts {
			if strings.Contains(part, ".") && len(part) < 10 {
				return part
			}
		}
	} else if strings.Contains(imageName, "Ubuntu") {
		// 查找 "Ubuntu_X.Y" 模式
		parts := strings.Fields(imageName)
		for _, part := range parts {
			if strings.Contains(part, ".") && len(part) < 10 {
				return part
			}
		}
	}

	return ""
}

// getImageInfo 根据镜像ID获取镜像详细信息
func getImageInfo(cli *ecs.Client, regionID, imageID string) (*ImageInfo, error) {
	req := ecs.CreateDescribeImagesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ImageId = imageID
	req.PageSize = requests.NewInteger(1)

	resp, err := cli.DescribeImages(req)
	if err != nil {
		return nil, fmt.Errorf("查询镜像信息失败: %w", err)
	}

	if len(resp.Images.Image) == 0 {
		return nil, fmt.Errorf("未找到镜像: %s", imageID)
	}

	img := resp.Images.Image[0]
	osVersion := ""
	if img.ImageName != "" {
		osVersion = extractOSVersionFromImageName(img.ImageName, img.Platform)
	}

	return &ImageInfo{
		ImageID:   img.ImageId,
		OSName:    img.OSName,
		Platform:  img.Platform,
		OSVersion: osVersion,
	}, nil
}

// getLatestSpotPrice 获取最近一次抢占式价格（元/小时）
func getLatestSpotPrice(cli *ecs.Client, regionID, zoneID, instanceType string) (float64, error) {
	req := ecs.CreateDescribeSpotPriceHistoryRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.ZoneId = zoneID
	req.InstanceType = instanceType
	req.NetworkType = "vpc"
	resp, err := cli.DescribeSpotPriceHistory(req)
	if err != nil {
		return 0, fmt.Errorf("查询抢占式价格失败: %w", err)
	}
	if len(resp.SpotPrices.SpotPriceType) == 0 {
		return 0, fmt.Errorf("未获取到抢占式价格")
	}
	return resp.SpotPrices.SpotPriceType[0].SpotPrice, nil
}

// maskString 简单掩码
func maskString(s string, visible int) string {
	if len(s) <= visible*2 {
		return s
	}
	return s[:visible] + "..." + s[len(s)-visible:]
}

// getInstanceTypeInfo 获取规格的 vCPU/内存/GPU 信息
func getInstanceTypeInfo(cli *ecs.Client, regionID, instanceType string) (cpu int, mem float64, gpuModel string, gpuCount int, err error) {
	req := ecs.CreateDescribeInstanceTypesRequest()
	req.Scheme = "https"
	req.RegionId = regionID

	resp, e := cli.DescribeInstanceTypes(req)
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

// getVSwitchZone 查询交换机所属可用区
func getVSwitchZone(cli *ecs.Client, regionID, vswitchID string) (string, error) {
	req := ecs.CreateDescribeVSwitchesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.VSwitchId = vswitchID
	req.PageSize = requests.NewInteger(1)

	resp, err := cli.DescribeVSwitches(req)
	if err != nil {
		return "", fmt.Errorf("查询VSwitch失败: %w", err)
	}
	if len(resp.VSwitches.VSwitch) == 0 {
		return "", fmt.Errorf("未找到VSwitch: %s", vswitchID)
	}
	return resp.VSwitches.VSwitch[0].ZoneId, nil
}

// runSpotInstance 调用 RunInstances 创建抢占式实例
func runSpotInstance(cli *ecs.Client, req RunSpotInstanceRequest) (*ecs.RunInstancesResponse, error) {
	// 校验必填
	if req.InstanceType == "" {
		return nil, fmt.Errorf("缺少 InstanceType")
	}
	if req.SecurityGroupID == "" {
		return nil, fmt.Errorf("缺少 SecurityGroupId")
	}
	if req.VSwitchID == "" {
		return nil, fmt.Errorf("缺少 VSwitchId")
	}
	if req.RegionID == "" {
		return nil, fmt.Errorf("缺少 RegionId")
	}

	r := ecs.CreateRunInstancesRequest()
	r.Scheme = "https"
	r.RegionId = req.RegionID
	r.InstanceChargeType = "PostPaid"
	if req.SpotStrategy == "" {
		req.SpotStrategy = "SpotAsPriceGo"
	}
	r.SpotStrategy = req.SpotStrategy
	if req.SpotStrategy == "SpotWithPriceLimit" && req.SpotPriceLimit > 0 {
		r.SpotPriceLimit = requests.NewFloat(req.SpotPriceLimit)
	}
	if req.SpotDuration <= 0 {
		req.SpotDuration = 1
	}
	r.SpotDuration = requests.NewInteger(req.SpotDuration)

	r.InstanceType = req.InstanceType
	// 镜像选择：直接按规格选第一个 Linux 64 公共镜像（忽略预设镜像）
	imgID, err := selectFirstLinux64Image(cli, req.RegionID, req.InstanceType)
	if err != nil {
		return nil, err
	}
	req.ImageID = imgID
	r.ImageId = req.ImageID

	// VSwitch 可用区对齐
	zoneID, err := getVSwitchZone(cli, req.RegionID, req.VSwitchID)
	if err != nil {
		return nil, err
	}
	r.ZoneId = zoneID
	r.SecurityGroupId = req.SecurityGroupID
	r.VSwitchId = req.VSwitchID

	if req.SystemDiskSize <= 0 {
		req.SystemDiskSize = 40
	}
	r.SystemDiskSize = fmt.Sprintf("%d", req.SystemDiskSize)
	// 默认 ESSD，如果不支持会报错；可按需改为 cloud_efficiency
	r.SystemDiskCategory = "cloud_essd"

	if req.Password != "" {
		r.Password = req.Password
	}

	// 配置公网带宽（按流量计费，5Mbps，确保有公网IP）
	r.InternetChargeType = "PayByTraffic"
	r.InternetMaxBandwidthOut = requests.NewInteger(5)

	return cli.RunInstances(r)
}

// waitForInstanceRunning 等待实例启动完成（状态变为 Running）
func waitForInstanceRunning(cli *ecs.Client, regionID, instanceID string, maxWaitMinutes int) error {
	fmt.Printf("\n等待实例启动中... (最多等待 %d 分钟)\n", maxWaitMinutes)
	maxWaitSeconds := maxWaitMinutes * 60
	checkInterval := 5 // 每5秒检查一次

	for i := 0; i < maxWaitSeconds; i += checkInterval {
		req := ecs.CreateDescribeInstancesRequest()
		req.Scheme = "https"
		req.RegionId = regionID
		req.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

		resp, err := cli.DescribeInstances(req)
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

// getInstanceIPs 获取实例的公网IP和内网IP
func getInstanceIPs(cli *ecs.Client, regionID, instanceID string) (publicIP, privateIP string, err error) {
	req := ecs.CreateDescribeInstancesRequest()
	req.Scheme = "https"
	req.RegionId = regionID
	req.InstanceIds = fmt.Sprintf("[\"%s\"]", instanceID)

	resp, err := cli.DescribeInstances(req)
	if err != nil {
		return "", "", fmt.Errorf("查询实例信息失败: %w", err)
	}

	if len(resp.Instances.Instance) == 0 {
		return "", "", fmt.Errorf("未找到实例: %s", instanceID)
	}

	instance := resp.Instances.Instance[0]

	// 获取公网IP
	if len(instance.PublicIpAddress.IpAddress) > 0 && instance.PublicIpAddress.IpAddress[0] != "" {
		publicIP = instance.PublicIpAddress.IpAddress[0]
	}

	// 获取内网IP
	if len(instance.InnerIpAddress.IpAddress) > 0 && instance.InnerIpAddress.IpAddress[0] != "" {
		privateIP = instance.InnerIpAddress.IpAddress[0]
	}

	if publicIP == "" && privateIP == "" {
		return "", "", fmt.Errorf("实例暂无IP地址")
	}

	return publicIP, privateIP, nil
}

// 注意：checkSSHReady, waitForSSHReady, autoSSHLogin 函数已移至 run_ecs.go
// 使用 run_ecs.go 中的版本（支持 Windows 环境的 Go SSH 库实现）

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

// loadConfig 从 config.yml 加载完整配置
func loadConfig() (*configFile, error) {
	cfgPath := "config.yml"
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg configFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	return &cfg, nil
}

// InstanceListItem 实例列表项（用于显示和选择）
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

// listAllInstances 列出所有 ECS 实例（返回列表用于选择）
func listAllInstances(cfg *configFile) ([]InstanceListItem, error) {
	var regions []string
	if cfg.QueryAllRegions {
		// 获取所有地域
		cli, err := newECSClient(cfg.RegionID)
		if err != nil {
			return nil, fmt.Errorf("初始化客户端失败: %w", err)
		}
		req := ecs.CreateDescribeRegionsRequest()
		req.Scheme = "https"
		resp, err := cli.DescribeRegions(req)
		if err != nil {
			return nil, fmt.Errorf("查询地域列表失败: %w", err)
		}
		for _, r := range resp.Regions.Region {
			regions = append(regions, r.RegionId)
		}
	} else {
		regions = []string{cfg.RegionID}
	}

	// 构建排除实例ID的快速查找映射
	excludedMap := make(map[string]bool)
	for _, excludedID := range cfg.ExcludedInstanceIDs {
		excludedMap[excludedID] = true
	}

	var allInstances []InstanceListItem
	index := 1
	excludedCount := 0

	for _, region := range regions {
		cli, err := newECSClient(region)
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
			response, err := cli.DescribeInstances(request)
			if err != nil {
				log.Printf("查询地域 %s 实例列表失败: %v", region, err)
				break
			}

			if len(response.Instances.Instance) == 0 {
				break
			}

			for _, instance := range response.Instances.Instance {
				// 检查是否在排除列表中
				if excludedMap[instance.InstanceId] {
					excludedCount++
					continue
				}
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

	if excludedCount > 0 {
		log.Printf("已排除 %d 个实例（根据 excluded_instance_ids 配置）", excludedCount)
	}

	return allInstances, nil
}

// displayInstances 显示实例列表
func displayInstances(instances []InstanceListItem) {
	statusMap := map[string]string{
		"Running":  "运行中",
		"Stopped":  "已停止",
		"Starting": "启动中",
		"Stopping": "停止中",
		"Pending":  "创建中",
	}

	fmt.Println("\n========================================")
	fmt.Println("   ECS 实例列表")
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

// executeOSSMountOnInstance 在指定实例上执行OSS挂载
// 注意：这是一个简化版本，完整实现需要从 cloud_assistant_oss.go 复制 ExecuteOSSMountOnInstance 函数
// 或者修改构建标签让两个文件可以一起编译
func executeOSSMountOnInstance(client *ecs.Client, instanceID, regionID string, ossConfig *OSSConfig, commandConfig *CloudAssistantConfig, dockerConfig *DockerConfig) error {
	// 检查 OSS 配置
	if ossConfig.Bucket == "" {
		return fmt.Errorf("OSS Bucket 未配置")
	}
	if ossConfig.Endpoint == "" {
		return fmt.Errorf("OSS Endpoint 未配置")
	}
	if ossConfig.AccessKeyID == "" {
		return fmt.Errorf("OSS AccessKeyID 未配置")
	}
	if ossConfig.AccessKeySecret == "" {
		return fmt.Errorf("OSS AccessKeySecret 未配置")
	}

	// 注意：这里需要实现完整的OSS挂载逻辑
	// 完整实现包括：
	// 1. 创建或获取云助手命令
	// 2. 执行命令
	// 3. 等待执行完成（如果配置了等待）

	// 由于构建标签限制，这里提供一个占位实现
	// 实际使用时，需要：
	// 1. 从 cloud_assistant_oss.go 复制 ExecuteOSSMountOnInstance 函数及其依赖
	// 2. 或者修改构建标签，让两个文件可以一起编译

	fmt.Printf("⚠️  注意: executeOSSMountOnInstance 需要完整实现\n")
	fmt.Printf("   请从 cloud_assistant_oss.go 复制 ExecuteOSSMountOnInstance 函数\n")
	fmt.Printf("   或者修改构建标签让两个文件可以一起编译\n")

	return fmt.Errorf("executeOSSMountOnInstance 未完整实现，请参考 cloud_assistant_oss.go")
}

// queryInstanceTypeInfoParallel 并行查询多个实例型号的完整信息
func queryInstanceTypeInfoParallel(cli *ecs.Client, regionID, zoneID string, instanceTypesWithComments []InstanceTypeWithComment) []InstanceTypeInfo {
	type queryResult struct {
		Index        int
		InstanceType string
		Info         InstanceTypeInfo
	}

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
			cpu, mem, gpuModel, gpuCount, err := getInstanceTypeInfo(cli, regionID, instTypeWithComment.InstanceType)
			if err != nil {
				info.SpecError = err
			} else {
				info.CPU = cpu
				info.Memory = mem
				info.GPUModel = gpuModel
				info.GPUCount = gpuCount
			}

			// 并行查询镜像
			imgID, err := selectFirstLinux64Image(cli, regionID, instTypeWithComment.InstanceType)
			if err != nil {
				info.ImageError = err
			} else {
				info.ImageID = imgID
			}

			// 并行查询价格
			price, err := getLatestSpotPrice(cli, regionID, zoneID, instTypeWithComment.InstanceType)
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

func main() {
	absCfg, _ := filepath.Abs("config.yml")
	fmt.Printf("使用配置文件: %s\n", absCfg)

	// 加载配置
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 使用配置中的 region_id（优先使用 config.yml 中的 region_id）
	region := cfg.RegionID
	if region == "" {
		// 如果 config.yml 中没有，尝试环境变量
		region = os.Getenv("ALIBABA_CLOUD_REGION_ID")
	}
	if region == "" {
		region = "cn-shenzhen" // 默认值
	}
	fmt.Printf("使用地域: %s (来自 config.yml)\n", region)

	// 查询所有实例
	fmt.Println("\n正在查询ECS实例列表...")
	instances, err := listAllInstances(cfg)
	if err != nil {
		log.Fatalf("查询实例列表失败: %v", err)
	}

	if len(instances) == 0 {
		fmt.Println("未找到任何实例")
		return
	}

	// 显示实例列表
	displayInstances(instances)

	// 用户选择实例
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\n请选择要挂载OSS的实例序号 (输入序号 1-%d，或 0 取消): ", len(instances))
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	choice, err := strconv.Atoi(line)
	if err != nil || choice < 0 || choice > len(instances) {
		fmt.Println("无效的选择，已取消")
		return
	}
	if choice == 0 {
		fmt.Println("已取消")
		return
	}

	selectedInstance := instances[choice-1]

	// 显示选中的实例信息
	fmt.Println("\n========================================")
	fmt.Println("  选中实例信息")
	fmt.Println("========================================")
	fmt.Printf("  实例ID: %s\n", selectedInstance.InstanceID)
	fmt.Printf("  地域: %s\n", selectedInstance.Region)
	fmt.Printf("  实例类型: %s\n", selectedInstance.Instance.InstanceType)
	fmt.Printf("  状态: %s\n", selectedInstance.Instance.Status)

	// 检查实例状态
	if selectedInstance.Instance.Status != "Running" {
		fmt.Printf("\n⚠️  警告: 实例状态为 %s，需要实例处于运行中状态才能挂载OSS\n", selectedInstance.Instance.Status)
		fmt.Println("   请先启动实例后再进行挂载操作")
		return
	}

	// 加载OSS配置
	fmt.Println("\n正在加载OSS配置...")

	// 如果OSS配置中没有AccessKey，使用主配置的AccessKey
	ossConfig := cfg.OSSConfig
	if ossConfig.AccessKeyID == "" {
		ossConfig.AccessKeyID = cfg.AccessKeyID
	}
	if ossConfig.AccessKeySecret == "" {
		ossConfig.AccessKeySecret = cfg.AccessKeySecret
	}
	if ossConfig.MountPoint == "" {
		ossConfig.MountPoint = "/mnt/oss"
	}

	// 验证OSS配置
	if ossConfig.Bucket == "" {
		log.Fatalf("OSS Bucket 未配置，请在 config.yml 中配置 oss_config.bucket")
	}
	if ossConfig.Endpoint == "" {
		log.Fatalf("OSS Endpoint 未配置，请在 config.yml 中配置 oss_config.endpoint")
	}
	if ossConfig.AccessKeyID == "" {
		log.Fatalf("OSS AccessKeyID 未配置，请在 config.yml 中配置 oss_config.access_key_id 或 access_key_id")
	}
	if ossConfig.AccessKeySecret == "" {
		log.Fatalf("OSS AccessKeySecret 未配置，请在 config.yml 中配置 oss_config.access_key_secret 或 access_key_secret")
	}

	// 设置默认的云助手配置
	cloudAssistantConfig := cfg.CloudAssistant
	if cloudAssistantConfig.OSSMountCommand.Name == "" {
		cloudAssistantConfig.OSSMountCommand.Name = "oss-mount-command"
	}
	if cloudAssistantConfig.OSSMountCommand.Timeout == 0 {
		cloudAssistantConfig.OSSMountCommand.Timeout = 3600
	}
	if cloudAssistantConfig.Execution.RepeatMode == "" {
		cloudAssistantConfig.Execution.RepeatMode = "Once"
	}
	if cloudAssistantConfig.Execution.WaitTimeout == 0 {
		cloudAssistantConfig.Execution.WaitTimeout = 600
	}
	if cloudAssistantConfig.Execution.PollInterval == 0 {
		cloudAssistantConfig.Execution.PollInterval = 5
	}

	// 显示OSS配置信息
	fmt.Println("\n========================================")
	fmt.Println("  OSS 配置信息")
	fmt.Println("========================================")
	fmt.Printf("  Bucket: %s\n", ossConfig.Bucket)
	fmt.Printf("  Endpoint: %s\n", ossConfig.Endpoint)
	fmt.Printf("  MountPoint: %s\n", ossConfig.MountPoint)
	fmt.Printf("  AutoRemount: %v\n", ossConfig.AutoRemount)

	// 确认执行
	fmt.Print("\n确认在实例上挂载OSS吗？(y/N): ")
	line, _ = reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line != "y" && line != "yes" {
		fmt.Println("已取消")
		return
	}

	// 创建ECS客户端（使用实例所在的地域）
	cli, err := newECSClient(selectedInstance.Region)
	if err != nil {
		log.Fatalf("初始化ECS客户端失败: %v", err)
	}

	// 调用挂载OSS的函数（需要从 cloud_assistant_oss.go 导入）
	// 注意：由于构建标签不同，这里需要直接调用或重新实现
	fmt.Println("\n========================================")
	fmt.Println("  开始执行OSS挂载")
	fmt.Println("========================================")
	fmt.Printf("实例ID: %s\n", selectedInstance.InstanceID)
	fmt.Printf("地域: %s\n", selectedInstance.Region)

	// 调用挂载OSS的函数
	// 注意：需要确保 cloud_assistant_oss.go 可以一起编译
	// 如果构建标签冲突，需要移除其中一个文件的构建标签
	// ================= WARNING: OSS 挂载功能已被临时注释，若需恢复请取消注释下面调用 =================
	// err = executeOSSMountOnInstance(
	// 	cli,
	// 	selectedInstance.InstanceID,
	// 	selectedInstance.Region,
	// 	&ossConfig,
	// 	&cloudAssistantConfig,
	// 	&cfg.DockerConfig,
	// )
	// if err != nil {
	// 	log.Fatalf("执行OSS挂载失败: %v", err)
	// }

	// 避免 cli 变量被编译器/静态检查器报告为未使用
	_ = cli
	fmt.Println("\n⚠️  已跳过 OSS 挂载（相关代码已注释），如需恢复请取消注释 executeOSSMountOnInstance 调用")
	return
}
