# 运行时间：常驻运行，消费RabbitMQ的tts_jobs队列 
#  主要功能：从RabbitMQ消费TTS任务消息，执行IndexTTS2语音合成推理（支持本地文件/API两种文本获取模式），生成音频文件，完成后发布tts_done事件到tts_done_exchange 
# 输出结果：音频文件(.wav)保存到output_dir，发布tts_done事件到RabbitMQ exchange

"""RabbitMQ consumer that drives IndexTTS2 inference jobs."""

from __future__ import annotations

import argparse
import contextlib
import json
import logging
import os
import socket
import sys
import threading
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any
from urllib.parse import urljoin, urlparse

import pika
from pika import PlainCredentials
from pika.connection import ConnectionParameters
from pika.exceptions import StreamLostError, AMQPConnectionError
import requests
import yaml
import traceback


PROJECT_ROOT = Path(__file__).resolve().parents[2]

# 依赖路径设置（必须在 sys.path 操作之前）
default_packages = "/opt/python/shared/packages"
packages_path = os.environ.get("EXTERNAL_PACKAGES", default_packages)

# 设置依赖路径到 sys.path（确保在最前面，避免路径冲突）
if packages_path and os.path.exists(packages_path):
    # 确保依赖路径在最前面
    if packages_path not in sys.path:
        sys.path.insert(0, packages_path)
    elif sys.path[0] != packages_path:
        sys.path.remove(packages_path)
        sys.path.insert(0, packages_path)

# 添加项目根目录到 sys.path（在依赖路径之后）
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(1, str(PROJECT_ROOT))

# 调试信息：在导入重库之前先输出基本信息
print("=" * 60)
print("RabbitMQ Worker 启动调试信息:")
print(f"  工作目录: {os.getcwd()}")
print(f"  Python 版本: {sys.version}")
print(f"  Python 路径: {sys.executable}")
print(f"  项目根目录: {PROJECT_ROOT}")
print(f"  PYTHONPATH: {os.environ.get('PYTHONPATH', '未设置')}")
print(f"  依赖包路径: {packages_path}")
print(f"  sys.path[0] (优先路径): {sys.path[0]}")

# Checkpoints 路径：统一使用项目内的 python_indextts_code/checkpoints 目录
checkpoints_path = PROJECT_ROOT / "checkpoints"
# Ensure the project checkpoints directory exists (create if missing)
try:
    checkpoints_path.mkdir(parents=True, exist_ok=True)
except Exception:
    # 如果创建失败，后续会在使用路径时回退到基准路径
    pass

# 检查依赖包路径
if os.path.exists(packages_path):
    print(f"    ✓ 依赖包路径存在: {packages_path}")
    # 检查 PyTorch 目录
    torch_path = os.path.join(packages_path, "torch")
    if os.path.exists(torch_path):
        print(f"    ✓ PyTorch 目录存在: {torch_path}")
        # 检查关键文件大小
        torch_init = os.path.join(torch_path, "__init__.py")
        if os.path.exists(torch_init):
            size = os.path.getsize(torch_init)
            print(f"    ✓ torch/__init__.py 存在 ({size} bytes)")
    else:
        print(f"    ✗ PyTorch 目录不存在: {torch_path}")
else:
    print(f"    ✗ 依赖包路径不存在: {packages_path}")

# 检查 Checkpoints 路径
print(f"  Checkpoints 路径: {checkpoints_path}")
if checkpoints_path.exists():
    print(f"    ✓ Checkpoints 目录存在")
    # 检查关键模型文件
    key_files = ["gpt.pth", "s2mel.pth", "config.yaml"]
    for key_file in key_files:
        file_path = checkpoints_path / key_file
        if file_path.exists():
            size_mb = file_path.stat().st_size / (1024 * 1024)
            print(f"    ✓ {key_file} 存在 ({size_mb:.1f} MB)")
        else:
            print(f"    ✗ {key_file} 不存在")
else:
    print(f"    ✗ Checkpoints 目录不存在: {checkpoints_path}")

print("=" * 60)
print("正在导入依赖库...")
print("  1. 检查基础库...")

# 检查并下载 HuggingFace 模型（如果需要）
def check_and_download_hf_models():
    """检查并下载缺失的 HuggingFace 模型"""
    try:
        from huggingface_hub import snapshot_download
    except ImportError:
        print("  ⚠️  huggingface_hub 未安装，跳过模型下载检查")
        print("     提示: 如果模型缺失，请手动安装: pip install huggingface_hub")
        return
    
    # 确定 HF 模型根目录（与 infer_v2.py 逻辑一致）
    hf_root_env = os.environ.get("HF_LOCAL_ROOT")
    if hf_root_env:
        hf_root = Path(hf_root_env)
    else:
        # 按优先级检查候选目录（优先使用 docker-data，保持代码目录干净）
        candidates = [
            Path("/docker-data/hf_models"),  # 优先使用 docker-data，与代码目录分离
            Path("/indexttsData/models/hf_models"),
            Path("/indexttsData/code/hf_models"),
            PROJECT_ROOT / "hf_models",  # 兼容旧路径
        ]
        hf_root = None
        for candidate in candidates:
            if candidate.exists():
                hf_root = candidate
                break
        if hf_root is None:
            # 默认使用 docker-data/hf_models，保持代码目录干净
            hf_root = Path("/docker-data/hf_models")
    
    # 确保根目录存在
    hf_root.mkdir(parents=True, exist_ok=True)
    print(f"  HF 模型根目录: {hf_root}")
    
    # 需要下载的模型列表（模型名, repo_id, 必需文件列表）
    models_to_check = [
        ("facebook/w2v-bert-2.0", "facebook/w2v-bert-2.0", ["preprocessor_config.json"]),
        ("amphion/MaskGCT", "amphion/MaskGCT", ["semantic_codec/model.safetensors"]),
        ("funasr/campplus", "funasr/campplus", ["campplus_cn_common.bin"]),
        ("nvidia/bigvgan_v2_22khz_80band_256x", "nvidia/bigvgan_v2_22khz_80band_256x", ["config.json"]),
    ]
    
    # 检查每个模型
    need_download = []
    for model_name, repo_id, required_files in models_to_check:
        model_dir = hf_root / repo_id
        
        # 检查模型是否完整
        is_complete = True
        missing_files = []
        
        if not model_dir.exists():
            is_complete = False
        else:
            # 检查必需文件是否存在
            for required_file in required_files:
                file_path = model_dir / required_file
                if not file_path.exists():
                    is_complete = False
                    missing_files.append(required_file)
        
        if is_complete:
            print(f"    ✓ {model_name} 已存在且完整")
        else:
            if model_dir.exists():
                print(f"    ⚠️  {model_name} 存在但不完整（缺少: {', '.join(missing_files)}），将重新下载")
                # 删除不完整的目录
                try:
                    import shutil
                    shutil.rmtree(model_dir)
                    print(f"      已删除不完整的目录: {model_dir}")
                except Exception as e:
                    print(f"      删除目录失败: {e}，将尝试覆盖下载")
            else:
                print(f"    ✗ {model_name} 不存在")
            need_download.append((model_name, repo_id, model_dir, required_files))
    
    if not need_download:
        print("  ✓ 所有 HuggingFace 模型已就绪")
        return
    
    print(f"  ⚠️  发现 {len(need_download)} 个缺失或不完整的模型，开始下载...")
    print("     提示: 首次下载可能需要较长时间，请耐心等待...")
    
    # 下载缺失的模型
    for model_name, repo_id, model_dir, required_files in need_download:
        print(f"    ↓ 正在下载 {model_name}...")
        try:
            model_dir.mkdir(parents=True, exist_ok=True)
            snapshot_download(
                repo_id=repo_id,
                local_dir=str(model_dir),
                local_dir_use_symlinks=False,
            )
            # 验证下载是否成功
            is_valid = True
            for required_file in required_files:
                if not (model_dir / required_file).exists():
                    is_valid = False
                    break
            
            if is_valid:
                print(f"    ✓ {model_name} 下载完成并验证通过")
            else:
                print(f"    ⚠️  {model_name} 下载完成但验证失败，可能文件不完整")
        except Exception as e:
            print(f"    ✗ {model_name} 下载失败: {e}")
            print(f"      提示: 请检查网络连接或手动下载到: {model_dir}")
            import traceback
            print(f"      详细错误: {traceback.format_exc()}")
            # 继续下载其他模型，不中断流程

print("  2. 检查 HuggingFace 模型...")
check_and_download_hf_models()

# 延迟导入 IndexTTS2，因为它会导入 PyTorch
print("  3. 准备导入 IndexTTS2 (将加载 PyTorch，可能需要较长时间)...")
print("     正在导入，请耐心等待...")

# 添加进度提示（使用线程在后台输出进度）
import time
import threading

progress_stop = threading.Event()
def show_progress():
    """显示加载进度"""
    dots = 0
    while not progress_stop.is_set():
        print(f"\r     加载中{'.' * (dots % 4)}{' ' * (3 - dots % 4)}", end="", flush=True)
        dots += 1
        time.sleep(1)

progress_thread = threading.Thread(target=show_progress, daemon=True)
progress_thread.start()

try:
    import_start = time.time()
    from indextts.infer_v2 import IndexTTS2
    import_elapsed = time.time() - import_start
    progress_stop.set()
    progress_thread.join(timeout=1)
    print(f"\r  ✓ IndexTTS2 导入成功 (耗时: {import_elapsed:.1f}秒)")
except ImportError as e:
    progress_stop.set()
    progress_thread.join(timeout=1)
    print(f"\n  ✗ IndexTTS2 导入失败: {e}")
    print("  请检查:")
    print("    - PyTorch 是否正确安装")
    print("    - 依赖包路径是否正确")
    raise
except Exception as e:
    progress_stop.set()
    progress_thread.join(timeout=1)
    print(f"\n  ✗ 导入过程中发生异常: {e}")
    print(f"  异常类型: {type(e).__name__}")
    import traceback
    print("  详细堆栈:")
    traceback.print_exc()
    raise

print("=" * 60)


def _format_duration_hms(seconds: float) -> str:
    """Format a duration as `X小时Y分Z秒` (always includes hours)."""
    total = int(max(0.0, seconds))
    hours = total // 3600
    minutes = (total % 3600) // 60
    secs = total % 60
    return f"{hours}小时{minutes}分{secs}秒"


@contextlib.contextmanager
def log_step(logger: logging.Logger, step_name: str):
    """Log step start/end timestamps and duration."""
    start_wall = datetime.now()
    start_mono = time.monotonic()
    logger.info("[STEP-START] %s | start=%s", step_name, start_wall.strftime("%Y-%m-%d %H:%M:%S"))
    try:
        yield
    finally:
        end_wall = datetime.now()
        elapsed = time.monotonic() - start_mono
        logger.info(
            "[STEP-END] %s | end=%s | duration=%s",
            step_name,
            end_wall.strftime("%Y-%m-%d %H:%M:%S"),
            _format_duration_hms(elapsed),
        )


@dataclass
class AppConfig:
    rabbitmq_url: str                # RabbitMQ 连接URL，例如 "amqp://guest:guest@localhost:5672/"
    queue: str                       # 队列名称，例如 "tts_jobs"
    prefetch_count: int              # 消费端预取的消息数
    done_exchange: str               # 推理完成事件交换机名称
    done_routing_key: str            # 推理完成事件路由键
    error_exchange: str              # 错误事件交换机名称
    error_routing_key: str           # 错误事件路由键
    local_root: Path | None          # 本地文本文件根目录（可选），用于模式为 local 时查找文本
    text_encoding: str               # 本地文本文件的编码方式，例如 "utf-8"
    api_base_url: str | None         # 在线拉取文本的 API 基础地址（可选）
    api_timeout: float               # 拉取 API 文本的超时时间（秒）
    tts_model_dir: Path              # IndexTTS2 模型目录
    tts_config_path: Path            # IndexTTS2 配置文件路径
    default_style_file: Path | None  # 默认风格文件（可选，优先用 Job 提供的）
    output_dir: Path                 # 推理结果音频文件的输出目录
    max_text_tokens_per_segment: int # 每段语音最大文本token数，超过会自动切分
    use_fp16: bool                   # 是否使用 fp16 推理
    use_cuda_kernel: bool            # 是否启用 CUDA kernel 优化
    log_level: str                   # 日志等级（如 "INFO", "DEBUG"）
    create_marker_file: bool         # 是否在首次成功启动时创建标记文件（用于后台运行）

    @classmethod
    def from_file(cls, path: Path) -> "AppConfig":
        if not path.exists():
            raise FileNotFoundError(f"Config file not found: {path}")
        with path.open("r", encoding="utf-8") as fh:
            raw = yaml.safe_load(fh) or {}

        # Resolve relative paths.
        # - If the config file is inside the project repo (common case), treat relative paths as relative to PROJECT_ROOT.
        # - Otherwise, treat them as relative to the config file directory (portable external configs).
        config_abs = path.resolve()
        base_dir = path.parent.resolve()
        try:
            root_for_relative = PROJECT_ROOT if config_abs.is_relative_to(PROJECT_ROOT) else base_dir
        except AttributeError:
            # Python < 3.9 fallback: best-effort check
            try:
                config_abs.relative_to(PROJECT_ROOT)
                root_for_relative = PROJECT_ROOT
            except Exception:
                root_for_relative = base_dir

        def _resolve_path(value: Any) -> str:
            """Resolve a possibly-relative path-like value to an absolute path string."""
            p = str(value)
            if os.path.isabs(p):
                return p
            # 特殊处理：将 "checkpoints" 路径解析为项目内的 checkpoints 目录
            if p.startswith("checkpoints") or p == "checkpoints":
                project_checkpoints = PROJECT_ROOT / "checkpoints"
                # Ensure project checkpoints exists; create if missing
                try:
                    project_checkpoints.mkdir(parents=True, exist_ok=True)
                except Exception:
                    # If creation fails, fall back to project path resolution
                    return str((root_for_relative / p).resolve())

                # 如果路径是 "checkpoints" 或 "checkpoints/xxx"，替换为 <project>/checkpoints/xxx
                if p == "checkpoints":
                    return str(project_checkpoints)
                elif p.startswith("checkpoints/"):
                    relative_part = p[len("checkpoints/"):]
                    return str(project_checkpoints / relative_part)
                else:
                    # 处理其他情况（理论上不应该发生）
                    relative_part = p.replace("checkpoints", "").lstrip("/")
                    return str(project_checkpoints / relative_part) if relative_part else str(project_checkpoints)
            return str((root_for_relative / p).resolve())

        rabbitmq = raw.get("rabbitmq", {})
        text_cfg = raw.get("text", {})
        api_cfg = raw.get("api", {})
        tts_cfg = raw.get("tts", {})
        logging_cfg = raw.get("logging", {})

        local_root = text_cfg.get("local_root")
        if local_root:
            local_root = _resolve_path(local_root)

        # 默认模型目录：统一使用项目内的 checkpoints 目录
        default_model_dir = str(PROJECT_ROOT / "checkpoints")
        default_config_path = str(PROJECT_ROOT / "checkpoints" / "config.yaml")

        # Ensure chosen checkpoints directory exists (create if missing)
        try:
            Path(default_model_dir).mkdir(parents=True, exist_ok=True)
        except Exception:
            raise RuntimeError(f"无法创建目录: {default_model_dir}")
        
        tts_model_dir = _resolve_path(tts_cfg.get("model_dir", default_model_dir))
        tts_config_path = _resolve_path(tts_cfg.get("config_path", default_config_path))

        style_file = tts_cfg.get("style_file")
        if style_file:
            style_file = _resolve_path(style_file)

        output_dir = _resolve_path(tts_cfg.get("output_dir", "rabbitmq_outputs"))

        return cls(
            rabbitmq_url=os.environ.get(
                "RABBITMQ_URL",
                rabbitmq.get("url", "amqp://guest:guest@host.docker.internal:5672/")
            ),
            queue=rabbitmq.get("queue", "tts_jobs"),
            prefetch_count=int(rabbitmq.get("prefetch_count", 1)),
            done_exchange=rabbitmq.get("done_exchange", "tts_done_exchange"),
            done_routing_key=rabbitmq.get("done_routing_key", "tts.done"),
            error_exchange=rabbitmq.get("error_exchange", "tts_error_exchange"),
            error_routing_key=rabbitmq.get("error_routing_key", "tts.error"),
            local_root=Path(local_root).resolve() if local_root else None,
            text_encoding=text_cfg.get("encoding", "utf-8"),
            api_base_url=api_cfg.get("base_url"),
            api_timeout=float(api_cfg.get("timeout", 10)),
            tts_model_dir=Path(tts_model_dir),
            tts_config_path=Path(tts_config_path),
            default_style_file=Path(style_file).resolve()
            if style_file
            else None,
            output_dir=Path(output_dir),
            max_text_tokens_per_segment=int(
                tts_cfg.get("max_text_tokens_per_segment", 160)
            ),
            use_fp16=bool(tts_cfg.get("use_fp16", False)),
            use_cuda_kernel=bool(tts_cfg.get("use_cuda_kernel", False)),
            log_level=str(logging_cfg.get("level", "INFO")).upper(),
            create_marker_file=bool(tts_cfg.get("create_marker_file", True)),
        )


@dataclass
class TTSJob:
    text_path: str
    user_id: str
    record_id: str
    timestamp: str
    mode: str
    style_file: str | None

    @classmethod
    def from_message(cls, payload: dict[str, Any]) -> "TTSJob":
        required = ["text_path", "user_id", "record_id", "timestamp", "mode", "style_file"]
        missing = [key for key in required if key not in payload]
        if missing:
            raise ValueError(f"Missing required fields: {missing}")
        mode = str(payload["mode"]).lower()
        if mode not in {"local", "api"}:
            raise ValueError(f"Unsupported mode: {mode}")
        return cls(
            text_path=str(payload["text_path"]),
            user_id=str(payload["user_id"]),
            record_id=str(payload["record_id"]),
            timestamp=str(payload["timestamp"]),
            mode=mode,
            style_file=str(payload.get("style_file"))
            if payload.get("style_file") is not None
            else None,
        )


class TTSRunner:
    def __init__(self, cfg: AppConfig) -> None:
        self.cfg = cfg
        self._logger = logging.getLogger(self.__class__.__name__)
        self._tts = self._load_tts()

    def _load_tts(self) -> IndexTTS2:
        if not self.cfg.tts_config_path.exists():
            raise FileNotFoundError(f"TTS config not found: {self.cfg.tts_config_path}")
        
        self._logger.info("=" * 60)
        self._logger.info("准备加载 IndexTTS2 模型:")
        self._logger.info("  配置文件: %s", self.cfg.tts_config_path)
        self._logger.info("  模型目录: %s", self.cfg.tts_model_dir)
        
        # 检查关键模型文件
        model_files = {
            "gpt.pth": self.cfg.tts_model_dir / "gpt.pth",
            "s2mel.pth": self.cfg.tts_model_dir / "s2mel.pth",
        }
        
        for name, path in model_files.items():
            if path.exists():
                size_mb = path.stat().st_size / (1024 * 1024)
                self._logger.info("  ✓ %s 存在 (%s MB)", name, f"{size_mb:.1f}")
            else:
                self._logger.error("  ✗ %s 不存在: %s", name, path)
                raise FileNotFoundError(f"Model file not found: {path}")
        
        self.cfg.output_dir.mkdir(parents=True, exist_ok=True)
        self._logger.info("  开始加载模型...")
        self._logger.info("=" * 60)
        
        try:
            with log_step(self._logger, "IndexTTS2: 初始化模型 (加载 checkpoints)"):
                tts = IndexTTS2(
                    cfg_path=str(self.cfg.tts_config_path),
                    model_dir=str(self.cfg.tts_model_dir),
                    use_fp16=self.cfg.use_fp16,
                    use_cuda_kernel=self.cfg.use_cuda_kernel,
                )
            self._logger.info("✓ IndexTTS2 模型加载成功")
            return tts
        except Exception as e:
            self._logger.error("✗ IndexTTS2 模型加载失败: %s", e)
            self._logger.error("  可能原因:")
            self._logger.error("    - 文件权限问题")
            self._logger.error("    - 模型文件损坏或不完整")
            self._logger.error("    - 模型文件路径不正确")
            raise

    def _build_output_path(self, job: TTSJob) -> Path:
        timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
        filename = f"{job.record_id}_{timestamp}.wav"
        return self.cfg.output_dir / filename

    def _resolve_style_file(self, style_file: str | None) -> Path:
        if style_file:
            path = Path(style_file)
            if not path.is_absolute():
                path = (PROJECT_ROOT / path).resolve()
        elif self.cfg.default_style_file:
            path = self.cfg.default_style_file
        else:
            raise ValueError("style_file is missing in job payload and no default is configured")
        if not path.exists():
            raise FileNotFoundError(f"Style file not found: {path}")
        return path

    def synthesize(self, text: str, job: TTSJob) -> Path:
        output_path = self._build_output_path(job)
        style_path = self._resolve_style_file(job.style_file)
        self._logger.info(
            "Generating speech for record_id=%s user_id=%s mode=%s style=%s",
            job.record_id,
            job.user_id,
            job.mode,
            style_path,
        )
        with log_step(self._logger, f"TTS 推理: record_id={job.record_id}"):
            self._tts.infer(
                spk_audio_prompt=str(style_path),
                text=text,
                output_path=str(output_path),
                emo_audio_prompt=None,
                emo_alpha=1.0,
                do_sample=True,
                temperature=0.8,
                top_p=0.8,
                top_k=30,
                num_beams=3,
                repetition_penalty=10.0,
                length_penalty=0.0,
                max_mel_tokens=1500,
                max_text_tokens_per_segment=self.cfg.max_text_tokens_per_segment,
                verbose=False,
            )
        self._logger.info("Audio saved to %s", output_path.resolve())
        return output_path


class JobProcessor:
    def __init__(self, cfg: AppConfig, tts_runner: TTSRunner) -> None:
        self.cfg = cfg
        self.tts_runner = tts_runner
        self._logger = logging.getLogger(self.__class__.__name__)

    def _resolve_local_path(self, text_path: str) -> Path:
        candidate = Path(text_path)
        if candidate.is_absolute():
            return candidate
        if self.cfg.local_root:
            return self.cfg.local_root / candidate
        return candidate

    def _read_local_text(self, job: TTSJob) -> str:
        path = self._resolve_local_path(job.text_path)
        if not path.exists():
            raise FileNotFoundError(f"Text file not found: {path}")
        return path.read_text(encoding=self.cfg.text_encoding)

    def _fetch_remote_text(self, job: TTSJob) -> str:
        if job.text_path.startswith("http://") or job.text_path.startswith("https://"):
            url = job.text_path
        elif self.cfg.api_base_url:
            url = urljoin(self.cfg.api_base_url.rstrip("/") + "/", job.text_path.lstrip("/"))
        else:
            raise ValueError("API base URL is not configured for remote mode")
        response = requests.get(url, timeout=self.cfg.api_timeout)
        response.raise_for_status()
        return response.text

    def _load_text(self, job: TTSJob) -> str:
        if job.mode == "local":
            return self._read_local_text(job)
        return self._fetch_remote_text(job)

    def handle_job(self, payload: bytes) -> dict[str, Any]:
        data = json.loads(payload.decode("utf-8"))
        job = TTSJob.from_message(data)

        with log_step(self._logger, f"Job: 加载文本 record_id={job.record_id} mode={job.mode}"):
            text = self._load_text(job)
            if not text.strip():
                raise ValueError("Fetched text is empty")

        audio_path = self.tts_runner.synthesize(text, job)
        
        # 使用本地路径作为 audio_url
        try:
            rel_path = audio_path.relative_to(self.cfg.output_dir)
        except ValueError:
            rel_path = audio_path.name
        audio_url = str(rel_path).replace("\\", "/")
        
        # 构造 tts_done 事件，供二级通知队列使用
        event = {
            "event": "tts_done",
            "record_id": job.record_id,
            "user_id": job.user_id,
            "audio_url": audio_url,
            "timestamp": job.timestamp,
            "meta": {
                "mode": job.mode,
                "text_path": job.text_path,
                "style_file": job.style_file,
            },
        }
        return event


class RabbitMQWorker:
    def __init__(self, cfg: AppConfig, processor: JobProcessor) -> None:
        self.cfg = cfg
        self.processor = processor
        self._logger = logging.getLogger(self.__class__.__name__)
        self._connection: pika.BlockingConnection | None = None
        self._channel = None
        self._processing_count = 0
        self._processing_lock = threading.Lock()

    def _test_network_connectivity(self, host: str, port: int, timeout: float = 5.0) -> bool:
        """测试网络连通性"""
        try:
            self._logger.info("🔍 测试网络连通性: %s:%d (超时: %.1f秒)", host, port, timeout)
            sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            sock.settimeout(timeout)
            result = sock.connect_ex((host, port))
            sock.close()
            if result == 0:
                self._logger.info("✅ 网络连通性测试通过: %s:%d 可达", host, port)
                return True
            else:
                error_msg_map = {
                    10061: "连接被拒绝（目标主机未运行服务或端口未开放）",
                    10060: "连接超时（网络不通或防火墙阻止）",
                    10051: "网络不可达（路由问题）",
                    11001: "主机名解析失败",
                }
                error_desc = error_msg_map.get(result, f"未知错误码: {result}")
                self._logger.error("❌ 网络连通性测试失败:")
                self._logger.error("  🌐 目标地址: %s:%d", host, port)
                self._logger.error("  ⚠️  错误码: %d", result)
                self._logger.error("  📝 错误说明: %s", error_desc)
                return False
        except socket.gaierror as e:
            self._logger.error("❌ DNS 解析失败:")
            self._logger.error("  🌐 主机名: %s", host)
            self._logger.error("  📝 错误信息: %s", e)
            self._logger.error("  💡 提示: 请检查主机名是否正确，或使用 IP 地址")
            return False
        except Exception as e:
            self._logger.error("❌ 网络测试异常:")
            self._logger.error("  🌐 目标地址: %s:%d", host, port)
            self._logger.error("  📝 错误信息: %s", e)
            self._logger.error("  🔍 异常类型: %s", type(e).__name__)
            return False

    def _connect(self) -> None:
        with log_step(self._logger, "RabbitMQ: 建立连接并声明队列/交换机"):
        # 调试信息：打印将要使用的 RabbitMQ URL
            self._logger.info("=" * 60)
            self._logger.info("RabbitMQ 连接调试信息:")
            self._logger.info("  配置文件中的 rabbitmq_url: %s", self.cfg.rabbitmq_url)
            self._logger.info("  队列名称: %s", self.cfg.queue)
            self._logger.info("  预取数量: %d", self.cfg.prefetch_count)
        
        # 解析 URL 并测试网络连通性
            try:
                parsed = urlparse(self.cfg.rabbitmq_url)
                host = parsed.hostname
                port = parsed.port or 5672  # RabbitMQ 默认端口
            
                self._logger.info("  解析后的主机: %s", host)
                self._logger.info("  解析后的端口: %d", port)
            
                if host:
                    self._logger.info("  正在测试网络连通性...")
                    if not self._test_network_connectivity(host, port, timeout=5.0):
                        self._logger.error("=" * 60)
                        self._logger.error("❌ ❌ ❌ RabbitMQ 网络连通性测试失败！❌ ❌ ❌")
                        self._logger.error("")
                        self._logger.error("📌 连接信息:")
                        self._logger.error("  🔗 完整 URL: %s", self.cfg.rabbitmq_url)
                        self._logger.error("  🌐 主机地址: %s", host)
                        self._logger.error("  🔌 端口号: %d", port)
                        self._logger.error("")
                        self._logger.error("🔍 请检查以下项目:")
                        self._logger.error("  1️⃣  RabbitMQ 服务器是否正在运行")
                        self._logger.error("  2️⃣  容器网络是否能访问 %s:%d", host, port)
                        self._logger.error("  3️⃣  防火墙规则是否允许连接")
                        self._logger.error("  4️⃣  Docker 网络配置是否正确")
                        self._logger.error("  5️⃣  如果使用 host.docker.internal，请确认 Docker Desktop 已运行")
                        self._logger.error("")
                        self._logger.error("💡 排查建议:")
                        self._logger.error("  - 在容器内执行: ping %s", host)
                        self._logger.error("  - 在容器内执行: python3 -c \"import socket; s = socket.socket(); s.settimeout(2); result = s.connect_ex(('%s', %d)); print('连接成功' if result == 0 else '连接失败'); s.close()\"", host, host, port)
                        self._logger.error("=" * 60)
                        raise ConnectionError(f"❌ 无法连接到 RabbitMQ 服务器 {host}:{port}")
            except Exception as e:
                self._logger.error("=" * 60)
                self._logger.error("❌ ❌ ❌ RabbitMQ URL 解析失败！❌ ❌ ❌")
                self._logger.error("")
                self._logger.error("📌 错误信息:")
                self._logger.error("  %s", e)
                self._logger.error("")
                self._logger.error("📌 尝试解析的 URL:")
                self._logger.error("  🔗 %s", self.cfg.rabbitmq_url)
                self._logger.error("")
                self._logger.error("💡 请检查 URL 格式是否正确:")
                self._logger.error("  正确格式: amqp://用户名:密码@主机:端口/虚拟主机")
                self._logger.error("  示例: amqp://guest:guest@host.docker.internal:5672/")
                self._logger.error("=" * 60)
                raise
        
            self._logger.info("=" * 60)
        
            self._logger.info("正在尝试连接到 RabbitMQ...")
            try:
                # 解析 URL 获取连接参数
                parsed = urlparse(self.cfg.rabbitmq_url)
                host = parsed.hostname or "localhost"
                port = parsed.port or 5672
                username = parsed.username or "guest"
                password = parsed.password or "guest"
                virtual_host = parsed.path.lstrip("/") or "/"
            
                # 使用 ConnectionParameters 以便设置超时
                params = ConnectionParameters(
                    host=host,
                    port=port,
                    virtual_host=virtual_host,
                    credentials=PlainCredentials(username, password),
                    heartbeat=0,  # 长耗时推理期间会阻塞心跳，关闭心跳以避免 10054 连接重置
                    blocked_connection_timeout=300,  # 5分钟超时
                    socket_timeout=10,  # 10秒 socket 连接超时
                )
            
                self._logger.info("连接参数: host=%s, port=%d, vhost=%s, socket_timeout=10s", 
                                host, port, virtual_host)
                self._logger.info("正在建立连接（最多等待 10 秒）...")
            
                self._connection = pika.BlockingConnection(params)
                self._logger.info("✓ RabbitMQ 连接成功")
                self._channel = self._connection.channel()
                self._logger.info("✓ RabbitMQ 通道创建成功")
            except socket.timeout as e:
                self._logger.error("=" * 60)
                self._logger.error("❌ ❌ ❌ RabbitMQ 连接超时！❌ ❌ ❌")
                self._logger.error("")
                self._logger.error("📌 连接信息:")
                self._logger.error("  🔗 完整 URL: %s", self.cfg.rabbitmq_url)
                self._logger.error("  🌐 主机地址: %s", host)
                self._logger.error("  🔌 端口号: %d", port)
                self._logger.error("  👤 用户名: %s", username)
                self._logger.error("  🔑 虚拟主机: %s", virtual_host)
                self._logger.error("  ⏱️  超时时间: 10 秒")
                self._logger.error("")
                self._logger.error("📌 错误详情:")
                self._logger.error("  %s", e)
                self._logger.error("")
                self._logger.error("🔍 可能的原因:")
                self._logger.error("  1️⃣  RabbitMQ 服务器未启动或未响应")
                self._logger.error("  2️⃣  网络连接问题（防火墙、路由等）")
                self._logger.error("  3️⃣  主机地址无法解析（DNS 问题）")
                self._logger.error("  4️⃣  端口被占用或不可访问")
                self._logger.error("")
                self._logger.error("💡 排查建议:")
                self._logger.error("  - 检查 RabbitMQ 服务状态")
                self._logger.error("  - 测试网络连通性: ping %s", host)
                self._logger.error("  - 测试端口连接: telnet %s %d", host, port)
                self._logger.error("=" * 60)
                raise
            except AMQPConnectionError as e:
                self._logger.error("=" * 60)
                self._logger.error("❌ ❌ ❌ RabbitMQ AMQP 连接错误！❌ ❌ ❌")
                self._logger.error("")
                self._logger.error("📌 连接信息:")
                self._logger.error("  🔗 完整 URL: %s", self.cfg.rabbitmq_url)
                self._logger.error("  🌐 主机地址: %s", host)
                self._logger.error("  🔌 端口号: %d", port)
                self._logger.error("  👤 用户名: %s", username)
                self._logger.error("  🔑 虚拟主机: %s", virtual_host)
                self._logger.error("")
                self._logger.error("📌 错误详情:")
                self._logger.error("  %s", e)
                self._logger.error("")
                self._logger.error("🔍 可能的原因:")
                self._logger.error("  1️⃣  用户名或密码错误")
                self._logger.error("  2️⃣  虚拟主机不存在或无权限访问")
                self._logger.error("  3️⃣  RabbitMQ 服务器拒绝连接")
                self._logger.error("  4️⃣  认证配置不正确")
                self._logger.error("")
                self._logger.error("💡 排查建议:")
                self._logger.error("  - 检查用户名和密码是否正确")
                self._logger.error("  - 确认虚拟主机 '%s' 存在", virtual_host)
                self._logger.error("  - 检查 RabbitMQ 用户权限配置")
                self._logger.error("  - 尝试使用默认凭据: guest/guest")
                self._logger.error("=" * 60)
                raise
            except Exception as e:
                self._logger.error("=" * 60)
                self._logger.error("❌ ❌ ❌ RabbitMQ 连接失败（未知错误）！❌ ❌ ❌")
                self._logger.error("")
                self._logger.error("📌 连接信息:")
                self._logger.error("  🔗 完整 URL: %s", self.cfg.rabbitmq_url)
                self._logger.error("  🌐 主机地址: %s", host)
                self._logger.error("  🔌 端口号: %d", port)
                self._logger.error("  👤 用户名: %s", username)
                self._logger.error("  🔑 虚拟主机: %s", virtual_host)
                self._logger.error("")
                self._logger.error("📌 错误详情:")
                self._logger.error("  异常类型: %s", type(e).__name__)
                self._logger.error("  错误信息: %s", e)
                self._logger.error("")
                self._logger.error("📋 详细堆栈:")
                import traceback
                self._logger.error(traceback.format_exc())
                self._logger.error("=" * 60)
                raise
            # 声明主任务队列
            self._channel.queue_declare(queue=self.cfg.queue, durable=True)
            # 声明推理完成事件交换机（fanout，方便多个消费者订阅）
            self._channel.exchange_declare(
                exchange=self.cfg.done_exchange,
                exchange_type="fanout",
                durable=True,
            )
                # 声明错误事件交换机（用于接收处理失败的 job 详情）
                self._channel.exchange_declare(
                    exchange=self.cfg.error_exchange,
                    exchange_type="fanout",
                    durable=True,
                )
            self._channel.basic_qos(prefetch_count=self.cfg.prefetch_count)
            self._logger.info(
                "Connected to RabbitMQ queue=%s prefetch=%d",
                self.cfg.queue,
                self.cfg.prefetch_count,
            )

    def _increment_processing(self) -> None:
        """增加正在处理的任务计数"""
        with self._processing_lock:
            self._processing_count += 1

    def _decrement_processing(self) -> None:
        """减少正在处理的任务计数"""
        with self._processing_lock:
            if self._processing_count > 0:
                self._processing_count -= 1

    def start(self) -> None:
        self._connect()

        def _publish_done_event(event: dict[str, Any]) -> None:
            body = json.dumps(event, ensure_ascii=False).encode("utf-8")
            self._channel.basic_publish(
                exchange=self.cfg.done_exchange,
                routing_key=self.cfg.done_routing_key,
                body=body,
                properties=pika.BasicProperties(delivery_mode=2),
            )
            self._logger.info(
                "Published tts_done event for record_id=%s user_id=%s",
                event.get("record_id"),
                event.get("user_id"),
            )

        def _callback(channel, method, properties, body):
            self._logger.info("Received message delivery_tag=%s", method.delivery_tag)
            
            # 增加正在处理的任务计数
            self._increment_processing()

            try:
                event = self.processor.handle_job(body)
                _publish_done_event(event)
            except Exception as exc:  # noqa: BLE001
                # 捕获处理异常，记录并将错误详情发送到错误交换机，然后确认（移除）原消息，继续处理下一条
                self._logger.exception("Job failed: %s", exc)
                try:
                    # 尝试解析原始消息负载为 JSON；若失败则以原始字符串保存
                    try:
                        payload_obj = json.loads(body.decode("utf-8"))
                    except Exception:
                        payload_obj = {"raw": body.decode("utf-8", errors="replace")}

                    error_event = {
                        "event": "tts_error",
                        "error": str(exc),
                        "traceback": traceback.format_exc(),
                        "payload": payload_obj,
                    }

                    # 发布到错误交换机，供后续人工或自动处理
                    self._channel.basic_publish(
                        exchange=self.cfg.error_exchange,
                        routing_key=self.cfg.error_routing_key,
                        body=json.dumps(error_event, ensure_ascii=False).encode("utf-8"),
                        properties=pika.BasicProperties(delivery_mode=2),
                    )
                    self._logger.info("Published tts_error event for failed job (removed from queue).")
                except Exception as pub_exc:
                    self._logger.exception("Failed to publish error event: %s", pub_exc)

                # 确认消息（移除），不要重入队列，避免重复失败阻塞消费
                try:
                    channel.basic_ack(delivery_tag=method.delivery_tag)
                except Exception as ack_exc:
                    self._logger.warning("Failed to ack failed message: %s", ack_exc)

                # 任务失败也要减少计数
                self._decrement_processing()
                return
            try:
                #调用 basic_ack 确认消息，RabbitMQ 从队列中删除该消息
                channel.basic_ack(delivery_tag=method.delivery_tag)
            except StreamLostError as exc:
                # 连接在长时间推理后已被服务器关闭，避免再次抛出大量堆栈，记录后优雅关闭
                self._logger.error("Connection lost while acking message: %s", exc)
                self._decrement_processing()
                self.stop()
                return
            
            # 任务成功完成，减少计数
            self._decrement_processing()

        self._channel.basic_consume(queue=self.cfg.queue, on_message_callback=_callback)
        
        # 显示成功提示（使用 ANSI 颜色码）
        print("\n" + "=" * 60)
        print("\033[92m✅ ✅ ✅ 系统运行成功！✅ ✅ ✅\033[0m")
        print("\033[92m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m")
        print("\033[92m  🎉 RabbitMQ Worker 已成功启动\033[0m")
        print("\033[92m  📡 正在等待队列消息...\033[0m")
        print("\033[93m  💡 提示：代码运行正常,正在订阅队列  \033[0m")
        print("\033[92m━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\033[0m")
        print("=" * 60 + "\n")
        
        # 直接在前台运行，不创建标记文件（便于调试）
        self._logger.info("Waiting for messages. Press Ctrl+C to exit.")
        
        try:
            self._channel.start_consuming()
        except KeyboardInterrupt:
            self._logger.info("Interrupted by user, shutting down...")
        finally:
            self.stop()

    def stop(self) -> None:
        # 安全地关闭 RabbitMQ 连接，捕获并忽略 pika 库的内部错误
        if self._connection:
            try:
                if not self._connection.is_closed:
                    # 先尝试正常关闭
                    self._connection.close()
                    self._logger.info("RabbitMQ connection closed.")
            except Exception as e:
                # 捕获 pika 库在关闭连接时可能出现的内部错误
                # 这些错误通常是连接已经关闭或正在关闭时的异步操作导致的
                # 可以安全地忽略，因为连接已经或正在关闭
                error_type = type(e).__name__
                if error_type in ('IndexError', 'AssertionError', 'ConnectionClosed', 'StreamLostError'):
                    self._logger.debug("连接关闭过程中的内部错误（可忽略）: %s", e)
                else:
                    self._logger.warning("关闭连接时出现异常: %s", e)
            finally:
                # 确保连接对象被清理
                self._connection = None
                self._channel = None


def configure_logging(level: str) -> None:
    logging.basicConfig(
        level=getattr(logging, level.upper(), logging.INFO),
        format="%(asctime)s | %(levelname)s | %(name)s | %(message)s",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="RabbitMQ worker for IndexTTS2")
    parser.add_argument(
        "--config",
        type=Path,
        default=Path("indextts/app/rabbitmq_config.yaml"),
        help="Path to YAML config file",
    )
    parser.add_argument(
        "--log-level",
        type=str,
        default=None,
        help="Override log level (INFO, DEBUG, ...)",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    
    # 调试信息：检查环境变量 RABBITMQ_URL
    print("=" * 60)
    print("RabbitMQ 环境变量检查:")
    env_rabbitmq_url = os.environ.get("RABBITMQ_URL")
    if env_rabbitmq_url:
        print(f"  ✓ RABBITMQ_URL 环境变量已设置")
        print(f"    值: {env_rabbitmq_url}")
    else:
        print(f"  ✗ RABBITMQ_URL 环境变量未设置")
    print("=" * 60)
    
    cfg = AppConfig.from_file(args.config)
    
    # 配置日志（需要在加载配置后设置，以便使用配置的日志级别）
    configure_logging(args.log_level or cfg.log_level)
    logger = logging.getLogger(__name__)
    
    # 读取配置文件中的原始值（用于对比）
    with open(args.config, "r", encoding="utf-8") as f:
        config_raw = yaml.safe_load(f)
    config_rabbitmq_url = config_raw.get("rabbitmq", {}).get("url", "未设置（使用默认值）")
    
    # 读取配置文件中的原始值（用于对比）
    with open(args.config, "r", encoding="utf-8") as f:
        config_raw = yaml.safe_load(f)
    config_rabbitmq_url = config_raw.get("rabbitmq", {}).get("url", "未设置（使用默认值）")
    
    # 调试信息：打印实际使用的配置
    logger.info("=" * 60)
    logger.info("RabbitMQ 配置信息:")
    logger.info("  配置文件路径: %s", args.config)
    logger.info("  实际使用的 rabbitmq_url: %s", cfg.rabbitmq_url)
    logger.info("  队列名称: %s", cfg.queue)
    logger.info("  预取数量: %d", cfg.prefetch_count)
    logger.info("  完成事件交换机: %s", cfg.done_exchange)
    logger.info("  完成事件路由键: %s", cfg.done_routing_key)
    if env_rabbitmq_url:
        logger.info("  ✓ 环境变量 RABBITMQ_URL 已设置，优先使用环境变量")
        logger.info("    环境变量值: %s", env_rabbitmq_url)
        if env_rabbitmq_url != cfg.rabbitmq_url:
            logger.info("    注意: 环境变量已覆盖配置文件中的值")
            logger.info("    配置文件中的值: %s", config_rabbitmq_url)
    logger.info("=" * 60)
    
    with log_step(logger, "启动: 初始化 TTSRunner (加载模型)"):
        tts_runner = TTSRunner(cfg)
    processor = JobProcessor(cfg, tts_runner)
    worker = RabbitMQWorker(cfg, processor)
    with log_step(logger, "启动: 连接 RabbitMQ 并开始消费"):
        worker.start()


if __name__ == "__main__":
    main()

