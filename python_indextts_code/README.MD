# IndexTTS2 基于 RabbitMQ 的分布式语音合成系统

## 项目概述

本项目是一个基于 **RabbitMQ 消息队列**的分布式文本转语音（TTS）任务处理系统。系统通过消息队列实现任务调度，使用阿里云 ECS GPU 服务器进行语音合成计算，实现按需使用 GPU 资源，有效节省成本。

**核心特点**：
- ✅ 基于消息队列的异步任务处理
- ✅ 自动开关机，按需使用 GPU 资源
- ✅ 支持本地文件和 API 两种文本获取模式
- ✅ 自动上传结果到云存储
- ✅ 完成事件通知机制

---

## 系统架构

### 整体架构图

```
┌─────────────────┐
│  任务生产者      │  (外部系统或测试脚本)
│  发送TTS任务     │
└────────┬────────┘
         │
         │ 发送消息
         ▼
┌─────────────────────────────────────┐
│      RabbitMQ 消息队列               │
│  ┌───────────────────────────────┐  │
│  │  tts_jobs 队列                │  │  (TTS任务队列)
│  └───────────────────────────────┘  │
│  ┌───────────────────────────────┐  │
│  │  data_queue 队列               │  │  (监控队列，带TTL)
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
         │                    │
         │                    │
    ┌────▼────┐         ┌─────▼─────┐
    │监控程序  │         │ECS GPU服务器│
    │(常驻运行)│         │(按需启动)  │
    └────┬────┘         └─────┬─────┘
         │                    │
         │ 检测消息            │ 启动后运行
         │ 启动ECS             │ IndexTTS镜像
         │                    │
         │                    ▼
         │            ┌─────────────────┐
         │            │ rabbitmq_worker │
         │            │ 消费TTS任务     │
         │            │ 执行语音合成    │
         │            └────────┬────────┘
         │                     │
         │                     │ 完成后
         │                     ▼
         │            ┌─────────────────┐
         │            │ 上传到七牛云    │
         │            │ 发布完成事件    │
         │            └─────────────────┘
         │
         │ 队列空闲3分钟
         │ 自动关闭ECS
         ▼
┌─────────────────┐
│  ECS服务器关闭   │
│  节省成本        │
└─────────────────┘
```

---

## 系统工作流程

### 第一阶段：任务提交

1. **任务消息发送**
   - 外部系统或测试脚本向 RabbitMQ 的 `tts_jobs` 队列发送 TTS 任务消息
   - 消息格式为 JSON，包含以下字段：
     - `text_path`: 文本文件路径（mode=local 时使用）
     - `user_id`: 用户ID
     - `record_id`: 任务记录ID
     - `timestamp`: 时间戳
     - `mode`: 文本获取模式（`local` 或 `api`）
     - `style_file`: 音色参考文件路径（可选）

2. **相关文件**
   - `tests/rabbitmq_publish_sample.py` - 测试消息发送脚本

---

### 第二阶段：ECS 服务器自动启动

1. **监控程序检测消息**
   - 监控程序 `aliyun/ttl_rabbitmq.go` 持续监听 RabbitMQ 队列
   - 当检测到 `data_queue` 队列中有新消息时，触发 ECS 启动流程

2. **启动 ECS GPU 服务器**
   - 监控程序调用阿里云 ECS API，启动指定的 GPU 服务器实例
   - 使用配置文件 `aliyun/config.yml` 中的 ECS 实例信息

3. **相关文件**
   - `aliyun/ttl_rabbitmq.go` - RabbitMQ 监控和自动开关机程序（Go语言）
   - `aliyun/config.yml` - ECS 服务器配置（包含 AccessKey、实例ID等）
   - `aliyun/run_ecs.go` - ECS 服务器控制工具（可单独使用）

4. **程序运行方式**
   - `ttl_rabbitmq.go` 需要**常驻内存运行**，持续监听队列消息
   - 建议部署在独立的低配置服务器上（不在 ECS GPU 服务器上运行）
   - 可以使用 systemd、Supervisor 等进程管理器保持运行

---

### 第三阶段：IndexTTS 镜像启动

1. **ECS 服务器启动后**
   - ECS GPU 服务器启动完成后，自动运行预装的 IndexTTS Docker 镜像
   - 镜像中包含了完整的 IndexTTS2 语音合成环境

2. **镜像启动 Worker 程序**
   - 镜像启动后，自动运行 `indextts/app/rabbitmq_worker.py` 程序
   - Worker 程序连接到 RabbitMQ，开始消费 `tts_jobs` 队列中的任务

3. **相关文件**
   - `indextts/app/rabbitmq_worker.py` - RabbitMQ 任务消费者（Python）
   - `indextts/app/rabbitmq_config.yaml` - Worker 配置文件

---

### 第四阶段：TTS 任务处理

1. **消费任务消息**
   - `rabbitmq_worker.py` 从 `tts_jobs` 队列中获取任务消息
   - 解析 JSON 消息，提取任务参数

2. **获取文本内容**
   - **模式1：local 模式**
     - 从本地文件系统读取 `text_path` 指定的文本文件
     - 使用配置中的 `text.local_root` 作为根目录
   - **模式2：api 模式**
     - 通过 HTTP API 请求获取文本内容
     - 使用配置中的 `api.base_url` 作为 API 基础地址

3. **执行语音合成**
   - 调用 IndexTTS2 核心推理引擎 `indextts/infer_v2.py`
   - 使用指定的音色参考文件（`style_file`）进行语音合成
   - 生成音频文件（.wav 格式）

4. **相关文件**
   - `indextts/app/rabbitmq_worker.py` - 任务处理主程序
   - `indextts/infer_v2.py` - IndexTTS2 核心推理引擎
   - `indextts/app/rabbitmq_config.yaml` - 配置文件（包含 RabbitMQ、文本、API、TTS 等配置）
   - `checkpoints/` - 模型文件目录（包含 GPT 模型、声码器等）

---

### 第五阶段：结果上传和通知

1. **上传到云存储**
   - 如果启用了七牛云上传（配置中 `qiniu.enable=true`）
   - 将生成的音频文件上传到七牛云对象存储
   - 获取音频文件的访问 URL

2. **发布完成事件**
   - 向 RabbitMQ 的 `tts_done_exchange` 交换机发布完成事件
   - 事件消息包含：
     - `event`: 事件类型（"tts_done"）
     - `record_id`: 任务记录ID
     - `user_id`: 用户ID
     - `audio_url`: 音频文件访问地址
     - `timestamp`: 完成时间戳
     - `meta`: 元数据（时长、模式、文本路径等）

3. **下游服务处理**
   - 其他服务可以订阅 `tts_done_exchange` 获取完成通知
   - 例如：数据库更新、Web 推送、日志分析等

4. **相关文件**
   - `indextts/app/rabbitmq_worker.py` - 处理上传和通知逻辑
   - `rabbitmq_outputs/` - 本地输出目录（音频文件临时存储）

---

### 第六阶段：ECS 服务器自动关闭

1. **队列空闲检测**
   - 监控程序 `ttl_rabbitmq.go` 使用 RabbitMQ 的 TTL（Time To Live）机制
   - `data_queue` 队列中的消息设置了 3 分钟的过期时间
   - 如果 3 分钟内没有新消息到达，消息会自动过期

2. **触发关机流程**
   - 过期的消息会被转发到死信队列 `shutdown_queue`
   - 监控程序接收到关机消息后，调用阿里云 ECS API 停止服务器实例
   - ECS 服务器正常关机，释放 GPU 资源

3. **成本节省**
   - 通过自动关机机制，只在有任务时使用 GPU 服务器
   - 空闲时自动关闭，有效节省云服务器成本

4. **相关文件**
   - `aliyun/ttl_rabbitmq.go` - 实现 TTL 和死信队列逻辑

---

## 核心文件说明

### 1. 监控和 ECS 控制程序（Go 语言）

| 文件路径 | 功能说明 |
|---------|---------|
| `aliyun/ttl_rabbitmq.go` | **核心监控程序**：常驻运行，监听 RabbitMQ 队列，自动开关 ECS GPU 服务器。使用 TTL 和死信队列机制实现空闲自动关机。 |
| `aliyun/run_ecs.go` | **ECS 控制工具**：独立的 ECS 服务器控制程序，支持启动、停止、查询状态等操作。可单独使用或由监控程序调用。 |
| `aliyun/config.yml` | **ECS 配置**：包含阿里云 AccessKey、实例ID、地域等配置信息。**注意**：此文件包含敏感信息，不应提交到版本控制。 |
| `aliyun/go.mod` | Go 模块依赖定义文件 |
| `aliyun/go.sum` | Go 模块依赖锁定文件 |

### 2. TTS 任务处理程序（Python）

| 文件路径 | 功能说明 |
|---------|---------|
| `indextts/app/rabbitmq_worker.py` | **核心 Worker 程序**：消费 RabbitMQ 队列中的 TTS 任务，执行语音合成，上传结果，发布完成事件。运行在 ECS GPU 服务器的 IndexTTS 镜像中。 |
| `indextts/app/rabbitmq_config.yaml` | **Worker 配置文件**：包含 RabbitMQ 连接信息、文本获取配置、TTS 模型配置、七牛云上传配置等。 |
| `indextts/app/tts_done_logger.py` | TTS 完成事件日志记录工具（可选） |
| `indextts/infer_v2.py` | **IndexTTS2 核心推理引擎**：执行实际的语音合成计算，由 `rabbitmq_worker.py` 调用。 |

### 3. 模型和配置文件

| 文件路径 | 功能说明 |
|---------|---------|
| `checkpoints/config.yaml` | IndexTTS2 模型配置文件 |
| `checkpoints/gpt.pth` | GPT 模型权重文件 |
| `checkpoints/s2mel.pth` | S2Mel 模型权重文件 |
| `checkpoints/` 目录下其他文件 | 其他模型文件和配置 |

### 4. 测试和工具文件

| 文件路径 | 功能说明 |
|---------|---------|
| `tests/rabbitmq_publish_sample.py` | **测试脚本**：向 RabbitMQ 队列发送测试 TTS 任务消息，用于测试整个系统流程。 |
| `tests/test_api_v1_text_segment.py` | API 测试脚本 |
| `tests/test_tts_generate.py` | TTS 生成测试脚本 |
| `tools/gpu_check.py` | GPU 环境检查工具 |

### 5. 文档文件

| 文件路径 | 功能说明 |
|---------|---------|
| `框架说明.md` | IndexTTS2 框架技术说明文档 |
| `测试运行.md` | 测试运行报告 |
| `rabbitmq管理.md` | RabbitMQ 功能开发说明 |
| `项目整理说明.md` | 项目整理说明文档 |
| `aliyun/run_ecs.md` | ECS 控制工具使用说明 |
| `aliyun/ttl_rabbitmq.md` | TTL 和死信队列实现说明 |

---

## 消息格式说明

### TTS 任务消息（发送到 tts_jobs 队列）

```json
{
  "text_path": "examples/texts/demo.txt",
  "user_id": "user-123",
  "record_id": "record-456",
  "timestamp": "2025-12-01T10:00:00Z",
  "mode": "local",
  "style_file": "uploads/sample_library/voice_01.mp3"
}
```

**字段说明**：
- `text_path`: 文本文件路径（mode=local 时使用）
- `user_id`: 用户标识
- `record_id`: 任务记录ID（唯一标识）
- `timestamp`: 任务创建时间戳
- `mode`: 文本获取模式（`"local"` 或 `"api"`）
- `style_file`: 音色参考文件路径（可选）

### TTS 完成事件（发布到 tts_done_exchange）

```json
{
  "event": "tts_done",
  "record_id": "record-456",
  "user_id": "user-123",
  "audio_url": "https://qiniu.ai-book.top/ai_indextts/record-456.wav",
  "timestamp": "2025-12-01T10:05:30Z",
  "meta": {
    "duration": 36.54,
    "mode": "local",
    "text_path": "examples/texts/demo.txt",
    "style_file": "uploads/sample_library/voice_01.mp3"
  }
}
```

**字段说明**：
- `event`: 事件类型（固定为 `"tts_done"`）
- `record_id`: 任务记录ID
- `user_id`: 用户标识
- `audio_url`: 音频文件访问地址
- `timestamp`: 完成时间戳
- `meta`: 元数据信息

---

## RabbitMQ 队列和交换机说明

### 队列（Queues）

| 队列名称 | 用途 | 说明 |
|---------|------|------|
| `tts_jobs` | TTS 任务队列 | 接收外部系统发送的 TTS 任务消息，由 `rabbitmq_worker.py` 消费 |
| `data_queue` | 监控队列 | 用于监控程序检测任务，设置 3 分钟 TTL，过期后触发关机 |
| `shutdown_queue` | 关机队列 | 死信队列，接收过期的消息，触发 ECS 关机操作 |

### 交换机（Exchanges）

| 交换机名称 | 类型 | 用途 |
|-----------|------|------|
| `tts_done_exchange` | fanout/topic | TTS 完成事件交换机，发布任务完成通知 |
| `data_exchange` | direct | 数据消息交换机，路由消息到 `data_queue` |
| `shutdown_exchange` | direct | 关机消息交换机，路由过期消息到 `shutdown_queue` |

---

## 系统部署说明

### 1. 监控程序部署（ttl_rabbitmq.go）

**部署位置**：
- 建议部署在**独立的低配置服务器**上
- 不要部署在 ECS GPU 服务器上（避免自己管理自己）

**运行方式**：
- 需要**常驻内存运行**
- 可以使用 systemd、Supervisor 等进程管理器
- 参考 `aliyun/ttl_rabbitmq.md` 中的部署说明

### 2. ECS GPU 服务器配置

**镜像要求**：
- 预装 IndexTTS Docker 镜像
- 镜像启动后自动运行 `rabbitmq_worker.py`
- 配置好 RabbitMQ 连接和模型文件路径

### 3. RabbitMQ 服务

**要求**：
- 安装并运行 RabbitMQ 服务
- 创建所需的队列和交换机
- 配置好用户权限

### 4. 配置文件

**ECS 配置**（`aliyun/config.yml`）：
- 包含阿里云 AccessKey（敏感信息，需保密）
- 包含 ECS 实例ID和地域信息

**Worker 配置**（`indextts/app/rabbitmq_config.yaml`）：
- RabbitMQ 连接信息
- 文本获取配置（本地路径或 API 地址）
- TTS 模型配置
- 七牛云上传配置（可选）

---

## 使用示例

### 1. 发送测试任务

使用测试脚本发送 TTS 任务：

```bash
# 运行测试脚本
uv run tests/rabbitmq_publish_sample.py
```

### 2. 查看任务处理

- 查看 `rabbitmq_worker.py` 的日志输出
- 检查 `rabbitmq_outputs/` 目录中的输出文件
- 查看七牛云存储中的上传文件

### 3. 监控系统状态

- 监控 RabbitMQ 队列消息数量
- 监控 ECS 服务器状态（运行中/已停止）
- 查看完成事件消息

---

## 注意事项

1. **监控程序必须常驻运行**
   - `ttl_rabbitmq.go` 必须持续运行，否则无法自动开关机
   - 建议使用进程管理器确保自动重启

2. **配置文件安全**
   - `aliyun/config.yml` 包含敏感信息，不要提交到版本控制
   - 使用文件权限控制访问

3. **TTL 时间设置**
   - 当前设置为 3 分钟，可根据实际业务调整
   - 过短可能导致频繁开关机
   - 过长可能导致资源浪费

4. **消息确认机制**
   - Worker 使用手动确认（Manual Ack）
   - 确保任务处理成功后才确认消息

5. **错误处理**
   - 任务处理失败时，消息会重新入队
   - 需要监控失败任务，及时处理

---

## 相关文档

- `框架说明.md` - IndexTTS2 框架技术说明
- `rabbitmq管理.md` - RabbitMQ 功能开发说明
- `aliyun/run_ecs.md` - ECS 控制工具使用说明
- `aliyun/ttl_rabbitmq.md` - TTL 和死信队列实现说明
- `项目整理说明.md` - 项目整理说明

---

**最后更新**: 2025-12-01
