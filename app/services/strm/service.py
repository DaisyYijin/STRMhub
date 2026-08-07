"""STRM 任务执行服务: 三模式生成 + 防误删清理 + 统计。

设计来源: LitePan internal/strm/service.go + scanner.go:
- URL 三要素: {base}/api/redirect?key={b64url(file_id)}&t={token}
- 防误删: 只删 seen 之外; 远端扫描结果为空时中止清理(防误删保护)
- 三模式: missing(只补缺) / update(内容不同才重写) / full(全量重写)
"""
from __future__ import annotations

import base64
import json
from dataclasses import dataclass, field
from pathlib import Path

from ...drivers import registry
from ...drivers.base import FileItem
from . import scanner
from .writer import local_rel_path, safe_name, strm_file_name, write_strm

VALID_MODES = {"incremental_missing", "incremental_update", "full_sync"}

_STRM_SUFFIX = ".strm"


@dataclass
class TaskResult:
    generated: int = 0          # 实际生成(含命中)数量
    written: int = 0            # 真正发生写入
    skipped: int = 0            # 已存在/未变化跳过
    cleaned: int = 0            # 清理的失效 strm
    cleanup_aborted: bool = False  # 清理被阈值保护中止
    total_remote: int = 0
    error: str = ""
    records: list = field(default_factory=list)  # 新快照记录(path, file_key, size, mtime)


def build_strm_url(base_url: str, token: str, file_id: str) -> str:
    key = base64.urlsafe_b64encode(file_id.encode("utf-8")).rstrip(b"=").decode("ascii")
    return f"{base_url.rstrip('/')}/api/redirect?key={key}&t={token}"


class StrmService:
    """执行一个 STRM 任务(同步)。调用方负责事务与并发控制。"""

    DEFAULT_CLEANUP_MAX_RATIO = 0.5  # 待删 strm 占现存比例超过则中止清理

    def run(self, driver, remote_path: str, local_output: str, scan_mode: str,
            extensions: set[str], base_url: str, token: str,
            min_size: int = 0,
            snapshot: dict | None = None,
            cleanup_max_ratio: float = DEFAULT_CLEANUP_MAX_RATIO) -> TaskResult:
        """执行任务。

        snapshot: {rel_path: (size, mtime)} 上次快照; 非 full 模式下
                  (size, mtime) 未变化且本地 strm 已存在时跳过(增量 diff)。
        cleanup_max_ratio: 待删 .strm 占比超过该值(如 0.5)时中止清理。
        """
        if scan_mode not in VALID_MODES:
            raise ValueError(f"未知扫描模式: {scan_mode}")

        result = TaskResult()
        try:
            candidates = scanner.collect_candidates(driver, remote_path, extensions, min_size)
        except Exception as exc:  # 远端不可达/超限
            result.error = str(exc)
            return result

        result.total_remote = len(candidates)
        out_root = Path(local_output)
        seen: set[Path] = set()
        use_snapshot = scan_mode != "full_sync" and snapshot is not None

        for cand in candidates:
            item = cand.item
            rel = local_rel_path(cand.rel_dirs)
            target = out_root / rel / strm_file_name(item.name)
            seen.add(target)
            result.generated += 1
            rel_path = "/".join(cand.rel_dirs + [item.name])

            # 增量 diff: 未变化且本地已存在 -> 跳过
            if use_snapshot:
                prev = snapshot.get(rel_path)
                if prev is not None and prev == (item.size, item.mtime or 0.0) \
                        and target.exists():
                    result.skipped += 1
                    result.records.append(
                        (rel_path, item.id, item.size, item.mtime or 0.0))
                    continue

            content = build_strm_url(base_url, token, item.id)
            try:
                if write_strm(target, content, _mode_writer(scan_mode)):
                    result.written += 1
                else:
                    result.skipped += 1
            except OSError as exc:
                result.error = f"写入失败 {target}: {exc}"
                break
            result.records.append(
                (rel_path, item.id, item.size, item.mtime or 0.0))

        # 防误删清理: 仅当远端扫描非空(避免远端异常导致全库误删)
        if candidates:
            result.cleaned, aborted = self._cleanup_stale(
                out_root, seen, cleanup_max_ratio)
            result.cleanup_aborted = aborted
            if aborted:
                result.error = (result.error + "; " if result.error else "") + \
                    "清理中止: 待删 .strm 比例超过安全阈值, 请人工确认"

        return result

    def _cleanup_stale(self, out_root: Path, seen: set[Path],
                       max_ratio: float) -> tuple[int, bool]:
        """删除 out_root 下不在 seen 中的 .strm(连带空目录)。

        返回 (cleaned, aborted); 待删比例 > max_ratio 时中止(防误删)。
        """
        cleaned = 0
        if not out_root.exists():
            return 0, False
        stale = [p for p in sorted(out_root.rglob(f"*{_STRM_SUFFIX}")) if p not in seen]
        total = len(stale) + len(seen)
        if total and len(stale) / total > max_ratio:
            return 0, True
        for p in stale:
            try:
                p.unlink()
                cleaned += 1
            except OSError:
                pass
        # 清理空目录(自底向上)
        for d in sorted([p for p in out_root.rglob("*") if p.is_dir()],
                        key=lambda p: -len(p.parts)):
            try:
                d.rmdir()
            except OSError:
                pass
        return cleaned, False


def _mode_writer(scan_mode: str) -> str:
    return {"incremental_missing": "missing",
            "incremental_update": "update",
            "full_sync": "full"}[scan_mode]


def task_to_extensions(extensions_json: str) -> set[str]:
    if not extensions_json:
        return set(scanner.DEFAULT_EXTENSIONS)
    return {e.lower() if e.startswith(".") else f".{e.lower()}"
            for e in json.loads(extensions_json)}
