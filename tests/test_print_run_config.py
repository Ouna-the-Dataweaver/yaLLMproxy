import subprocess
import sys
from pathlib import Path

import yaml


def _run_print_run_config(config_path: Path) -> dict[str, str]:
    repo_root = Path(__file__).resolve().parents[1]
    script = repo_root / "scripts" / "print_run_config.py"
    result = subprocess.run(
        [sys.executable, str(script), "--config", str(config_path)],
        check=True,
        capture_output=True,
        text=True,
    )
    values: dict[str, str] = {}
    for line in result.stdout.splitlines():
        if line.startswith("CFG_") and "=" in line:
            key, value = line.split("=", 1)
            values[key] = value
    return values


def test_print_run_config_outputs_forwarder_settings(tmp_path: Path):
    config_data = {
        "proxy_settings": {"server": {"host": "127.0.0.1", "port": 7000}},
        "forwarder_settings": {
            "listen": {"host": "0.0.0.0", "port": 9000},
            "target": {"host": "10.0.0.5", "port": 7001},
        },
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(config_data), encoding="utf-8")

    values = _run_print_run_config(config_path)

    assert values["CFG_PROXY_HOST"] == "127.0.0.1"
    assert values["CFG_PROXY_PORT"] == "7000"
    assert values["CFG_FORWARD_LISTEN_HOST"] == "0.0.0.0"
    assert values["CFG_FORWARD_LISTEN_PORT"] == "9000"
    assert values["CFG_FORWARD_TARGET_HOST"] == "10.0.0.5"
    assert values["CFG_FORWARD_TARGET_PORT"] == "7001"


def test_print_run_config_defaults_forwarder_target_host(tmp_path: Path):
    config_data = {
        "proxy_settings": {"server": {"host": "0.0.0.0", "port": 7978}},
        "forwarder_settings": {
            "listen": {"host": "0.0.0.0", "port": 7979},
            "target": {"port": 7978},
        },
    }
    config_path = tmp_path / "config.yaml"
    config_path.write_text(yaml.safe_dump(config_data), encoding="utf-8")

    values = _run_print_run_config(config_path)

    assert values["CFG_FORWARD_TARGET_HOST"] == "127.0.0.1"
