//go:build ttl_rabbitmq
// +build ttl_rabbitmq
//
// 简要说明：
// 本文件实现基于 RabbitMQ TTL 与死信队列的自动开/关机逻辑，包含消费者用于接收数据与超时消息并调用 ECS 开关机接口。
// 为避免与主程序的重复定义（多个 main），此文件默认不参与包的默认构建。
// 使用方式：
//  - 默认构建（.\run_run_ecs.bat）不会编译本文件。
//  - 如需单独编译运行此文件，请使用： `go build -tags ttl_rabbitmq -o ttl_rabbitmq.exe ttl_rabbitmq.go`
//
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"github.com/streadway/amqp"
)

// 配置参数
var (
	RABBITMQ_URL         string
	DATA_QUEUE           = "data_queue"
	SHUTDOWN_QUEUE       = "shutdown_queue"
	DATA_EXCHANGE        = "data_exchange"
	SHUTDOWN_EXCHANGE    = "shutdown_exchange"
	DATA_ROUTING_KEY     = "data_key"
	SHUTDOWN_ROUTING_KEY = "shutdown_key"
	TTL_MILLISECONDS     = 180000 // 3分钟
	ECS_REGION           = "cn-hangzhou"
	ECS_ACCESS_KEY       = "your-access-key"
	ECS_ACCESS_SECRET    = "your-secret-key"
	ECS_INSTANCE_ID      = "i-xxxxxxxx"
)

// ECS操作客户端
var ecsClient *ecs.Client

func init() {
	// 检查并读取 RABBITMQ_URL 环境变量
	envURL := os.Getenv("RABBITMQ_URL")
	if envURL != "" {
		RABBITMQ_URL = envURL
		log.Printf("✓ 从环境变量读取 RABBITMQ_URL")
	} else {
		RABBITMQ_URL = "amqp://guest:guest@localhost:5672/"
		log.Printf("⚠️  环境变量 RABBITMQ_URL 未设置，使用默认值")
	}

	// 打印 RABBITMQ_URL 的值（隐藏密码部分）
	maskedURL := maskRabbitMQURL(RABBITMQ_URL)
	log.Printf("📋 RABBITMQ_URL = %s", maskedURL)

	// 初始化ECS客户端
	client, err := ecs.NewClientWithAccessKey(
		ECS_REGION,
		ECS_ACCESS_KEY,
		ECS_ACCESS_SECRET,
	)
	if err != nil {
		log.Fatalf("初始化ECS客户端失败: %v", err)
	}
	ecsClient = client
}

// maskRabbitMQURL 隐藏 RabbitMQ URL 中的密码
func maskRabbitMQURL(url string) string {
	// 简单的密码掩码：amqp://user:password@host:port/ -> amqp://user:***@host:port/
	if len(url) > 20 {
		// 查找 @ 符号的位置
		atIndex := -1
		for i := 0; i < len(url); i++ {
			if url[i] == '@' {
				atIndex = i
				break
			}
		}
		if atIndex > 0 {
			// 查找 : 符号的位置（在 @ 之前）
			colonIndex := -1
			for i := 0; i < atIndex; i++ {
				if url[i] == ':' {
					colonIndex = i
					break
				}
			}
			if colonIndex > 0 && colonIndex < atIndex {
				// 掩码密码部分
				return url[:colonIndex+1] + "***" + url[atIndex:]
			}
		}
	}
	return url
}

func main() {
	log.Println("========================================")
	log.Println("  RabbitMQ TTL 自动开关机程序启动")
	log.Println("========================================")

	// 打印配置信息
	log.Println("📋 配置信息:")
	log.Printf("  RABBITMQ_URL: %s", maskRabbitMQURL(RABBITMQ_URL))
	log.Printf("  数据队列: %s", DATA_QUEUE)
	log.Printf("  关机队列: %s", SHUTDOWN_QUEUE)
	log.Printf("  TTL: %d 毫秒 (%.1f 分钟)", TTL_MILLISECONDS, float64(TTL_MILLISECONDS)/60000)
	log.Printf("  ECS 实例ID: %s", ECS_INSTANCE_ID)
	log.Println("========================================")

	// 检查环境变量
	log.Println("🔍 检查环境变量:")
	envURL := os.Getenv("RABBITMQ_URL")
	if envURL != "" {
		log.Printf("  ✓ RABBITMQ_URL 环境变量已设置: %s", maskRabbitMQURL(envURL))
	} else {
		log.Printf("  ⚠️  RABBITMQ_URL 环境变量未设置，使用默认值")
	}
	log.Println("========================================")

	// 建立RabbitMQ连接
	log.Println("🔌 正在连接 RabbitMQ...")
	log.Printf("   连接地址: %s", maskRabbitMQURL(RABBITMQ_URL))
	conn, err := amqp.Dial(RABBITMQ_URL)
	if err != nil {
		log.Fatalf("❌ 连接RabbitMQ失败: %v", err)
	}
	defer conn.Close()
	log.Println("✓ RabbitMQ 连接成功")

	// 创建通道
	log.Println("📡 正在创建通道...")
	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("❌ 创建通道失败: %v", err)
	}
	defer ch.Close()
	log.Println("✓ 通道创建成功")

	// 声明交换机
	log.Println("📤 正在声明交换机...")
	err = ch.ExchangeDeclare(
		DATA_EXCHANGE, // name
		"direct",      // type
		true,          // durable
		false,         // auto-deleted
		false,         // internal
		false,         // no-wait
		nil,           // args
	)
	if err != nil {
		log.Fatalf("❌ 声明数据交换机失败: %v", err)
	}
	log.Printf("✓ 数据交换机声明成功: %s", DATA_EXCHANGE)

	err = ch.ExchangeDeclare(
		SHUTDOWN_EXCHANGE, // name
		"direct",          // type
		true,              // durable
		false,             // auto-deleted
		false,             // internal
		false,             // no-wait
		nil,               // args
	)
	if err != nil {
		log.Fatalf("❌ 声明关机交换机失败: %v", err)
	}
	log.Printf("✓ 关机交换机声明成功: %s", SHUTDOWN_EXCHANGE)

	// 声明死信队列（关机队列）
	log.Println("📥 正在声明队列...")
	_, err = ch.QueueDeclare(
		SHUTDOWN_QUEUE, // name
		true,           // durable
		false,          // auto-delete
		false,          // exclusive
		false,          // no-wait
		nil,            // args
	)
	if err != nil {
		log.Fatalf("❌ 声明关机队列失败: %v", err)
	}
	log.Printf("✓ 关机队列声明成功: %s", SHUTDOWN_QUEUE)

	// 绑定关机队列
	err = ch.QueueBind(
		SHUTDOWN_QUEUE,       // queue
		SHUTDOWN_ROUTING_KEY, // routing key
		SHUTDOWN_EXCHANGE,    // exchange
		false,                // no-wait
		nil,                  // args
	)
	if err != nil {
		log.Fatalf("❌ 绑定关机队列失败: %v", err)
	}
	log.Printf("✓ 关机队列绑定成功: %s -> %s", SHUTDOWN_QUEUE, SHUTDOWN_EXCHANGE)

	// 声明数据队列（带TTL和死信配置）
	args := amqp.Table{
		"x-message-ttl":             TTL_MILLISECONDS,
		"x-dead-letter-exchange":    SHUTDOWN_EXCHANGE,
		"x-dead-letter-routing-key": SHUTDOWN_ROUTING_KEY,
	}
	_, err = ch.QueueDeclare(
		DATA_QUEUE, // name
		true,       // durable
		false,      // auto-delete
		false,      // exclusive
		false,      // no-wait
		args,       // arguments
	)
	if err != nil {
		log.Fatalf("❌ 声明数据队列失败: %v", err)
	}
	log.Printf("✓ 数据队列声明成功: %s (TTL: %d 毫秒)", DATA_QUEUE, TTL_MILLISECONDS)

	// 绑定数据队列
	err = ch.QueueBind(
		DATA_QUEUE,       // queue
		DATA_ROUTING_KEY, // routing key
		DATA_EXCHANGE,    // exchange
		false,            // no-wait
		nil,              // args
	)
	if err != nil {
		log.Fatalf("❌ 绑定数据队列失败: %v", err)
	}
	log.Printf("✓ 数据队列绑定成功: %s -> %s", DATA_QUEUE, DATA_EXCHANGE)

	// 启动数据队列的消费者（开机逻辑）
	log.Println("")
	log.Println("🚀 启动消费者...")
	go func() {
		log.Printf("  [数据队列消费者] 正在消费数据队列: %s", DATA_QUEUE)
		msgs, err := ch.Consume(
			DATA_QUEUE, // queue
			"",         // consumer
			false,      // auto-ack
			false,      // exclusive
			false,      // no-local
			false,      // no-wait
			nil,        // args
		)
		if err != nil {
			log.Fatalf("❌ 消费数据队列失败: %v", err)
		}
		log.Printf("  [数据队列消费者] ✓ 消费者已启动，等待消息...")

		for d := range msgs {
			log.Printf("  [数据队列消费者] 📨 收到数据消息: %s", d.Body)

			// 调用ECS开机API
			if err := startECS(); err != nil {
				log.Printf("  [数据队列消费者] ❌ 开机失败: %v", err)
				d.Nack(false, false) // 拒绝消息并重新入队
				continue
			}

			log.Printf("  [数据队列消费者] ✓ ECS已开启，处理消息: %s", d.Body)
			d.Ack(false) // 确认消息处理成功
		}
	}()

	// 启动关机队列的消费者（关机逻辑）
	go func() {
		log.Printf("  [关机队列消费者] 正在消费关机队列: %s", SHUTDOWN_QUEUE)
		msgs, err := ch.Consume(
			SHUTDOWN_QUEUE, // queue
			"",             // consumer
			false,          // auto-ack
			false,          // exclusive
			false,          // no-local
			false,          // no-wait
			nil,            // args
		)
		if err != nil {
			log.Fatalf("❌ 消费关机队列失败: %v", err)
		}
		log.Printf("  [关机队列消费者] ✓ 消费者已启动，等待消息...")

		for d := range msgs {
			log.Printf("  [关机队列消费者] 📨 收到关机消息 (数据超时): %s", d.Body)

			// 调用ECS关机API
			if err := shutdownECS(); err != nil {
				log.Printf("  [关机队列消费者] ❌ 关机失败: %v", err)
				d.Nack(false, false) // 拒绝消息并重新入队
				continue
			}

			log.Printf("  [关机队列消费者] ✓ ECS已关闭")
			d.Ack(false) // 确认消息处理成功
		}
	}()

	// 等待 goroutine 启动
	log.Println("")
	log.Println("========================================")
	log.Println("✓ 程序初始化完成")
	log.Println("✓ 所有消费者已启动")
	log.Println("")
	log.Println("📌 程序正在运行，等待消息...")
	log.Println("   - 数据队列消费者: 监听数据消息，触发开机")
	log.Println("   - 关机队列消费者: 监听超时消息，触发关机")
	log.Println("")
	log.Println("💡 提示: 程序会持续运行，按 Ctrl+C 退出")
	log.Println("========================================")

	// 等待信号程序退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("")
	log.Println("========================================")
	log.Println("🛑 收到退出信号，程序正在退出...")
	log.Println("========================================")
}

// ECS开机函数
func startECS() error {
	log.Printf("  [ECS操作] 正在启动 ECS 实例: %s", ECS_INSTANCE_ID)
	request := ecs.CreateStartInstancesRequest()
	request.InstanceId = ECS_INSTANCE_ID
	request.Scheme = "https" // 使用HTTPS协议

	response, err := ecsClient.StartInstances(request)
	if err != nil {
		return err
	}

	log.Printf("  [ECS操作] ✓ 开机API响应: %s", response.String())
	return nil
}

// ECS关机函数
func shutdownECS() error {
	log.Printf("  [ECS操作] 正在关闭 ECS 实例: %s", ECS_INSTANCE_ID)
	request := ecs.CreateStopInstancesRequest()
	request.InstanceId = ECS_INSTANCE_ID
	request.Scheme = "https" // 使用HTTPS协议
	request.ForceStop = true // 强制关机

	response, err := ecsClient.StopInstances(request)
	if err != nil {
		return err
	}

	log.Printf("  [ECS操作] ✓ 关机API响应: %s", response.String())
	return nil
}
