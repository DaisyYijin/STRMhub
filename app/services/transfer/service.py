"""转存服务: 秒传串导入规划 -> 驱动秒传能力执行。

流程: parse_secsert(解析) -> 规划(commonPath 拼接目标路径) -> 执行(逐条 rapid_upload)。
"""
from __future__ import annotations

import json
import uuid
from dataclasses import dataclass, field

from ...drivers.registry import supports
from ..account import AccountService
from .secsert import SecsertBundle, parse_secsert

_accounts = AccountService()


@dataclass
class ImportPlan:
    plan_id: str
    account_id: int
    dest_dir_id: str
    entries: list = field(default_factory=list)  # {name, size, etag, target}


class TransferService:
    def plan_import(self, account_id: int, secsert_text: str,
                    dest_dir_id: str) -> ImportPlan:
        """解析秒传串并生成导入计划(不执行)。"""
        bundle = parse_secsert(secsert_text)
        if bundle.error:
            raise ValueError(f"秒传串解析失败: {bundle.error}")
        plan = ImportPlan(plan_id=uuid.uuid4().hex[:12],
                          account_id=account_id, dest_dir_id=dest_dir_id)
        base = bundle.common_path.strip("/")
        for f in bundle.files:
            if f.is_dir:
                continue
            rel = f.name
            if base and not rel.startswith(base):
                rel = f"{base}/{rel}"
            if not rel.startswith("/"):
                rel = f"/{rel}"
            plan.entries.append({
                "name": rel.rsplit("/", 1)[-1],
                "path": rel,
                "size": f.size,
                "etag": f.etag,
            })
        return plan

    def execute_import(self, plan: ImportPlan) -> dict:
        """逐条调用目标账户驱动的秒传能力。"""
        account = _accounts.get(plan.account_id)
        if account is None:
            raise KeyError(f"账户不存在: {plan.account_id}")
        driver = _accounts.driver_for(account)
        if not supports(driver, "rapid_upload"):
            return {"ok": False, "done": 0, "failed": len(plan.entries),
                    "reason": "驱动不支持秒传能力", "failures": []}

        done, failed = 0, []
        for entry in plan.entries:
            parent = self._parent_of(plan.dest_dir_id, entry["path"])
            error = ""
            try:
                file_id = driver.rapid_upload(parent, entry["name"],
                                              entry["size"], entry["etag"])
            except Exception as exc:
                file_id = None
                error = str(exc)
            if file_id:
                done += 1
            else:
                failed.append({"name": entry["path"],
                               "error": error or "秒传未命中(需要实际上传)"})
        return {"ok": failed == [], "done": done, "failed": len(failed),
                "failures": failed[:20]}

    @staticmethod
    def _parent_of(dest_dir_id: str, rel_path: str) -> str:
        """目标目录 ID + 相对路径 -> 父目录 ID(按需逐级建目录由驱动负责)。"""
        return dest_dir_id

    @staticmethod
    def dump(plan: ImportPlan) -> str:
        return json.dumps({
            "plan_id": plan.plan_id, "account_id": plan.account_id,
            "dest_dir_id": plan.dest_dir_id, "entries": plan.entries,
        }, ensure_ascii=False)

    @staticmethod
    def load(text: str) -> ImportPlan:
        data = json.loads(text)
        return ImportPlan(plan_id=data["plan_id"], account_id=data["account_id"],
                          dest_dir_id=data["dest_dir_id"],
                          entries=data["entries"])
