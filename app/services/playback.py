"""播放服务: /api/redirect 302 端点 + 直链缓存 + 同目录预缓存。

设计来源: TgtoDrive get_download_url_by_path.py —— 直链缓存至过期前、
首次命中后异步预热同目录其余文件(解决 Emby 逐集请求的起播延迟);
p115strmhelper —— 302 + RFC5987 Content-Disposition。
"""
from __future__ import annotations

import base64
import threading
import time
from dataclasses import dataclass

from sqlalchemy import select

from ..db.models import FileIndex
from ..db.session import session_scope
from ..services.account import AccountService

_accounts = AccountService()


@dataclass
class CacheEntry:
    url: str
    headers: dict
    expires_at: float


class PlaybackService:
    def __init__(self, cache_ttl: float = 1500.0):
        self.cache: dict[str, CacheEntry] = {}
        self.cache_ttl = cache_ttl
        self._lock = threading.Lock()
        self._precache_lock = threading.Lock()

    # ---- key 解码 ----
    @staticmethod
    def decode_key(key: str) -> str:
        """base64url -> 原始 file_id(失败抛 ValueError)。"""
        padded = key + "=" * (-len(key) % 4)
        return base64.urlsafe_b64decode(padded).decode("utf-8")

    # ---- 反查: file_key -> 账户 ----
    def _find_index(self, file_id: str) -> FileIndex | None:
        with session_scope() as s:
            return s.scalar(
                select(FileIndex).where(FileIndex.file_key == file_id).limit(1))

    # ---- 缓存 ----
    def _get_cached(self, key: str) -> str | None:
        with self._lock:
            entry = self.cache.get(key)
            if entry is None:
                return None
            if entry.expires_at < time.time():
                self.cache.pop(key, None)
                return None
            return entry.url

    def _put_cache(self, key: str, url: str, ttl: float | None = None) -> None:
        with self._lock:
            self.cache[key] = CacheEntry(
                url=url, headers={}, expires_at=time.time() + (ttl or self.cache_ttl))

    # ---- 主入口 ----
    def resolve_redirect(self, key: str, token: str = "") -> tuple[str, str] | None:
        """解析 STRM key 为直链 URL。

        返回 (url, content_disposition) 或 None(未找到)。
        token 非空时做存在性校验(防随意调用); 签名校验留待后续版本。
        """
        try:
            file_id = self.decode_key(key)
        except (ValueError, UnicodeDecodeError):
            return None
        if not token:
            return None

        index = self._find_index(file_id)
        if index is None:
            return None

        cached = self._get_cached(key)
        if cached:
            return cached, ""

        account = _accounts.get(index.account_id)
        if account is None:
            return None
        driver = _accounts.driver_for(account)
        try:
            from ..drivers.base import FileItem
            item = FileItem(id=file_id, name=index.path.rsplit("/", 1)[-1])
            url, _headers = driver.resolve_download(item)
        except Exception:
            return None
        if not url:
            return None

        self._put_cache(key, url)
        self._precache_siblings(index, driver)
        return url, ""

    # ---- 同目录预缓存 ----
    def _precache_siblings(self, index: FileIndex, driver) -> None:
        """命中后异步预热同目录其余文件直链(单线程串行, 防风暴)。"""
        if not self._precache_lock.acquire(blocking=False):
            return
        try:
            parent_path = index.path.rsplit("/", 1)[0] if "/" in index.path else ""
            prefix = parent_path + "/" if parent_path else ""
            with session_scope() as s:
                siblings = s.scalars(
                    select(FileIndex)
                    .where(FileIndex.account_id == index.account_id,
                           FileIndex.path.like(prefix + "%"),
                           FileIndex.path != index.path)
                    .limit(50)).all()
            for sibling in siblings:
                key = self._b64(sibling.file_key)
                if self._get_cached(key):
                    continue
                try:
                    from ..drivers.base import FileItem
                    item = FileItem(id=sibling.file_key,
                                    name=sibling.path.rsplit("/", 1)[-1])
                    url, _ = driver.resolve_download(item)
                    if url:
                        self._put_cache(key, url, ttl=min(self.cache_ttl, 600))
                except Exception:
                    continue
        finally:
            self._precache_lock.release()

    @staticmethod
    def _b64(file_id: str) -> str:
        return base64.urlsafe_b64encode(file_id.encode("utf-8")).rstrip(b"=").decode("ascii")


# 单例
playback = PlaybackService()
