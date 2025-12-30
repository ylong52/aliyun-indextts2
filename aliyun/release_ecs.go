//go:build release_ecs
// +build release_ecs

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"gopkg.in/yaml.v3"
)

// 配置结构
type ReleaseConfig struct {
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	RegionID        string `yaml:"region_id"`
	QueryAllRegions bool   `yaml:"query_all_regions"`
}

const excludedInstanceID = "i-wz94gf7jfsfzqsfc1mn4"

// LoadConfig 读取配置
func LoadConfig(path string) (*ReleaseConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var cfg ReleaseConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, fmt.Errorf("配置缺少 access_key_id / access_key_secret")
	}
	if cfg.RegionID == "" {
		return nil, fmt.Errorf("配置缺少 region_id")
	}
	return &cfg, nil
}

func newClient(region, ak, sk string) (*ecs.Client, error) {
	conf := sdk.NewConfig()
	conf.Timeout = 15 * time.Second
	cred := credentials.NewAccessKeyCredential(ak, sk)
	return ecs.NewClientWithOptions(region, conf, cred)
}

type InstanceInfo struct {
	Region string
	Data   ecs.Instance
}

func listRegions(baseClient *ecs.Client) ([]string, error) {
	req := ecs.CreateDescribeRegionsRequest()
	req.Scheme = "https"
	resp, err := baseClient.DescribeRegions(req)
	if err != nil {
		return nil, fmt.Errorf("查询地域失败: %w", err)
	}
	var regions []string
	for _, r := range resp.Regions.Region {
		regions = append(regions, r.RegionId)
	}
	return regions, nil
}

func collectInstances(region string, cli *ecs.Client) ([]InstanceInfo, error) {
	var result []InstanceInfo
	req := ecs.CreateDescribeInstancesRequest()
	req.Scheme = "https"
	req.PageSize = requests.NewInteger(100)
	page := 1

	for {
		req.PageNumber = requests.NewInteger(page)
		resp, err := cli.DescribeInstances(req)
		if err != nil {
			return nil, fmt.Errorf("查询实例失败(%s): %w", region, err)
		}
		for _, inst := range resp.Instances.Instance {
			if inst.InstanceId == excludedInstanceID {
				continue
			}
			result = append(result, InstanceInfo{Region: region, Data: inst})
		}
		if len(resp.Instances.Instance) < 100 {
			break
		}
		page++
	}
	return result, nil
}

func formatIPs(inst ecs.Instance) (string, string) {
	publicIPs := inst.PublicIpAddress.IpAddress
	if inst.EipAddress.IpAddress != "" {
		publicIPs = append(publicIPs, inst.EipAddress.IpAddress)
	}
	privateIPs := inst.InnerIpAddress.IpAddress
	if len(inst.VpcAttributes.PrivateIpAddress.IpAddress) > 0 {
		privateIPs = append(privateIPs, inst.VpcAttributes.PrivateIpAddress.IpAddress...)
	}

	pub := "无"
	if len(publicIPs) > 0 {
		pub = strings.Join(publicIPs, ",")
	}
	prv := "无"
	if len(privateIPs) > 0 {
		prv = strings.Join(privateIPs, ",")
	}
	return pub, prv
}

func displayInstances(list []InstanceInfo) {
	for i, item := range list {
		inst := item.Data
		pub, prv := formatIPs(inst)
		name := inst.InstanceName
		if name == "" {
			name = inst.InstanceId
		}
		fmt.Printf("\n[%d] %s\n", i+1, name)
		fmt.Printf("    实例ID: %s\n", inst.InstanceId)
		fmt.Printf("    地域: %s\n", item.Region)
		fmt.Printf("    规格: %s\n", inst.InstanceType)
		fmt.Printf("    公网IP: %s\n", pub)
		fmt.Printf("    内网IP: %s\n", prv)
		fmt.Printf("    可用区: %s\n", inst.ZoneId)
		if inst.CreationTime != "" {
			fmt.Printf("    创建时间: %s\n", inst.CreationTime)
		}
		fmt.Printf("    状态: %s\n", inst.Status)
	}
}

func promptSelect(list []InstanceInfo) InstanceInfo {
	if len(list) == 1 {
		return list[0]
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("\n请选择要释放的实例序号 (1-%d): ", len(list))
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		idx, err := strconv.Atoi(text)
		if err != nil || idx < 1 || idx > len(list) {
			fmt.Println("输入无效，请重试。")
			continue
		}
		return list[idx-1]
	}
}

func confirm(msg string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print(msg)
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(strings.ToLower(text))
	return text == "y" || text == "yes"
}

func deleteInstance(cli *ecs.Client, inst InstanceInfo) error {
	req := ecs.CreateDeleteInstanceRequest()
	req.Scheme = "https"
	req.InstanceId = inst.Data.InstanceId
	req.Force = requests.NewBoolean(true) // 强制释放，避免运行中失败

	resp, err := cli.DeleteInstance(req)
	if err != nil {
		return fmt.Errorf("释放实例失败: %w", err)
	}
	fmt.Printf("释放成功，RequestId: %s, InstanceId: %s\n", resp.RequestId, inst.Data.InstanceId)

	// 从管理列表中移除实例ID
	manager := NewInstanceManager("managed_instances.json")
	if err := manager.RemoveInstance(inst.Data.InstanceId); err != nil {
		fmt.Printf("⚠️  警告: 从管理列表移除实例ID失败: %v\n", err)
	} else {
		fmt.Printf("✓ 实例ID已从管理列表移除\n")
	}

	return nil
}

func main() {
	configPath := "config.yml"
	if len(os.Args) > 1 {
		// 简单解析 -config=xxx 或 -config xxx
		for i, arg := range os.Args {
			if strings.HasPrefix(arg, "-config=") {
				configPath = strings.TrimPrefix(arg, "-config=")
			}
			if arg == "-config" && i+1 < len(os.Args) {
				configPath = os.Args[i+1]
			}
		}
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	baseClient, err := newClient(cfg.RegionID, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		log.Fatalf("初始化客户端失败: %v", err)
	}

	regions := []string{cfg.RegionID}
	if cfg.QueryAllRegions {
		all, err := listRegions(baseClient)
		if err != nil {
			log.Fatalf("获取地域列表失败: %v", err)
		}
		regions = all
	}

	var allInstances []InstanceInfo
	for _, region := range regions {
		cli, err := newClient(region, cfg.AccessKeyID, cfg.AccessKeySecret)
		if err != nil {
			log.Printf("初始化地域 %s 客户端失败: %v", region, err)
			continue
		}
		insts, err := collectInstances(region, cli)
		if err != nil {
			log.Printf("获取地域 %s 实例失败: %v", region, err)
			continue
		}
		allInstances = append(allInstances, insts...)
	}

	if len(allInstances) == 0 {
		fmt.Println("未找到可释放的实例（或全部被排除）。")
		return
	}

	fmt.Println("\n可释放的 ECS 实例：")
	displayInstances(allInstances)

	target := promptSelect(allInstances)
	pub, prv := formatIPs(target.Data)
	fmt.Printf("\n即将释放实例：%s (%s)\n", target.Data.InstanceId, target.Region)
	fmt.Printf("  名称: %s\n", target.Data.InstanceName)
	fmt.Printf("  规格: %s\n", target.Data.InstanceType)
	fmt.Printf("  公网IP: %s\n", pub)
	fmt.Printf("  内网IP: %s\n", prv)

	if !confirm("确认释放该实例吗？(y/N): ") {
		fmt.Println("已取消。")
		return
	}

	cli, err := newClient(target.Region, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		log.Fatalf("初始化客户端失败: %v", err)
	}
	if err := deleteInstance(cli, target); err != nil {
		log.Fatalf("释放失败: %v", err)
	}
}
