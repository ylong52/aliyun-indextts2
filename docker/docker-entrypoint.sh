#!/bin/bash
set -e

# 进入挂载的代码目录并运行 worker（使用 exec 使 python 成为 PID 1）
cd /python_indextts_code || { echo "ERROR: /python_indextts_code not found"; exec "$@"; }
exec python indextts/app/rabbitmq_worker.py
