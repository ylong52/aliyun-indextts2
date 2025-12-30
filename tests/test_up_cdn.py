"""七牛云 CDN 上传测试脚本

使用七牛云 SDK 上传文件到 CDN，并返回访问 URL。

配置信息：
- Access Key: PD2mTA8iuUqLynO8qJTY_GWAXnDRDchLZanLsDAp
- Secret Key: FQOaZ11ko9viHE0AWb5awww8R0jKKupq4flnKoqB
- 存储目录: ai_indextts
- 访问域名: qiniu.ai-book.top
"""

from __future__ import annotations

import os
from pathlib import Path

from qiniu import Auth, put_file_v2, etag


# 七牛云配置
QINIU_ACCESS_KEY = "FQOaZ11ko9viHE0AWb5awww8R0jKKupq4flnKoqB"
QINIU_SECRET_KEY = "PD2mTA8iuUqLynO8qJTY_GWAXnDRDchLZanLsDAp"
QINIU_BUCKET = "weimi220207"  # 存储空间名称
QINIU_DOMAIN = "qiniu.ai-book.top"  # 访问域名
QINIU_DIR = "ai_indextts"  # 存储目录


def upload_to_qiniu(local_file_path: str | Path, remote_key: str | None = None) -> str:
    """上传文件到七牛云 CDN
    
    Args:
        local_file_path: 本地文件路径
        remote_key: 远程文件 key（可选，默认使用文件名）
    
    Returns:
        文件的完整访问 URL
    """
    local_path = Path(local_file_path)
    if not local_path.exists():
        raise FileNotFoundError(f"File not found: {local_path}")
    
    # 构建远程文件 key（目录/文件名）
    if remote_key is None:
        remote_key = f"{QINIU_DIR}/{local_path.name}"
    elif not remote_key.startswith(QINIU_DIR + "/"):
        remote_key = f"{QINIU_DIR}/{remote_key}"
    
    # 构建七牛云认证对象
    q = Auth(QINIU_ACCESS_KEY, QINIU_SECRET_KEY)
    
    # 生成上传 token
    token = q.upload_token(QINIU_BUCKET, remote_key, 3600)
    
    # 上传文件（使用新版本 API）
    # 注意：如果 AccessKey 验证失败，可能是配置问题
    try:
        ret, info = put_file_v2(token, remote_key, str(local_path))
    except Exception as e:
        error_detail = str(e)
        if "accesskey" in error_detail.lower() or "612" in error_detail:
            raise RuntimeError(
                f"AccessKey validation failed. Please check:\n"
                f"  1. AccessKey is correct: {QINIU_ACCESS_KEY[:10]}...\n"
                f"  2. SecretKey is correct\n"
                f"  3. Bucket name is correct: {QINIU_BUCKET}\n"
                f"Original error: {error_detail}"
            ) from e
        raise
    
    if ret is not None:
        print(f"[OK] Upload successful!")
        print(f"  - Local file: {local_path}")
        print(f"  - Remote Key: {remote_key}")
        print(f"  - ETag: {ret.get('hash')}")
        
        # 构建访问 URL
        base_url = f"https://{QINIU_DOMAIN}/{remote_key}"
        print(f"  - Access URL: {base_url}")
        return base_url
    else:
        error_msg = f"Upload failed: {info}"
        print(f"[ERROR] {error_msg}")
        raise RuntimeError(error_msg)


def find_test_file() -> Path:
    """查找一个测试文件用于上传"""
    # 优先使用 tests/output 目录下的 wav 文件
    output_dir = Path(__file__).parent / "output"
    if output_dir.exists():
        wav_files = list(output_dir.glob("*.wav"))
        if wav_files:
            return wav_files[0]
    
    # 如果没有，创建一个简单的测试文本文件
    test_file = Path(__file__).parent / "test_upload_sample.txt"
    if not test_file.exists():
        test_file.write_text("这是一个七牛云上传测试文件\n上传时间: 2025-12-02\n", encoding="utf-8")
    return test_file


def main() -> None:
    """主函数：执行上传测试"""
    print("=" * 60)
    print("Qiniu CDN Upload Test")
    print("=" * 60)
    print(f"Bucket: {QINIU_BUCKET}")
    print(f"Directory: {QINIU_DIR}")
    print(f"Domain: {QINIU_DOMAIN}")
    print(f"AccessKey: {QINIU_ACCESS_KEY[:15]}...")
    print("=" * 60)
    print("\nNOTE: If upload fails with 'accesskey is not found', please verify:")
    print("  1. AccessKey and SecretKey are correct")
    print("  2. Bucket name matches your Qiniu account")
    print("  3. AccessKey has upload permissions for this bucket")
    print("=" * 60)
    
    # 查找测试文件
    test_file = find_test_file()
    print(f"\nPreparing to upload: {test_file}")
    print(f"File size: {test_file.stat().st_size / 1024:.2f} KB")
    
    try:
        # 执行上传
        url = upload_to_qiniu(test_file)
        
        print("\n" + "=" * 60)
        print("[OK] Upload test completed!")
        print("=" * 60)
        print(f"\nAccess domain: {QINIU_DOMAIN}")
        print(f"Full access URL: {url}")
        print("\nYou can access the file via:")
        print(f"  1. Browser: {url}")
        print(f"  2. curl: curl {url}")
        print("=" * 60)
        
    except Exception as exc:
        print(f"\n[ERROR] Test failed: {exc}")
        raise


if __name__ == "__main__":
    main()
