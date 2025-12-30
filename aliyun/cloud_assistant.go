//go:build cloud_assistant_oss_main

package main

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
)

// 共享类型定义
type ECSConfig struct {
	AccessKeyID         string   `yaml:"access_key_id"`
	AccessKeySecret     string   `yaml:"access_key_secret"`
	RegionID            string   `yaml:"region_id"`
	QueryAllRegions     bool     `yaml:"query_all_regions"`
	ExcludedInstanceIDs []string `yaml:"excluded_instance_ids"`
}

type InstanceInfo struct {
	InstanceID string
	RegionID   string
	Instance   ecs.Instance
}

// 核心函数
func NewECSClientWithRegion(cfg *ECSConfig, regionID string) (*ecs.Client, error) {
	return ecs.NewClientWithAccessKey(regionID, cfg.AccessKeyID, cfg.AccessKeySecret)
}

func ListAllInstances(cfg *ECSConfig) ([]InstanceInfo, error) {
	var result []InstanceInfo
	regions := []string{cfg.RegionID} // 简化为单区域

	for _, rid := range regions {
		client, err := NewECSClientWithRegion(cfg, rid)
		if err != nil {
			fmt.Printf("无法连接区域 %s: %v\n", rid, err)
			continue
		}

		req := ecs.CreateDescribeInstancesRequest()
		req.RegionId = rid
		req.PageSize = requests.NewInteger(50)

		resp, err := client.DescribeInstances(req)
		if err != nil {
			fmt.Printf("查询实例失败 %s: %v\n", rid, err)
			continue
		}

		for _, inst := range resp.Instances.Instance {
			isExcluded := false
			for _, exID := range cfg.ExcludedInstanceIDs {
				if inst.InstanceId == exID {
					isExcluded = true
					break
				}
			}
			if isExcluded {
				continue
			}
			result = append(result, InstanceInfo{
				InstanceID: inst.InstanceId,
				RegionID:   rid,
				Instance:   inst,
			})
		}
	}
	return result, nil
}

func DisplayInstances(instances []InstanceInfo) {
	fmt.Println("---------------------------------------------------------------------------------")
	fmt.Printf("%-3s | %-15s | %-22s | %-15s | %s\n", "No.", "Region", "InstanceID", "IP", "Name")
	fmt.Println("---------------------------------------------------------------------------------")
	for i, info := range instances {
		ip := ""
		if len(info.Instance.PublicIpAddress.IpAddress) > 0 {
			ip = info.Instance.PublicIpAddress.IpAddress[0]
		}
		fmt.Printf("%-3d | %-15s | %-22s | %-15s | %s\n", i+1, info.RegionID, info.InstanceID, ip, info.Instance.InstanceName)
	}
	fmt.Println("---------------------------------------------------------------------------------")
}

func CreateCommand(client *ecs.Client, regionID, name, cmdType, content string, timeout int) (string, error) {
	req := ecs.CreateCreateCommandRequest()
	req.RegionId = regionID
	req.Name = name
	req.Type = cmdType
	req.CommandContent = base64.StdEncoding.EncodeToString([]byte(content))
	req.ContentEncoding = "Base64"
	req.Timeout = requests.NewInteger(timeout)

	resp, err := client.CreateCommand(req)
	if err != nil {
		return "", fmt.Errorf("创建命令失败: %w", err)
	}
	return resp.CommandId, nil
}

func InvokeCommand(client *ecs.Client, regionID, instanceID, commandID, repeatMode string) (string, error) {
	req := ecs.CreateInvokeCommandRequest()
	req.RegionId = regionID
	req.InstanceId = &[]string{instanceID}
	req.CommandId = commandID
	req.RepeatMode = repeatMode

	resp, err := client.InvokeCommand(req)
	if err != nil {
		return "", err
	}
	return resp.InvokeId, nil
}

func DescribeInvocationResults(client *ecs.Client, regionID, invokeID string) (*ecs.InvocationResult, error) {
	req := ecs.CreateDescribeInvocationResultsRequest()
	req.RegionId = regionID
	req.InvokeId = invokeID
	req.ContentEncoding = "Base64"

	resp, err := client.DescribeInvocationResults(req)
	if err != nil {
		return nil, fmt.Errorf("查询命令执行结果失败: %w", err)
	}

	if len(resp.Invocation.InvocationResults.InvocationResult) == 0 {
		return nil, fmt.Errorf("未找到执行结果")
	}

	return &resp.Invocation.InvocationResults.InvocationResult[0], nil
}

func WaitForCommandCompletion(client *ecs.Client, regionID, invokeID string, timeout, pollInterval int) error {
	ticker := time.NewTicker(time.Duration(pollInterval) * time.Second)
	defer ticker.Stop()
	timeoutCh := time.After(time.Duration(timeout) * time.Second)

	for {
		select {
		case <-timeoutCh:
			return fmt.Errorf("执行超时 (%d秒)", timeout)
		case <-ticker.C:
			result, err := DescribeInvocationResults(client, regionID, invokeID)
			if err != nil {
				fmt.Printf("[Warning] 获取日志出错: %v (重试中...)\n", err)
				continue
			}

			status := result.InvocationStatus
			if status == "Finished" || status == "Success" {
				return nil
			}
			if status == "Failed" || status == "Stopped" || status == "PartialFailed" || status == "Error" {
				return fmt.Errorf("任务结束状态异常: %s", status)
			}
		}
	}
}
