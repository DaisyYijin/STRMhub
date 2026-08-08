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


class LifeIn(BaseModel):
    monitor_life: bool = False
    interval: int = 10


@router.get("/{task_id}/life")
def get_life(task_id: int, _: str = Depends(require_user)):
    """生活事件监控状态(游标/最近事件/统计)。"""
    task = _tasks.get(task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="任务不存在")
    import json as _json
    try:
        cursor = _json.loads(task.life_cursor_json or "{}")
    except ValueError:
        cursor = {}
    return {
        "monitor_life": bool(task.monitor_life),
        "interval": int(cursor.get("interval") or 10),
        "from_id": cursor.get("from_id", 0),
        "from_time": cursor.get("from_time", 0),
        "last_event_at": cursor.get("last_event_at"),
        "processed": cursor.get("processed", 0),
    }


@router.put("/{task_id}/life")
def set_life(task_id: int, body: LifeIn, _: str = Depends(require_user)):
    """开启/关闭生活事件监控(推送式增量, 秒级感知网盘变化)。"""
    task = _tasks.get(task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="任务不存在")
    import json as _json
    try:
        cursor = _json.loads(task.life_cursor_json or "{}")
    except ValueError:
        cursor = {}
    cursor["interval"] = body.interval
    task.monitor_life = body.monitor_life
    task.life_cursor_json = _json.dumps(cursor, ensure_ascii=False)
    from ..db.session import session_scope
    with session_scope() as s:
        s.add(task)
    return {"ok": True, "monitor_life": body.monitor_life,
            "interval": body.interval}
