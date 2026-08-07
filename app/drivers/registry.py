"""驱动注册表: 注册/获取/能力探测。

设计来源: LitePan internal/driver/registry.go —— 注册表 + 表单 schema 生成。
M1 实现注册与获取; 表单 schema 生成留待 M2 与前端对接。
"""
from __future__ import annotations

from typing import Any, Callable, Optional

from .base import DriverMeta

# driver_type -> 工厂 (factory(credential, config) -> Driver)
_FACTORIES: dict[str, Callable[..., Any]] = {}
_METAS: dict[str, DriverMeta] = {}


def register(factory: Callable[..., Any], meta: DriverMeta) -> None:
    _FACTORIES[meta.name] = factory
    _METAS[meta.name] = meta


def get_meta(driver_type: str) -> Optional[DriverMeta]:
    return _METAS.get(driver_type)


def list_drivers() -> list[DriverMeta]:
    return sorted(_METAS.values(), key=lambda m: m.name)


def create(driver_type: str, credential: str = "", config: Optional[dict] = None) -> Any:
    """按类型实例化驱动。凭据与配置由工厂自行消费。"""
    factory = _FACTORIES.get(driver_type)
    if factory is None:
        raise KeyError(f"未知驱动类型: {driver_type}")
    return factory(credential=credential, config=config or {})


def supports(driver: Any, capability: str) -> bool:
    """能力探测: 检查驱动实例是否实现对应能力接口(按方法名判断)。"""
    method_map = {
        "download": "resolve_download",
        "delete": "delete_file",
        "mkdir": "create_folder",
        "move": "move",
        "rapid_upload": "rapid_upload",
    }
    return hasattr(driver, method_map[capability])
