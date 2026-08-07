"""驱动抽象: 最小契约 + 可选能力接口。

设计来源: LitePan internal/driver —— Driver 只要求「元信息 + 列目录」,
其余能力(下载/删除/秒传...)全部是可选接口, 调用方探测后降级。
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Optional, Protocol, runtime_checkable


@dataclass(frozen=True)
class FileItem:
    """统一文件模型(跨盘归一)。"""
    id: str                 # 文件标识(盘内稳定 ID 或路径型 ID)
    name: str
    size: int = 0
    is_dir: bool = False
    mtime: Optional[float] = None
    hash: dict = field(default_factory=dict)  # {sha1|md5: value}

    @property
    def is_file(self) -> bool:
        return not self.is_dir


@dataclass(frozen=True)
class DriverMeta:
    """驱动的静态策略声明。"""
    name: str                       # 驱动类型标识, 如 "local" / "p115"
    label: str                      # 界面显示名
    auth_type: str = "none"         # none | cookie | token
    read_only: bool = False
    capabilities: tuple = ()        # ("download", "delete", "mkdir", ...)


@runtime_checkable
class Driver(Protocol):
    """最小契约: 元信息 + 列目录。"""

    def meta(self) -> DriverMeta: ...

    def list_files(self, parent_id: str) -> list[FileItem]: ...


# ---- 可选能力接口(M1 实现 download/delete, 其余占位供后续扩展) ----


@runtime_checkable
class Downloader(Protocol):
    """获取文件的可播放/可下载信息。返回 (url_or_local_path, headers)。"""

    def resolve_download(self, item: FileItem) -> tuple[str, dict]: ...


@runtime_checkable
class Deleter(Protocol):
    def delete_file(self, item: FileItem) -> bool: ...


@runtime_checkable
class FolderCreator(Protocol):
    def create_folder(self, parent_id: str, name: str) -> str: ...


@runtime_checkable
class Mover(Protocol):
    def move(self, item: FileItem, dest_parent_id: str, new_name: Optional[str] = None) -> FileItem: ...


@runtime_checkable
class AuthCredentialConsumer(Protocol):
    """需要登录凭据(如 cookie/token)的驱动。"""

    def set_credential(self, credential: str) -> None: ...


@runtime_checkable
class Pingable(Protocol):
    def ping(self) -> bool: ...


@runtime_checkable
class RapidUploader(Protocol):
    """秒传能力: 目标盘用 etag 直接建文件(不经上传)。

    返回新文件 file_id; 驱动不支持或秒传失败返回 None(调用方降级)。
    """

    def rapid_upload(self, parent_id: str, name: str, size: int,
                     etag: str) -> str | None: ...
