import hashlib
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Dict, List, Optional, Tuple

from . import __version__
from .editions import EDITIONS, HOSTS, hosts_for, source_for


STATE_VERSION = 1
STATE_PATH = Path(".devflow/install-state.json")


class DevFlowError(RuntimeError):
    pass


def is_runtime_artifact(relative: Path) -> bool:
    """Local run output (logs, caches) that must never be installed or packaged.

    agent-observability writes metrics, state and debug logs under its ``logs/``
    directory; only the ``.gitkeep`` placeholder belongs to the asset bundle.
    """
    parts = Path(relative).parts
    if "__pycache__" in parts or Path(relative).suffix in {".pyc", ".pyo"}:
        return True
    for index in range(len(parts) - 2):
        if parts[index:index + 2] == ("agent-observability", "logs"):
            return parts[-1] != ".gitkeep"
    return False


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def file_mode(path: Path) -> int:
    mode = stat.S_IMODE(path.stat().st_mode)
    if path.suffix == ".sh":
        mode |= 0o111
    return mode


def apply_mode(path: Path, mode: int) -> None:
    mode = stat.S_IMODE(mode)
    if path.suffix == ".sh":
        mode |= 0o111
    path.chmod(mode)


def write_payload(path: Path, data: bytes, mode: int) -> None:
    path.write_bytes(data)
    apply_mode(path, mode)


def asset_root() -> Path:
    override = os.environ.get("DEVFLOW_ASSET_ROOT")
    candidates = []
    if override:
        candidates.append(Path(override))
    candidates.extend([
        Path(__file__).resolve().parents[2],
        Path(sys.prefix) / "share/devflow",
    ])
    for candidate in candidates:
        if (candidate / "skills/devflow/SKILL.md").is_file() and (candidate / ".codebuddy").is_dir():
            return candidate.resolve()
    raise DevFlowError("找不到 DevFlow 安装资产；请从源码目录运行或重新安装 loopforge CLI 包")


def load_state(project: Path) -> dict:
    path = project / STATE_PATH
    if not path.is_file():
        return {"version": STATE_VERSION, "installer_version": __version__, "installations": {}}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise DevFlowError(f"安装状态损坏: {path}: {exc}") from exc
    if data.get("version") != STATE_VERSION or not isinstance(data.get("installations"), dict):
        raise DevFlowError(f"不支持的安装状态版本: {path}")
    return data


def save_state(project: Path, state: dict) -> None:
    path = project / STATE_PATH
    path.parent.mkdir(parents=True, exist_ok=True)
    state["installer_version"] = __version__
    path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def snapshot(root: Path) -> Dict[str, Tuple[bytes, int]]:
    result = {}
    for path in sorted(root.rglob("*")):
        if not path.is_file() or is_runtime_artifact(path.relative_to(root)):
            continue
        relative = str(path.relative_to(root))
        if relative.endswith(".devflow-managed-skills.json"):
            continue
        result[relative] = (path.read_bytes(), file_mode(path))
    return result


def copy_skill(source: Path, destination: Path) -> None:
    shutil.copytree(source, destination, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))


def copy_skills_manifest(skills_root: Path, destination: Path) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(skills_root / "manifest.json", destination / "manifest.json")


def materialize_portable(host: str, stage: Path) -> None:
    assets = asset_root()
    skills_root = assets / source_for("portable", host)
    if host in {"cursor", "claude"}:
        installer = skills_root / "devflow/scripts/install_adapter.py"
        command = [
            sys.executable, str(installer), "--adapter", host,
            "--project-root", str(stage), "--copy-skills", "--refresh-managed",
        ]
        subprocess.run(command, check=True, capture_output=True, text=True)
        return
    if host == "codebuddy":
        skill_target = stage / ".codebuddy/skills/devflow"
        skill_target.parent.mkdir(parents=True, exist_ok=True)
        copy_skills_manifest(skills_root, skill_target.parent)
        copy_skill(skills_root / "devflow", skill_target)
        installer = skills_root / "devflow/adapters/codebuddy/install.py"
        subprocess.run(
            [sys.executable, str(installer), "--project-root", str(stage), "--refresh-managed"],
            check=True, capture_output=True, text=True,
        )
        clarifier = stage / ".codebuddy/skills/devflow-clarify-requirements"
        if clarifier.is_symlink():
            clarifier.unlink()
            copy_skill(skills_root / "devflow-clarify-requirements", clarifier)
        return
    if host == "codex":
        target = stage / ".agents/skills"
        target.mkdir(parents=True, exist_ok=True)
        manifest = json.loads((skills_root / "manifest.json").read_text(encoding="utf-8"))
        copy_skills_manifest(skills_root, target)
        for name in manifest["skill_sets"]["portable"]:
            copy_skill(skills_root / name, target / name)
        return
    if host == "pi":
        target = stage / ".pi/skills"
        target.mkdir(parents=True, exist_ok=True)
        manifest = json.loads((skills_root / "manifest.json").read_text(encoding="utf-8"))
        copy_skills_manifest(skills_root, target)
        for name in manifest["skill_sets"]["portable"]:
            copy_skill(skills_root / name, target / name)
        return
    if host == "dsh":
        target = stage / ".dsh/skills"
        target.mkdir(parents=True, exist_ok=True)
        manifest = json.loads((skills_root / "manifest.json").read_text(encoding="utf-8"))
        copy_skills_manifest(skills_root, target)
        for name in manifest["skill_sets"]["portable"]:
            copy_skill(skills_root / name, target / name)
        return
    if host == "hermes":
        # Hermes discovers project skills under .hermes/skills after the user
        # explicitly trusts the repository. Keep installation project-local;
        # never mutate ~/.hermes/config.yaml or the user's profile skills.
        target = stage / ".hermes/skills"
        target.mkdir(parents=True, exist_ok=True)
        manifest = json.loads((skills_root / "manifest.json").read_text(encoding="utf-8"))
        copy_skills_manifest(skills_root, target)
        for name in manifest["skill_sets"]["portable"]:
            copy_skill(skills_root / name, target / name)
        return
    raise DevFlowError(f"不支持的宿主: {host}")


def materialize_classic(host: str, stage: Path) -> None:
    source_relative = source_for("classic", host)
    source = asset_root() / source_relative
    if not source.is_dir():
        raise DevFlowError(f"Classic 宿主包不存在: {host}")
    shutil.copytree(source, stage / source_relative)
    if host == "codex":
        links = stage / ".agents/skills"
        links.mkdir(parents=True, exist_ok=True)
        mapping = {
            "devflow-codex": stage / ".codex/skills/devflow-codex",
            "knowledge-distillation": stage / ".codex/skills/knowledge-distillation",
            "superpowers-brainstorming": stage / ".codex/skills/superpowers/brainstorming",
        }
        for name, source_dir in mapping.items():
            copy_skill(source_dir, links / name)


def build_plan(host: str, edition: str) -> Dict[str, Tuple[bytes, int]]:
    from .targets import load

    with tempfile.TemporaryDirectory(prefix="devflow-install-") as temporary:
        stage = Path(temporary)
        load(host).materialize(edition, stage)
        return snapshot(stage)


def installation_key(host: str, edition: str) -> str:
    return f"{edition}:{host}"


def validate_choice(host: str, edition: str) -> None:
    if host not in HOSTS:
        raise DevFlowError(f"未知宿主 {host}；可选: {', '.join(HOSTS)}")
    if edition not in EDITIONS:
        raise DevFlowError(f"未知 edition {edition}；可选: {', '.join(EDITIONS)}")
    if host not in hosts_for(edition):
        available = [item for item in EDITIONS if host in hosts_for(item)]
        hint = (
            f"；Pi 只有 Portable，请运行: loopforge skills install {host}"
            if host == "pi" and "portable" in available
            else f"；{host} 可用 edition: {', '.join(available)}"
        )
        raise DevFlowError(f"{edition} edition 不支持宿主 {host}{hint}")


def path_present(path: Path) -> bool:
    return path.exists() or path.is_symlink()


def blocking_parent(project: Path, destination: Path) -> Optional[Path]:
    current = project
    for part in destination.relative_to(project).parts[:-1]:
        current = current / part
        if current.is_symlink() or (current.exists() and not current.is_dir()):
            return current
    return None


def remove_path(path: Path) -> None:
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.is_dir():
        shutil.rmtree(path)


def prepare_forced_destination(project: Path, destination: Path) -> None:
    current = project
    for part in destination.relative_to(project).parts[:-1]:
        current = current / part
        if current.is_symlink() or (current.exists() and not current.is_dir()):
            remove_path(current)
        current.mkdir(exist_ok=True)
    if path_present(destination):
        remove_path(destination)


class InstallPlan(object):
    """Read-only preview of an install/update. Does not write the project."""

    def __init__(
        self,
        project: Path,
        host: str,
        edition: str,
        update_only: bool,
        force: bool,
        key: str,
        bundle: Dict[str, Tuple[bytes, int]],
        old_files: Dict[str, dict],
        switch_from: Optional[str],
        conflicts: List[str],
        files: List[Tuple[str, str]],
    ) -> None:
        self.project = project
        self.host = host
        self.edition = edition
        self.update_only = update_only
        self.force = force
        self.key = key
        self.bundle = bundle
        self.old_files = old_files
        self.switch_from = switch_from
        self.conflicts = conflicts
        self.files = files

    def counts(self) -> Dict[str, int]:
        result = {
            "add": 0,
            "update": 0,
            "unchanged": 0,
            "remove": 0,
            "conflict": len(self.conflicts),
        }
        for _path, action in self.files:
            if action in {"add", "update", "unchanged", "remove"}:
                result[action] += 1
        return result


def _planned_present(project: Path, relative: str, removed: set) -> bool:
    if relative in removed:
        return False
    return path_present(project / relative)


def _planned_bytes(project: Path, relative: str, removed: set) -> Optional[bytes]:
    if relative in removed:
        return None
    destination = project / relative
    if destination.is_file() and not destination.is_symlink():
        return destination.read_bytes()
    return None


def _planned_blocking_parent(project: Path, relative: str, removed: set) -> Optional[str]:
    current = project
    for part in Path(relative).parts[:-1]:
        current = current / part
        rel = str(current.relative_to(project))
        if rel in removed:
            continue
        if current.is_symlink() or (current.exists() and not current.is_dir()):
            return rel
    return None


def _files_uninstalled_unmodified(project: Path, records: List[dict]) -> set:
    removed = set()
    for record in records:
        relative = record["path"]
        destination = project / relative
        if not destination.exists():
            continue
        if (
            destination.is_symlink()
            or not destination.is_file()
            or digest(destination.read_bytes()) != record["sha256"]
        ):
            continue
        removed.add(relative)
    return removed


def plan_install(
    project: Path,
    host: str,
    edition: str,
    update_only: bool = False,
    force: bool = False,
) -> InstallPlan:
    """Classify per-file install actions without writing the project."""
    validate_choice(host, edition)
    project = project.resolve()
    if not project.is_dir():
        raise DevFlowError(f"项目目录不存在: {project}")
    state = load_state(project)
    key = installation_key(host, edition)
    other_edition = "classic" if edition == "portable" else "portable"
    other = installation_key(host, other_edition)
    removed = set()
    switch_from = None  # type: Optional[str]
    if other in state["installations"]:
        if update_only:
            raise DevFlowError(
                f"{host} 当前安装的是 {other_edition} edition；"
                f"update 不能切换 edition，请使用 install --force"
            )
        if not force:
            raise DevFlowError(
                f"{host} 当前安装的是 {other_edition} edition；"
                f"如需切换到 {edition}，请添加 --force"
            )
        # Materialize the target before removing the current edition so a broken
        # installer package cannot destroy an otherwise healthy installation.
        bundle = build_plan(host, edition)
        switch_from = other_edition
        removed = _files_uninstalled_unmodified(
            project, state["installations"][other].get("files", []),
        )
    else:
        bundle = build_plan(host, edition)
    previous = state["installations"].get(key)
    if update_only and previous is None:
        raise DevFlowError(f"尚未安装 {edition}/{host}")
    old_files = {item["path"]: item for item in (previous or {}).get("files", [])}
    conflicts = []
    for relative, (data, _mode) in bundle.items():
        parent_conflict = _planned_blocking_parent(project, relative, removed)
        if parent_conflict is not None:
            conflicts.append(parent_conflict)
            continue
        if not _planned_present(project, relative, removed):
            continue
        current = _planned_bytes(project, relative, removed)
        old = old_files.get(relative)
        if old:
            if current is None or digest(current) != old["sha256"]:
                if current != data:
                    conflicts.append(relative)
        elif current != data:
            conflicts.append(relative)
    for relative, old in old_files.items():
        if relative in bundle:
            continue
        parent_conflict = _planned_blocking_parent(project, relative, removed)
        if parent_conflict is not None:
            conflicts.append(parent_conflict)
            continue
        if not _planned_present(project, relative, removed):
            continue
        current = _planned_bytes(project, relative, removed)
        if current is None or digest(current) != old["sha256"]:
            conflicts.append(relative)
    conflicts = sorted(set(conflicts))
    conflict_set = set(conflicts)
    labeled = {}
    for relative, (data, _mode) in bundle.items():
        parent_conflict = _planned_blocking_parent(project, relative, removed)
        if parent_conflict is not None:
            labeled[relative] = "add"
            continue
        if relative in conflict_set:
            continue
        if not _planned_present(project, relative, removed):
            labeled[relative] = "add"
            continue
        current = _planned_bytes(project, relative, removed)
        old = old_files.get(relative)
        if current is not None and current == data:
            labeled[relative] = "unchanged"
        elif old and current is not None and digest(current) == old["sha256"]:
            labeled[relative] = "update"
        else:
            labeled[relative] = "conflict"
    for relative, old in old_files.items():
        if relative in bundle or relative in conflict_set:
            continue
        parent_conflict = _planned_blocking_parent(project, relative, removed)
        if parent_conflict is not None:
            continue
        if not _planned_present(project, relative, removed):
            continue
        current = _planned_bytes(project, relative, removed)
        if current is not None and digest(current) == old["sha256"]:
            labeled[relative] = "remove"
    for relative in conflicts:
        labeled[relative] = "conflict"
    files = sorted(labeled.items(), key=lambda item: item[0])
    return InstallPlan(
        project=project,
        host=host,
        edition=edition,
        update_only=update_only,
        force=force,
        key=key,
        bundle=bundle,
        old_files=old_files,
        switch_from=switch_from,
        conflicts=conflicts,
        files=files,
    )


def apply_install(
    project: Path,
    host: str,
    edition: str,
    update_only: bool = False,
    force: bool = False,
) -> Tuple[int, int]:
    preview = plan_install(project, host, edition, update_only=update_only, force=force)
    project = preview.project
    if preview.conflicts and not force:
        listing = ", ".join(preview.conflicts[:8])
        raise DevFlowError(f"检测到用户文件冲突，未修改任何内容: {listing}")
    state = load_state(project)
    if preview.switch_from is not None:
        uninstall(project, host, preview.switch_from)
        state = load_state(project)
    plan = preview.bundle
    key = preview.key
    previous = state["installations"].get(key)
    old_files = {item["path"]: item for item in (previous or {}).get("files", [])}
    installed = unchanged = 0
    records = []
    for relative, old in old_files.items():
        if relative in plan:
            continue
        destination = project / relative
        parent_conflict = blocking_parent(project, destination)
        if force and parent_conflict is not None:
            remove_path(parent_conflict)
        elif force and path_present(destination):
            remove_path(destination)
        elif destination.is_file() and not destination.is_symlink() and digest(destination.read_bytes()) == old["sha256"]:
            destination.unlink()
    for relative, (data, mode) in sorted(plan.items()):
        destination = project / relative
        if (
            blocking_parent(project, destination) is None
            and destination.is_file()
            and not destination.is_symlink()
            and destination.read_bytes() == data
        ):
            apply_mode(destination, mode)
            unchanged += 1
        else:
            if force:
                prepare_forced_destination(project, destination)
            else:
                destination.parent.mkdir(parents=True, exist_ok=True)
            write_payload(destination, data, mode)
            installed += 1
        records.append({"path": relative, "sha256": digest(data)})
    state["installations"][key] = {
        "host": host, "edition": edition, "package_version": __version__, "files": records,
    }
    save_state(project, state)
    return installed, unchanged


def uninstall(project: Path, host: str, edition: Optional[str] = None) -> Tuple[int, List[str]]:
    project = project.resolve()
    state = load_state(project)
    matches = [
        key for key, item in state["installations"].items()
        if item.get("host") == host and (edition is None or item.get("edition") == edition)
    ]
    if not matches:
        raise DevFlowError(f"尚未安装 {host}")
    removed = 0
    preserved = []
    for key in matches:
        item = state["installations"].pop(key)
        for record in reversed(item["files"]):
            path = project / record["path"]
            if not path.exists():
                continue
            if path.is_symlink() or not path.is_file() or digest(path.read_bytes()) != record["sha256"]:
                preserved.append(record["path"])
                continue
            path.unlink()
            removed += 1
            parent = path.parent
            while parent != project and parent.name != ".devflow":
                try:
                    parent.rmdir()
                except OSError:
                    break
                parent = parent.parent
    if state["installations"]:
        save_state(project, state)
    else:
        state_path = project / STATE_PATH
        if state_path.is_file():
            state_path.unlink()
        try:
            state_path.parent.rmdir()
        except OSError:
            pass
    return removed, preserved


def status_rows(project: Path, host: Optional[str] = None) -> List[dict]:
    state = load_state(project.resolve())
    rows = []
    for key, item in sorted(state["installations"].items()):
        if host and item.get("host") != host:
            continue
        changed = missing = 0
        for record in item["files"]:
            path = project.resolve() / record["path"]
            if not path.is_file():
                missing += 1
            elif digest(path.read_bytes()) != record["sha256"]:
                changed += 1
        rows.append({
            "key": key, "host": item["host"], "edition": item["edition"],
            "files": len(item["files"]), "changed": changed, "missing": missing,
        })
    return rows


def doctor(project: Path) -> List[str]:
    problems = []
    try:
        root = asset_root()
    except DevFlowError as exc:
        return [str(exc)]
    for required in ("skills/manifest.json", "skills/devflow/SKILL.md", ".codebuddy", ".codex", ".cursor", ".claude"):
        if not (root / required).exists():
            problems.append(f"安装资产缺失: {required}")
    for row in status_rows(project):
        if row["changed"] or row["missing"]:
            problems.append(f"{row['key']}: changed={row['changed']} missing={row['missing']}")
    return problems
