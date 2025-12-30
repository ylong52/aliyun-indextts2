# 运行时间：常驻运行，监听RabbitMQ的tts_done_exchange 
# | 主要功能：订阅TTS推理完成事件，记录详细日志（队列参数、消息属性、事件JSON），在本地模式(mode=local)下触发Windows 11 Toast通知 
# | 输出结果：控制台日志输出，Windows Toast通知（当mode=local时）
"""Minimal consumer for tts_done events, with queue / message details."""

from __future__ import annotations

import argparse
import json
import logging
import platform
from pathlib import Path

import pika
import yaml

# 根据操作系统判断是否需要加载 win10toast
# Windows 系统需要 win10toast 用于 Toast 通知
# Linux 或其他系统不需要，直接设置为 None
ToastNotifier = None
if platform.system() == "Windows":
    try:
        from win10toast import ToastNotifier
    except ImportError:  # pragma: no cover - optional dependency
        # win10toast 未安装时，ToastNotifier 保持为 None
        ToastNotifier = None  # type: ignore[assignment]


def load_config(config_path: Path | None = None) -> dict:
    """从配置文件加载 RabbitMQ 配置."""
    if config_path is None:
        # 默认配置文件路径：当前文件所在目录的 rabbitmq_config.yaml
        config_path = Path(__file__).parent / "rabbitmq_config.yaml"
    
    if not config_path.exists():
        raise FileNotFoundError(f"Config file not found: {config_path}")
    
    with config_path.open("r", encoding="utf-8") as fh:
        config = yaml.safe_load(fh) or {}
    
    rabbitmq_config = config.get("rabbitmq", {})
    
    return {
        "url": rabbitmq_config.get("url", "amqp://guest:guest@localhost:5672/"),
        "done_exchange": rabbitmq_config.get("done_exchange", "tts_done_exchange"),
        "queue": "tts_done_log_queue",  # 日志队列名称，固定值
    }


# 全局配置变量，在 main 函数中初始化
RABBITMQ_URL: str = ""
EXCHANGE: str = ""
QUEUE: str = ""


def configure_logging() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s | %(levelname)s | %(name)s | %(message)s",
    )


def log_queue_info(channel: pika.adapters.blocking_connection.BlockingChannel) -> None:
    """打印队列的一些参数：消息数、消费者数等。"""
    # passive=True 表示只查询，不会新建队列；如果队列不存在会抛异常
    result = channel.queue_declare(queue=QUEUE, durable=True, passive=True)
    logging.info(
        "Queue info: name=%s message_count=%d consumer_count=%d",
        result.method.queue,
        result.method.message_count,
        result.method.consumer_count,
    )


def main() -> None:
    # 解析命令行参数
    parser = argparse.ArgumentParser(description="TTS Done Event Logger")
    parser.add_argument(
        "--config",
        type=Path,
        default=None,
        help="Path to rabbitmq_config.yaml (default: indextts/app/rabbitmq_config.yaml)",
    )
    args = parser.parse_args()
    
    # 加载配置
    global RABBITMQ_URL, EXCHANGE, QUEUE
    config = load_config(args.config)
    RABBITMQ_URL = config["url"]
    EXCHANGE = config["done_exchange"]
    QUEUE = config["queue"]
    
    configure_logging()
    logger = logging.getLogger("tts_done_logger")
    logger.info("Loaded config: url=%s, exchange=%s, queue=%s", RABBITMQ_URL, EXCHANGE, QUEUE)

    toaster = ToastNotifier() if ToastNotifier is not None else None

    params = pika.URLParameters(RABBITMQ_URL)
    connection = pika.BlockingConnection(params)
    channel = connection.channel()

    # 1. 确保交换机存在（类型为 fanout，对应你现在的设计）
    channel.exchange_declare(
        exchange=EXCHANGE,
        exchange_type="fanout",
        durable=True,
    )

    # 2. 声明并绑定接收 tts_done 事件的队列
    channel.queue_declare(queue=QUEUE, durable=True)
    channel.queue_bind(queue=QUEUE, exchange=EXCHANGE)  # fanout 一般不看 routing_key

    logger.info("Listening on queue=%s bound to exchange=%s", QUEUE, EXCHANGE)
    log_queue_info(channel)

    def callback(ch: pika.adapters.blocking_connection.BlockingChannel,
                 method: pika.spec.Basic.Deliver,
                 properties: pika.spec.BasicProperties,
                 body: bytes) -> None:
        # 解析消息体
        try:
            event = json.loads(body.decode("utf-8"))
        except Exception as exc:  # noqa: BLE001
            logger.error("Failed to decode JSON: %s", exc)
            logger.error("Raw body: %r", body)
            ch.basic_ack(delivery_tag=method.delivery_tag)
            return

        # 打印队列 + RabbitMQ 交付相关参数
        logger.info("---- tts_done message received ----")
        logger.info("Queue: %s", QUEUE)
        logger.info(
            "Delivery: tag=%s exchange=%s routing_key=%s redelivered=%s",
            method.delivery_tag,
            method.exchange,
            method.routing_key,
            method.redelivered,
        )
        logger.info(
            "Properties: content_type=%s headers=%s delivery_mode=%s",
            properties.content_type,
            properties.headers,
            properties.delivery_mode,
        )

        # 打印事件 JSON（record_id / user_id / audio_url / meta 等）
        logger.info("Event JSON: %s", json.dumps(event, ensure_ascii=False, indent=2))

        # 如果是本地模式，使用 Win11 Toast 通知用户
        try:
            meta = event.get("meta") or {}
            mode = meta.get("mode")
        except Exception:
            mode = None

        if toaster is not None and mode == "local":
            title = "语音克隆已经完成"
            message = json.dumps(event, ensure_ascii=False, indent=2)
            try:
                toaster.show_toast(
                    title,
                    message,
                    duration=10,
                    threaded=True,
                )
                logger.info("Windows toast notification sent for record_id=%s", event.get("record_id"))
            except Exception as exc:  # noqa: BLE001
                logger.warning("Failed to show Windows toast: %s", exc)

        # 处理完成后 ack
        ch.basic_ack(delivery_tag=method.delivery_tag)

        # 可以再查询一下当前队列的消息数 / 消费者数
        log_queue_info(ch)

    channel.basic_consume(queue=QUEUE, on_message_callback=callback)

    logger.info(" [*] Waiting for tts_done events. To exit press CTRL+C")
    try:
        channel.start_consuming()
    except KeyboardInterrupt:
        logger.info("Interrupted by user, closing connection...")
    finally:
        connection.close()
        logger.info("Connection closed")


if __name__ == "__main__":
    # 如果在项目根目录运行：
    #   python -m indextts.app.tts_done_logger
    # 或直接：
    #   python indextts/app/tts_done_logger.py
    main()