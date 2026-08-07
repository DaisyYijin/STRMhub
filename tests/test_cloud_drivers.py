"""115/123 驱动骨架测试: 用 fake client 注入, 验证归一化/直链/封控识别/依赖提示。

真实网盘 API 联调需在有网环境进行; 此处验证协议适配层逻辑。
"""
from __future__ import annotations

import pytest

from app.drivers.p115.driver import P115Driver
from app.drivers.p123.driver import P123Driver


class FakeP115Client:
    """模拟 p115client 最小接口。"""

    def __init__(self, pages=None, blocked=False):
        self.pages = pages or [
            [{"fid": "100", "cid": "1", "n": "movie.mkv", "s": 1024, "t": 0,
              "pick_code": "ABC123"},
             {"fid": "101", "cid": "1", "n": "TV", "t": 1}]
        ]
        self.blocked = blocked
        self.list_calls = 0
        self.download_calls = 0

    def fs_files(self, payload):
        self.list_calls += 1
        if self.blocked:
            return {"data": [], "state": True,
                    "error": "您的访问被阻断, 请稍后再试"}
        limit = payload.get("limit") or 115
        offset = payload.get("offset") or 0
        idx = offset // limit if limit else 0
        rows = self.pages[idx] if idx < len(self.pages) else []
        return {"data": rows, "state": True}

    def download_url(self, pickcode, user_agent=None):
        self.download_calls += 1
        return f"https://cdn.115.com/{pickcode}?ua=1"

    def user_info(self):
        return {"state": True, "user_id": 1}


class TestP115Driver:
    def test_list_files_normalized(self):
        client = FakeP115Client()
        driver = P115Driver(client=client)
        items = driver.list_files("1")
        assert len(items) == 2
        f = items[0]
        assert f.id == "ABC123" and f.name == "movie.mkv" and f.size == 1024
        assert f.is_file
        d = items[1]
        assert d.is_dir and d.name == "TV"

    def test_pagination(self):
        # 真实 115 每页最多 115 条; 满页才继续翻页
        def _page(start, count):
            return [{"fid": f"f{i}", "cid": "1", "n": f"f{i}.mkv", "s": 1,
                     "pick_code": f"P{i}"} for i in range(start, start + count)]
        pages = [_page(0, 115), _page(115, 115), _page(230, 1)]
        driver = P115Driver(client=FakeP115Client(pages=pages))
        items = driver.list_files("1")
        assert len(items) == 231
        assert driver.client.list_calls == 3

    def test_resolve_download(self):
        client = FakeP115Client()
        driver = P115Driver(client=client)
        from app.drivers.base import FileItem
        url, _ = driver.resolve_download(
            FileItem(id="ABC123", name="movie.mkv"))
        assert url.startswith("https://cdn.115.com/")
        assert client.download_calls == 1

    def test_blocked_detection(self):
        driver = P115Driver(client=FakeP115Client(blocked=True))
        with pytest.raises(RuntimeError, match="风控"):
            driver.list_files("1")
        assert driver.gate.is_blocked()  # 进入冷却

    def test_missing_dependency_hint(self):
        # 未注入 client 且 p115client 不可用 -> 明确提示
        import sys
        saved = sys.modules.get("p115client")
        sys.modules["p115client"] = None  # 模拟缺失
        try:
            with pytest.raises(ImportError, match="pip install p115client"):
                P115Driver(credential="cookie")
        finally:
            if saved is not None:
                sys.modules["p115client"] = saved
            else:
                sys.modules.pop("p115client", None)


class FakeP123Client:
    def __init__(self, pages=None, blocked=False):
        self.pages = pages or [
            [{"FileId": 200, "FileName": "a.mp4", "Size": 2048, "Type": 0}]
        ]
        self.blocked = blocked
        self.list_calls = 0

    def fs_list(self, parentFileId=None, limit=100, page=1, inDirectSpace=False):
        self.list_calls += 1
        if self.blocked:
            return {"data": {"fileList": []}, "code": 429,
                    "message": "操作频繁, 请稍后再试"}
        rows = self.pages[page - 1] if page - 1 < len(self.pages) else []
        return {"data": {"fileList": rows, "total": len(self.pages) * 100}}

    def download_info(self, fileId=None):
        return {"DownloadUrl": f"https://dl.123pan.com/{fileId}"}

    def user_info(self):
        return {"user_id": 1}


class TestP123Driver:
    def test_list_files_normalized(self):
        driver = P123Driver(client=FakeP123Client())
        items = driver.list_files("1")
        assert len(items) == 1
        f = items[0]
        assert f.id == "200" and f.name == "a.mp4" and f.size == 2048
        assert f.is_file

    def test_resolve_download(self):
        driver = P123Driver(client=FakeP123Client())
        from app.drivers.base import FileItem
        url, _ = driver.resolve_download(FileItem(id="200", name="a.mp4"))
        assert url == "https://dl.123pan.com/200"

    def test_blocked_detection(self):
        driver = P123Driver(client=FakeP123Client(blocked=True))
        with pytest.raises(RuntimeError, match="风控"):
            driver.list_files("1")
        assert driver.gate.is_blocked()

    def test_credential_split(self):
        # credential "phone:password" 应被拆分(不真正连网, 仅验证不崩溃于缺依赖)
        import sys
        saved = sys.modules.get("p123client")
        sys.modules["p123client"] = None
        try:
            with pytest.raises(ImportError, match="pip install p123client"):
                P123Driver(credential="13800000000:pass")
        finally:
            if saved is not None:
                sys.modules["p123client"] = saved
            else:
                sys.modules.pop("p123client", None)


def client_calls(driver) -> int:
    return driver.client.list_calls


class TestCredentialExpired:
    def test_credential_expired_detection(self):
        from app.drivers.common import (CredentialExpired, is_credential_expired)
        assert is_credential_expired("请先验证安全密钥")
        assert is_credential_expired("登录已过期, 请重新登录")
        assert is_credential_expired("please login first")
        assert not is_credential_expired("ok")
        assert not is_credential_expired("您的访问被阻断")  # 风控不是过期

    def test_p115_driver_raises_credential_expired(self):
        from app.drivers.common import CredentialExpired
        from app.drivers.p115.driver import P115Driver

        class ExpiredClient:
            def fs_files(self, payload):
                return {"state": False, "data": [],
                        "error": "请先验证安全密钥"}

        driver = P115Driver(client=ExpiredClient())
        try:
            driver.list_files("0")
            assert False, "应抛出 CredentialExpired"
        except CredentialExpired as exc:
            assert "重新扫码" in str(exc)

    def test_browse_marks_account_expired(self):
        from fastapi.testclient import TestClient
        from app.main import app
        from app.drivers.common import CredentialExpired
        import app.api.accounts as accounts_mod

        with TestClient(app) as c:
            token = c.post("/api/auth/login",
                           json={"password": "testpass"}).json()["token"]
            h = {"Authorization": f"Bearer {token}"}
            acc = c.post("/api/accounts", headers=h, json={
                "name": "过期账户", "driver_type": "local",
                "credential": ""}).json()
            # 模拟 115 过期: monkeypatch driver_for 抛 CredentialExpired
            orig = accounts_mod._accounts.driver_for
            def boom(acc_obj):
                raise CredentialExpired("115 登录已过期, 请重新扫码登录")
            accounts_mod._accounts.driver_for = boom
            try:
                r = c.get(f"/api/accounts/{acc['id']}/browse", headers=h)
                assert r.status_code == 401
                assert "重新扫码" in r.json()["detail"]
            finally:
                accounts_mod._accounts.driver_for = orig
            # 账户状态被标记为 expired
            mine = [a for a in c.get("/api/accounts",
                                     headers=h).json() if a["id"] == acc["id"]][0]
            assert mine["status"] == "expired"
