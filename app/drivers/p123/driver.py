"""123 云盘驱动(基于 p123client 真实 API)。

- 登录: 扫码登录(login_qrcode_generate/result)拿 token, 或直接 token/手机号:密码。
- credential 存储: 扫码 token(优先), 兼容 "手机号:密码" / 纯 token。
- 调用契约(p123client >= 0.0.5): fs_list(payload dict), fs_mkdir({name,parentID}),
  fs_move({fileIDs,toParentFileID}), fs_rename({renameList:["id|name"]}),
  download_info(fileId), user_info()。
- 目录行: Type == 0(123 open API 文件夹 Type 为 0)。
"""
from __future__ import annotations

from ..base import DriverMeta, FileItem
from ..common import AccountGate, is_blocked_response


class P123Driver:
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
                from p123client import P123Client
            except ImportError as exc:
                raise ImportError(
                    "123 驱动需要可选依赖 p123client: pip install p123client"
                ) from exc
            if ":" in credential:
                phone, password = credential.split(":", 1)
                self.client = P123Client(phone, password)
            else:
                # token 或空(扫码后以 token 创建)
                self.client = P123Client(token=credential or None)

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

    def list_files(self, parent_id: str = "0", only_dirs: bool = False) -> list[FileItem]:
        """列目录(v2 接口, lastFileId 翻页)。"""
        items: list[FileItem] = []
        last_id: int | None = None
        while True:
            self.gate.wait()
            payload: dict = {"parentFileId": int(parent_id or 0), "limit": 100,
                             "trashed": False}
            if last_id:
                payload["lastFileId"] = last_id
            data = self.client.fs_list(payload)
            self._check_blocked(data)
            body = data.get("data") or {}
            rows = body.get("fileList") or []
            for row in rows:
                item = _row_to_item(row)
                if only_dirs and not item.is_dir:
                    continue
                items.append(item)
            marker = body.get("lastFileId")
            # 防呆: -1 最后一页 / 无 rows / 接口未返回翻页标记 -> 停止
            if marker == -1 or not rows or marker is None:
                break
            last_id = marker
        return items

    def resolve_download(self, item: FileItem) -> tuple[str, dict]:
        """取下载直链(open API download_info)。"""
        self.gate.wait()
        data = self.client.download_info(int(item.id))
        self._check_blocked(data)
        body = data.get("data") or {}
        url = body.get("DownloadUrl") or body.get("downloadUrl") or ""
        return url, {}

    def create_folder(self, parent_id: str, name: str) -> str:
        """创建目录, 返回新目录 FileID; 已存在则回查返回。"""
        self.gate.wait()
        data = self.client.fs_mkdir({"name": name, "parentID": int(parent_id or 0)})
        self._check_blocked(data)
        body = data.get("data") or {}
        fid = body.get("FileID") or body.get("fileID") or body.get("fileId")
        if fid is not None:
            return str(fid)
        # 已存在(重名): 列父目录查找
        for item in self.list_files(parent_id):
            if item.is_dir and item.name == name:
                return item.id
        raise RuntimeError(f"创建目录失败: {data}")

    def move(self, item: FileItem, dest_parent_id: str,
             new_name: str | None = None) -> FileItem:
        """移动(可同时重命名)。"""
        if new_name and new_name != item.name:
            self.gate.wait()
            r = self.client.fs_rename({"renameList": [f"{int(item.id)}|{new_name}"]})
            self._check_blocked(r)
        self.gate.wait()
        data = self.client.fs_move({"fileIDs": [int(item.id)],
                                    "toParentFileID": int(dest_parent_id or 0)})
        self._check_blocked(data)
        return FileItem(id=item.id, name=new_name or item.name,
                        size=item.size, is_dir=item.is_dir)

    def ping(self) -> bool:
        self.gate.wait()
        info = self.client.user_info()
        self._check_blocked(info)
        return bool(info.get("data"))


def _row_to_item(row: dict) -> FileItem:
    """123 open API 文件行 -> FileItem(目录 Type==0)。"""
    name = row.get("FileName") or row.get("fileName") or row.get("name") or "?"
    fid = row.get("FileID") or row.get("fileID") or row.get("fileId") or 0
    is_dir = int(row.get("Type") or row.get("type") or 0) == 0
    return FileItem(
        id=str(fid),
        name=name,
        size=int(row.get("Size") or row.get("size") or 0),
        is_dir=is_dir,
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
