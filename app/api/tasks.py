"""任务 API: CRUD + 手动触发执行。"""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..services.taskmanager import TaskManager
from .auth import require_user

router = APIRouter(prefix="/api/tasks", tags=["tasks"])
_tasks = TaskManager()


class TaskIn(BaseModel):
    account_id: int
    name: str
    remote_path: str
    local_output: str
    scan_mode: str = "incremental_missing"
    extensions: list[str] = []
    base_url: str = ""
    token: str = ""


@router.get("")
def list_tasks(_: str = Depends(require_user)):
    return [_tasks.to_dict(t) for t in _tasks.list()]


@router.post("")
def create_task(body: TaskIn, _: str = Depends(require_user)):
    try:
        t = _tasks.create(
            account_id=body.account_id, name=body.name,
            remote_path=body.remote_path, local_output=body.local_output,
            scan_mode=body.scan_mode, extensions=body.extensions,
            base_url=body.base_url, token=body.token)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    return _tasks.to_dict(t)


@router.delete("/{task_id}")
def delete_task(task_id: int, _: str = Depends(require_user)):
    if not _tasks.delete(task_id):
        raise HTTPException(status_code=404, detail="任务不存在")
    return {"ok": True}


@router.post("/{task_id}/run")
def run_task(task_id: int, _: str = Depends(require_user)):
    try:
        return _tasks.run_sync(task_id)
    except KeyError as exc:
        raise HTTPException(status_code=404, detail=str(exc))
    except RuntimeError as exc:
        raise HTTPException(status_code=409, detail=str(exc))
