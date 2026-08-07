"""驱动配置 API: 驱动级规则(识别/重命名/分类/目录, 不随账户)。"""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException

from ..drivers import registry
from ..services import driver_config
from .accounts import _normalize_rules
from .auth import require_user

router = APIRouter(prefix="/api/drivers", tags=["drivers"])


def _check_driver(name: str) -> None:
    if not registry.get_meta(name):
        raise HTTPException(status_code=404, detail=f"未知驱动: {name}")


@router.get("/{name}/rules")
def get_driver_rules(name: str, _: str = Depends(require_user)):
    """驱动级规则(未创建账户也能配置)。"""
    _check_driver(name)
    return {"driver_type": name, "rules": driver_config.get_config(name).get("rules") or {}}


@router.put("/{name}/rules")
def save_driver_rules(name: str, body: dict, _: str = Depends(require_user)):
    """保存驱动级规则(白名单校验, YAML 分类策略校验)。"""
    _check_driver(name)
    try:
        rules = _normalize_rules(body.get("rules") or {})
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    cfg = driver_config.get_config(name)
    cfg["rules"] = rules
    driver_config.save_config(name, cfg)
    return {"ok": True, "rules": rules}
