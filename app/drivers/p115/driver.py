"""115 网盘驱动骨架。

- 依赖: 可选 p115client 库(未安装时创建驱动会给出明确提示)。
- 登录: Cookie(credential) 或扫码(留待前端); file_id 使用 pickcode。
- 风控: AccountGate 限流 + 响应特征识别 -> 冷却。
- 说明: 字段映射基于 p115client 常见返回结构编写, 真实联调时按实际返回微调。
"""
from __future__ import annotations

from ..base import DriverMeta, FileItem
from ..common import AccountGate, is_blocked_response


class P115Driver:
    def __init__(self, credential: str = "", config: dict | None = None,
                 client=None):
        config = config or {}
        self.gate = AccountGate(
            max_calls=float(config.get("qps", 2.0)),
            window=1.0,
            cooldown=float(config.get("cooldown", 60)),
        )
        self.client = client
        if self.client is None:
            try:
                from p115client import P115Client
            except ImportError as exc:
                raise ImportError(
                    "115 驱动需要可选依赖 p115client: pip install p115client"
                ) from exc
            self.client = P115Client(cookies=credential)

    def meta(self) -> DriverMeta:
        return DriverMeta(
            name="p115",
            label="115 网盘",
            auth_type="cookie",
            capabilities=("download", "delete", "mkdir", "move"),
        )

    def _check_blocked(self, payload) -> None:
        if is_blocked_response(str(payload)):
            self.gate.report_blocked()
            raise RuntimeError("115 接口触发风控, 已进入冷却")

    def list_files(self, parent_id: str) -> list[FileItem]:
        """列目录(分页)。parent_id 为 115 目录 cid。"""
        items: list[FileItem] = []
        offset = 0
        while True:
            self.gate.wait()
            data = self.client.fs_files(cid=parent_id, limit=115, offset=offset)
            self._check_blocked(data)
            rows = data.get("data") or []
            for row in rows:
                items.append(_row_to_item(row))
            if len(rows) < 115:
                break
            offset += len(rows)
        return items

    def resolve_download(self, item: FileItem) -> tuple[str, dict]:
        """取下载直链(UA 随客户端透传, 由调用方注入 headers)。"""
        self.gate.wait()
        url = self.client.download_url(item.id, user_agent="Mozilla/5.0")
        return url, {}

    def ping(self) -> bool:
        self.gate.wait()
        info = self.client.user_info()
        return bool(info and info.get("state"))


def _row_to_item(row: dict) -> FileItem:
    """115 行数据 -> FileItem(字段名按 p115client 返回结构防御性提取)。"""
    name = row.get("n") or row.get("name") or "?"
    is_dir = row.get("t") == 1 or "pick_code" not in row or not row.get("pick_code")
    return FileItem(
        id=str(row.get("pick_code") or row.get("fid") or row.get("cid")),
        name=name,
        size=int(row.get("s") or 0),
        is_dir=is_dir,
        mtime=float(row.get("t") or 0) if not is_dir else None,
    )


def _factory(credential: str = "", config: dict | None = None) -> P115Driver:
    return P115Driver(credential=credential, config=config)


def register() -> None:
    from ..registry import register as _register

    _register(_factory, DriverMeta(
        name="p115",
        label="115 网盘",
        auth_type="cookie",
        capabilities=("download", "delete", "mkdir", "move"),
    ))
