//go:build !release_ecs && !cloud_assistant_oss && !cloud_assistant_oss_main
// +build !release_ecs,!cloud_assistant_oss,!cloud_assistant_oss_main

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	// 命令行参数
	action := flag.String("action", "", "操作类型: start / stop / status / reboot / list")
	configPath := flag.String("config", "config.yml", "配置文件路径")
	forceStop := flag.Bool("force", false, "停止实例时是否强制关机 (仅在 -action=stop 时生效)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	reader := bufio.NewReader(os.Stdin)

	switch *action {
	case "list":
		handleListInstances(cfg, reader)
	case "start":
		client, err := NewECSClient(cfg)
		if err != nil {
			log.Fatalf("初始化客户端失败: %v", err)
		}
		if err := StartECS(client, cfg.InstanceID); err != nil {
			log.Fatalf("启动实例失败: %v", err)
		}
		fmt.Println("✓ 实例启动成功")
	case "stop":
		client, err := NewECSClient(cfg)
		if err != nil {
			log.Fatalf("初始化客户端失败: %v", err)
		}
		if err := StopECS(client, cfg.InstanceID, *forceStop); err != nil {
			log.Fatalf("停止实例失败: %v", err)
		}
		fmt.Println("✓ 实例停止成功")
	case "reboot":
		client, err := NewECSClient(cfg)
		if err != nil {
			log.Fatalf("初始化客户端失败: %v", err)
		}
		if err := RebootECS(client, cfg.InstanceID, false); err != nil {
			log.Fatalf("重启实例失败: %v", err)
		}
		fmt.Println("✓ 实例重启成功")
	case "status":
		client, err := NewECSClient(cfg)
		if err != nil {
			log.Fatalf("初始化客户端失败: %v", err)
		}
		status, err := GetECSStatus(client, cfg.InstanceID)
		if err != nil {
			log.Fatalf("查询状态失败: %v", err)
		}
		fmt.Printf("实例状态: %s\n", status)
	default:
		// 使用 run_ecs.go 中的主菜单实现以保持一致的交互体验
		showMainMenu(cfg)
		return
	}
}

