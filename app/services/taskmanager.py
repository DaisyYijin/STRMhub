"""任务管理器: 持久化 CRUD + 同步执行器 + 防并发。

设计来源: SmartStrm v0.4.7 任务管理器 —— 任务定义落库、单任务防并发、
执行状态持久化(重启不丢); 本 M1 提供同步执行, 后台线程由 API 层调度。
"""
from __future__ import annotations

import datetime as dt
import json
import threading

from sqlalchemy import select

from .. import config
from ..db.models import Task
from ..db.session import session_scope
from .account import AccountService
from .strm.service import StrmService, task_to_extensions

_accounts = AccountService()
_strm = StrmService()
_running: set[int] = set()
_lock = threading.Lock()


class TaskManager:
    def create(self, account_id: int, name: str, remote_path: str, local_output: str,
               scan_mode: str = "incremental_missing",
               extensions: list[str] | None = None,
               base_url: str = "", token: str = "",
               extra: dict | None = None) -> Task:
        with session_scope() as s:
            task = Task(
                account_id=account_id,
                name=name,
                remote_path=remote_path,
                local_output=local_output,
                scan_mode=scan_mode,
                extra_json=json.dumps(extra or {}, ensure_ascii=False),
                extensions_json=json.dumps(extensions or []),
                base_url=base_url,
                token=token or _gen_token(),
            )
            s.add(task)
            s.flush()
            s.refresh(task)
            return task

    def list(self) -> list[Task]:
        with session_scope() as s:
            return list(s.scalars(select(Task).order_by(Task.id)))

    def get(self, task_id: int) -> Task | None:
        with session_scope() as s:
            return s.get(Task, task_id)

    def delete(self, task_id: int) -> bool:
        with session_scope() as s:
            t = s.get(Task, task_id)
            if t is None:
                return False
            s.delete(t)
            return True

    def is_running(self, task_id: int) -> bool:
        with _lock:
            return task_id in _running

    def run_sync(self, task_id: int) -> dict:
        """同步执行任务(带同任务防并发)。返回任务结果 dict。"""
        task = self.get(task_id)
        if task is None:
            raise KeyError(f"任务不存在: {task_id}")
        with _lock:
            if task_id in _running:
                raise RuntimeError("任务正在运行中")
            _running.add(task_id)
        try:
            account = _accounts.get(task.account_id)
            if account is None:
                raise KeyError(f"账户不存在: {task.account_id}")
            driver = _accounts.driver_for(account)
            extensions = task_to_extensions(task.extensions_json)
            snapshot = self._load_snapshot(account.id)
            extra = json.loads(task.extra_json or "{}")
            result = _strm.run(
                driver=driver,
                remote_path=task.remote_path,
                local_output=task.local_output,
                scan_mode=task.scan_mode,
                extensions=extensions,
                base_url=task.base_url or f"http://localhost:{config.ADMIN_PORT}",
                token=task.token,
                snapshot=snapshot,
                extra=extra,
            )
            if result.records:
                self._save_snapshot(account.id, result.records)
            self._update_status(task_id, "error" if result.error else "done",
                                result.error)
            return {
                "task_id": task_id,
                "generated": result.generated,
                "written": result.written,
                "skipped": result.skipped,
                "cleaned": result.cleaned,
                "cleanup_aborted": result.cleanup_aborted,
                "total_remote": result.total_remote,
                "error": result.error,
            }
        except Exception as exc:  # 统一落库为 error
            self._update_status(task_id, "error", str(exc))
            raise
        finally:
            with _lock:
                _running.discard(task_id)

    def _load_snapshot(self, account_id: int) -> dict | None:
        """读取账户文件索引快照: {path: (size, mtime)}; 无记录返回 None。"""
        from sqlalchemy import select
        from ..db.models import FileIndex
        with session_scope() as s:
            rows = s.execute(
                select(FileIndex.path, FileIndex.size, FileIndex.mtime)
                .where(FileIndex.account_id == account_id)).all()
        if not rows:
            return None
        return {p: (size, mtime) for p, size, mtime in rows}

    def _save_snapshot(self, account_id: int, records: list) -> None:
        """全量替换账户快照(先删后插, 单事务)。"""
        from sqlalchemy import delete
        from ..db.models import FileIndex
        with session_scope() as s:
            s.execute(delete(FileIndex).where(FileIndex.account_id == account_id))
            for path, file_key, size, mtime in records:
                s.add(FileIndex(account_id=account_id, path=path,
                                file_key=file_key, size=size, mtime=mtime))

    def _update_status(self, task_id: int, status: str, error: str = "") -> None:
        with session_scope() as s:
            t = s.get(Task, task_id)
            if t is None:
                return
            t.status = status
            t.last_error = error
            t.last_run_at = dt.datetime.now(dt.UTC)

    @staticmethod
    def to_dict(t: Task) -> dict:
        return {
            "id": t.id,
            "account_id": t.account_id,
            "name": t.name,
            "remote_path": t.remote_path,
            "local_output": t.local_output,
            "scan_mode": t.scan_mode,
            "extensions": json.loads(t.extensions_json or "[]"),
            "extra": json.loads(t.extra_json or "{}"),
            "base_url": t.base_url,
            "token": t.token,
            "status": t.status,
            "last_run_at": t.last_run_at.isoformat() if t.last_run_at else None,
            "last_error": t.last_error,
            "created_at": t.created_at.isoformat() if t.created_at else None,
        }


def _gen_token() -> str:
    import secrets
    return "lpk_strm_" + secrets.token_hex(16)
