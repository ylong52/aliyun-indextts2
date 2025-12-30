"""
本地测试脚本 for IndexTTS2.

本地模式：直接加载checkpoints并运行推理，不依赖任何API服务。

Usage:

    # 本地模式（唯一模式）
    python tests/test_api_v1_text_segment.py
"""
from __future__ import annotations

import argparse
import os
import sys
from datetime import datetime
from pathlib import Path
from uuid import uuid4

PROJECT_ROOT = Path(__file__).resolve().parents[1]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from indextts.infer_v2 import IndexTTS2


MODEL_DIR = Path("checkpoints")
CONFIG_PATH = MODEL_DIR / "config.yaml"
STYLE_FILE = Path("uploads/sample_library/【磁性成熟情感旁白】江西.mp3")
OUTPUT_DIR = Path("tests/output")
MAX_TOKENS_PER_SEGMENT = 160


def build_unique_output_path(prefix: str) -> Path:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    return OUTPUT_DIR / f"{prefix}_{timestamp}_{uuid4().hex[:6]}.wav"


def segment_text(tts: IndexTTS2, text: str) -> list[dict[str, int | str]]:
    tokens = tts.tokenizer.tokenize(text)
    segments = tts.tokenizer.split_segments(tokens, max_text_tokens_per_segment=MAX_TOKENS_PER_SEGMENT)
    return [
        {
            "index": idx,
            "text": "".join(segment),
            "tokens": len(segment),
        }
        for idx, segment in enumerate(segments)
    ]


def run_local_inference(tts: IndexTTS2, text: str) -> Path:
    output_path = build_unique_output_path("text_segment_demo_local")
    tts.infer(
        spk_audio_prompt=str(STYLE_FILE),
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
        max_text_tokens_per_segment=MAX_TOKENS_PER_SEGMENT,
        verbose=False,
    )
    return output_path


def run_local_mode(demo_text: str) -> None:
    if not CONFIG_PATH.exists():
        raise FileNotFoundError(f"Missing config file: {CONFIG_PATH}")
    if not STYLE_FILE.exists():
        raise FileNotFoundError(f"Prompt audio not found: {STYLE_FILE}")

    print("[Local Mode] Loading IndexTTS2 checkpoint stack...")
    tts = IndexTTS2(
        cfg_path=str(CONFIG_PATH),
        model_dir=str(MODEL_DIR),
        use_fp16=False,
        use_cuda_kernel=False,
    )

    print(f"[Local Mode] Segmenting text (max {MAX_TOKENS_PER_SEGMENT} tokens/segment)")
    segments = segment_text(tts, demo_text)
    for seg in segments:
        print(f"  - #{seg['index']:02d}: tokens={seg['tokens']:>3} text={seg['text']}")

    print(f"[Local Mode] Generating speech using prompt {STYLE_FILE}")
    output_path = run_local_inference(tts, demo_text)
    print(f"[Local Mode] Audio saved to: {output_path.resolve()}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="IndexTTS2 本地测试脚本")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    demo_text = (
        "在星轨缓慢转动的凌晨，我独自留在实验室，"
        "为即将上线的 VibeVoice 项目撰写发布稿。"
        "为了测试新的文本分段接口，我随手写下这段随机但又颇有画面感的句子，"
        "希望它足够长，也足够丰富，能够让模型把握语义停顿。"
        "如果你正听着测试生成的音频，欢迎来到这个略带仪式感的开发瞬间。"
    )
    demo_text = """
    今天是 2025 年 11 月 29 日，星期六，农历乙巳年十月初十，干支为乙巳年丁亥月壬寅日。此时正值小雪节气第八天 。根据黄历，今日宜出行、纳财、开市、会亲友等，是不少人办理喜事、规划事务的好日子。不过农谚有 “最怕十月初十晴” 的说法，因小雪节气晴天可能预示冬季偏暖，需留意来年倒春寒风险。万琼萍每天坚持晨练，还在公园组织 “银发互助队”，带着同龄伙伴练太极、分享养生心得，用自己的康复经历鼓励大家。她说：“岁月给的不只是皱纹，还有对抗困境的底气。” 这份向阳而生的力量，让身边人都重拾了对生活的热爱。
    """
    run_local_mode(demo_text)


if __name__ == "__main__":
    main()
