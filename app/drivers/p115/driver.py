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
        """列目录(分页)。parent_id 为 115 目录 cid。

        p115client fs_files(payload dict) -> {"state", "data", ...};
        data 可能是数组(open/ufile/files)或 dict(个别版本返回 {"list": ...})。
        """
        import os
        items: list[FileItem] = []
        offset = 0
        while True:
            self.gate.wait()
            data = self.client.fs_files(
                {"cid": int(parent_id or 0), "limit": 115, "offset": offset})
            self._check_blocked(data)
            rows = data.get("data") or []
            if isinstance(rows, dict):  # 防御: 个别版本 data 为 dict
                rows = (rows.get("list") or rows.get("items")
                        or rows.get("files") or [])
            if not isinstance(rows, list):
                rows = []
            if os.environ.get("STRMHUB_DEBUG") and not offset:
                import logging
                logging.getLogger("strmhub.p115").info(
                    "fs_files cid=%s -> rows=%d keys=%s",
                    parent_id, len(rows),
                    [sorted(r.keys()) for r in rows[:2]])
            for row in rows:
                if not isinstance(row, dict):
                    continue
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

    def create_folder(self, parent_id: str, name: str) -> str:
        """创建目录, 返回新目录 id; 已存在则返回已有目录 id。"""
        self.gate.wait()
        resp = self.client.fs_mkdir(name, pid=int(parent_id or 0))
        self._check_blocked(resp)
        if resp.get("state"):
            return str(resp.get("cid") or 0)
        if resp.get("errno") == 20004:  # 目录已存在: 查找返回
            for item in self.list_files(parent_id):
                if item.is_dir and item.name == name:
                    return item.id
        raise RuntimeError(f"创建目录失败: {resp}")

    def move(self, item: FileItem, dest_parent_id: str,
             new_name: str | None = None) -> FileItem:
        """移动(可同时重命名)文件/目录。"""
        if new_name and new_name != item.name:
            self.gate.wait()
            r = self.client.fs_rename((item.id, new_name))
            self._check_blocked(r)
        self.gate.wait()
        data = self.client.fs_move([item.id], pid=dest_parent_id)
        self._check_blocked(data)
        return FileItem(id=item.id, name=new_name or item.name,
                        size=item.size, is_dir=item.is_dir)

    def ping(self) -> bool:
        self.gate.wait()
        info = self.client.user_info()
        return bool(info and info.get("state"))


def _row_to_item(row: dict) -> FileItem:
    """115 行数据 -> FileItem。

    目录行无 fid 键(id=cid, 子目录浏览用); 文件行有 fid/pick_code。
    兼容旧格式(t==1 标记目录)。
    """
    name = row.get("n") or row.get("name") or "?"
    is_dir = "fid" not in row or row.get("t") == 1
    if is_dir:
        fid = row.get("cid") or row.get("fid")
    else:
        fid = row.get("pick_code") or row.get("fid")
    return FileItem(
        id=str(fid or 0),
        name=name,
        size=int(row.get("s") or 0),
        is_dir=is_dir,
        mtime=float(row.get("tp") or row.get("t") or 0) if not is_dir else None,
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
