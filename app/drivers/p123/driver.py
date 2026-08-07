"""123 云盘驱动骨架。

- 依赖: 可选 p123client 库(未安装时创建驱动会给出明确提示)。
- 登录: 手机号+密码(credential 格式 "phone:password"), 或注入 client。
- file_id 使用 123 的 fileId。
- 说明: 字段映射基于 p123client 常见返回结构编写, 真实联调时按实际返回微调。
"""
from __future__ import annotations

from ..base import DriverMeta, FileItem
from ..common import AccountGate, is_blocked_response


class P123Driver:
    def __init__(self, credential: str = "", config: dict | None = None,
                 client=None):
        config = config or {}
        self.gate = AccountGate(
            max_calls=float(config.get("qps", 1.0)),
            window=1.0,
            cooldown=float(config.get("cooldown", 60)),
        )
        self.client = client
        if self.client is None:
            try:
                from p123client import P123Client
            except ImportError as exc:
                raise ImportError(
                    "123 驱动需要可选依赖 p123client: pip install p123client"
                ) from exc
            if ":" in credential:
                phone, password = credential.split(":", 1)
                self.client = P123Client(phone, password)
            else:
                self.client = P123Client(credential)

    def meta(self) -> DriverMeta:
        return DriverMeta(
            name="p123",
            label="123 云盘",
            auth_type="token",
            capabilities=("download", "delete", "mkdir", "move"),
        )

    def _check_blocked(self, payload) -> None:
        if is_blocked_response(str(payload)):
            self.gate.report_blocked()
            raise RuntimeError("123 接口触发风控, 已进入冷却")

    def list_files(self, parent_id: str) -> list[FileItem]:
        """列目录(分页)。parent_id 为 123 目录 fileId。"""
        items: list[FileItem] = []
        page = 0
        while True:
            self.gate.wait()
            data = self.client.fs_list(parentFileId=parent_id, limit=100,
                                       page=page + 1, inDirectSpace=False)
            self._check_blocked(data)
            rows = (data.get("data") or {}).get("fileList") or []
            for row in rows:
                items.append(_row_to_item(row))
            total = (data.get("data") or {}).get("total") or len(rows)
            page += 1
            if page * 100 >= int(total):
                break
            if not rows:
                break
        return items

    def resolve_download(self, item: FileItem) -> tuple[str, dict]:
        """取下载直链。"""
        self.gate.wait()
        url = self.client.download_info(fileId=item.id)
        if isinstance(url, dict):
            url = url.get("DownloadUrl") or url.get("downloadUrl") or ""
        return url, {}

    def ping(self) -> bool:
        self.gate.wait()
        info = self.client.user_info()
        return bool(info)


def _row_to_item(row: dict) -> FileItem:
    """123 行数据 -> FileItem(防御性字段提取)。"""
    name = row.get("FileName") or row.get("name") or "?"
    is_dir = row.get("Type") == 1 or row.get("type") == 1
    return FileItem(
        id=str(row.get("FileId") or row.get("fileId") or row.get("fileID")),
        name=name,
        size=int(row.get("Size") or row.get("size") or 0),
        is_dir=is_dir,
        mtime=float(row.get("UpdateTime") or row.get("updateTime") or 0) or None,
    )


def _factory(credential: str = "", config: dict | None = None) -> P123Driver:
    return P123Driver(credential=credential, config=config)


def register() -> None:
    from ..registry import register as _register

    _register(_factory, DriverMeta(
        name="p123",
        label="123 云盘",
        auth_type="token",
        capabilities=("download", "delete", "mkdir", "move"),
    ))
