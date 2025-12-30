# RabbitMQ 连接问题排查指南 - Windows PowerShell

## 问题现象

```
ConnectionError: 无法连接到 RabbitMQ 服务器 127.0.0.1:5672
```

这表明代码使用了错误的地址 `127.0.0.1:5672`（容器内的 localhost），而不是宿主机的地址。

---

## 排查步骤

### 步骤 1: 检查环境变量是否设置

**在 PowerShell 中执行**:

```powershell
# 检查容器内的环境变量
docker exec indextts-deplyment printenv RABBITMQ_URL
```

**预期结果**:
- ✅ **有值**: 应该显示类似 `amqp://guest:guest@host.docker.internal:5672/`
- ❌ **无值**: 显示为空，说明环境变量未设置

**如果未设置，继续步骤 2**

---

### 步骤 2: 检查宿主机环境变量文件

**在 PowerShell 中执行**:

```powershell
# 检查 .env_local 文件是否存在
Test-Path "docker-data\.env_local"

# 查看 .env_local 文件内容
Get-Content "docker-data\.env_local" | Select-String -Pattern "RABBITMQ"
```

**预期结果**:
- ✅ **有配置**: 应该看到 `RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/`
- ❌ **无配置**: 没有找到 `RABBITMQ_URL` 配置

**如果没有配置，需要添加**:

```powershell
# 在 .env_local 文件末尾添加（使用 UTF-8 编码）
Add-Content -Path "docker-data\.env_local" -Value "`n# RabbitMQ配置`nRABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/" -Encoding UTF8
```

---

### 步骤 3: 检查容器启动时是否加载了环境变量

**在 PowerShell 中执行**:

```powershell
# 查看容器启动日志，检查环境变量加载情况
docker logs indextts-deplyment | Select-String -Pattern "环境变量|RABBITMQ|env"
```

**预期结果**:
- ✅ **已加载**: 看到 "使用环境文件: .env_local" 或类似信息
- ❌ **未加载**: 没有看到环境文件加载信息

**如果未加载，检查 docker-entrypoint.sh 是否正确加载了 .env_local**

---

### 步骤 4: 检查 RabbitMQ 服务是否在宿主机运行

**在 PowerShell 中执行**:

```powershell
# 检查 RabbitMQ 服务状态（如果使用 Windows 服务）
Get-Service | Where-Object {$_.Name -like "*rabbitmq*"}

# 或者检查端口是否被占用
netstat -an | Select-String ":5672"

# 或者使用 Test-NetConnection 测试连接
Test-NetConnection -ComputerName localhost -Port 5672
```

**预期结果**:
- ✅ **服务运行**: 看到 RabbitMQ 服务状态为 "Running"
- ✅ **端口监听**: 看到 `0.0.0.0:5672` 或 `127.0.0.1:5672` 在监听
- ✅ **连接成功**: Test-NetConnection 显示 "TcpTestSucceeded : True"
- ❌ **服务未运行**: 需要启动 RabbitMQ 服务

**如果服务未运行，启动 RabbitMQ**:

```powershell
# 如果使用 Windows 服务
Start-Service RabbitMQ

# 或者如果使用 Docker 运行 RabbitMQ
docker ps | Select-String -Pattern "rabbitmq"
```

---

## 在宿主机 Windows PowerShell 中测试 RabbitMQ 服务

### 方法 1: 检查 Windows 服务状态

**在 PowerShell 中执行**:

```powershell
# 查找 RabbitMQ 服务
Get-Service | Where-Object {$_.Name -like "*rabbitmq*"}

# 查看服务详细信息
$service = Get-Service | Where-Object {$_.Name -like "*rabbitmq*"} | Select-Object -First 1
if ($service) {
    Write-Host "服务名称: $($service.Name)" -ForegroundColor Cyan
    Write-Host "显示名称: $($service.DisplayName)" -ForegroundColor Cyan
    Write-Host "状态: $($service.Status)" -ForegroundColor $(if ($service.Status -eq 'Running') {'Green'} else {'Red'})
    Write-Host "启动类型: $($service.StartType)" -ForegroundColor Cyan
}
```

**预期结果**:
- ✅ **服务运行**: 状态显示为 "Running"
- ❌ **服务停止**: 状态显示为 "Stopped"，需要启动服务

**启动服务**:
```powershell
# 启动 RabbitMQ 服务
Start-Service RabbitMQ

# 或者使用服务名称（根据实际服务名调整）
Start-Service -Name "RabbitMQ"
```

---

### 方法 2: 测试端口连接

**在 PowerShell 中执行**:

```powershell
# 方法 1: 使用 Test-NetConnection（推荐）
Test-NetConnection -ComputerName localhost -Port 5672

# 方法 2: 使用 netstat 查看端口监听状态
netstat -an | Select-String ":5672"

# 方法 3: 使用 Get-NetTCPConnection（PowerShell 5.1+）
Get-NetTCPConnection -LocalPort 5672 -ErrorAction SilentlyContinue | Format-Table LocalAddress, LocalPort, State, OwningProcess
```

**预期结果**:
- ✅ **Test-NetConnection**: `TcpTestSucceeded : True`
- ✅ **netstat**: 看到 `0.0.0.0:5672` 或 `127.0.0.1:5672` 状态为 `LISTENING`
- ✅ **Get-NetTCPConnection**: 显示端口状态为 `Listen`

---

### 方法 3: 使用 Python 测试 AMQP 连接

**前提**: 需要安装 Python 和 pika 库

**在 PowerShell 中执行**:

```powershell
# 检查是否安装了 Python
python --version

# 如果没有安装 pika，先安装
pip install pika

# 测试连接
python -c "import pika; connection = pika.BlockingConnection(pika.ConnectionParameters('localhost', 5672)); print('✅ 连接成功！'); connection.close()"

# 或者使用完整的 URL（带用户名密码）
python -c "import pika; params = pika.URLParameters('amqp://guest:guest@localhost:5672/'); connection = pika.BlockingConnection(params); print('✅ 连接成功！'); connection.close()"
```

**更详细的测试脚本**:

```powershell
# 保存为 test_rabbitmq.ps1 或直接执行
python -c @"
import pika
import sys

try:
    # 测试连接
    print('正在测试 RabbitMQ 连接...')
    params = pika.ConnectionParameters(
        host='localhost',
        port=5672,
        virtual_host='/',
        credentials=pika.PlainCredentials('guest', 'guest')
    )
    connection = pika.BlockingConnection(params)
    print('✅ 连接成功！')
    
    # 获取服务器信息
    channel = connection.channel()
    print(f'✅ 通道创建成功')
    
    # 测试队列操作
    test_queue = 'test_queue_connection'
    channel.queue_declare(queue=test_queue, durable=False, auto_delete=True)
    print(f'✅ 队列操作正常')
    
    # 清理
    channel.queue_delete(queue=test_queue)
    channel.close()
    connection.close()
    print('✅ 所有测试通过！')
    sys.exit(0)
except pika.exceptions.AMQPConnectionError as e:
    print(f'❌ 连接失败: {e}')
    sys.exit(1)
except Exception as e:
    print(f'❌ 错误: {e}')
    sys.exit(1)
"@
```

---

### 方法 4: 使用 RabbitMQ 管理工具（rabbitmqctl）

**在 PowerShell 中执行**:

```powershell
# 查找 RabbitMQ 安装目录（通常在 Erlang 安装目录下）
$rabbitmqPath = Get-ChildItem -Path "C:\Program Files" -Recurse -Filter "rabbitmqctl.bat" -ErrorAction SilentlyContinue | Select-Object -First 1

if ($rabbitmqPath) {
    Write-Host "找到 RabbitMQ: $($rabbitmqPath.FullName)" -ForegroundColor Green
    
    # 测试服务器状态
    & $rabbitmqPath.FullName status
    
    # 查看节点信息
    & $rabbitmqPath.FullName node_health_check
    
    # 查看用户列表
    & $rabbitmqPath.FullName list_users
} else {
    Write-Host "未找到 rabbitmqctl.bat，可能未安装或路径不同" -ForegroundColor Yellow
    Write-Host "尝试在常见路径查找..." -ForegroundColor Yellow
    
    # 常见安装路径
    $commonPaths = @(
        "C:\Program Files\RabbitMQ Server\rabbitmq_server-*\sbin\rabbitmqctl.bat",
        "$env:APPDATA\RabbitMQ\rabbitmq_server-*\sbin\rabbitmqctl.bat"
    )
    
    foreach ($path in $commonPaths) {
        $found = Get-ChildItem -Path $path -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($found) {
            Write-Host "找到: $($found.FullName)" -ForegroundColor Green
            & $found.FullName status
            break
        }
    }
}
```

---

### 方法 5: 使用 Web 管理界面测试

**在 PowerShell 中执行**:

```powershell
# 测试管理界面端口（默认 15672）
Test-NetConnection -ComputerName localhost -Port 15672

# 如果端口可访问，可以在浏览器中打开
# http://localhost:15672
# 默认用户名: guest, 密码: guest

# 使用 PowerShell 测试 HTTP 连接
try {
    $response = Invoke-WebRequest -Uri "http://localhost:15672" -TimeoutSec 5 -UseBasicParsing
    Write-Host "✅ 管理界面可访问 (状态码: $($response.StatusCode))" -ForegroundColor Green
} catch {
    Write-Host "❌ 管理界面不可访问: $_" -ForegroundColor Red
}
```

---

### 方法 6: 使用 Telnet 测试端口（如果已安装）

**在 PowerShell 中执行**:

```powershell
# Windows 10/11 默认未安装 Telnet，需要先启用
# 启用 Telnet 客户端（需要管理员权限）
# Enable-WindowsOptionalFeature -Online -FeatureName TelnetClient

# 使用 Telnet 测试（需要先启用）
# telnet localhost 5672

# 或者使用 PowerShell 的 TCP 连接测试
$tcpClient = New-Object System.Net.Sockets.TcpClient
try {
    $tcpClient.Connect("localhost", 5672)
    if ($tcpClient.Connected) {
        Write-Host "✅ 端口 5672 可连接" -ForegroundColor Green
        $tcpClient.Close()
    }
} catch {
    Write-Host "❌ 端口 5672 不可连接: $_" -ForegroundColor Red
} finally {
    if ($tcpClient) { $tcpClient.Dispose() }
}
```

---

### 方法 7: 一键测试脚本

**在 PowerShell 中执行以下完整测试脚本**:

```powershell
Write-Host "=== RabbitMQ 服务测试 ===" -ForegroundColor Cyan
Write-Host ""

# 1. 检查服务状态
Write-Host "1. 检查 Windows 服务状态..." -ForegroundColor Yellow
$service = Get-Service | Where-Object {$_.Name -like "*rabbitmq*"} | Select-Object -First 1
if ($service) {
    $statusColor = if ($service.Status -eq 'Running') {'Green'} else {'Red'}
    Write-Host "  服务: $($service.DisplayName)" -ForegroundColor Cyan
    Write-Host "  状态: $($service.Status)" -ForegroundColor $statusColor
    if ($service.Status -ne 'Running') {
        Write-Host "  ⚠️  服务未运行，尝试启动..." -ForegroundColor Yellow
        try {
            Start-Service -Name $service.Name
            Write-Host "  ✅ 服务已启动" -ForegroundColor Green
        } catch {
            Write-Host "  ❌ 启动失败: $_" -ForegroundColor Red
        }
    }
} else {
    Write-Host "  ⚠️  未找到 RabbitMQ Windows 服务" -ForegroundColor Yellow
}

Write-Host ""

# 2. 测试端口连接
Write-Host "2. 测试端口 5672 连接..." -ForegroundColor Yellow
$portTest = Test-NetConnection -ComputerName localhost -Port 5672 -WarningAction SilentlyContinue
if ($portTest.TcpTestSucceeded) {
    Write-Host "  ✅ 端口 5672 可访问" -ForegroundColor Green
} else {
    Write-Host "  ❌ 端口 5672 不可访问" -ForegroundColor Red
}

Write-Host ""

# 3. 测试管理界面
Write-Host "3. 测试管理界面 (端口 15672)..." -ForegroundColor Yellow
try {
    $webTest = Invoke-WebRequest -Uri "http://localhost:15672" -TimeoutSec 3 -UseBasicParsing -ErrorAction Stop
    Write-Host "  ✅ 管理界面可访问 (状态码: $($webTest.StatusCode))" -ForegroundColor Green
    Write-Host "  📌 访问地址: http://localhost:15672" -ForegroundColor Cyan
} catch {
    Write-Host "  ⚠️  管理界面不可访问（可能未启用管理插件）" -ForegroundColor Yellow
}

Write-Host ""

# 4. Python 测试（如果可用）
Write-Host "4. Python AMQP 连接测试..." -ForegroundColor Yellow
$pythonCmd = Get-Command python -ErrorAction SilentlyContinue
if ($pythonCmd) {
    try {
        $result = python -c "import pika; c = pika.BlockingConnection(pika.ConnectionParameters('localhost', 5672)); c.close(); print('OK')" 2>&1
        if ($result -match "OK") {
            Write-Host "  ✅ Python AMQP 连接成功" -ForegroundColor Green
        } else {
            Write-Host "  ❌ Python AMQP 连接失败: $result" -ForegroundColor Red
        }
    } catch {
        Write-Host "  ⚠️  Python 测试失败（可能需要安装 pika: pip install pika）" -ForegroundColor Yellow
    }
} else {
    Write-Host "  ⚠️  未找到 Python" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "=== 测试完成 ===" -ForegroundColor Cyan
```

---

## 在 Docker 容器内测试 RabbitMQ 连接

### 前置步骤：进入容器

**在 PowerShell 中执行**:

```powershell
# 方法 1: 进入容器的交互式 shell
docker exec -it indextts-deplyment bash

# 方法 2: 直接执行命令（不进入交互式 shell）
docker exec indextts-deplyment <命令>

# 方法 3: 检查容器是否运行
docker ps | Select-String "indextts-deplyment"
```

---

### 方法 1: 检查环境变量

**在容器内执行**:

```bash
# 查看 RABBITMQ_URL 环境变量
echo $RABBITMQ_URL

# 或者使用 printenv
printenv RABBITMQ_URL

# 查看所有包含 RABBITMQ 的环境变量
env | grep -i rabbitmq

# 使用 Python 查看环境变量
python3 -c "import os; print('RABBITMQ_URL:', os.environ.get('RABBITMQ_URL', '未设置'))"
```

**预期结果**:
- ✅ **有值**: 应该显示类似 `amqp://guest:guest@host.docker.internal:5672/`
- ❌ **无值**: 显示为空，说明环境变量未设置

---

### 方法 2: 测试网络连通性

**在容器内执行**:

```bash
# 1. 测试 host.docker.internal 域名解析
ping -c 3 host.docker.internal

# 2. 使用 Python 测试端口连接
python3 -c "
import socket
import sys

def test_connection(host, port, timeout=3):
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(timeout)
        result = s.connect_ex((host, port))
        s.close()
        return result == 0
    except Exception as e:
        print(f'错误: {e}')
        return False

# 测试连接
hosts = ['host.docker.internal', 'localhost', '127.0.0.1']
port = 5672

for host in hosts:
    if test_connection(host, port):
        print(f'✅ {host}:{port} 连接成功')
    else:
        print(f'❌ {host}:{port} 连接失败')
"

# 3. 使用 nc (netcat) 测试（如果已安装）
# nc -zv host.docker.internal 5672

# 4. 使用 telnet 测试（如果已安装）
# telnet host.docker.internal 5672

# 5. 使用 curl 测试（如果已安装）
# curl -v telnet://host.docker.internal:5672
```

**预期结果**:
- ✅ **host.docker.internal:5672 连接成功**: 说明可以从容器访问宿主机的 RabbitMQ
- ❌ **连接失败**: 需要检查 Docker 网络配置或 RabbitMQ 服务状态

---

### 方法 3: 使用 Python pika 测试 AMQP 连接

**在容器内执行**:

```bash
# 简单连接测试
python3 << 'EOF'
import pika
import os
import sys

# 获取 RabbitMQ URL（优先使用环境变量）
rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
print(f"测试连接: {rabbitmq_url}")

try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print("✅ 连接成功！")
    
    # 获取服务器信息
    channel = connection.channel()
    print("✅ 通道创建成功")
    
    connection.close()
    sys.exit(0)
except pika.exceptions.AMQPConnectionError as e:
    print(f"❌ AMQP 连接失败: {e}")
    sys.exit(1)
except Exception as e:
    print(f"❌ 错误: {e}")
    sys.exit(1)
EOF
```

**更详细的测试脚本**:

```bash
# 完整功能测试
python3 << 'EOF'
import pika
import os
import sys
import time

def test_rabbitmq_connection():
    """测试 RabbitMQ 连接的完整功能"""
    
    # 获取连接 URL
    rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
    print(f"📌 连接地址: {rabbitmq_url}")
    print("")
    
    try:
        # 1. 测试连接
        print("1. 测试连接...")
        params = pika.URLParameters(rabbitmq_url)
        connection = pika.BlockingConnection(params)
        print("   ✅ 连接成功")
        
        # 2. 创建通道
        print("2. 创建通道...")
        channel = connection.channel()
        print("   ✅ 通道创建成功")
        
        # 3. 测试队列操作
        print("3. 测试队列操作...")
        test_queue = 'test_connection_queue'
        channel.queue_declare(queue=test_queue, durable=False, auto_delete=True)
        print(f"   ✅ 队列 '{test_queue}' 声明成功")
        
        # 4. 测试消息发送
        print("4. 测试消息发送...")
        channel.basic_publish(
            exchange='',
            routing_key=test_queue,
            body='Hello RabbitMQ!'
        )
        print("   ✅ 消息发送成功")
        
        # 5. 测试消息接收
        print("5. 测试消息接收...")
        method_frame, header_frame, body = channel.basic_get(queue=test_queue, auto_ack=True)
        if method_frame:
            print(f"   ✅ 消息接收成功: {body.decode()}")
        else:
            print("   ⚠️  未收到消息")
        
        # 6. 清理测试队列
        print("6. 清理测试队列...")
        channel.queue_delete(queue=test_queue)
        print("   ✅ 队列删除成功")
        
        # 7. 关闭连接
        channel.close()
        connection.close()
        print("")
        print("✅ 所有测试通过！")
        return True
        
    except pika.exceptions.AMQPConnectionError as e:
        print(f"❌ AMQP 连接错误: {e}")
        return False
    except pika.exceptions.ChannelClosed as e:
        print(f"❌ 通道错误: {e}")
        return False
    except Exception as e:
        print(f"❌ 未知错误: {e}")
        import traceback
        traceback.print_exc()
        return False

if __name__ == "__main__":
    success = test_rabbitmq_connection()
    sys.exit(0 if success else 1)
EOF
```

---

### 方法 4: 使用 curl 测试 HTTP API（管理界面）

**在容器内执行**:

```bash
# 测试管理界面 API（如果启用了管理插件）
# 注意：需要将 host.docker.internal 替换为实际的管理界面地址

# 测试连接（使用 guest/guest 默认凭据）
curl -u guest:guest http://host.docker.internal:15672/api/overview

# 或者使用环境变量中的地址
RABBITMQ_MGMT_URL=$(echo $RABBITMQ_URL | sed 's|amqp://|http://|' | sed 's|:5672|:15672|')
if [ ! -z "$RABBITMQ_MGMT_URL" ]; then
    curl -u guest:guest "${RABBITMQ_MGMT_URL}api/overview"
fi
```

---

### 方法 5: 检查 RabbitMQ 配置和日志

**在容器内执行**:

```bash
# 1. 查看配置文件中的 RabbitMQ 配置
cat /python_indextts_code/indextts/app/rabbitmq_config.yaml | grep -A 10 -i rabbitmq

# 2. 查看应用日志中的 RabbitMQ 相关信息
tail -50 /docker-data/logs/rabbitmq_worker.log | grep -i "rabbitmq\|连接\|connection"

# 3. 查看最近的错误日志
grep -i "error\|exception\|失败" /docker-data/logs/rabbitmq_worker.log | tail -20

# 4. 检查 Python 代码中使用的连接参数
python3 << 'EOF'
import os
import sys

# 检查环境变量
rabbitmq_url = os.environ.get("RABBITMQ_URL")
if rabbitmq_url:
    print(f"✅ 环境变量 RABBITMQ_URL: {rabbitmq_url}")
else:
    print("❌ 环境变量 RABBITMQ_URL 未设置")

# 尝试导入配置（如果存在）
try:
    # 根据实际项目结构调整导入路径
    sys.path.insert(0, '/python_indextts_code')
    # 这里需要根据实际代码结构调整
    print("📌 提示: 检查代码中的 RabbitMQ 配置")
except Exception as e:
    print(f"⚠️  无法导入配置: {e}")
EOF
```

---

### 方法 6: 一键测试脚本（容器内）

**在容器内执行完整的测试脚本**:

```bash
cat > /tmp/test_rabbitmq.sh << 'SCRIPTEOF'
#!/bin/bash

echo "=== Docker 容器内 RabbitMQ 连接测试 ==="
echo ""

# 1. 检查环境变量
echo "1. 检查环境变量..."
RABBITMQ_URL=$(printenv RABBITMQ_URL)
if [ -z "$RABBITMQ_URL" ]; then
    echo "   ❌ RABBITMQ_URL 未设置"
    RABBITMQ_URL="amqp://guest:guest@host.docker.internal:5672/"
    echo "   📌 使用默认值: $RABBITMQ_URL"
else
    echo "   ✅ RABBITMQ_URL: $RABBITMQ_URL"
fi
echo ""

# 2. 测试网络连通性
echo "2. 测试网络连通性..."
if ping -c 1 -W 2 host.docker.internal > /dev/null 2>&1; then
    echo "   ✅ host.docker.internal 可访问"
else
    echo "   ❌ host.docker.internal 不可访问"
fi
echo ""

# 3. 测试端口连接
echo "3. 测试端口 5672 连接..."
python3 -c "
import socket
s = socket.socket()
s.settimeout(2)
result = s.connect_ex(('host.docker.internal', 5672))
s.close()
if result == 0:
    print('   ✅ 端口 5672 可连接')
else:
    print('   ❌ 端口 5672 不可连接')
" 2>/dev/null || echo "   ⚠️  Python 测试失败"
echo ""

# 4. 测试 AMQP 连接
echo "4. 测试 AMQP 连接..."
python3 << 'PYEOF'
import pika
import os
import sys

rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
print(f"   📌 连接地址: {rabbitmq_url}")

try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print("   ✅ AMQP 连接成功")
    
    channel = connection.channel()
    print("   ✅ 通道创建成功")
    
    connection.close()
    sys.exit(0)
except Exception as e:
    print(f"   ❌ AMQP 连接失败: {e}")
    sys.exit(1)
PYEOF

if [ $? -eq 0 ]; then
    echo ""
    echo "✅ 所有测试通过！"
else
    echo ""
    echo "❌ 测试失败，请检查配置"
fi

echo ""
echo "=== 测试完成 ==="
SCRIPTEOF

chmod +x /tmp/test_rabbitmq.sh
/tmp/test_rabbitmq.sh
```

---

### 方法 7: 从宿主机执行容器内测试（无需进入容器）

**在 PowerShell 中执行**:

```powershell
# 1. 检查容器内环境变量
Write-Host "检查容器内环境变量..." -ForegroundColor Yellow
docker exec indextts-deplyment printenv RABBITMQ_URL

# 2. 测试网络连通性
Write-Host "`n测试网络连通性..." -ForegroundColor Yellow
docker exec indextts-deplyment ping -c 3 host.docker.internal

# 3. 测试端口连接
Write-Host "`n测试端口连接..." -ForegroundColor Yellow
docker exec indextts-deplyment python3 -c "import socket; s = socket.socket(); s.settimeout(2); result = s.connect_ex(('host.docker.internal', 5672)); print('✅ 连接成功' if result == 0 else '❌ 连接失败'); s.close()"

# 4. 测试 AMQP 连接
Write-Host "`n测试 AMQP 连接..." -ForegroundColor Yellow
docker exec indextts-deplyment python3 << 'EOF'
import pika
import os
rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
print(f"连接地址: {rabbitmq_url}")
try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print("✅ 连接成功！")
    connection.close()
except Exception as e:
    print(f"❌ 连接失败: {e}")
EOF
```

---

### 方法 8: 完整的容器内测试脚本（从宿主机执行）

**在 PowerShell 中执行**:

```powershell
Write-Host "=== Docker 容器内 RabbitMQ 测试 ===" -ForegroundColor Cyan
Write-Host ""

# 检查容器是否运行
$containerRunning = docker ps --format "{{.Names}}" | Select-String "indextts-deplyment"
if (-not $containerRunning) {
    Write-Host "❌ 容器 indextts-deplyment 未运行" -ForegroundColor Red
    exit 1
}

Write-Host "1. 检查环境变量..." -ForegroundColor Yellow
$envVar = docker exec indextts-deplyment printenv RABBITMQ_URL 2>$null
if ($envVar) {
    Write-Host "   ✅ RABBITMQ_URL: $envVar" -ForegroundColor Green
} else {
    Write-Host "   ❌ RABBITMQ_URL 未设置" -ForegroundColor Red
}

Write-Host ""
Write-Host "2. 测试网络连通性..." -ForegroundColor Yellow
$pingResult = docker exec indextts-deplyment ping -c 1 -W 2 host.docker.internal 2>&1
if ($pingResult -match "1 received" -or $pingResult -match "1 packets transmitted") {
    Write-Host "   ✅ host.docker.internal 可访问" -ForegroundColor Green
} else {
    Write-Host "   ❌ host.docker.internal 不可访问" -ForegroundColor Red
}

Write-Host ""
Write-Host "3. 测试端口连接..." -ForegroundColor Yellow
$portTest = docker exec indextts-deplyment python3 -c "import socket; s = socket.socket(); s.settimeout(2); result = s.connect_ex(('host.docker.internal', 5672)); print('OK' if result == 0 else 'FAIL'); s.close()" 2>&1
if ($portTest -match "OK") {
    Write-Host "   ✅ 端口 5672 可连接" -ForegroundColor Green
} else {
    Write-Host "   ❌ 端口 5672 不可连接" -ForegroundColor Red
}

Write-Host ""
Write-Host "4. 测试 AMQP 连接..." -ForegroundColor Yellow
$amqpTest = docker exec indextts-deplyment python3 -c @"
import pika
import os
import sys
rabbitmq_url = os.environ.get('RABBITMQ_URL', 'amqp://guest:guest@host.docker.internal:5672/')
try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print('OK')
    connection.close()
except Exception as e:
    print(f'FAIL: {e}')
    sys.exit(1)
"@ 2>&1

if ($amqpTest -match "^OK$") {
    Write-Host "   ✅ AMQP 连接成功" -ForegroundColor Green
} else {
    Write-Host "   ❌ AMQP 连接失败: $amqpTest" -ForegroundColor Red
}

Write-Host ""
Write-Host "=== 测试完成 ===" -ForegroundColor Cyan
```

---

### 步骤 5: 检查容器内网络连接（原步骤，保留作为快速参考）

**在 PowerShell 中执行**:

```powershell
# 进入容器
docker exec -it indextts-deplyment bash
```

**在容器内执行**:

```bash
# 测试连接到 host.docker.internal:5672
ping -c 3 host.docker.internal

# 测试端口连接（如果安装了 telnet 或 nc）
# 方法1: 使用 Python 测试
python3 -c "import socket; s = socket.socket(); s.settimeout(2); result = s.connect_ex(('host.docker.internal', 5672)); print('连接成功' if result == 0 else '连接失败'); s.close()"

# 方法2: 使用 curl（如果安装了）
curl -v telnet://host.docker.internal:5672
```

**预期结果**:
- ✅ **ping 成功**: 能够 ping 通 `host.docker.internal`
- ✅ **端口连接成功**: Python 测试显示 "连接成功"
- ❌ **连接失败**: 需要检查 Docker 网络配置

---

### 步骤 6: 检查 RabbitMQ 配置和实际使用的地址

**在容器内执行**:

```bash
# 查看实际使用的 RabbitMQ URL
python3 -c "import os; print('RABBITMQ_URL:', os.environ.get('RABBITMQ_URL', '未设置'))"

# 查看配置文件中的 URL
cat /python_indextts_code/indextts/app/rabbitmq_config.yaml | grep -A 5 rabbitmq

# 查看 worker 启动时的配置信息（从日志）
tail -50 /docker-data/logs/rabbitmq_worker.log | grep -i "rabbitmq\|实际使用"
```

**预期结果**:
- ✅ **环境变量正确**: 显示 `amqp://guest:guest@host.docker.internal:5672/`
- ✅ **配置文件**: 显示正确的配置或注释掉（使用环境变量）
- ❌ **地址错误**: 显示 `localhost` 或 `127.0.0.1`，说明环境变量未生效

---

### 步骤 7: 手动测试 RabbitMQ 连接

**在容器内执行**:

```bash
# 使用 Python 测试 RabbitMQ 连接
python3 << 'EOF'
import pika
import os

# 获取 RabbitMQ URL
rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
print(f"测试连接: {rabbitmq_url}")

try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print("✅ 连接成功！")
    connection.close()
except Exception as e:
    print(f"❌ 连接失败: {e}")
EOF
```

**预期结果**:
- ✅ **连接成功**: 显示 "连接成功！"
- ❌ **连接失败**: 显示错误信息，根据错误信息进一步排查

---

### 步骤 8: 重新启动容器以应用环境变量

**如果修改了 .env_local 文件，需要重新启动容器**:

```powershell
# 停止容器
docker stop indextts-deplyment

# 重新启动容器（会重新加载环境变量）
.\docker\run_docker_win11.ps1
```

---

## 常见问题解决方案

### 问题 1: 环境变量未设置

**解决方案**:
1. 在 `docker-data/.env_local` 中添加:
   ```bash
   RABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/
   ```
2. 重新启动容器

### 问题 2: host.docker.internal 无法解析

**解决方案**:
1. 检查 Docker Desktop 是否运行
2. 检查 `docker run` 命令中是否有 `--add-host host.docker.internal:host-gateway`
3. 如果使用 Linux 原生 Docker，可能需要使用宿主机的实际 IP 地址

### 问题 3: RabbitMQ 服务未运行

**解决方案**:
1. 启动 RabbitMQ 服务
2. 检查防火墙设置，确保端口 5672 可访问
3. 如果使用 Docker 运行 RabbitMQ，确保容器正在运行

### 问题 4: 端口被占用或无法访问

**解决方案**:
1. 检查是否有其他程序占用端口 5672
2. 检查防火墙规则
3. 尝试使用不同的端口

### 问题 5: 用户名密码错误

**解决方案**:
1. 检查 RabbitMQ 的用户名和密码
2. 确认 URL 格式正确: `amqp://用户名:密码@主机:端口/`
3. 测试连接时使用正确的凭据

---

## 快速排查命令集合

**在 PowerShell 中一键执行所有检查**:

```powershell
Write-Host "=== RabbitMQ 连接问题排查 ===" -ForegroundColor Cyan
Write-Host ""

Write-Host "1. 检查环境变量文件..." -ForegroundColor Yellow
if (Test-Path "docker-data\.env_local") {
    $rabbitmq = Get-Content "docker-data\.env_local" | Select-String -Pattern "RABBITMQ_URL"
    if ($rabbitmq) {
        Write-Host "  ✓ 找到 RABBITMQ_URL 配置" -ForegroundColor Green
        Write-Host "    $rabbitmq" -ForegroundColor Gray
    } else {
        Write-Host "  ✗ 未找到 RABBITMQ_URL 配置" -ForegroundColor Red
    }
} else {
    Write-Host "  ✗ .env_local 文件不存在" -ForegroundColor Red
}

Write-Host ""
Write-Host "2. 检查容器内环境变量..." -ForegroundColor Yellow
$envVar = docker exec indextts-deplyment printenv RABBITMQ_URL 2>$null
if ($envVar) {
    Write-Host "  ✓ 环境变量已设置: $envVar" -ForegroundColor Green
} else {
    Write-Host "  ✗ 环境变量未设置" -ForegroundColor Red
}

Write-Host ""
Write-Host "3. 检查 RabbitMQ 服务..." -ForegroundColor Yellow
$service = Get-Service | Where-Object {$_.Name -like "*rabbitmq*"} | Select-Object -First 1
if ($service) {
    Write-Host "  ✓ RabbitMQ 服务状态: $($service.Status)" -ForegroundColor $(if ($service.Status -eq 'Running') {'Green'} else {'Red'})
} else {
    Write-Host "  ⚠️  未找到 RabbitMQ Windows 服务（可能使用 Docker 运行）" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "4. 检查端口 5672..." -ForegroundColor Yellow
$port = Test-NetConnection -ComputerName localhost -Port 5672 -WarningAction SilentlyContinue
if ($port.TcpTestSucceeded) {
    Write-Host "  ✓ 端口 5672 可访问" -ForegroundColor Green
} else {
    Write-Host "  ✗ 端口 5672 不可访问" -ForegroundColor Red
}

Write-Host ""
Write-Host "5. 检查容器日志..." -ForegroundColor Yellow
$logs = docker logs indextts-deplyment 2>&1 | Select-String -Pattern "RABBITMQ|rabbitmq|ConnectionError" | Select-Object -Last 5
if ($logs) {
    Write-Host "  最近的 RabbitMQ 相关日志:" -ForegroundColor Gray
    $logs | ForEach-Object { Write-Host "    $_" -ForegroundColor Gray }
} else {
    Write-Host "  ⚠️  未找到相关日志" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "=== 排查完成 ===" -ForegroundColor Cyan
```

---

## 修复建议

根据排查结果，按以下顺序修复：

1. **如果环境变量未设置**:
   ```powershell
   Add-Content -Path "docker-data\.env_local" -Value "`n# RabbitMQ配置`nRABBITMQ_URL=amqp://guest:guest@host.docker.internal:5672/" -Encoding UTF8
   ```

2. **如果 RabbitMQ 服务未运行**:
   ```powershell
   # 启动 RabbitMQ 服务（根据实际安装方式）
   Start-Service RabbitMQ
   # 或
   docker start rabbitmq-container
   ```

3. **如果容器未加载环境变量**:
   ```powershell
   # 重新启动容器
   docker stop indextts-deplyment
   .\docker\run_docker_win11.ps1
   ```

4. **如果网络连接问题**:
   - 检查 Docker Desktop 网络设置
   - 确认 `--add-host host.docker.internal:host-gateway` 参数已添加
   - 尝试使用宿主机的实际 IP 地址

---

## 验证修复

修复后，验证连接：

```powershell
# 进入容器测试
docker exec -it indextts-deplyment bash

# 在容器内执行
python3 << 'EOF'
import pika
import os
rabbitmq_url = os.environ.get("RABBITMQ_URL", "amqp://guest:guest@host.docker.internal:5672/")
print(f"连接地址: {rabbitmq_url}")
try:
    params = pika.URLParameters(rabbitmq_url)
    connection = pika.BlockingConnection(params)
    print("✅ 连接成功！")
    connection.close()
except Exception as e:
    print(f"❌ 连接失败: {e}")
EOF
```

如果显示 "✅ 连接成功！"，说明问题已解决。

