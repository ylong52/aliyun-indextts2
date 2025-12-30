# RabbitMQ 地址配置分析

## 问题描述

Dockerfile 中注释说 `RABBITMQ_URL 留空，运行时由 .env/.env_linux/.env_win 提供`，但实际代码中使用了 `localhost:5672` 作为默认值，这在容器内是错误的，因为 `localhost` 指向容器本身，而不是宿主机。

## 配置来源分析

### 1. 配置文件优先级（从高到低）

#### 优先级 1: `rabbitmq_config.yaml` 配置文件
- **文件位置**: `python_indextts_code/indextts/app/rabbitmq_config.yaml`
- **当前状态**: 第 2 行的 `url` 被注释掉了
  ```yaml
  rabbitmq:
    # url: amqp://guest:guest@host.docker.internal:5672/  # 已注释
    queue: tts_jobs
  ```
- **代码位置**: `rabbitmq_worker.py` 第 440 行
  ```python
  rabbitmq_url=rabbitmq.get("url", "amqp://guest:guest@localhost:5672/"),
  ```
- **问题**: 如果配置文件中没有 `url`，会使用默认值 `localhost:5672`，这是**错误的**

#### 优先级 2: 环境变量 `RABBITMQ_URL`
- **加载位置**: `docker/docker-entrypoint.sh` 第 191-209 行
  - 按优先级加载: `.env` > `.env_local` > `.env_dev`
- **当前状态**:
  - `.env_dev` (Linux): ✅ 有设置 `RABBITMQ_URL=amqp://guest:guest@172.31.149.22:5672/`
  - `.env_local` (Windows): ❌ **没有设置** `RABBITMQ_URL`
- **代码位置**: `rabbitmq_worker.py` 第 1055-1084 行
  - 代码会检查环境变量，但**不会覆盖配置文件中的值**（第 1081 行注释说明）

#### 优先级 3: Dockerfile 环境变量
- **文件位置**: `docker/Dockerfile` 第 86-93 行
- **当前状态**: `RABBITMQ_URL` **没有在 Dockerfile 中设置**
- **注释说明**: "RABBITMQ_URL 留空，运行时由 .env/.env_linux/.env_win 提供"

## 问题根源

1. **Windows 环境** (`.env_local`):
   - ❌ 没有设置 `RABBITMQ_URL` 环境变量
   - ❌ `rabbitmq_config.yaml` 中的 `url` 被注释掉
   - ❌ 代码使用默认值 `localhost:5672`（错误）

2. **代码逻辑问题**:
   - `rabbitmq_worker.py` 第 1081 行：环境变量**不会覆盖**配置文件中的值
   - 如果配置文件中没有 `url`，会使用硬编码的默认值 `localhost:5672`

3. **容器网络问题**:
   - 容器内的 `localhost` 指向容器本身，不是宿主机
   - 应该使用 `host.docker.internal` 来访问宿主机的 RabbitMQ（Windows/Mac）
   - 或者使用宿主机的实际 IP 地址

## 解决方案

### 方案 1: 在 `.env_local` 中添加 `RABBITMQ_URL`（推荐）

**优点**: 
- 符合 Dockerfile 注释的说明
- 环境变量统一管理
- 不同环境可以使用不同的配置

**操作**:
在 `docker-data/.env_local` 中添加：
```bash
# RabbitMQ配置
RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/
```

**注意**: 需要修改 `rabbitmq_worker.py` 使其优先使用环境变量（见方案 3）

### 方案 2: 在 `rabbitmq_config.yaml` 中取消注释并设置正确的地址

**优点**: 
- 配置直观，直接在配置文件中
- 不需要修改代码逻辑

**操作**:
在 `python_indextts_code/indextts/app/rabbitmq_config.yaml` 中：
```yaml
rabbitmq:
  url: amqp://guest:guest@host.docker.internal:5672/  # 取消注释
  queue: tts_jobs
```

### 方案 3: 修改 `rabbitmq_worker.py` 使其优先使用环境变量（最佳方案）

**优点**: 
- 符合 12-Factor App 原则（配置通过环境变量）
- 灵活性最高
- 可以覆盖配置文件中的值

**需要修改的代码**:
`python_indextts_code/indextts/app/rabbitmq_worker.py` 第 440 行：

**修改前**:
```python
rabbitmq_url=rabbitmq.get("url", "amqp://guest:guest@localhost:5672/"),
```

**修改后**:
```python
rabbitmq_url=os.environ.get(
    "RABBITMQ_URL",
    rabbitmq.get("url", "amqp://guest:guest@host.docker.internal:5672/")
),
```

**同时修改默认值**:
将默认值从 `localhost:5672` 改为 `host.docker.internal:5672`（适用于 Windows/Mac Docker Desktop）

## 推荐的综合解决方案

1. **修改 `rabbitmq_worker.py`**:
   - 优先使用环境变量 `RABBITMQ_URL`
   - 将默认值改为 `host.docker.internal:5672`

2. **在 `.env_local` 中添加**:
   ```bash
   RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/
   ```

3. **在 `rabbitmq_config.yaml` 中保留注释**:
   - 作为备用配置，如果环境变量未设置，则使用配置文件中的值

## 不同环境的配置建议

### Windows 环境 (`.env_local`)
```bash
RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/
```
- `host.docker.internal` 是 Docker Desktop 提供的特殊主机名，指向宿主机

### Linux 环境 (`.env_dev`)
```bash
RABBITMQ_URL=amqp://guest:guest@172.31.149.22:5672/
```
- 使用实际的 IP 地址或主机名

### Mac 环境
```bash
RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/
```
- 同 Windows，使用 `host.docker.internal`

## 验证方法

1. **检查环境变量是否加载**:
   ```bash
   docker exec indextts-deplyment printenv RABBITMQ_URL
   ```

2. **查看 worker 启动日志**:
   ```bash
   # 在容器内运行
   python -u /python_indextts_code/indextts/app/rabbitmq_worker.py --config /python_indextts_code/indextts/app/rabbitmq_config.yaml
   ```
   查看输出中的 "RabbitMQ 环境变量检查" 和 "实际使用的 rabbitmq_url"

3. **测试连接**:
   - 确保宿主机上的 RabbitMQ 服务正在运行
   - 确保端口 5672 可访问
   - 检查防火墙设置

## 相关文件清单

1. **配置文件**:
   - `docker-data/.env_local` - Windows 环境变量
   - `docker-data/.env_dev` - Linux 环境变量
   - `python_indextts_code/indextts/app/rabbitmq_config.yaml` - RabbitMQ 配置

2. **代码文件**:
   - `python_indextts_code/indextts/app/rabbitmq_worker.py` - Worker 主程序
   - `docker/docker-entrypoint.sh` - 环境变量加载脚本
   - `docker/Dockerfile` - Docker 镜像构建文件

3. **脚本文件**:
   - `docker/run_docker_win11.ps1` - Windows 启动脚本

