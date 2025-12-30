#
# 最小化镜像：不内置 Python 依赖，全部依赖从宿主挂载（/opt/python/shared/packages）
#
ARG PYTHON_VERSION=3.11-slim
FROM python:${PYTHON_VERSION}

# 安装运行时必需的系统依赖（音频处理等）
RUN apt-get update && apt-get install -y --no-install-recommends \
    libsndfile1 \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# 运行目录（容器主目录），运行时通过 -w 参数或 docker-entrypoint.sh 设置
WORKDIR /
# 创建必要的目录结构（代码和 checkpoints 通过卷挂载，不包含在镜像中）
RUN mkdir -p \
    && rm -rf /usr/local/lib/python3.11/test \
           /usr/local/lib/python3.11/idlelib \
           /usr/local/lib/python3.11/turtledemo \
           /usr/local/lib/python3.11/tkinter \
           /usr/local/lib/python3.11/lib2to3 \
           /usr/local/lib/python3.11/distutils \
           /usr/local/lib/python3.11/ensurepip \
           /usr/local/lib/python3.11/pydoc_data 2>/dev/null || true \
    && rm -rf /root/.cache/pip /root/.cache/* \
    && rm -rf /var/log/* /var/cache/* /tmp/* /var/tmp/*

# 在容器启动时确保 indextts/utils/xtransformers.py 存在（兼容挂载代码缺失该文件的情况）
# 这里仍然以 root 身份写入 /usr/local/bin，避免权限问题
RUN mkdir -p /usr/local/bin && cat <<'PY' > /usr/local/bin/ensure_xtransformers.py
import os, textwrap
target = "/python_indextts_code/indextts/utils/xtransformers.py"
os.makedirs(os.path.dirname(target), exist_ok=True)
if not os.path.exists(target):
    shim = textwrap.dedent("""
    import warnings
    try:
        from xformers.components.attention import RelativePositionBias  # type: ignore
    except Exception:
        import torch

        class RelativePositionBias(torch.nn.Module):
            \"\"\"简易占位实现，若缺少 xformers 则不添加相对位置偏置\"\"\"
            def __init__(self, *args, **kwargs):
                super().__init__()
                warnings.warn("xformers 未安装，使用简化的 RelativePositionBias，占位实现不添加相对位置偏置")

            def forward(self, attn):
                return attn
    """).strip() + "\n"
    with open(target, "w", encoding="utf-8") as f:
        f.write(shim)
PY

# 启动脚本现在从 OSS 的 docker-data/docker-entrypoint.sh 挂载获取
 

RUN mkdir -p /usr/local/bin && cat <<'SH' > /usr/local/bin/start.sh && chmod +x /usr/local/bin/start.sh
#!/bin/bash
set -e
python /usr/local/bin/ensure_xtransformers.py

# Run startup validations if present and executable (don't fail startup if missing)
if [ -x /usr/local/bin/validate_startup.sh ]; then
    /usr/local/bin/validate_startup.sh
else
    echo "validate_startup.sh not found or not executable, skipping validation"
fi

# If external /docker-data/docker-entrypoint.sh exists and is executable, run it.
# Otherwise fall back to executing the provided command arguments.
if [ -x /docker-data/docker-entrypoint.sh ]; then
    exec /docker-data/docker-entrypoint.sh "$@"
else
    echo "/docker-entrypoint.sh not found or not executable, executing provided command"
    exec "$@"
fi
SH

# 创建必要的目录结构（使用 root 用户，避免 Windows 挂载目录的权限问题）
RUN mkdir -p /opt/python/shared/packages /opt/python/shared/cache /var/log/app /opt/venv/app

# 可选：构建时预装依赖（默认不执行），并强制显示 pip 进度条
# 通过 --build-arg INSTALL_DEPS=true 启用；依赖安装到 /opt/python/shared/packages（与运行时挂载位置一致）
ARG INSTALL_DEPS=false

# 设置环境变量（RABBITMQ_URL 留空，运行时由 .env/.env_dev 提供
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    NUMBA_CACHE_DIR=/tmp/numba-cache \
    MPLCONFIGDIR=/tmp/matplotlib \
    APP_NAME=indextts_app \
    MPLBACKEND=Agg \
    MATPLOTLIB_DISABLE_CACHE=1

# 确保缓存目录可写
RUN mkdir -p /tmp/numba-cache /tmp/matplotlib && chmod 777 /tmp/numba-cache /tmp/matplotlib

# 默认启动命令（保持最小化，进入容器后由用户手动操作）
# ENTRYPOINT 用于指定容器启动时默认执行的命令
# 取消注释后，容器启动会先运行 /usr/local/bin/start.sh 脚本
ENTRYPOINT ["/usr/local/bin/start.sh"]
CMD ["tail", "-f", "/dev/null"]
