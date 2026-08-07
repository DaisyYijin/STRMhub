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


# ---- 整理归档规则配置(识别/重命名/分类/AI 等, 按网盘账户独立) ----

RULES_FIELDS = {
    "min_video_size_mb", "blacklist", "custom_words", "custom_matches",
    "release_groups", "rename_template", "category_rules", "ai",
    # 三目录整理 + 5 段重命名模板
    "organize_dirs", "rename_movie_folder", "rename_movie_file",
    "rename_tv_folder", "rename_season_folder", "rename_episode_file",
}


def _normalize_rules(rules: dict) -> dict:
    """校验并规范化规则(仅保留白名单字段)。"""
    if not isinstance(rules, dict):
        raise ValueError("规则格式错误")
    out: dict = {}
    for key in RULES_FIELDS:
        if key in rules and rules[key] is not None:
            out[key] = rules[key]
    return out


@router.get("/{account_id}/rules")
def get_account_rules(account_id: int, _: str = Depends(require_user)):
    """网盘账户的整理规则配置。"""
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    config = json.loads(acc.config_json or "{}")
    return {"rules": config.get("rules") or {}}


@router.put("/{account_id}/rules")
def save_account_rules(account_id: int, body: dict,
                       _: str = Depends(require_user)):
    """保存网盘账户的整理规则(识别/重命名/分类/AI 等)。"""
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    try:
        rules = _normalize_rules(body.get("rules") or {})
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    config = json.loads(acc.config_json or "{}")
    config["rules"] = rules
    _accounts.update_config(account_id, config)
    return {"ok": True, "rules": rules}


@router.get("/{account_id}/browse")
def browse_account_dirs(account_id: int, parent: str = "",
                        _: str = Depends(require_user)):
    """目录树浏览: 列出账户驱动的子目录(供目录选择器, 无需填 cid)。

    返回 {"dirs": [{id, name}], "parent": ...}。
    """
    acc = _accounts.get(account_id)
    if acc is None:
        raise HTTPException(status_code=404, detail="账户不存在")
    try:
        driver = _accounts.driver_for(acc)
        items = driver.list_files(parent or "0")
    except Exception as exc:
        raise HTTPException(status_code=400, detail=f"浏览目录失败: {exc}")
    return {"parent": parent or "0",
            "dirs": [{"id": it.id, "name": it.name}
                      for it in items if it.is_dir]}


@router.get("/drivers")
def list_driver_types(_: str = Depends(require_user)):
    return [{"name": m.name, "label": m.label, "auth_type": m.auth_type,
             "read_only": m.read_only} for m in registry.list_drivers()]
