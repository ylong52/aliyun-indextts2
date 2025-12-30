"""Send sample TTS jobs to RabbitMQ for manual testing."""

from __future__ import annotations

import argparse
import json
from datetime import datetime, timezone
import os
from pathlib import Path
from typing import Any
from uuid import uuid4

import pika

# ---------------------------------------------------------------------------
# 手工可编辑的示例任务（可根据需要直接修改下方字段）
# ---------------------------------------------------------------------------
SAMPLE_PAYLOAD: dict[str, Any] = {
    "text_path": "/python_indextts_code/uploads/user_data/demo.txt",
    "user_id": "demo-user",
    "record_id": "demo-record-123456",
    "timestamp": datetime.now(timezone.utc).isoformat(),
    "mode": "api",  # local 或 api
    "style_file": "/python_indextts_code/uploads/sample_library/【磁性成熟情感旁白】江西.mp3",
}


def build_payload(args: argparse.Namespace) -> dict[str, Any]:
    payload = SAMPLE_PAYLOAD.copy()
    if args.text_path:
        payload["text_path"] = args.text_path
    if args.user_id:
        payload["user_id"] = args.user_id
    if args.record_id:
        payload["record_id"] = args.record_id
    else:
        payload["record_id"] = payload.get("record_id") or uuid4().hex
    if args.timestamp:
        payload["timestamp"] = args.timestamp
    else:
        payload["timestamp"] = datetime.now(timezone.utc).isoformat()
    if args.mode:
        payload["mode"] = args.mode
    if args.style_file:
        payload["style_file"] = args.style_file
    return payload


def publish(args: argparse.Namespace) -> None:
    params = pika.URLParameters(args.url)
    connection = pika.BlockingConnection(params)
    channel = connection.channel()
    channel.queue_declare(queue=args.queue, durable=True)

    payload = build_payload(args)
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    channel.basic_publish(
        exchange="",
        routing_key=args.queue,
        body=body,
        properties=pika.BasicProperties(delivery_mode=2),
    )
    print(f"[OK] Published job to queue={args.queue}: {payload}")
    connection.close()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Publish sample RabbitMQ TTS jobs")
    parser.add_argument(
        "--url",
        default=os.getenv("RABBITMQ_URL", "amqp://guest:guest@47.115.54.136:5672/"),
        help="RabbitMQ connection URL (default: env RABBITMQ_URL or localhost)",
    )
    parser.add_argument(
        "--queue",
        default="tts_jobs",
        help="Queue name to publish to",
    )
    parser.add_argument(
        "--mode",
        choices=("local", "api"),
        default="local",
        help="Text fetching mode",
    )
    parser.add_argument("--text-path", default=None, help="Override text path")
    parser.add_argument("--user-id", default=None, help="Override user id")
    parser.add_argument(
        "--record-id",
        default=None,
        help="Override record id (default: use sample value or random)",
    )
    parser.add_argument(
        "--timestamp",
        default=None,
        help="Timestamp string (defaults to current UTC time)",
    )
    parser.add_argument(
        "--style-file",
        default=None,
        help="Override style file path used by the worker",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    publish(args)


if __name__ == "__main__":
    main()

