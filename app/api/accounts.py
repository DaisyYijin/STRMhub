"""账户 API: CRUD + 驱动列表 + 目录管理(每网盘独立目录)。"""
from __future__ import annotations

import json

from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel

from ..drivers import registry
from ..services.account import AccountService
from .auth import require_user

router = APIRouter(prefix="/api/accounts", tags=["accounts"])
_accounts = AccountService()


class AccountIn(BaseModel):
    name: str
    driver_type: str
    credential: str = ""
    config: dict = {}


class DirIn(BaseModel):
    path: str


@router.get("")
def list_accounts(_: str = Depends(require_user)):
    return [_accounts.to_dict(a) for a in _accounts.list()]


@router.post("")
def create_account(body: AccountIn, _: str = Depends(require_user)):
    try:
        acc = _accounts.create(body.name, body.driver_type, body.credential, body.config)
    except (KeyError, ValueError) as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    return _accounts.to_dict(acc)


@router.delete("/{account_id}")
def delete_account(account_id: int, _: str = Depends(require_user)):
    if not _accounts.delete(account_id):
        raise HTTPException(status_code=404, detail="账户不存在")
    return {"ok": True}


@router.get("/{account_id}/dirs")
def list_account_dirs(account_id: int, _: str = Depends(require_user)):
    """网盘账户的目录列表。"""
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    config = json.loads(acc.config_json or "{}")
    return {"dirs": config.get("dirs") or []}


@router.post("/{account_id}/dirs")
def add_account_dir(account_id: int, body: DirIn,
                    _: str = Depends(require_user)):
    """为网盘账户添加目录(独立于其他网盘)。"""
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    path = body.path.strip().strip("/")
    if not path:
        raise HTTPException(status_code=400, detail="目录不能为空")
    config = json.loads(acc.config_json or "{}")
    dirs = config.get("dirs") or []
    if path in dirs:
        raise HTTPException(status_code=400, detail=f"目录已存在: /{path}")
    dirs.append(path)
    config["dirs"] = dirs
    _accounts.update_config(account_id, config)
    return {"ok": True, "dirs": dirs}


@router.delete("/{account_id}/dirs/{index}")
def remove_account_dir(account_id: int, index: int,
                       _: str = Depends(require_user)):
    """删除网盘账户的目录。"""
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    config = json.loads(acc.config_json or "{}")
    dirs = config.get("dirs") or []
    if index < 0 or index >= len(dirs):
        raise HTTPException(status_code=404, detail="目录不存在")
    dirs.pop(index)
    config["dirs"] = dirs
    _accounts.update_config(account_id, config)
    return {"ok": True, "dirs": dirs}


@router.get("/drivers")
def list_driver_types(_: str = Depends(require_user)):
    return [{"name": m.name, "label": m.label, "auth_type": m.auth_type,
             "read_only": m.read_only} for m in registry.list_drivers()]
