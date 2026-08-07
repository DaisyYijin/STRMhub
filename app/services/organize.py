"""整理归档服务: 文件名解析 + 模板渲染 + 三目录整理流程。

旧接口(计划-预览-执行, 供目录整理页)与新接口(账户内三目录整理)并存。
三目录流程(在"等待整理"目录内完成整理):
  1. 列出等待整理目录的直接子文件(仅视频)
  2. 逐个识别(parse_filename + 资源属性提取)
  3. 识别失败 / 非视频 -> 移入"冗余"目录
  4. 识别成功:
     - 按重命名规则(5 模板)生成目标目录结构与文件名
     - 等待整理目录下已存在同目标目录 -> 移入"已经存在"目录
     - 否则 -> 在等待整理目录下创建目录结构并移入(重命名)
"""
from __future__ import annotations

import json
import re
import uuid
from dataclasses import dataclass, field
from pathlib import Path

from ..db.models import Account
from ..db.session import session_scope
from ..services.account import AccountService

YEAR_RE = re.compile(r"(?:\(|\.)(\d{4})(?:\)|\.|$)")
EPISODE_RE = re.compile(r"[Ss](\d{1,2})[Ee](\d{1,3})")
CN_EPISODE_RE = re.compile(r"第(\d{1,2})季第(\d{1,3})集")
QUALITY_RE = re.compile(
    r"(?<=[.\s_])(2160p|1080p|720p|480p|4k|WEB-?DL|BluRay|REMUX|HDTV|UHD|"
    r"HDR|DV|HEVC|x265|x264|10bit|"
    r"TrueHD(?:\.\d(?:\.\d)?)?|DTS(?:\.\d(?:\.\d)?)?|EAC3|AC3|FLAC|AAC|Atmos)"
    r"(?=[.\s_\d-]|$)", re.IGNORECASE)

AUDIO_WORDS = {"truehd", "dts", "eac3", "ac3", "flac", "aac", "atmos"}
TEAM_RE = re.compile(r"[-\s]\[?([A-Za-z0-9]{2,12})\]?$")

VAR_RE = re.compile(r"\{([^{}]+)\}")
BLOCK_RE = re.compile(r"<([^<>]*)>")

VIDEO_EXTS = {".mkv", ".mp4", ".avi", ".ts", ".mov", ".wmv", ".flv",
              ".m2ts", ".iso", ".rmvb", ".webm", ".m4v", ".mpg", ".mpeg"}


@dataclass
class ParsedMedia:
    title: str
    year: int | None = None
    season: int | None = None
    episode: int | None = None
    pix: str = ""            # 分辨率 2160p
    quality: str = ""        # 资源质量 BluRay/WEB-DL
    effect: str = ""         # 特效 DV.HDR
    encode: str = ""         # 视频编码 H265.10bit
    audio: str = ""          # 音频编码 TrueHD.7.1
    team: str = ""           # 发布组
    fps: str = ""            # 帧率


def parse_filename(name: str) -> ParsedMedia | None:
    """解析影视文件名; 无法识别返回 None。"""
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
    m = YEAR_RE.search(stem)
    year = None
    if m:
        year = int(m.group(1))
        stem = YEAR_RE.sub(" ", stem)
    # 资源属性(先清理质量词, 避免误伤发布组提取; 保留原文件大小写)
    pix = quality = effect = encode = audio = ""
    for q in re.findall(QUALITY_RE, name):
        ql = q.lower()
        if ql in ("2160p", "1080p", "720p", "480p", "4k"):
            pix = "2160p" if ql == "4k" else q
        elif ql in ("bluray", "web-dl", "webdl", "hdtv", "remux", "uhd"):
            quality = q.upper() if ql == "webdl" else q
        elif ql in ("hdr", "dv"):
            effect = f"{effect}.{q}".strip(".") if effect else q
        elif ql.split(".")[0] in AUDIO_WORDS:
            audio = q
        else:
            encode = f"{encode}.{q}".strip(".") if encode else q
    stem = QUALITY_RE.sub(" ", stem)
    # 发布组(文件名尾部)
    team = ""
    tm = TEAM_RE.search(stem)
    if tm and tm.group(1).lower() not in {"strm", "mp4", "mkv"}:
        team = tm.group(1)
        stem = TEAM_RE.sub(" ", stem)
    title = stem.replace(".", " ").replace("_", " ")
    title = re.sub(r"\s+", " ", title).strip(" .-_")
    title = re.sub(r"\bS\d{1,2}(E\d{1,3})?\b.*$", "", title).strip(" .-_")
    if not title or len(title) < 2:
        return None
    return ParsedMedia(title=title, year=year, season=season, episode=episode,
                       pix=pix, quality=quality, effect=effect, encode=encode,
                       audio=audio, team=team)


# ---------- 模板渲染 ----------

def render_template(template: str, ctx: dict) -> str:
    """渲染重命名模板。

    语法: {var} 取值; {var:fmt} 格式化; <...> 块(块内变量全非空才输出);
          [[ ]] 转义为 { }。
    """
    if not template:
        return ""
    template = template.replace("[[", "{").replace("]]", "}")

    def fill(text: str) -> str:
        def repl(m: re.Match) -> str:
            value = _eval_var(m.group(1), ctx)
            if value is None or value == "":
                return ""
            return str(value)
        return VAR_RE.sub(repl, text)

    def block_ok(inner: str) -> bool:
        for vm in VAR_RE.finditer(inner):
            if not _eval_var(vm.group(1), ctx):
                return False
        return True

    out = ""
    pos = 0
    for m in BLOCK_RE.finditer(template):
        out += fill(template[pos:m.start()])
        inner = m.group(1)
        if block_ok(inner):
            out += fill(inner)
        pos = m.end()
    out += fill(template[pos:])
    return out


def _eval_var(expr: str, ctx: dict):
    """求值 {expr}: 'var' / 'var:fmt'。"""
    var = expr
    fmt = ""
    if ":" in expr:
        var, fmt = expr.split(":", 1)
    value = ctx.get(var, "")
    if value is None:
        return ""
    if fmt:
        try:
            return format(value, fmt)
        except (ValueError, TypeError):
            return str(value)
    return value


def _first_letter(title: str) -> str:
    """标题的大写拼音首字母(中文转拼音, 失败时返回首个可见字符大写)。"""
    try:
        from pypinyin import lazy_pinyin
        s = "".join(lazy_pinyin(title))[:1].upper()
        if s:
            return s
    except Exception:
        pass
    for ch in title:
        if ch.isalnum():
            return ch.upper()
    return ""


def parse_category_yaml(text: str) -> dict:
    """解析二级分类策略 YAML -> {"movie": {分类名: {...}}, "tv": {...}}。

    格式(MoviePilot 风格):
      movie:
        大陆动画: {cid: "342...", cid123: "123", genre_ids: "16", origin_country: "CN"}
        ...
      tv:
        ...
    返回空 dict 表示无配置; 解析失败抛 ValueError。
    """
    text = (text or "").strip()
    if not text:
        return {}
    try:
        import yaml
        data = yaml.safe_load(text)
    except Exception as exc:
        raise ValueError(f"YAML 解析失败: {exc}") from exc
    if data is None:
        return {}
    if not isinstance(data, dict):
        raise ValueError("YAML 顶层必须是 movie/tv 映射")
    out = {}
    for kind in ("movie", "tv"):
        section = data.get(kind)
        if section is None:
            continue
        if not isinstance(section, dict):
            raise ValueError(f"YAML 的 {kind} 必须是分类映射")
        items = {}
        for name, rule in section.items():
            if not isinstance(rule, dict):
                raise ValueError(f"分类 [{kind}] {name} 的配置必须是映射")
            items[str(name)] = {
                "cid": str(rule.get("cid") or ""),
                "cid123": str(rule.get("cid123") or ""),
                "genre_ids": str(rule.get("genre_ids") or ""),
                "origin_country": str(rule.get("origin_country") or ""),
            }
        out[kind] = items
    return out


def match_category(categories: dict, kind: str, meta: dict | None) -> str:
    """按 TMDB 元数据匹配分类名; 无元数据/未命中时返回"其他"类。

    meta: {"genre_ids": [16], "origin_country": ["CN"]}(TMDB 刮削后传入)
    规则按 YAML 顺序优先; genre_ids/origin_country 均为空的条件视为兜底类。
    """
    items = (categories or {}).get(kind) or {}
    if not items:
        return ""
    meta_genres = {str(g) for g in (meta or {}).get("genre_ids") or []}
    meta_countries = {str(c).upper() for c in (meta or {}).get("origin_country") or []}
    fallback = ""
    for name, rule in items.items():
        genres = {g.strip() for g in rule.get("genre_ids", "").split(",") if g.strip()}
        countries = {c.strip().upper() for c in rule.get("origin_country", "").split(",") if c.strip()}
        if not genres and not countries:
            fallback = name  # 兜底类(如 其他电影)
            continue
        if genres and not (genres & meta_genres):
            continue
        if countries and not (countries & meta_countries):
            continue
        return name
    return fallback


def build_context(parsed: ParsedMedia, original_name: str) -> dict:
    ext = original_name.rsplit(".", 1)[-1].lower() if "." in original_name else ""
    season_episode = ""
    if parsed.season is not None and parsed.episode is not None:
        season_episode = f"S{parsed.season:02d}E{parsed.episode:02d}"
    return {
        "original_name": original_name,
        "ext": ext,
        "title": parsed.title,
        "year": parsed.year or "",
        "first_letter": _first_letter(parsed.title),
        "tmdb_id": "",
        "season_episode": season_episode,
        "season_num": parsed.season or "",
        "episode_num": parsed.episode or "",
        "resource_pix": parsed.pix,
        "resource_type": parsed.quality,
        "resource_effect": parsed.effect,
        "video_encode": parsed.encode,
        "audio_encode": parsed.audio,
        "resource_team": parsed.team,
        "fps": parsed.fps,
    }


DEFAULT_TEMPLATES = {
    "movie_folder": "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
    "movie_file": "{title}.{year}<.{resource_pix}><.{fps}><.{resource_version}>"
                  "<.{resource_source}><.{resource_type}><.{resource_effect}>"
                  "<.{video_encode}><.{audio_encode}><-{resource_team}>",
    "tv_folder": "{first_letter}-{title}-{year}-[tmdb={tmdb_id}]",
    "season_folder": "Season {season_num:02d}",
    "episode_file": "{title}.{year}.{season_episode}<.{resource_pix}><.{fps}>"
                    "<.{resource_version}><.{resource_source}><.{resource_type}>"
                    "<.{resource_effect}><.{video_encode}><.{audio_encode}>"
                    "<-{resource_team}>",
}


# ---------- 旧接口: 计划-预览-执行(目录整理页) ----------

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
    # ---------- 新接口: 三目录整理 ----------
    def run(self, account_id: int) -> dict:
        """三目录整理: 扫描等待整理目录 -> 识别 -> 分类移动。"""
        accounts = AccountService()
        with session_scope() as s:
            acc = s.get(Account, account_id)
            if acc is None:
                raise ValueError("账户不存在")
            config = json.loads(acc.config_json or "{}")
            rules = config.get("rules") or {}
            driver_type = acc.driver_type
        driver = accounts.driver_for(acc)
        if not (hasattr(driver, "move") and hasattr(driver, "create_folder")):
            raise ValueError(f"驱动 {driver_type} 不支持整理归档(需要移动/建目录能力)")
        dirs = rules.get("organize_dirs") or {}
        pending = str(dirs.get("pending") or "")
        existing = str(dirs.get("existing") or "")
        redundant = str(dirs.get("redundant") or "")
        if not pending:
            raise ValueError("未配置等待整理目录")
        templates = {k: (rules.get(f"rename_{k}") or v)
                     for k, v in DEFAULT_TEMPLATES.items()}
        # 兼容旧版单模板: 旧 rename_template 作为 movie_file 兜底
        if (rules.get("rename_template")
                and templates["movie_file"] == DEFAULT_TEMPLATES["movie_file"]):
            templates["movie_file"] = rules["rename_template"]
        # 二级分类策略(YAML, 含目标 cid; TMDB 元数据缺失时归"其他"类)
        try:
            categories = parse_category_yaml(rules.get("category_yaml") or "")
        except ValueError:
            categories = {}

        items = driver.list_files(pending)
        files = [it for it in items if it.is_file]
        result = {"ok": [], "existing": [], "redundant": []}
        for item in files:
            if Path(item.name).suffix.lower() not in VIDEO_EXTS:
                self._move_to(driver, item, redundant, result, "redundant",
                              "非视频文件")
                continue
            parsed = parse_filename(item.name)
            if parsed is None:
                self._move_to(driver, item, redundant, result, "redundant",
                              "识别失败/非影视文件")
                continue
            ctx = build_context(parsed, item.name)
            is_tv = parsed.season is not None
            kind = "tv" if is_tv else "movie"
            folder_name = render_template(
                templates["tv_folder" if is_tv else "movie_folder"], ctx)
            file_name = render_template(
                templates["episode_file" if is_tv else "movie_file"], ctx)
            if not file_name:
                file_name = item.name
            suffix = Path(item.name).suffix
            if suffix and not file_name.lower().endswith(suffix.lower()):
                file_name = f"{file_name}{suffix}"
            # 二级分类: 匹配分类 -> 目标目录(cid); 无分类配置时用 pending 目录
            target_root = pending
            category_name = match_category(categories, kind, None)
            if category_name:
                rule = (categories.get(kind) or {}).get(category_name) or {}
                cid = rule.get("cid123") if driver_type == "p123" else rule.get("cid")
                if cid:
                    target_root = cid
            # 已存在判定: 目标目录下已有同名目标目录
            try:
                target_items = driver.list_files(target_root)
            except Exception:
                target_items = []
            target_exists = any(
                d.is_dir and d.name == folder_name for d in target_items)
            if target_exists:
                self._move_to(driver, item, existing, result, "existing",
                              f"目标已存在: {folder_name}")
                continue
            # 正常整理: 在目标目录下建结构并移入(自动创建目录)
            try:
                folder_id = driver.create_folder(target_root, folder_name)
                driver.move(item, folder_id, new_name=file_name)
                result["ok"].append({
                    "name": item.name,
                    "target": f"{category_name + '/' if category_name else ''}{folder_name}/{file_name}",
                    "type": kind,
                    "category": category_name or "",
                })
            except Exception as exc:
                result["redundant"].append({
                    "name": item.name, "reason": f"整理失败: {exc}"})
        result["counts"] = {k: len(v) for k, v in result.items()}
        return result

    @staticmethod
    def _move_to(driver, item, dest_dir, result, key, reason):
        if dest_dir:
            try:
                driver.move(item, dest_dir)
            except Exception as exc:
                result["redundant"].append({
                    "name": item.name, "reason": f"移入{key}目录失败: {exc}"})
                return
        result[key].append({"name": item.name, "reason": reason,
                            "target": dest_dir or ""})

    # ---------- 旧接口: 计划-预览-执行 ----------
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
            if not parsed or not parsed.title:
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


organize = OrganizeService()
