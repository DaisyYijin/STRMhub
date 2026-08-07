"""驱动层包: 自动注册内置驱动(网盘驱动为延迟依赖, 未安装对应 SDK 时仅注册不可用)。"""
from . import registry
from .base import FileItem, DriverMeta, Driver
from .local.driver import register as _register_local
from .p115.driver import register as _register_p115
from .p123.driver import register as _register_p123

_register_local()
_register_p115()
_register_p123()

__all__ = ["registry", "FileItem", "DriverMeta", "Driver"]
