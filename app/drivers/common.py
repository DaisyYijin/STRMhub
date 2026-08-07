"""网盘驱动公共工具: 限流器 + 封控特征识别。

设计来源: p115disk tools.py 滑动窗口限流 + OpenStrm 每账号并发/共享 QPS
两级限流; 封控特征参考 OpenStrm 115.ts 的响应文本判断。
"""
from __future__ import annotations

import threading
import time

# 常见封控/风控响应特征(命中即应退避或报错)
BLOCKED_MARKERS = (
    "您的访问被阻断",
    "potential threats",
    "访问过于频繁",
    "操作频繁",
    "<!doctype html",
    "verify",
    "captcha",
    "验证码",
)

# 凭据过期/需要重新登录的特征(115 等网盘 cookie 过期后返回)
CREDENTIAL_EXPIRED_MARKERS = (
    "请先验证安全密钥",
    "安全密钥",
    "登录已过期",
    "登录状态已过期",
    "请先登录",
    "need login",
    "please login",
)


class CredentialExpired(RuntimeError):
    """凭据已失效(需重新登录)。"""


def is_blocked_response(text: str) -> bool:
    """根据响应文本判断是否触发风控/封控。"""
    lower = (text or "").lower()
    return any(m.lower() in lower for m in BLOCKED_MARKERS)


def is_credential_expired(text: str) -> bool:
    """根据响应文本判断凭据是否过期(需重新登录)。"""
    lower = (text or "").lower()
    return any(m.lower() in lower for m in CREDENTIAL_EXPIRED_MARKERS)


class RateLimiter:
    """滑动窗口限流器(线程安全): 窗口内最多 max_calls 次, 超出则阻塞等待。

    用法: limiter = RateLimiter(max_calls=2, window=1.0); limiter.wait()
    """

    def __init__(self, max_calls: float = 2.0, window: float = 1.0):
        assert max_calls > 0 and window > 0
        self.max_calls = max_calls
        self.window = window
        self._timestamps: list[float] = []
        self._lock = threading.Condition()

    def wait(self) -> None:
        """阻塞直到允许下一次调用。"""
        with self._lock:
            while True:
                now = time.monotonic()
                # 清理窗口外的记录
                cutoff = now - self.window
                self._timestamps = [t for t in self._timestamps if t > cutoff]
                if len(self._timestamps) < self.max_calls:
                    self._timestamps.append(now)
                    return
                # 等待最早记录过期
                wait_for = self._timestamps[0] + self.window - now
                self._lock.wait(timeout=max(wait_for, 0.01))

    def reset(self) -> None:
        with self._lock:
            self._timestamps.clear()
            self._lock.notify_all()


class AccountGate:
    """每账号闸门: 限流器 + 健康标志(封控后冷却)。"""

    def __init__(self, max_calls: float = 2.0, window: float = 1.0,
                 cooldown: float = 60.0):
        self.limiter = RateLimiter(max_calls, window)
        self.cooldown = cooldown
        self._blocked_until = 0.0

    def wait(self) -> None:
        """调用前等待: 冷却期内同样阻塞。wait 返回时冷却必然已结束。"""
        now = time.monotonic()
        if now < self._blocked_until:
            time.sleep(self._blocked_until - now + 0.001)  # +1ms 余量防浮点边界
            self._blocked_until = 0.0
        self.limiter.wait()

    def report_blocked(self) -> None:
        """标记封控, 进入冷却期。"""
        self._blocked_until = time.monotonic() + self.cooldown

    def is_blocked(self) -> bool:
        return time.monotonic() < self._blocked_until
