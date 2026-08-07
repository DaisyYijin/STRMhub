"""本地文件系统驱动。

- parent_id / file_id: 使用绝对路径字符串(Windows 也兼容, 统一用 pathlib)。
- 能力: download(返回本地路径, 由播放层直接读盘)、delete、mkdir。
"""
from __future__ import annotations

import os
from pathlib import Path

from ..base import DriverMeta, FileItem, FolderCreator


class LocalDriver(FolderCreator):
    """本地驱动。Driver 是 Protocol, 以具体类实现并实现 meta()。"""

    def __init__(self, credential: str = "", config: dict | None = None):
        self.root = Path((config or {}).get("root") or "/")

    def meta(self) -> DriverMeta:
        return DriverMeta(
            name="local",
            label="本地文件",
            auth_type="none",
            capabilities=("download", "delete", "mkdir", "move"),
        )

    def _to_item(self, p: Path) -> FileItem:
        st = p.stat()
        return FileItem(
            id=str(p),
            name=p.name,
            size=st.st_size if p.is_file() else 0,
            is_dir=p.is_dir(),
            mtime=st.st_mtime,
        )

    def list_files(self, parent_id: str, only_dirs: bool = False) -> list[FileItem]:
        parent = Path(parent_id) if parent_id and parent_id != "0" else self.root
        if not parent.is_dir():
            raise FileNotFoundError(f"目录不存在: {parent}")
        items = []
        for child in sorted(parent.iterdir(), key=lambda c: c.name.lower()):
            try:
                if only_dirs and not child.is_dir():
                    continue
                items.append(self._to_item(child))
            except OSError:
                continue  # 权限等异常跳过单个条目
        return items

    def resolve_download(self, item: FileItem) -> tuple[str, dict]:
        # 本地驱动: 返回 file:// URI(302 location 要求合法 URL)
        return Path(item.id).as_uri(), {}

    def delete_file(self, item: FileItem) -> bool:
        p = Path(item.id)
        if p.is_dir():
            if not any(p.iterdir()):
                p.rmdir()
                return True
            return False
        p.unlink(missing_ok=True)
        return True

    def create_folder(self, parent_id: str, name: str) -> str:
        d = Path(parent_id) / name
        d.mkdir(parents=True, exist_ok=True)
        return str(d)

    def move(self, item: FileItem, dest_parent_id: str,
             new_name: str | None = None) -> FileItem:
        """移动(可同时重命名)。返回移动后的条目。"""
        import shutil
        src = Path(item.id)
        dest_dir = Path(dest_parent_id)
        dest_dir.mkdir(parents=True, exist_ok=True)
        dest = dest_dir / (new_name or src.name)
        if dest == src:
            return item
        if dest.exists():
            raise FileExistsError(f"目标已存在: {dest}")
        shutil.move(str(src), str(dest))
        return FileItem(id=str(dest), name=dest.name,
                        size=dest.stat().st_size if dest.is_file() else 0,
                        is_dir=dest.is_dir(), mtime=dest.stat().st_mtime)


def _factory(credential: str = "", config: dict | None = None) -> LocalDriver:
    return LocalDriver(credential=credential, config=config)


def register() -> None:
    from ..registry import register as _register

    _register(_factory, DriverMeta(
        name="local",
        label="本地文件",
        auth_type="none",
        capabilities=("download", "delete", "mkdir", "move"),
    ))
