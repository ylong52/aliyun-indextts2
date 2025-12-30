package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ManagedInstance 管理的实例信息
type ManagedInstance struct {
	InstanceID   string    `json:"instance_id"`
	RegionID     string    `json:"region_id"`
	InstanceType string    `json:"instance_type,omitempty"`
	ImageID      string    `json:"image_id,omitempty"`   // 镜像ID
	OSName       string    `json:"os_name,omitempty"`    // 操作系统名称，如 "CentOS", "Ubuntu"
	Platform     string    `json:"platform,omitempty"`   // 平台类型，如 "CentOS", "Ubuntu", "Aliyun"
	OSVersion    string    `json:"os_version,omitempty"` // 操作系统版本
	CreatedAt    time.Time `json:"created_at"`
	// 登录信息
	PublicIP     string `json:"public_ip,omitempty"`     // 公网IP
	PrivateIP    string `json:"private_ip,omitempty"`    // 内网IP
	Username     string `json:"username,omitempty"`       // 用户名，默认 "root"
	Password     string `json:"password,omitempty"`      // 密码（如果使用密码登录）
	LoginMethod  string `json:"login_method,omitempty"`  // 登录方式：password/keypair
	SSHCommand   string `json:"ssh_command,omitempty"`   // SSH登录命令
	// 其他ECS信息
	ZoneID       string `json:"zone_id,omitempty"`       // 可用区ID
	Status       string `json:"status,omitempty"`        // 实例状态
	SpotStrategy string `json:"spot_strategy,omitempty"` // 抢占策略
}

// InstanceManager 实例ID管理器
type InstanceManager struct {
	filePath string
	mu       sync.Mutex
}

// NewInstanceManager 创建实例管理器
func NewInstanceManager(filePath string) *InstanceManager {
	if filePath == "" {
		filePath = "managed_instances.json"
	}
	return &InstanceManager{
		filePath: filePath,
	}
}

// loadInstances 从文件加载实例列表
func (im *InstanceManager) loadInstances() ([]ManagedInstance, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	data, err := os.ReadFile(im.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在，返回空列表
			return []ManagedInstance{}, nil
		}
		return nil, fmt.Errorf("读取实例列表文件失败: %w", err)
	}

	var instances []ManagedInstance
	if len(data) == 0 {
		return []ManagedInstance{}, nil
	}

	// 如果文件只包含 "[]" 或空白，返回空列表
	trimmedData := strings.TrimSpace(string(data))
	if trimmedData == "" || trimmedData == "[]" || trimmedData == "null" {
		return []ManagedInstance{}, nil
	}

	if err := json.Unmarshal(data, &instances); err != nil {
		return nil, fmt.Errorf("解析实例列表文件失败: %w", err)
	}

	return instances, nil
}

// saveInstances 保存实例列表到文件
func (im *InstanceManager) saveInstances(instances []ManagedInstance) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	data, err := json.MarshalIndent(instances, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化实例列表失败: %w", err)
	}

	// 获取当前工作目录，用于调试
	wd, _ := os.Getwd()

	// 写入文件
	if err := os.WriteFile(im.filePath, data, 0644); err != nil {
		return fmt.Errorf("写入实例列表文件失败 (工作目录: %s, 文件路径: %s): %w", wd, im.filePath, err)
	}

	// 验证文件是否成功写入
	if _, err := os.Stat(im.filePath); err != nil {
		return fmt.Errorf("文件写入后验证失败 (工作目录: %s, 文件路径: %s): %w", wd, im.filePath, err)
	}

	return nil
}

// AddInstance 添加实例ID到记录
func (im *InstanceManager) AddInstance(instanceID, regionID, instanceType string) error {
	return im.AddInstanceWithImage(instanceID, regionID, instanceType, "", "", "", "")
}

// AddInstanceWithImage 添加实例ID到记录（包含镜像信息）
func (im *InstanceManager) AddInstanceWithImage(instanceID, regionID, instanceType, imageID, osName, platform, osVersion string) error {
	instances, err := im.loadInstances()
	if err != nil {
		return err
	}

	// 检查是否已存在
	for _, inst := range instances {
		if inst.InstanceID == instanceID {
			// 已存在，更新信息
			for i := range instances {
				if instances[i].InstanceID == instanceID {
					instances[i].RegionID = regionID
					instances[i].InstanceType = instanceType
					if imageID != "" {
						instances[i].ImageID = imageID
					}
					if osName != "" {
						instances[i].OSName = osName
					}
					if platform != "" {
						instances[i].Platform = platform
					}
					if osVersion != "" {
						instances[i].OSVersion = osVersion
					}
					instances[i].CreatedAt = time.Now()
					break
				}
			}
			return im.saveInstances(instances)
		}
	}

	// 添加新实例
	newInstance := ManagedInstance{
		InstanceID:   instanceID,
		RegionID:     regionID,
		InstanceType: instanceType,
		ImageID:      imageID,
		OSName:       osName,
		Platform:     platform,
		OSVersion:    osVersion,
		CreatedAt:    time.Now(),
	}
	instances = append(instances, newInstance)

	return im.saveInstances(instances)
}

// AddInstanceWithLoginInfo 添加实例并包含完整的登录信息
func (im *InstanceManager) AddInstanceWithLoginInfo(
	instanceID, regionID, instanceType, imageID, osName, platform, osVersion string,
	publicIP, privateIP, username, password, loginMethod, sshCommand, zoneID, status, spotStrategy string,
) error {
	instances, err := im.loadInstances()
	if err != nil {
		return err
	}

	// 检查是否已存在
	for i, inst := range instances {
		if inst.InstanceID == instanceID {
			// 更新现有实例的信息
			instances[i].RegionID = regionID
			instances[i].InstanceType = instanceType
			if imageID != "" {
				instances[i].ImageID = imageID
			}
			if osName != "" {
				instances[i].OSName = osName
			}
			if platform != "" {
				instances[i].Platform = platform
			}
			if osVersion != "" {
				instances[i].OSVersion = osVersion
			}
			if publicIP != "" {
				instances[i].PublicIP = publicIP
			}
			if privateIP != "" {
				instances[i].PrivateIP = privateIP
			}
			if username != "" {
				instances[i].Username = username
			}
			if password != "" {
				instances[i].Password = password
			}
			if loginMethod != "" {
				instances[i].LoginMethod = loginMethod
			}
			if sshCommand != "" {
				instances[i].SSHCommand = sshCommand
			}
			if zoneID != "" {
				instances[i].ZoneID = zoneID
			}
			if status != "" {
				instances[i].Status = status
			}
			if spotStrategy != "" {
				instances[i].SpotStrategy = spotStrategy
			}
			return im.saveInstances(instances)
		}
	}

	// 添加新实例
	newInstance := ManagedInstance{
		InstanceID:   instanceID,
		RegionID:     regionID,
		InstanceType: instanceType,
		ImageID:      imageID,
		OSName:       osName,
		Platform:     platform,
		OSVersion:    osVersion,
		PublicIP:     publicIP,
		PrivateIP:    privateIP,
		Username:     username,
		Password:     password,
		LoginMethod:  loginMethod,
		SSHCommand:   sshCommand,
		ZoneID:       zoneID,
		Status:       status,
		SpotStrategy: spotStrategy,
		CreatedAt:    time.Now(),
	}
	instances = append(instances, newInstance)
	return im.saveInstances(instances)
}

// RemoveInstance 从记录中移除实例ID
func (im *InstanceManager) RemoveInstance(instanceID string) error {
	instances, err := im.loadInstances()
	if err != nil {
		return err
	}

	// 查找并移除
	newInstances := make([]ManagedInstance, 0, len(instances))
	found := false
	for _, inst := range instances {
		if inst.InstanceID != instanceID {
			newInstances = append(newInstances, inst)
		} else {
			found = true
		}
	}

	if !found {
		// 实例不在记录中，不算错误
		return nil
	}

	return im.saveInstances(newInstances)
}

// GetInstances 获取所有记录的实例ID列表
func (im *InstanceManager) GetInstances() ([]ManagedInstance, error) {
	return im.loadInstances()
}

// HasInstance 检查实例ID是否在记录中
func (im *InstanceManager) HasInstance(instanceID string) (bool, error) {
	instances, err := im.loadInstances()
	if err != nil {
		return false, err
	}

	for _, inst := range instances {
		if inst.InstanceID == instanceID {
			return true, nil
		}
	}

	return false, nil
}
