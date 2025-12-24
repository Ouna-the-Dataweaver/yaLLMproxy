"""Configuration loading from YAML files with environment variable support."""

import logging
import os
from pathlib import Path
from typing import Any

import yaml
from dotenv import load_dotenv

logger = logging.getLogger("yallmp-proxy")

# Default path to the LiteLLM-style config file (relative to project root)
DEFAULT_CONFIG_PATH = "configs/config.yaml"

# Environment variable to override config path
CONFIG_PATH = os.getenv("YALLMP_CONFIG", DEFAULT_CONFIG_PATH)


def load_config(path: str | None = None) -> dict:
    """Load configuration from a YAML file.
    
    Args:
        path: Path to the config file. Defaults to YALLMP_CONFIG env var,
              or configs/config.yaml in the project root.
    
    Returns:
        Parsed configuration dictionary.
    """
    if path is None:
        path = CONFIG_PATH
    
    # Resolve path relative to project root if not absolute
    if not Path(path).is_absolute():
        # Try to find the project root (where config.yaml typically is)
        project_root = Path(__file__).parent.parent
        config_path = project_root / path
    else:
        config_path = Path(path)
    
    logger.info(f"Loading configuration from {config_path}")
    
    if not config_path.exists():
        logger.error(f"Config file not found: {config_path}")
        raise RuntimeError(f"Config file not found: {config_path}")
    
    # Load .env file from the same directory as the config file
    env_path = config_path.parent / ".env"
    if env_path.exists():
        logger.info(f"Loading environment variables from {env_path}")
        load_dotenv(env_path)
    
    with config_path.open("r", encoding="utf-8") as fh:
        data = yaml.safe_load(fh) or {}
    
    # Substitute environment variables in the configuration
    data = _substitute_env_vars(data)
    
    logger.info(f"Configuration loaded successfully from {path}")
    return data


def _substitute_env_vars(obj: Any) -> Any:
    """Recursively substitute environment variables in configuration values.
    
    Supports two formats:
    - ${VAR_NAME}: Braced format
    - $VAR_NAME: Simple format
    
    Args:
        obj: The configuration object (dict, list, or string).
    
    Returns:
        The object with environment variables substituted.
    """
    import re
    
    if isinstance(obj, dict):
        return {k: _substitute_env_vars(v) for k, v in obj.items()}
    elif isinstance(obj, list):
        return [_substitute_env_vars(item) for item in obj]
    elif isinstance(obj, str):
        # Replace ${VAR_NAME} or $VAR_NAME with environment variable value
        pattern = re.compile(r'\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)')
        
        def replace_var(match):
            var_name = match.group(1) or match.group(2)
            return os.getenv(var_name, match.group(0))  # Return original if not found
        
        return pattern.sub(replace_var, obj)
    else:
        return obj
