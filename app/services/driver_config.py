"""驱动级配置服务: 每个驱动的规则/目录独立存储(不随账户)。

设计动机: 规则(识别/重命名/分类/目录)属于"驱动类型"而非单个账户,
用户未创建账户时也能配置; 整理执行时按账户所属驱动读取。
"""
from __future__ import annotations

import json

from sqlalchemy import select

from ..db.models import DriverConfig
from ..db.session import session_scope


def get_config(driver_type: str) -> dict:
    """读取驱动配置(无则返回空 dict)。"""
    with session_scope() as s:
        row = s.get(DriverConfig, driver_type)
        return json.loads(row.config_json) if row else {}


def save_config(driver_type: str, config: dict) -> None:
    """保存驱动配置(upsert)。"""
    with session_scope() as s:
        row = s.get(DriverConfig, driver_type)
        if row is None:
            row = DriverConfig(driver_type=driver_type,
                               config_json=json.dumps(config or {}, ensure_ascii=False))
            s.add(row)
        else:
            row.config_json = json.dumps(config or {}, ensure_ascii=False)


def get_rules(driver_type: str, fallback_rules: dict | None = None) -> dict:
    """读取驱动的规则; 空时回退到给定规则(旧账户级数据迁移兼容)。"""
    cfg = get_config(driver_type)
    if cfg.get("rules"):
        return cfg["rules"]
    if fallback_rules:
        return fallback_rules
    return cfg.get("rules") or {}
