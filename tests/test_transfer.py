"""转存服务测试: 导入规划 + 秒传执行(fake 驱动)+ 能力缺失降级。"""
from __future__ import annotations

import pytest

from app.services.transfer.service import TransferService
from app.services.transfer.secsert import parse_secsert

SECTEXT = (
    '123FLCPV1${"commonPath":"/电影","files":['
    '{"name":"a.mkv","size":100,"etag":"900150983cd24fb0d6963f7d28e17f72"},'
    '{"name":"b.mkv","size":50,"etag":"900150983cd24fb0d6963f7d28e17f72"}]}'
)


class FakeRapidDriver:
    """支持秒传的 fake 驱动。"""

    def __init__(self, hits=None, errors=None):
        self.hits = hits or set()
        self.errors = errors or {}
        self.calls = []

    def meta(self):
        from app.drivers.base import DriverMeta
        return DriverMeta(name="fake", label="fake", capabilities=("rapid_upload",))

    def rapid_upload(self, parent_id, name, size, etag):
        self.calls.append((parent_id, name, size, etag))
        if name in self.errors:
            raise RuntimeError(self.errors[name])
        return f"fid_{name}" if name in self.hits else None


class NoRapidDriver:
    """无秒传能力的驱动。"""

    def meta(self):
        from app.drivers.base import DriverMeta
        return DriverMeta(name="norapid", label="norapid")


class TestPlanImport:
    def test_plan_generation(self):
        plan = TransferService().plan_import(1, SECTEXT, "dir1")
        assert len(plan.entries) == 2
        assert plan.entries[0]["path"] == "/电影/a.mkv"
        assert plan.entries[0]["name"] == "a.mkv"
        assert plan.entries[0]["etag"] == "900150983cd24fb0d6963f7d28e17f72"

    def test_plan_bad_input(self):
        with pytest.raises(ValueError, match="解析失败"):
            TransferService().plan_import(1, "garbage", "dir1")

    def test_plan_roundtrip(self):
        svc = TransferService()
        plan = svc.plan_import(1, SECTEXT, "dir1")
        plan2 = svc.load(svc.dump(plan))
        assert plan2.plan_id == plan.plan_id
        assert len(plan2.entries) == 2


class TestExecuteImport:
    def test_execute_with_rapid_driver(self, monkeypatch):
        svc = TransferService()
        plan = svc.plan_import(1, SECTEXT, "dir1")
        driver = FakeRapidDriver(hits={"a.mkv", "b.mkv"})

        # 替换账户/驱动获取
        class FakeAccounts:
            def get(self, _id):
                return object()

            def driver_for(self, _acc):
                return driver

        monkeypatch.setattr("app.services.transfer.service._accounts", FakeAccounts())
        result = svc.execute_import(plan)
        assert result["ok"] is True and result["done"] == 2
        assert len(driver.calls) == 2
        assert driver.calls[0][0] == "dir1"  # parent 目录

    def test_execute_miss_reported(self, monkeypatch):
        svc = TransferService()
        plan = svc.plan_import(1, SECTEXT, "dir1")
        driver = FakeRapidDriver(hits=set())  # 全部未命中

        class FakeAccounts:
            def get(self, _id):
                return object()

            def driver_for(self, _acc):
                return driver

        monkeypatch.setattr("app.services.transfer.service._accounts", FakeAccounts())
        result = svc.execute_import(plan)
        assert result["ok"] is False and result["done"] == 0
        assert result["failed"] == 2
        assert "未命中" in result["failures"][0]["error"]

    def test_execute_driver_error_tolerated(self, monkeypatch):
        svc = TransferService()
        plan = svc.plan_import(1, SECTEXT, "dir1")
        driver = FakeRapidDriver(hits={"a.mkv"}, errors={"b.mkv": "boom"})

        class FakeAccounts:
            def get(self, _id):
                return object()

            def driver_for(self, _acc):
                return driver

        monkeypatch.setattr("app.services.transfer.service._accounts", FakeAccounts())
        result = svc.execute_import(plan)
        assert result["done"] == 1 and result["failed"] == 1
        assert "boom" in result["failures"][0]["error"]

    def test_execute_without_rapid_capability(self, monkeypatch):
        svc = TransferService()
        plan = svc.plan_import(1, SECTEXT, "dir1")

        class FakeAccounts:
            def get(self, _id):
                return object()

            def driver_for(self, _acc):
                return NoRapidDriver()

        monkeypatch.setattr("app.services.transfer.service._accounts", FakeAccounts())
        result = svc.execute_import(plan)
        assert result["ok"] is False and result["done"] == 0
        assert "不支持秒传" in result["reason"]
