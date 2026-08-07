"""网盘扫码登录服务(当前支持 115)。

115 流程(p115client):
  1. login_qrcode_token() -> (qr_token, qr_uid)
  2. 前端展示二维码 https://qrcodeapi.115.com/api/1.0/web/1.0/token?qr_token=&qr_uid=
  3. 轮询 login_qrcode_scan(qr_token, qr_uid):
     data.status: 0=等待扫码 1=已扫码待确认 2=确认成功
  4. 成功后从 client.cookies 提取 Cookie 字符串, 用于创建账户

实现为防御式(探测方法名/返回值形态), 真实联调需安装 p115client。
"""
from __future__ import annotations

import json


class QrcodeLoginService:
    def start(self, driver_type: str, client=None) -> dict:
        """生成二维码, 返回 {driver_type, qr_token, qr_uid, image_url}。"""
        client = client or self._make_client(driver_type)
        if driver_type == "p115":
            if not hasattr(client, "login_qrcode_token"):
                raise RuntimeError("p115client 版本不支持扫码登录, 请升级")
            tok = client.login_qrcode_token()
            if isinstance(tok, tuple) and len(tok) >= 2:
                qr_token, qr_uid = tok[0], tok[1]
            else:
                qr_token = (tok or {}).get("qr_token") or (tok or {}).get("token")
                qr_uid = (tok or {}).get("qr_uid") or (tok or {}).get("uid")
            if not qr_token or not qr_uid:
                raise RuntimeError("获取二维码失败")
            image_url = (
                "https://qrcodeapi.115.com/api/1.0/web/1.0/token"
                f"?qr_token={qr_token}&qr_uid={qr_uid}")
            return {"driver_type": driver_type, "qr_token": qr_token,
                    "qr_uid": qr_uid, "image_url": image_url}
        raise ValueError(f"驱动 {driver_type} 不支持扫码登录")

    def poll(self, driver_type: str, qr_token: str, qr_uid: str,
             client=None) -> dict:
        """轮询扫码状态。

        返回: {"status": "waiting"|"scanned"|"confirmed"|"expired"|"error",
               "cookies": "k=v; ..."(仅 confirmed)}
        """
        client = client or self._make_client(driver_type)
        if driver_type == "p115":
            if not hasattr(client, "login_qrcode_scan"):
                raise RuntimeError("p115client 版本不支持扫码轮询")
            result = client.login_qrcode_scan(qr_token, qr_uid)
            data = result.get("data") or {} if isinstance(result, dict) else {}
            status = int(data.get("status") or 0)
            if status == 2:
                cookies = self._extract_cookies(client)
                return {"status": "confirmed", "cookies": cookies}
            if status == 1:
                return {"status": "scanned"}
            if status in (-1, -2, -3):
                return {"status": "expired"}
            return {"status": "waiting"}
        raise ValueError(f"驱动 {driver_type} 不支持扫码登录")

    @staticmethod
    def _extract_cookies(client) -> str:
        """从 client 提取 Cookie 字符串。"""
        try:
            cookies = client.cookies
        except AttributeError:
            return ""
        if isinstance(cookies, dict):
            return "; ".join(f"{k}={v}" for k, v in cookies.items())
        # http.cookiejar 形态
        try:
            return "; ".join(f"{c.name}={c.value}" for c in cookies)
        except TypeError:
            return str(cookies)

    @staticmethod
    def _make_client(driver_type: str):
        if driver_type == "p115":
            try:
                from p115client import P115Client
            except ImportError as exc:
                raise RuntimeError(
                    "115 扫码登录需要 p115client: pip install p115client") from exc
            return P115Client()
        raise ValueError(f"驱动 {driver_type} 不支持扫码登录")


qrcode_login = QrcodeLoginService()
