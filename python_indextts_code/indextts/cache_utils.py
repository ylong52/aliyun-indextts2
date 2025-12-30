"""
Shared cache configuration for downloading large model assets.
"""
from __future__ import annotations

import os
from pathlib import Path

from huggingface_hub import hf_hub_download as _hf_hub_download

CACHE_DIR = Path(__file__).resolve().parents[1] / "checkpoints" / "hf_cache"
CACHE_DIR.mkdir(parents=True, exist_ok=True)
_CACHE_DIR_STR = str(CACHE_DIR)

for _env in (
    "HF_HOME",
    "HF_DATASETS_CACHE",
    "TRANSFORMERS_CACHE",
    "HF_HUB_CACHE",
    "MODELSCOPE_CACHE",
):
    os.environ.setdefault(_env, _CACHE_DIR_STR)


def hf_cached_download(*args, **kwargs):
    """Wrapper around huggingface hf_hub_download that enforces our cache dir."""
    kwargs.setdefault("cache_dir", _CACHE_DIR_STR)
    return _hf_hub_download(*args, **kwargs)


