"""内存环形日志缓冲: 后端日志实时供前端面板查看(含 uvicorn access log)。

安装到 root logger 后, 所有 logging 输出(含 uvicorn/我们的业务日志)
同时进入: ①控制台/docker logs(保留原有行为) ②内存环形缓冲(面板读取)。
"""
from __future__ import annotations

import logging
import threading
from collections import deque


class RingBufferHandler(logging.Handler):
    """环形缓冲 Handler(线程安全, 容量默认 500 条)。"""

    def __init__(self, capacity: int = 500):
        super().__init__()
        self.capacity = capacity
        self._buf: deque = deque(maxlen=capacity)
        self._lock = threading.Lock()

    def emit(self, record: logging.LogRecord) -> None:
        try:
            msg = self.format(record)
        except Exception:
            msg = record.getMessage()
        with self._lock:
            self._buf.append({
                "ts": record.created,
                "level": record.levelname,
                "msg": msg,
            })

    def snapshot(self, tail: int | None = None) -> list[dict]:
        with self._lock:
            lines = list(self._buf)
        return lines[-tail:] if tail else lines


_handler = RingBufferHandler()
_handler.setFormatter(logging.Formatter(
    "%(asctime)s %(levelname)s %(name)s: %(message)s"))
_installed = False


def install() -> None:
    """挂载环形缓冲到 root logger(幂等), 并保证控制台输出仍在。"""
    global _installed
    if _installed:
        return
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    # 控制台输出(uvicorn log_config 已重置过, 这里补回)
    if not any(isinstance(h, logging.StreamHandler) for h in root.handlers):
        root.addHandler(logging.StreamHandler())
    if _handler not in root.handlers:
        root.addHandler(_handler)
    _installed = True


def get_logs(tail: int | None = None) -> list[dict]:
    return _handler.snapshot(tail)
