"""账户 API: CRUD + 驱动列表。"""
from __future__ import annotations

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


@router.get("/drivers")
def list_driver_types(_: str = Depends(require_user)):
    return [{"name": m.name, "label": m.label, "auth_type": m.auth_type,
             "read_only": m.read_only} for m in registry.list_drivers()]
