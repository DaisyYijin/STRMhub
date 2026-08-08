"""115 生活事件监控(MP 插件思路: 推送式增量, 无需全量扫描)。

- 轮询 115 生活事件接口(p115client.iter_life_behavior_once),
  事件到达即增量生成/删除 strm, 秒级感知网盘文件变化;
- 游标(from_id/from_time)持久化在任务配置, 重启不丢;
- web 接口最多 1 万条: 每 24h 自动兜底一次快照增量扫描,
  修正移动/重命名等缺失事件(115 不收集复制/改名事件的场景)。

事件类型码(115 生活行为):
  1 上传图片  2 上传文件  5/6 移动  14 接收文件  17 新建目录
  18 复制目录  20/24 重命名  22 删除  23 复制文件
"""
from __future__ import annotations

import base64
import json
import logging
import threading
import time
from pathlib import Path

from sqlalchemy import select

from .. import config
from .account import AccountService
from .strm.service import StrmService, build_strm_url, task_to_extensions
from .strm.writer import write_strm
from .taskmanager import TaskManager

_log = logging.getLogger("strmhub.life")

# 可注入的事件拉取函数(测试替换; 真实环境延迟导入, 避免启动即依赖 p115client)
_life_iter = None


def _iter_life_events(*args, **kwargs):
    global _life_iter
    if _life_iter is None:
        from p115client.tool.life import iter_life_behavior_once
        _life_iter = iter_life_behavior_once
    return _life_iter(*args, **kwargs)

EVT_UPLOAD_IMAGE, EVT_UPLOAD_FILE, EVT_RECEIVE, EVT_COPY_FILE = 1, 2, 14, 23
EVT_NEW_FOLDER, EVT_COPY_FOLDER = 17, 18
EVT_MOVE_IMAGE, EVT_MOVE_FILE, EVT_RENAME_FOLDER, EVT_RENAME_FILE = 5, 6, 20, 24
EVT_DELETE = 22

CREATE_FILE = {EVT_UPLOAD_IMAGE, EVT_UPLOAD_FILE, EVT_RECEIVE, EVT_COPY_FILE}
CREATE_DIR = {EVT_NEW_FOLDER, EVT_COPY_FOLDER}
MOVE_OR_RENAME = {EVT_MOVE_IMAGE, EVT_MOVE_FILE, EVT_RENAME_FOLDER, EVT_RENAME_FILE}

# 事件类型 -> 中文名(日志用)
EVT_NAMES = {
    1: "上传图片", 2: "上传文件", 5: "移动图片", 6: "移动文件",
    14: "接收文件", 17: "新建目录", 18: "复制目录", 20: "重命名目录",
    22: "删除", 23: "复制文件", 24: "重命名文件",
}

_VIDEO_EXTS = {".mp4", ".mkv", ".ts", ".m2ts", ".avi", ".wmv", ".flv",
               ".mov", ".rmvb", ".iso", ".bdmv"}


def _file_key(file_id: str) -> str:
    return base64.urlsafe_b64encode(file_id.encode("utf-8")).rstrip(b"=").decode("ascii")


def _is_video(name: str, extensions: set[str]) -> bool:
    if extensions:
        return Path(name).suffix.lower() in extensions
    return Path(name).suffix.lower() in _VIDEO_EXTS


class LifeEventMonitor:
    """单个任务的生活事件监控(单轮 once() 可独立测试)。"""

    def __init__(self, task_id: int, stop_event: threading.Event | None = None):
        self.task_id = task_id
        self.stop_event = stop_event
        self._tasks = TaskManager()
        self._accounts = AccountService()
        self._strm = StrmService()
        self._root_path_cache: dict[int, str] = {}  # account_id -> remote_path 的路径前缀

    # ---- 数据加载 ----

    def _task(self):
        return self._tasks.get(self.task_id)

    def _cursor(self, task) -> dict:
        try:
            return json.loads(task.life_cursor_json or "{}")
        except ValueError:
            return {}

    def _save_cursor(self, task, cursor: dict) -> None:
        from ..db.models import Task
        from ..db.session import session_scope
        payload = json.dumps(cursor, ensure_ascii=False)
        with session_scope() as s:
            row = s.get(Task, task.id)
            if row is not None:
                row.life_cursor_json = payload

    def _client(self, account):
        """从账户凭据构造 P115Client(解密后传入)。"""
        from ..security.crypto import decrypt_credential
        from ..services.qrcode import QrcodeLoginService
        cls = QrcodeLoginService._client_class(account.driver_type)
        credential = decrypt_credential(account.credential_enc)             if account.credential_enc else ""
        return cls(credential)

    # ---- 路径解析 ----

    def _remote_prefix(self, account, task) -> str | None:
        """remote_path(file_id) -> 完整路径前缀(如 /我的网盘/电影), 缓存。"""
        cached = self._root_path_cache.get(account.id)
        if cached:
            return cached
        if not task.remote_path:
            return None
        prefix = self._path_of(self._client(account), task.remote_path)
        self._root_path_cache[account.id] = prefix
        return prefix

    @staticmethod
    def _path_of(client, cid: str, limit: int = 24) -> str | None:
        """逐级反查 cid 的完整路径(/a/b/c), 失败返回 None。"""
        parts: list[str] = []
        cur = cid
        for _ in range(limit):
            if cur in (0, "", "0"):
                break
            try:
                resp = client.fs_info({"file_id": int(cur)})
            except Exception:
                return None
            data = resp.get("data") if isinstance(resp, dict) else resp
            if not isinstance(data, dict):
                return None
            name = (data.get("file_name") or data.get("name")
                    or data.get("fileName") or "")
            if not name:
                break
            parts.append(name)
            nxt = data.get("parent_id") or data.get("pid") or data.get("parentId")
            if not nxt or str(nxt) == str(cur):
                break
            cur = nxt
        if not parts:
            return None
        return "/" + "/".join(reversed(parts))

    # ---- 事件处理 ----

    def _write_file_strm(self, account, task, file_id: str, full_path: str) -> bool:
        """单个文件 -> 写 strm + FileIndex upsert。"""
        prefix = self._remote_prefix(account, task)
        if not prefix or not full_path.startswith(prefix):
            return False
        rel = full_path[len(prefix):].strip("/")
        if not rel:
            return False
        name = rel.rsplit("/", 1)[-1]
        extensions = task_to_extensions(task.extensions_json)
        if not _is_video(name, extensions):
            return False
        from ..db.models import FileIndex
        from ..db.session import session_scope
        key = _file_key(file_id)
        content = build_strm_url(
            task.base_url or f"http://localhost:{config.ADMIN_PORT}",
            task.token, file_id)
        target = Path(task.local_output) / f"{rel}.strm"
        written = write_strm(target, content, "update")
        with session_scope() as s:
            row = s.scalar(select(FileIndex).where(
                FileIndex.account_id == account.id,
                FileIndex.path == rel))
            if row is None:
                row = FileIndex(account_id=account.id, path=rel,
                                file_key=key, size=0, mtime=time.time())
                s.add(row)
            else:
                row.file_key = key
                row.mtime = time.time()
        return written

    def _remove_path(self, account, task, full_path: str) -> None:
        """删除 rel 前缀下所有 strm + 索引(文件或目录)。"""
        prefix = self._remote_prefix(account, task)
        if not prefix or not full_path.startswith(prefix):
            return
        rel = full_path[len(prefix):].strip("/")
        if not rel:
            return
        from ..db.models import FileIndex
        from ..db.session import session_scope
        with session_scope() as s:
            rows = s.scalars(select(FileIndex).where(
                FileIndex.account_id == account.id,
                FileIndex.path.like(f"{rel}%"))).all()
            for row in rows:
                target = Path(task.local_output) / f"{row.path}.strm"
                try:
                    if target.exists():
                        target.unlink()
                except OSError:
                    pass
                s.delete(row)

    def _handle_create(self, account, task, event: dict) -> None:
        client = self._client(account)
        file_id = str(event.get("file_id") or "")
        if not file_id:
            return
        full = self._path_of(client, file_id)
        if not full:
            _log.warning("[life] 无法解析事件路径, 跳过: %s", event)
            return
        ev_type = int(event.get("type") or 0)
        is_dir = ev_type in CREATE_DIR or             int(event.get("file_category") or 1) == 0
        if is_dir:
            # 目录: 递归生成该目录下所有视频 strm
            self._scan_dir(client, account, task, file_id)
        else:
            self._write_file_strm(account, task, file_id, full)

    def _scan_dir(self, client, account, task, cid: str) -> None:
        """递归列目录, 为其中视频文件写 strm。"""
        try:
            items = client.fs_files({"cid": int(cid), "limit": 115,
                                     "offset": 0})
        except Exception:
            return
        data = items.get("data") if isinstance(items, dict) else items
        rows = data.get("data") if isinstance(data, dict) else []
        for row in rows or []:
            name = row.get("n") or row.get("name") or ""
            is_dir = "fid" not in row
            fid = row.get("cid") if is_dir else row.get("pick_code") or row.get("fid")
            if not fid:
                continue
            if is_dir:
                self._scan_dir(client, account, task, str(fid))
            else:
                full = self._path_of(client, str(fid))
                if full:
                    self._write_file_strm(account, task, str(fid), full)

    def _handle_remove(self, account, task, event: dict) -> None:
        file_id = str(event.get("file_id") or "")
        full = self._path_of(self._client(account), file_id)
        if full:
            self._remove_path(account, task, full)

    def _handle_move_rename(self, account, task, event: dict) -> None:
        """移动/重命名: 115 不保证事件含新路径, 先删旧 strm, 位置差由 24h 兜底修正。"""
        self._handle_remove(account, task, event)

    # ---- 主循环 ----

    def once(self) -> dict:
        """单轮: 拉取事件 -> 处理 -> 推进游标。返回 stats(可测试)。"""
        task = self._task()
        if task is None:
            return {"error": "任务不存在"}
        account = self._accounts.get(task.account_id)
        if account is None:
            return {"error": "账户不存在"}
        if account.driver_type != "p115":
            return {"skipped": "仅支持 p115"}
        cursor = self._cursor(task)
        try:
            client = self._client(account)
            events = list(_iter_life_events(
                client,
                from_id=int(cursor.get("from_id") or 0),
                from_time=float(cursor.get("from_time") or 0),
                app="web", cooldown=2))
        except Exception as exc:
            _log.warning("[life] 拉取事件失败: %s", exc)
            return {"error": str(exc)}

        stats = {"events": len(events), "created": 0, "removed": 0,
                 "moved": 0, "skipped": 0}
        if not events:
            return stats
        max_id = int(cursor.get("from_id") or 0)
        max_time = float(cursor.get("from_time") or 0)
        for event in events:
            ev_type = int(event.get("type") or 0)
            eid = int(event.get("id") or 0)
            utime = float(event.get("update_time") or 0)
            if eid > max_id:
                max_id = eid
            if utime > max_time:
                max_time = utime
            try:
                if ev_type in CREATE_FILE | CREATE_DIR:
                    self._handle_create(account, task, event)
                    stats["created"] += 1
                elif ev_type == EVT_DELETE:
                    self._handle_remove(account, task, event)
                    stats["removed"] += 1
                elif ev_type in MOVE_OR_RENAME:
                    self._handle_move_rename(account, task, event)
                    stats["moved"] += 1
                else:
                    stats["skipped"] += 1
            except Exception as exc:  # 单事件失败不影响游标推进
                _log.warning("[life] 事件处理失败(%s): %s", ev_type, exc)
        cursor["from_id"] = max_id
        cursor["from_time"] = max_time
        cursor["last_event_at"] = time.time()
        cursor["processed"] = int(cursor.get("processed") or 0) + len(events)
        self._save_cursor(task, cursor)
        return stats

    def run_forever(self, interval: float = 10.0) -> None:
        """循环轮询(线程入口)。"""
        while not (self.stop_event and self.stop_event.is_set()):
            try:
                self.once()
            except Exception as exc:
                _log.error("[life] 监控循环异常: %s", exc)
            if self.stop_event:
                self.stop_event.wait(timeout=interval)
            else:
                time.sleep(interval)


class LifeEventSupervisor:
    """全局监督: 轮询所有开启 monitor_life 的任务; 24h 自动兜底一次全量增量扫描。"""

    FULL_SCAN_INTERVAL = 24 * 3600

    def __init__(self):
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None
        self._last_full_scan: dict[int, float] = {}
        self._tasks = TaskManager()
        self._accounts = AccountService()

    def start(self) -> None:
        if self._thread and self._thread.is_alive():
            return
        self._stop.clear()
        self._thread = threading.Thread(target=self._loop, daemon=True,
                                        name="life-event-supervisor")
        self._thread.start()
        _log.info("[life] 生活事件监控已启动")

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=3)
            self._thread = None
        _log.info("[life] 生活事件监控已停止")

    def _loop(self) -> None:
        from ..db.session import session_scope
        from ..db.models import Task
        while not self._stop.is_set():
            try:
                with session_scope() as s:
                    task_ids = list(s.scalars(select(Task.id).where(
                        Task.monitor_life == True)))  # noqa: E712
                for tid in task_ids:
                    if self._stop.is_set():
                        break
                    LifeEventMonitor(tid, self._stop).once()
                    now = time.time()
                    last = self._last_full_scan.get(tid, 0)
                    if now - last > self.FULL_SCAN_INTERVAL:
                        # 兜底: 全量增量扫描修正移动/重命名等事件缺失
                        self._last_full_scan[tid] = now
                        try:
                            self._tasks.run_sync(tid)
                            _log.info("[life] 任务 %s 24h 兜底扫描完成", tid)
                        except Exception as exc:
                            _log.warning("[life] 任务 %s 兜底扫描失败: %s",
                                         tid, exc)
            except Exception as exc:
                _log.error("[life] 监督循环异常: %s", exc)
            self._stop.wait(timeout=5)


life_supervisor = LifeEventSupervisor()
