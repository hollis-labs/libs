#!/usr/bin/env python3
"""Private Cairn seed renderer, called only by scripts/update-goldens.

Builds the pinned source in a detached worktree with cached Go dependencies;
never runs capture.sh or reads a real provider home. Writes only to private
staging beneath TMPDIR; the repository script scans, diffs and publishes.
"""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile

COMMIT = "f08641f056911927bb01b1f0de1472de2290cfb2"
CORPUS = Path(__file__).resolve().parent.parent / "testdata/goldens/seeds/cairn"


DESTINATION = None

def write_diff(path, value, mode):
    destination = DESTINATION / path.relative_to(CORPUS)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    destination.chmod(0o600)


def snapshot(root, normalize):
    entries = []
    for path in [root] + sorted(root.rglob("*")):
        info = path.lstat()
        entry = {"path": path.relative_to(root).as_posix(),
                 "kind": "directory" if path.is_dir() else "file",
                 "mode": f"{stat.S_IMODE(info.st_mode):04o}"}
        if stat.S_ISREG(info.st_mode):
            raw = path.read_bytes()
            try:
                content = normalize(raw.decode("utf-8"))
                if content:
                    entry["content"] = content
            except UnicodeDecodeError:
                entry["base64"] = base64.b64encode(raw).decode("ascii")
        elif not stat.S_ISDIR(info.st_mode):
            raise ValueError("seed contains a non-regular entry")
        entries.append(entry)
    return entries


def capture(binary, scratch, mode):
    bundle = scratch / "bundle"
    shutil.copytree(CORPUS / "bundle", bundle)
    # Git preserves executable bits, not 0751 precisely. Set the declared
    # support-file mode explicitly in private scratch on every capture.
    (bundle / "skills/sample/scripts/run.sh").chmod(0o751)
    scope = scratch / "scope"
    scope.mkdir(mode=0o700)
    for provider in ["claude", "codex", "opencode"]:
        for operation in ["boot", "install"]:
            for scenario in (["create"] if operation == "boot" else ["create", "refresh"]):
                private = scratch / (provider + "-" + operation + "-" + scenario)
                private.mkdir(mode=0o700)
                home = private / "home"
                home.mkdir(mode=0o700)
                for name in ["codex", "opencode", "config"]:
                    (home / name).mkdir(mode=0o700)
                env = {"HOME": str(home), "PATH": "/usr/bin:/bin",
                       "CODEX_HOME": str(home / "codex"),
                       "OPENCODE_CONFIG_DIR": str(home / "opencode"),
                       "XDG_CONFIG_HOME": str(home / "config"),
                       "CAIRN_PROFILE_ROOT": str(bundle), "TMPDIR": str(private)}
                root = private / "tree"
                args = [str(binary), operation, "fixture", "--profile", str(bundle),
                        "--provider", provider]
                if operation == "boot":
                    args += ["--scope", str(scope), "--boot-root", str(root),
                             "--session", "fixture-session", "--json"]
                    tree = root / "fixture/fixture-session"
                else:
                    root.mkdir(mode=0o700)
                    tree = root
                    args += ["--root", str(root)]
                result = subprocess.run(args, env=env, cwd=scope, capture_output=True, text=True)
                if scenario == "refresh" and result.returncode == 0:
                    (root / "operator.txt").write_text("Operator owned.\n")
                    (root / "operator.txt").chmod(0o640)
                    # Preserve unrelated installed keys and files during reapply.
                    config = root / (".claude/settings.json" if provider == "claude" else ".codex/config.toml")
                    if provider == "claude":
                        doc = json.loads(config.read_text())
                        doc["fixture_operator"] = True
                        config.write_text(json.dumps(doc) + "\n")
                    else:
                        with config.open("a") as out:
                            out.write('\nfixture_operator = true\n')
                    result = subprocess.run(args, env=env, cwd=scope, capture_output=True, text=True)
                if result.returncode and not (provider == "opencode" and operation == "install"):
                    raise RuntimeError(result.stderr)
                if not tree.exists():
                    tree.mkdir(parents=True, mode=0o700)
                pairs = [(str(tree), "<boot>" if operation == "boot" else "<installed>"),
                         (str(bundle), "<bundle>"), (str(scope), "<scope>"),
                         (str(home), "<home>"), (str(scratch), "<scratch>")]
                def normalize(text):
                    for old, new in pairs:
                        text = text.replace(old, new)
                    return text
                binding = {}
                if operation == "boot" and result.returncode == 0:
                    report = json.loads(result.stdout)
                    # Preserve semantic launch seams, not old CLI/profile banners.
                    binding = {k: report[k] for k in ["cwd_preference", "project_dir_arg",
                               "env_amendments", "home_resource_paths"]}
                case = CORPUS / provider / (operation + "-" + scenario)
                write_diff(case / "input.json", {"provider": provider, "scenario": scenario,
                                                "operation": operation}, mode)
                write_diff(case / "expected.json", snapshot(tree, normalize), mode)
                write_diff(case / "evidence.json", {
                    "writer": "cairn", "source_commit": COMMIT,
                    "legacy_bundle_seed_commit": "6da0e3864698c75bf4e00b115b1bc264a614c8d2",
                    "fixture": "neutral miniature bundle (not the legacy bundle)",
                    "bindings": binding, "exit_code": result.returncode,
                    "diagnostics": normalize(result.stderr).splitlines(),
                    "ownership": {"boot": "disposable tree", "installed": "Cairn owned-key merge"}
                }, mode)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if not os.environ.get("TMPDIR"):
        parser.error("private TMPDIR is required")
    global DESTINATION
    DESTINATION = Path(args.output).resolve()
    if not DESTINATION.is_relative_to(Path(os.environ["TMPDIR"]).resolve()):
        parser.error("output must be beneath private TMPDIR")
    with tempfile.TemporaryDirectory(dir=os.environ["TMPDIR"]) as temporary:
        scratch = Path(temporary)
        source = scratch / "source"
        subprocess.run(["git", "-C", args.repo, "worktree", "add", "--detach", str(source), COMMIT], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            binary = scratch / "cairn"
            env = dict(os.environ, GOFLAGS="-p=2", GOPROXY="off", GOSUMDB="off", GOWORK="off", GOTMPDIR=temporary)
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/cairn"], cwd=source, env=env, check=True)
            capture(binary, scratch, "stage")
        finally:
            subprocess.run(["git", "-C", args.repo, "worktree", "remove", str(source)], check=True)

if __name__ == "__main__":
    main()
