"""启动器: 同时启动管理服务(ADMIN_PORT=6060)与 Emby 302 反代(PROXY_PORT=6086)。

用法: python -m app.launcher
"""
from __future__ import annotations

import threading

import uvicorn

from . import config
from .main import app as admin_app
from .proxy.server import app as proxy_app


def _serve(app, port: int, log_name: str,
           quiet_startup: bool = False) -> None:
    log_config = None
    if quiet_startup:
        # 反代服务不打印启动消息, 避免与管理服务重复
        import copy
        log_config = copy.deepcopy(uvicorn.config.LOGGING_CONFIG)
        for name in ("uvicorn", "uvicorn.error"):
            log_config["loggers"][name]["level"] = "WARNING"
    uvicorn.run(app, host="0.0.0.0", port=port, log_level="info",
                access_log=True, log_config=log_config)


def main() -> None:
    print(f"[STRMhub] 管理服务: http://0.0.0.0:{config.ADMIN_PORT}")
    print(f"[STRMhub] Emby 302 反代: http://0.0.0.0:{config.PROXY_PORT}")
    # 反代在独立线程运行; 主线程跑管理服务(ctrl-c 同时退出)
    t = threading.Thread(
        target=_serve, args=(proxy_app, config.PROXY_PORT, "proxy"),
        kwargs={"quiet_startup": True}, daemon=True)
    t.start()
    _serve(admin_app, config.ADMIN_PORT, "admin")


if __name__ == "__main__":
    main()
