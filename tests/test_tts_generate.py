#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
测试本地IndexTTS2声音克隆功能
直接调用本地模型，不通过API接口
包含语音识别功能测试
注意：本脚本仅支持本地模式，不依赖任何API服务
"""

import os
import sys
import uuid
import time
import torch
import argparse
from pathlib import Path
from typing import Optional, Dict, Any

# 添加项目根目录到Python路径
sys.path.append(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.append(os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "indextts"))

from indextts.infer_v2 import IndexTTS2
from faster_whisper import WhisperModel

def calculate_optimal_max_tokens(text: str, target_segments: int = 5) -> int:
    """
    Calculate optimal max tokens per segment based on text length and target segments.
    """
    # Estimate token count (rough estimation)
    estimated_tokens = len(text) // 4  # Rough estimation: 4 chars per token on average
    if estimated_tokens == 0:
        estimated_tokens = 1
    
    # Calculate optimal tokens per segment
    optimal_tokens = max(20, estimated_tokens // target_segments)  # At least 20 tokens
    optimal_tokens = min(optimal_tokens, 200)  # At most 200 tokens
    return optimal_tokens


def test_local_tts_generate(prompt_audio_path: str, transcript_text: str, output_dir: str) -> Dict[str, Any]:
    """
    测试本地IndexTTS2声音克隆功能
    使用指定的样本语音文件生成TTS音频
    
    参数:
        prompt_audio_path: 提示音频文件路径
        transcript_text: 要合成的文本
        output_dir: 输出目录
    
    返回:
        包含转录文本和音频路径的字典
    """
    print("=== 测试本地IndexTTS2声音克隆功能 ===")
    
    # 输入文件路径 - 使用指定的样本语音文件
    input_file = Path(prompt_audio_path)
    
    if not input_file.exists():
        print(f"错误: 输入文件不存在: {input_file}")
        return None
    
    print(f"输入文件: {input_file}")
    print(f"文件大小: {input_file.stat().st_size / 1024:.2f} KB")
    
    # 生成唯一的输出文件名
    output_file = os.path.join(output_dir, f"tts_local_result_{uuid.uuid4().hex[:8]}.wav")
    os.makedirs(output_dir, exist_ok=True)
    print(f"输出文件: {output_file}")
    
    try:
        # 初始化IndexTTS2模型
        print("\n=== 初始化模型 ===")
        start_init = time.time()
        
        # 检测可用设备
        device = "cuda" if torch.cuda.is_available() else "cpu"
        print(f"使用设备: {device}")
        
        tts = IndexTTS2(
            cfg_path="checkpoints/config.yaml",
            model_dir="checkpoints",
            use_fp16=device != "cpu",  # 只有非CPU设备才使用半精度
            device=device,
            use_cuda_kernel=device.startswith("cuda"),
            use_deepspeed=False,
            use_torch_compile=False  # 禁用torch.compile以避免潜在问题
        )
        
        end_init = time.time()
        print(f"模型初始化耗时: {end_init - start_init:.2f} 秒")
        
        # 使用传入的文本
        test_text = transcript_text
        print(f"\n测试文本: {test_text}")
        
        # 计算最佳分段大小
        max_text_tokens_per_segment = calculate_optimal_max_tokens(test_text)
        print(f"使用max_text_tokens_per_segment: {max_text_tokens_per_segment}")
        
        # 执行TTS生成（声音克隆）
        print("\n=== 执行声音克隆 ===")
        start_generate = time.time()
        
        # 调用infer方法进行声音克隆
        tts.infer(
            spk_audio_prompt=str(input_file),
            text=test_text,
            output_path=output_file,
            verbose=True,
            # 生成参数
            temperature=0.8,
            top_k=30,
            top_p=0.8,
            max_text_tokens_per_segment=max_text_tokens_per_segment,
            num_beams=3,
            repetition_penalty=10.0,
            length_penalty=0.0,
            max_mel_tokens=1500
        )
        
        end_generate = time.time()
        print(f"声音克隆耗时: {end_generate - start_generate:.2f} 秒")
        print(f"总耗时: {end_generate - start_init:.2f} 秒")
        
        # 验证输出文件
        if os.path.exists(output_file):
            file_size = os.path.getsize(output_file) / 1024
            print(f"\n✓ 声音克隆成功!")
            print(f"输出文件: {output_file}")
            print(f"文件大小: {file_size:.2f} KB")
            return {
                "transcription": test_text,
                "audio_path": output_file
            }
        else:
            print(f"\n✗ 生成失败: 输出文件不存在")
            return None
            
    except Exception as e:
        print(f"\n✗ 发生错误: {e}")
        import traceback
        print("错误详情:")
        traceback.print_exc()
        return None

def test_long_text_generation(prompt_audio_path: str, output_dir: str):
    """
    测试长文本生成功能
    
    参数:
        prompt_audio_path: 提示音频文件路径
        output_dir: 输出目录
        
    返回:
        音频文件路径或None
    """
    print("\n=== 测试长文本生成功能 ===")
    
    # 输入文件路径（与上面相同）
    input_file = Path(prompt_audio_path)
    
    # 生成唯一的输出文件名
    output_file = os.path.join(output_dir, f"tts_long_text_result_{uuid.uuid4().hex[:8]}.wav")
    os.makedirs(output_dir, exist_ok=True)
    print(f"输出文件: {output_file}")
    
    try:
        # 长测试文本
        long_text = "这是一个长文本测试示例。IndexTTS2模型支持长文本的分段处理，可以将较长的文本自动分割成多个段落，然后逐段生成语音。" \
                   "这种分段处理机制可以有效避免内存溢出问题，同时保持生成语音的连贯性。" \
                   "用户可以通过调整max_text_tokens_per_segment参数来控制每段文本的最大长度。"
        print(f"\n测试文本长度: {len(long_text)} 字符")
        print(f"测试文本: {long_text}")
        
        # 复用之前初始化的模型（为了节省时间，这里直接重新初始化）
        # 在实际应用中，可以考虑在不同测试间共享模型实例
        device = "cuda" if torch.cuda.is_available() else "cpu"
        tts = IndexTTS2(
            cfg_path="checkpoints/config.yaml",
            model_dir="checkpoints",
            use_fp16=device != "cpu",
            device=device,
            use_cuda_kernel=device.startswith("cuda"),
            use_deepspeed=False,
            use_torch_compile=False
        )
        
        # 执行长文本TTS生成
        start_generate = time.time()
        
        tts.infer(
            spk_audio_prompt=str(input_file),
            text=long_text,
            output_path=output_file,
            verbose=True,
            temperature=0.8,
            max_text_tokens_per_segment=100,  # 较小的分段大小以测试分段功能
            interval_silence=200,  # 段间静默时间（毫秒）
            do_sample=True,
            top_p=0.8,
            top_k=30,
            num_beams=3,
            repetition_penalty=10.0,
            length_penalty=0.0,
            max_mel_tokens=1500
        )
        
        end_generate = time.time()
        print(f"长文本生成耗时: {end_generate - start_generate:.2f} 秒")
        
        # 验证输出文件
        if os.path.exists(output_file):
            file_size = os.path.getsize(output_file) / 1024
            print(f"\n✓ 长文本生成成功!")
            print(f"输出文件: {output_file}")
            print(f"文件大小: {file_size:.2f} KB")
            return output_file
        else:
            print(f"\n✗ 长文本生成失败: 输出文件不存在")
            return None
            
    except Exception as e:
        print(f"\n✗ 发生错误: {e}")
        import traceback
        print("错误详情:")
        traceback.print_exc()
        return None

def test_speech_recognition(audio_path: str) -> str:
    """
    测试语音识别功能
    使用faster_whisper模型将音频文件转换为文字
    从指定的WAV文件中提取文字内容
    
    参数:
        audio_path: 音频文件路径
        
    返回:
        识别的文本
    """
    print("\n=== 测试语音识别功能 ===")
    
    # 输入文件路径 - 使用指定的WAV文件
    input_file = Path(audio_path)
    
    if not input_file.exists():
        print(f"错误: 输入文件不存在: {input_file}")
        return None
    
    print(f"输入文件: {input_file}")
    print(f"文件大小: {input_file.stat().st_size / 1024:.2f} KB")
    
    try:
        # 初始化Whisper模型
        print("\n=== 初始化语音识别模型 ===")
        start_init = time.time()
        
        # 检测可用设备
        device = "cuda" if torch.cuda.is_available() else "cpu"
        compute_type = "float16" if device == "cuda" else "int8"
        print(f"使用设备: {device}")
        print(f"计算类型: {compute_type}")
        
        # 使用medium模型，平衡速度和精度
        model = WhisperModel("medium", device=device, compute_type=compute_type)
        
        end_init = time.time()
        print(f"模型初始化耗时: {end_init - start_init:.2f} 秒")
        
        # 执行语音识别
        print("\n=== 执行语音识别 ===")
        start_recognition = time.time()
        
        segments, info = model.transcribe(
            str(input_file),
            beam_size=5,
            word_timestamps=True,
            language="zh"  # 指定中文语言以提高识别准确率
        )
        
        end_recognition = time.time()
        print(f"语音识别耗时: {end_recognition - start_recognition:.2f} 秒")
        print(f"检测到的语言: {info.language}, 概率: {info.language_probability:.4f}")
        
        # 输出识别结果
        print("\n=== 语音识别结果 ===")
        full_text = ""
        for i, segment in enumerate(segments, 1):
            print(f"片段 {i} [{segment.start:.2f}s - {segment.end:.2f}s]: {segment.text}")
            full_text += segment.text
        
        print("\n=== 完整识别文本 ===")
        print(full_text)
        print(f"识别文本长度: {len(full_text)} 字符")
        
        return full_text  # 返回识别的文本，用于后续的TTS合成
        
    except Exception as e:
        print(f"\n✗ 发生错误: {e}")
        import traceback
        print("错误详情:")
        traceback.print_exc()
        return None




def main():
    """
    主函数
    流程：
    1. 从指定的WAV文件提取文字
    2. 使用提取的文字和样本语音进行TTS合成
    """
    parser = argparse.ArgumentParser(description="测试TTS生成")
    parser.add_argument(
        "--mode",
        type=str,
        default="local",
        choices=["local"],
        help="运行测试的模式（仅支持本地模式）",
    )
    parser.add_argument(
        "--prompt-audio",
        type=str,
        default="uploads/sample_library/外部-小黑-讲述.mp3",
        help="提示音频文件的路径",
    )
    parser.add_argument(
        "--source-audio",
        type=str,
        default="examples/sample_TAL_ASR.wav",
        help="用于转录的源音频文件路径",
    )
    parser.add_argument(
        "--output-dir",
        type=str,
        default="tests/output",
        help="保存输出文件的目录",
    )
    
    args = parser.parse_args()
    
    print(f"\n=== TTS生成测试 (本地模式) ===")
    print(f"提示音频: {args.prompt_audio}")
    print(f"源音频: {args.source_audio}")
    print(f"输出目录: {args.output_dir}")
    
    try:
        total_start_time = time.time()
        
        # 本地模式：从音频生成TTS
        print("\n1. 从音频生成TTS（本地模式）...")
        # 第一步：语音识别
        print("\n   [子步骤1] 开始从WAV文件提取文字")
        transcript = test_speech_recognition(args.source_audio)
        
        # 第二步：TTS生成
        if transcript:
            print("\n   [子步骤2] 开始使用提取的文字进行声音克隆")
            result = test_local_tts_generate(args.prompt_audio, transcript, args.output_dir)
        else:
            print("\n✗ 语音识别失败，无法继续声音克隆测试")
            result = None
        
        if result:
            print(f"\n=== 生成摘要 ===")
            print(f"转录文本: {result['transcription']}")
            if 'audio_path' in result:
                print(f"输出文件: {result['audio_path']}")
            elif 'audio_url' in result:
                print(f"输出URL: {result['audio_url']}")
        
        total_time = time.time() - total_start_time
        print(f"\n=== 所有测试在 {total_time:.2f} 秒内成功完成! ===")
        
    except Exception as e:
        print(f"\n测试过程中的错误: {e}")
        import traceback
        traceback.print_exc()


if __name__ == "__main__":
    main()
