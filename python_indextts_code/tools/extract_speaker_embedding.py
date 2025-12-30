#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
音色嵌入提取工具
从参考音频中提取音色嵌入并保存，用于后续快速推理
"""

import os
import sys
import json
import numpy as np
import torch
import torchaudio
from pathlib import Path
from typing import Optional, Dict, Any

# 添加项目路径
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "indextts"))

from indextts.infer_v2 import IndexTTS2


class SpeakerEmbeddingExtractor:
    """音色嵌入提取器"""
    
    def __init__(
        self,
        cfg_path: str = "checkpoints/config.yaml",
        model_dir: str = "checkpoints",
        device: Optional[str] = None,
        use_fp16: bool = False
    ):
        """
        初始化提取器
        
        参数:
            cfg_path: 配置文件路径
            model_dir: 模型目录
            device: 设备（cuda/cpu），None 自动选择
            use_fp16: 是否使用半精度
        """
        print("=" * 60)
        print("初始化 IndexTTS2 模型...")
        print("=" * 60)
        
        self.tts = IndexTTS2(
            cfg_path=cfg_path,
            model_dir=model_dir,
            device=device,
            use_fp16=use_fp16
        )
        
        print("✓ 模型加载完成")
    
    def extract_from_audio(
        self,
        audio_path: str,
        max_duration: float = 15.0,
        verbose: bool = True
    ) -> Dict[str, torch.Tensor]:
        """
        从音频文件提取音色嵌入
        
        参数:
            audio_path: 音频文件路径
            max_duration: 最大处理时长（秒）
            verbose: 是否显示详细信息
        
        返回:
            包含所有嵌入的字典
        """
        if verbose:
            print(f"\n处理音频: {audio_path}")
        
        # 加载并裁剪音频
        audio, sr = self.tts._load_and_cut_audio(audio_path, max_duration, verbose)
        
        # 重采样到不同采样率
        audio_22k = torchaudio.transforms.Resample(sr, 22050)(audio)
        audio_16k = torchaudio.transforms.Resample(sr, 16000)(audio)
        
        # 提取 W2V-BERT 语义嵌入
        if verbose:
            print("  提取语义嵌入...")
        inputs = self.tts.extract_features(audio_16k, sampling_rate=16000, return_tensors="pt")
        input_features = inputs["input_features"].to(self.tts.device)
        attention_mask = inputs["attention_mask"].to(self.tts.device)
        spk_cond_emb = self.tts.get_emb(input_features, attention_mask)
        
        # 提取 CAMPPlus 音色嵌入
        if verbose:
            print("  提取音色嵌入...")
        feat = torchaudio.compliance.kaldi.fbank(
            audio_16k.to(self.tts.device),
            num_mel_bins=80,
            dither=0,
            sample_frequency=16000
        )
        feat = feat - feat.mean(dim=0, keepdim=True)
        style = self.tts.campplus_model(feat.unsqueeze(0))
        
        # 生成 S2Mel 提示条件
        if verbose:
            print("  生成 S2Mel 提示条件...")
        _, S_ref = self.tts.semantic_codec.quantize(spk_cond_emb)
        ref_mel = self.tts.mel_fn(audio_22k.to(spk_cond_emb.device).float())
        ref_target_lengths = torch.LongTensor([ref_mel.size(2)]).to(ref_mel.device)
        
        prompt_condition = self.tts.s2mel.models['length_regulator'](
            S_ref,
            ylens=ref_target_lengths,
            n_quantizers=3,
            f0=None
        )[0]
        
        # 返回所有嵌入
        embeddings = {
            'spk_cond_emb': spk_cond_emb.cpu(),      # W2V-BERT 语义嵌入
            's2mel_style': style.cpu(),              # CAMPPlus 音色嵌入
            's2mel_prompt': prompt_condition.cpu(),  # S2Mel 提示条件
            'ref_mel': ref_mel.cpu(),                # 参考梅尔谱
        }
        
        if verbose:
            print("✓ 嵌入提取完成")
            print(f"  - 语义嵌入形状: {spk_cond_emb.shape}")
            print(f"  - 音色嵌入形状: {style.shape}")
            print(f"  - 提示条件形状: {prompt_condition.shape}")
        
        return embeddings
    
    def save_embeddings(
        self,
        embeddings: Dict[str, torch.Tensor],
        output_path: str,
        metadata: Optional[Dict[str, Any]] = None
    ):
        """
        保存嵌入到文件
        
        参数:
            embeddings: 嵌入字典
            output_path: 输出文件路径（.npz 格式）
            metadata: 元数据（可选）
        """
        # 转换为 numpy 数组
        np_embeddings = {}
        for key, value in embeddings.items():
            if isinstance(value, torch.Tensor):
                np_embeddings[key] = value.numpy()
            else:
                np_embeddings[key] = value
        
        # 保存嵌入
        np.savez_compressed(output_path, **np_embeddings)
        
        # 保存元数据（如果有）
        if metadata:
            metadata_path = output_path.replace('.npz', '_metadata.json')
            with open(metadata_path, 'w', encoding='utf-8') as f:
                json.dump(metadata, f, ensure_ascii=False, indent=2)
        
        print(f"✓ 嵌入已保存: {output_path}")
    
    def load_embeddings(self, embedding_path: str) -> Dict[str, torch.Tensor]:
        """
        从文件加载嵌入
        
        参数:
            embedding_path: 嵌入文件路径
        
        返回:
            嵌入字典
        """
        # 加载 numpy 数组
        data = np.load(embedding_path, allow_pickle=True)
        
        # 转换为 torch tensor
        embeddings = {}
        for key in data.files:
            embeddings[key] = torch.from_numpy(data[key])
        
        return embeddings


def extract_single_speaker(
    audio_path: str,
    output_dir: str = "speaker_embeddings/embeddings",
    speaker_name: Optional[str] = None,
    cfg_path: str = "checkpoints/config.yaml",
    model_dir: str = "checkpoints",
    device: Optional[str] = None
):
    """
    提取单个说话人的音色嵌入
    
    参数:
        audio_path: 参考音频路径
        output_dir: 输出目录
        speaker_name: 说话人名称（如果不提供，使用文件名）
        cfg_path: 配置文件路径
        model_dir: 模型目录
        device: 设备
    """
    # 创建输出目录
    os.makedirs(output_dir, exist_ok=True)
    
    # 确定说话人名称
    if speaker_name is None:
        speaker_name = Path(audio_path).stem
    
    # 初始化提取器
    extractor = SpeakerEmbeddingExtractor(
        cfg_path=cfg_path,
        model_dir=model_dir,
        device=device
    )
    
    # 提取嵌入
    print(f"\n提取音色嵌入: {speaker_name}")
    embeddings = extractor.extract_from_audio(audio_path, verbose=True)
    
    # 保存嵌入
    output_path = os.path.join(output_dir, f"{speaker_name}.npz")
    metadata = {
        'speaker_name': speaker_name,
        'source_audio': audio_path,
        'extraction_time': str(Path(audio_path).stat().st_mtime)
    }
    extractor.save_embeddings(embeddings, output_path, metadata)
    
    print(f"\n✓ 完成！音色嵌入已保存到: {output_path}")
    return output_path


def batch_extract(
    audio_dir: str,
    output_dir: str = "speaker_embeddings/embeddings",
    cfg_path: str = "checkpoints/config.yaml",
    model_dir: str = "checkpoints",
    device: Optional[str] = None
):
    """
    批量提取多个说话人的音色嵌入
    
    参数:
        audio_dir: 音频文件目录
        output_dir: 输出目录
        cfg_path: 配置文件路径
        model_dir: 模型目录
        device: 设备
    """
    # 支持的音频格式
    audio_extensions = {'.wav', '.mp3', '.flac', '.m4a', '.ogg'}
    
    # 查找所有音频文件
    audio_files = []
    for ext in audio_extensions:
        audio_files.extend(Path(audio_dir).glob(f'*{ext}'))
        audio_files.extend(Path(audio_dir).glob(f'*{ext.upper()}'))
    
    if not audio_files:
        print(f"错误: 在 {audio_dir} 中未找到音频文件")
        return
    
    print(f"找到 {len(audio_files)} 个音频文件")
    
    # 初始化提取器（只初始化一次，提高效率）
    extractor = SpeakerEmbeddingExtractor(
        cfg_path=cfg_path,
        model_dir=model_dir,
        device=device
    )
    
    # 批量处理
    results = []
    for i, audio_path in enumerate(audio_files, 1):
        print(f"\n[{i}/{len(audio_files)}] 处理: {audio_path.name}")
        
        try:
            speaker_name = audio_path.stem
            embeddings = extractor.extract_from_audio(str(audio_path), verbose=False)
            
            output_path = os.path.join(output_dir, f"{speaker_name}.npz")
            metadata = {
                'speaker_name': speaker_name,
                'source_audio': str(audio_path),
            }
            extractor.save_embeddings(embeddings, output_path, metadata)
            
            results.append({
                'speaker_name': speaker_name,
                'embedding_path': output_path,
                'status': 'success'
            })
        except Exception as e:
            print(f"  ✗ 处理失败: {e}")
            results.append({
                'speaker_name': audio_path.stem,
                'status': 'failed',
                'error': str(e)
            })
    
    # 生成音色列表
    speaker_list = {
        'speakers': [
            {
                'name': r['speaker_name'],
                'embedding_path': r['embedding_path'],
                'status': r['status']
            }
            for r in results if r['status'] == 'success'
        ],
        'total': len(results),
        'success': sum(1 for r in results if r['status'] == 'success'),
        'failed': sum(1 for r in results if r['status'] == 'failed')
    }
    
    list_path = os.path.join(output_dir, 'speaker_list.json')
    with open(list_path, 'w', encoding='utf-8') as f:
        json.dump(speaker_list, f, ensure_ascii=False, indent=2)
    
    print(f"\n" + "=" * 60)
    print("批量提取完成")
    print("=" * 60)
    print(f"成功: {speaker_list['success']}/{speaker_list['total']}")
    print(f"失败: {speaker_list['failed']}/{speaker_list['total']}")
    print(f"音色列表已保存: {list_path}")


if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="音色嵌入提取工具")
    parser.add_argument(
        "--audio",
        type=str,
        help="单个音频文件路径"
    )
    parser.add_argument(
        "--audio-dir",
        type=str,
        help="音频文件目录（批量处理）"
    )
    parser.add_argument(
        "--output-dir",
        type=str,
        default="speaker_embeddings/embeddings",
        help="输出目录"
    )
    parser.add_argument(
        "--speaker-name",
        type=str,
        help="说话人名称（仅用于单文件模式）"
    )
    parser.add_argument(
        "--cfg-path",
        type=str,
        default="checkpoints/config.yaml",
        help="配置文件路径"
    )
    parser.add_argument(
        "--model-dir",
        type=str,
        default="checkpoints",
        help="模型目录"
    )
    parser.add_argument(
        "--device",
        type=str,
        help="设备 (cuda/cpu)，默认自动选择"
    )
    
    args = parser.parse_args()
    
    if args.audio:
        # 单文件模式
        extract_single_speaker(
            audio_path=args.audio,
            output_dir=args.output_dir,
            speaker_name=args.speaker_name,
            cfg_path=args.cfg_path,
            model_dir=args.model_dir,
            device=args.device
        )
    elif args.audio_dir:
        # 批量模式
        batch_extract(
            audio_dir=args.audio_dir,
            output_dir=args.output_dir,
            cfg_path=args.cfg_path,
            model_dir=args.model_dir,
            device=args.device
        )
    else:
        parser.print_help()
        print("\n示例:")
        print("  单文件: python extract_speaker_embedding.py --audio speaker.wav")
        print("  批量:   python extract_speaker_embedding.py --audio-dir audio_samples/")

