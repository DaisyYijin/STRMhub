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
    """保存驱动级规则(按字段合并, 白名单校验)。

    前端每个 tab 只提交自己的字段; 后端合并进现有规则,
    避免多标签页/多 tab 之间用旧值全量覆盖(目录被覆盖丢失)。
    """
    _check_driver(name)
    try:
        patch = _normalize_rules(body.get("rules") or {})
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc))
    cfg = driver_config.get_config(name)
    existing = cfg.get("rules") or {}
    existing.update(patch)  # 只更新提交的字段, 其余保留
    cfg["rules"] = existing
    driver_config.save_config(name, cfg)
    return {"ok": True, "rules": existing}
