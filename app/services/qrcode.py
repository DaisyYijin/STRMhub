"""网盘扫码登录服务(当前支持 115, 基于 p115client 0.0.9.x 新 API)。

流程(p115strmhelper api.py 同款, 已验证):
  1. P115Client.login_qrcode_token() -> data: {uid, time, sign, qrcode}
     qrcode 为二维码内容(可能为空, 空则用 https://115.com/scan/dg-{uid})
  2. 前端展示二维码(SVG 由后端生成) + 选择登录设备(app)
  3. P115Client.login_qrcode_scan_status({"uid","time","sign"}) 轮询:
     data.status: 0=等待 1=已扫码待确认 2=确认成功 -1=过期 -2=用户取消
  4. status==2: P115Client.login_qrcode_scan_result(uid, app=设备)
     -> data.cookie dict -> "k=v; ..." cookie 字符串(用于创建账户)
"""
from __future__ import annotations

import io

# 设备 key -> 中文名(仅作 AVAILABLE_APPS 导入失败的兜底)
APP_LABELS = {
    "web": "网页版", "desktop": "桌面客户端", "android": "安卓",
    "harmony": "鸿蒙", "alipaymini": "支付宝小程序", "wechatmini": "微信小程序",
    "qandroid": "安卓(默认)", "ios": "iOS", "os_windows": "Windows",
}
DEFAULT_APPS = [("web", "网页版"), ("android", "安卓"), ("harmony", "鸿蒙"),
                ("alipaymini", "支付宝小程序"), ("wechatmini", "微信小程序")]


class QrcodeLoginService:
    def __init__(self, client_cls=None):
        # 可注入 fake 客户端类(测试用); None 时延迟导入 p115client.P115Client
        self.client_cls = client_cls

    def _client_class(self, driver_type: str):
        if driver_type != "p115":
            raise ValueError(f"驱动 {driver_type} 不支持扫码登录")
        if self.client_cls is not None:
            return self.client_cls
        try:
            from p115client import P115Client
        except ImportError as exc:
            raise RuntimeError(
                "115 扫码登录需要 p115client: pip install p115client") from exc
        return P115Client

    def start(self, driver_type: str) -> dict:
        """生成二维码, 返回 {driver_type, uid, time, sign, qr_image, apps}。"""
        cls = self._client_class(driver_type)
        if not hasattr(cls, "login_qrcode_token"):
            raise RuntimeError("p115client 版本不支持扫码登录, 请升级")
        resp = cls.login_qrcode_token()
        data = resp.get("data") or {}
        uid = data.get("uid")
        if not uid:
            raise RuntimeError(f"获取二维码失败: {resp}")
        content = data.get("qrcode") or f"https://115.com/scan/dg-{uid}"
        return {
            "driver_type": driver_type,
            "uid": str(uid),
            "time": str(data.get("time") or ""),
            "sign": str(data.get("sign") or ""),
            "qr_image": self._svg_data_uri(content),
            "apps": self._app_list(),
        }

    def poll(self, driver_type: str, uid: str, time: str, sign: str,
             app: str = "web") -> dict:
        """轮询扫码状态。

        返回 {"status": "waiting"|"scanned"|"confirmed"|"expired"|"cancelled"|"error",
              "cookies": "k=v; ..."(仅 confirmed)}
        """
        cls = self._client_class(driver_type)
        if not hasattr(cls, "login_qrcode_scan_status"):
            raise RuntimeError("p115client 版本不支持扫码轮询, 请升级")
        payload = {"uid": uid, "time": time, "sign": sign}
        result = cls.login_qrcode_scan_status(payload)
        status = int((result.get("data") or {}).get("status") or 0)
        if status == 2:
            return {"status": "confirmed",
                    "cookies": self._confirm(cls, uid, app)}
        if status == 1:
            return {"status": "scanned"}
        if status == -1:
            return {"status": "expired"}
        if status == -2:
            return {"status": "cancelled"}
        return {"status": "waiting"}

    @staticmethod
    def _confirm(cls, uid: str, app: str) -> str:
        """扫码确认后获取 cookie 字符串。"""
        if not hasattr(cls, "login_qrcode_scan_result"):
            raise RuntimeError("p115client 版本不支持扫码结果获取, 请升级")
        res = cls.login_qrcode_scan_result(uid, app=app)
        cookie = (res.get("data") or {}).get("cookie") or {}
        return "; ".join(f"{k}={v}" for k, v in cookie.items() if k and v)

    @staticmethod
    def _app_list() -> list[dict]:
        """可用登录设备列表: 直接用 p115client 官方 AVAILABLE_APPS 全量(18 种)。"""
        try:
            from p115client.const import AVAILABLE_APPS
            return [{"key": k, "label": v} for k, v in AVAILABLE_APPS.items()]
        except Exception:
            return [{"key": k, "label": v} for k, v in DEFAULT_APPS]

    @staticmethod
    def _app_label(app: str) -> str:
        """设备 key -> 官方中文名(供 info.device 展示)。"""
        for a in QrcodeLoginService._app_list():
            if a["key"] == app:
                return a["label"]
        return app

    def fetch_account_info(self, driver_type: str, credential: str) -> dict:
        """用凭据拉取账号基本信息(昵称/头像/容量/VIP)。失败时返回 {} 不影响登录。

        115: client.user_my_info() -> {uname, vip{is_vip,is_forever,expire_str}, face{face_s}}
             client.fs_index_info(payload=0) -> data.space_info{all_total, all_use, all_remain}
        """
        if driver_type != "p115":
            return {}
        try:
            cls = self._client_class(driver_type)
            client = cls(credential)
        except Exception:
            return {}
        info = {}
        try:
            data = (client.user_my_info() or {}).get("data") or {}
            info["nickname"] = data.get("uname") or ""
            vip = data.get("vip") or {}
            if vip.get("is_forever"):
                info["vip"] = "永久 VIP"
            elif vip.get("is_vip"):
                info["vip"] = "VIP"
            else:
                info["vip"] = ""
            if vip.get("expire_str"):
                info["vip_expire"] = vip["expire_str"]
            face = data.get("face") or {}
            if face.get("face_s"):
                info["avatar"] = face["face_s"]
        except Exception:
            pass
        try:
            space = ((client.fs_index_info(payload=0) or {}).get("data") or {})\
                .get("space_info") or {}
            total = space.get("all_total") or {}
            used = space.get("all_use") or {}
            info["total_size"] = total.get("size", 0)
            info["used_size"] = used.get("size", 0)
            info["total_size_fmt"] = total.get("size_format", "")
            info["used_size_fmt"] = used.get("size_format", "")
        except Exception:
            pass
        return info

    @staticmethod
    def _svg_data_uri(content: str) -> str:
        """二维码内容 -> SVG data URI(避免另开图片接口与防盗链问题)。"""
        import base64
        import qrcode
        import qrcode.image.svg
        img = qrcode.make(content, image_factory=qrcode.image.svg.SvgPathImage)
        buf = io.BytesIO()
        img.save(buf)
        b64 = base64.b64encode(buf.getvalue()).decode("ascii")
        return f"data:image/svg+xml;base64,{b64}"


qrcode_login = QrcodeLoginService()


class P123QrcodeLoginService:
    """123 云盘扫码登录(p123client: generate -> 轮询 result, loginStatus 3 拿 token)。"""

    @staticmethod
    def start() -> dict:
        from p123client import P123Client
        resp = P123Client.login_qrcode_generate()
        data = resp.get("data") or {}
        uni_id = str(data.get("uniID") or data.get("uniId") or "")
        qr = (data.get("qrCode") or data.get("qrCodeUrl")
              or data.get("url") or "")
        if not uni_id:
            raise RuntimeError(f"123 二维码生成失败: {resp}")
        content = qr or uni_id
        return {
            "driver_type": "p123",
            "uni_id": uni_id,
            "qr_image": QrcodeLoginService._svg_data_uri(content),
            "apps": [],
        }

    @staticmethod
    def poll(uni_id: str) -> dict:
        """轮询扫码状态: 0 等待 / 1 已扫 / 2 取消 / 3 已登录 / 4 失效。"""
        from p123client import P123Client
        resp = P123Client.login_qrcode_result({"uniID": uni_id})
        data = resp.get("data") or {}
        status = int(data.get("loginStatus") or 0)
        if status == 3:
            token = (data.get("token") or data.get("accessToken")
                     or data.get("access_token") or "")
            if not token:
                raise RuntimeError(f"123 登录结果缺少 token: {resp}")
            return {"status": "confirmed", "cookies": token,
                    "info": {"device": "扫码登录"}}
        if status == 2:
            return {"status": "cancelled"}
        if status == 4:
            return {"status": "expired"}
        return {"status": "waiting"}


p123_qrcode_login = P123QrcodeLoginService()
