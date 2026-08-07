"""整理服务: 轻量文件名解析 + 计划-预览-执行。

设计来源: LitePan mediaorganize —— 生成计划 -> 预览/人工编辑 -> 执行;
命名目标: {Title} ({Year})/Season N/SxxEyy 标准结构(兼容刮削与 Emby 识别)。
"""
from __future__ import annotations

import json
import re
import uuid
from dataclasses import dataclass, field
from pathlib import Path

YEAR_RE = re.compile(r"\((\d{4})\)")
EPISODE_RE = re.compile(r"[Ss](\d{1,2})[Ee](\d{1,3})")
CN_EPISODE_RE = re.compile(r"第(\d{1,2})季第(\d{1,3})集")
QUALITY_RE = re.compile(r"\.?(1080p|720p|2160p|4k|WEB-?DL|BluRay|REMUX|HDR|DV|HEVC|x264|x265|HDTV)",
                        re.IGNORECASE)


@dataclass
class ParsedMedia:
    title: str
    year: int | None = None
    season: int | None = None
    episode: int | None = None


def parse_filename(name: str) -> ParsedMedia:
    """解析媒体文件名: 'Movie.Title.2024.1080p.mkv' / 'Show.S01E02.1080p.mkv'。"""
    stem = name.rsplit(".", 1)[0] if "." in name else name
    m = EPISODE_RE.search(stem)
    if m:
        season, episode = int(m.group(1)), int(m.group(2))
        stem = EPISODE_RE.sub(" ", stem)
    else:
        m = CN_EPISODE_RE.search(stem)
        season = episode = None
        if m:
            season, episode = int(m.group(1)), int(m.group(2))
            stem = CN_EPISODE_RE.sub(" ", stem)
    # 年份提取必须先于质量词清理(否则 ".2024.1080p" 的断点被破坏)
    m = YEAR_RE.search(stem)
    year = None
    if m:
        year = int(m.group(1))
        stem = YEAR_RE.sub(" ", stem)
    else:
        # 兼容无括号年份: "Movie.2024.1080p" / "Movie.2024"
        dotted = f".{stem}."
        m2 = re.search(r"\.(19\d{2}|20\d{2})\.", dotted)
        if m2:
            year = int(m2.group(1))
            stem = re.sub(r"\.(19\d{2}|20\d{2})\.", " ", dotted)[1:-1]
    stem = QUALITY_RE.sub(" ", stem)
    title = stem.replace(".", " ").replace("_", " ")
    title = re.sub(r"\s+", " ", title).strip(" -")
    return ParsedMedia(title=title, year=year, season=season, episode=episode)


@dataclass
class PlanAction:
    source: str          # 源文件/目录路径
    target: str          # 目标路径
    action: str = "move"  # move | rename


@dataclass
class OrganizePlan:
    plan_id: str
    actions: list = field(default_factory=list)


class OrganizeService:
    def create_plan(self, path: str) -> OrganizePlan:
        """扫描目录, 生成重命名计划(仅文件名规范化, 不改目录结构)。"""
        root = Path(path)
        if not root.is_dir():
            raise ValueError(f"目录不存在: {path}")
        plan = OrganizePlan(plan_id=uuid.uuid4().hex[:12])
        for f in sorted(root.rglob("*")):
            if not f.is_file() or f.name.lower().endswith((".nfo", ".jpg", ".jpeg", ".png")):
                continue
            parsed = parse_filename(f.name)
            if not parsed.title:
                continue
            target_name = self._target_name(parsed, f.name)
            if target_name != f.name:
                plan.actions.append(PlanAction(
                    source=str(f), target=str(f.with_name(target_name))))
        return plan

    def _target_name(self, parsed: ParsedMedia, original: str) -> str:
        """目标文件名: {Title} ({Year}) [SxxEyy].{ext}"""
        ext = original.rsplit(".", 1)[-1] if "." in original else ""
        parts = [parsed.title]
        if parsed.year:
            parts.append(f"({parsed.year})")
        if parsed.season is not None and parsed.episode is not None:
            parts.append(f"(S{parsed.season:02d}E{parsed.episode:02d})")
        name = " ".join(parts)
        return f"{name}.{ext}" if ext else name

    def preview(self, plan: OrganizePlan) -> list[dict]:
        return [{"source": a.source, "target": a.target, "action": a.action}
                for a in plan.actions]

    def execute(self, plan: OrganizePlan) -> dict:
        """执行计划(重命名), 返回统计; 冲突跳过。"""
        done, skipped = 0, 0
        for a in plan.actions:
            src = Path(a.source)
            dst = Path(a.target)
            if not src.exists() or dst.exists():
                skipped += 1
                continue
            try:
                src.rename(dst)
                done += 1
            except OSError:
                skipped += 1
        return {"plan_id": plan.plan_id, "done": done, "skipped": skipped}

    @staticmethod
    def load(plan_json: str) -> OrganizePlan:
        data = json.loads(plan_json)
        return OrganizePlan(plan_id=data["plan_id"],
                            actions=[PlanAction(**a) for a in data["actions"]])

    def dump(self, plan: OrganizePlan) -> str:
        return json.dumps({"plan_id": plan.plan_id,
                           "actions": [a.__dict__ for a in plan.actions]},
                          ensure_ascii=False)
