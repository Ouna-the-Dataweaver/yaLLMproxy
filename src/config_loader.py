"""Configuration loading from YAML files with environment variable support."""

import logging
import os
from pathlib import Path
from typing import Any, Mapping

import yaml
from dotenv import dotenv_values

logger = logging.getLogger("yallmp-proxy")

# Default path to config file (relative to project root)
DEFAULT_CONFIG_PATH = "configs/config.yaml"

# Environment variable to override config path
# Priority: YALLMP_CONFIG (if set) > DEFAULT_CONFIG_PATH
config_path_override = os.getenv("YALLMP_CONFIG")
if config_path_override:
    CONFIG_PATH = config_path_override
else:
    CONFIG_PATH = DEFAULT_CONFIG_PATH


def resolve_config_path(path: str) -> Path:
    """Resolve config path relative to project root if needed."""
    if Path(path).is_absolute():
        return Path(path)
    project_root = Path(__file__).parent.parent
    return project_root / path


def resolve_env_path(config_path: Path, env_path: str | None = None) -> Path:
    """Resolve the env file path for a config file."""
    if env_path:
        return resolve_config_path(env_path)
    stem = config_path.stem
    if stem.startswith("config_"):
        suffix = stem[len("config_"):]
        return config_path.with_name(f".env_{suffix}")
    return config_path.with_name(".env")


def load_env_values(env_path: Path) -> dict[str, str]:
    """Load environment values from a .env file without mutating os.environ."""
    if not env_path.exists():
        return {}
    raw_values = dotenv_values(env_path)
    return {key: value for key, value in raw_values.items() if value is not None}


def load_config(
    path: str | None = None,
    env_path: str | None = None,
    substitute_env: bool = True,
) -> dict:
    """Load configuration from a YAML file.

    Args:
        path: Path to the config file. Defaults to YALLMP_CONFIG,
              or configs/config.yaml in the project root.
        env_path: Optional .env path override for env substitution.
        substitute_env: Whether to substitute environment variables in the config.

    Returns:
        Parsed configuration dictionary.
    """
    if path is None:
        path = CONFIG_PATH

    config_path = resolve_config_path(path)

    # Log which config paths are being used (now that logging is configured)
    if config_path_override:
        logger.info(
            "Using override config path from YALLMP_CONFIG: %s", CONFIG_PATH
        )
    else:
        logger.info(
            "Using default config path: %s (set YALLMP_CONFIG to override)",
            CONFIG_PATH,
        )

    logger.info(f"Loading configuration from: {config_path}")
    logger.info(f"Resolved absolute path: {config_path.resolve()}")

    if not config_path.exists():
        logger.error(f"Config file not found: {config_path}")
        raise RuntimeError(f"Config file not found: {config_path}")

    env_values: dict[str, str] = {}
    if substitute_env:
        env_file = resolve_env_path(config_path, env_path)
        if env_file.exists():
            logger.info(f"Loading environment variables from {env_file}")
            env_values = load_env_values(env_file)

    with config_path.open("r", encoding="utf-8") as fh:
        data = yaml.safe_load(fh) or {}

    if substitute_env:
        data = _substitute_env_vars(data, env_values)

    logger.info(f"Configuration loaded successfully from {config_path}")
    return data


def _substitute_env_vars(
    obj: Any,
    env_values: Mapping[str, str] | None = None,
    warn_on_missing: bool = True,
) -> Any:
    """Recursively substitute environment variables in configuration values.

    Supports two formats:
    - ${VAR_NAME}: Braced format
    - $VAR_NAME: Simple format

    Args:
        obj: The configuration object (dict, list, or string).
        env_values: Optional map of env values to use for substitution.
        warn_on_missing: If False, missing variables are left as placeholders without warnings.

    Returns:
        The object with environment variables substituted.

    Raises:
        ValueError: If a referenced environment variable is not set.
    """
    import re

    env_values = env_values or {}

    if isinstance(obj, dict):
        return {
            k: _substitute_env_vars(v, env_values, warn_on_missing)
            for k, v in obj.items()
        }
    if isinstance(obj, list):
        return [
            _substitute_env_vars(item, env_values, warn_on_missing)
            for item in obj
        ]
    if isinstance(obj, str):
        # Replace ${VAR_NAME} or $VAR_NAME with environment variable value
        pattern = re.compile(r"\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)")

        def replace_var(match):
            var_name = match.group(1) or match.group(2)
            value = env_values.get(var_name)
            if value is None:
                value = os.getenv(var_name)
            if value is None:
                if warn_on_missing:
                    logger.warning(
                        f"CONFIG ERROR: Environment variable '${var_name}' is not set! "
                        f"Check your .env file or export it in your shell. "
                        f"The literal placeholder will be used (request will likely fail)."
                    )
                return match.group(0)  # Return original placeholder
            return value

        return pattern.sub(replace_var, obj)
    return obj
