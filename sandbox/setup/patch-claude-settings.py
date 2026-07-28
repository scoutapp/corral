#!/usr/bin/env python3
"""Patch ~/.claude/settings.json to add required fields while preserving existing config."""

import json
import os
import shutil
import tempfile

SETTINGS_PATH = os.path.expanduser("~/.claude/settings.json")

ENFORCE_SMALL_COMMITS_CMD = "/home/claude/bin/enforce-small-commits.sh"

REQUIRED_FIELDS = {
    "env": {
        "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1"
    },
    "preferences": {
        "tmuxSplitPlanes": True
    },
    "skipDangerousModePermissionPrompt": True,
}

# Fields where the required value always wins (not merged with existing)
FORCE_FIELDS = {
    "statusLine": {
        "type": "command",
        "command": "~/bin/statusline.sh",
    },
}


def ensure_stop_hook(settings: dict, command: str) -> None:
    """Add a Stop hook command if not already present (idempotent)."""
    hooks = settings.setdefault("hooks", {})
    stop_hooks = hooks.setdefault("Stop", [])
    for group in stop_hooks:
        for hook in group.get("hooks", []):
            if hook.get("command") == command:
                return
    stop_hooks.append({"hooks": [{"type": "command", "command": command}]})


def deep_merge(base: dict, override: dict) -> dict:
    """Merge override into base, preserving existing values for nested keys."""
    result = base.copy()
    for key, value in override.items():
        if key in result and isinstance(result[key], dict) and isinstance(value, dict):
            result[key] = deep_merge(result[key], value)
        else:
            result.setdefault(key, value) if isinstance(value, dict) else result.__setitem__(key, result.get(key, value))
    return result


def patch_settings():
    os.makedirs(os.path.dirname(SETTINGS_PATH), exist_ok=True)

    # Load existing settings if present
    existing = {}
    if os.path.exists(SETTINGS_PATH):
        try:
            with open(SETTINGS_PATH, "r") as f:
                existing = json.load(f)
        except (json.JSONDecodeError, OSError) as e:
            print(f"Warning: could not read {SETTINGS_PATH}: {e}. Starting fresh.")

    # Merge required fields (existing values take precedence within nested dicts)
    merged = existing.copy()
    for key, value in REQUIRED_FIELDS.items():
        if isinstance(value, dict):
            merged[key] = {**value, **merged.get(key, {})}
        else:
            merged.setdefault(key, value)

    # Force fields always win regardless of existing values
    merged.update(FORCE_FIELDS)

    # Ensure Stop hook for small-commit enforcement is present
    ensure_stop_hook(merged, ENFORCE_SMALL_COMMITS_CMD)

    # Write atomically via temp file
    dir_ = os.path.dirname(SETTINGS_PATH)
    fd, tmp_path = tempfile.mkstemp(dir=dir_, prefix=".settings.tmp.")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(merged, f, indent=2)
            f.write("\n")
        shutil.move(tmp_path, SETTINGS_PATH)
    except Exception:
        os.unlink(tmp_path)
        raise

    print(f"Patched {SETTINGS_PATH}")


if __name__ == "__main__":
    patch_settings()
