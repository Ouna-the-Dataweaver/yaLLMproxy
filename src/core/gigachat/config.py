"""GigaChat-specific backend configuration."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Literal, Mapping, cast

DEFAULT_AUTH_URL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
DEFAULT_BASE_URL = "https://gigachat.devices.sberbank.ru/api/v1"
DEFAULT_MODEL = "GigaChat-2-Max"
DEFAULT_SCOPE = "GIGACHAT_API_CORP"
DEFAULT_TIMEOUT = 120.0
DEFAULT_VERIFY_SSL = True

ConnectionMode = Literal["cloud", "local"]


@dataclass(frozen=True, slots=True)
class GigaChatBackendConfig:
    mode: ConnectionMode
    model_name: str
    base_url: str
    auth_url: str
    scope: str
    api_key: str | None
    client_cert_file: str | None
    client_key_file: str | None
    verify_ssl: bool
    timeout: float
    emulate_tool_calls: bool


def _parse_bool(value: Any, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, str):
        return value.strip().lower() not in {"0", "false", "no", "off", ""}
    return bool(value)


def _resolve_mode(
    mode_value: str | None,
    api_key: str | None,
    scope: str | None,
    client_cert_file: str | None,
    client_key_file: str | None,
) -> ConnectionMode:
    if mode_value and mode_value.strip():
        normalized = mode_value.strip().lower()
        if normalized not in {"cloud", "local"}:
            raise ValueError("GigaChat mode must be either 'cloud' or 'local'.")
        return cast(ConnectionMode, normalized)

    has_cloud = bool(api_key and scope)
    has_local = bool(client_cert_file and client_key_file)
    if has_cloud and has_local:
        raise ValueError(
            "Both GigaChat cloud and local credentials are configured. "
            "Set mode to either 'cloud' or 'local'."
        )
    if has_local:
        return "local"
    if has_cloud:
        return "cloud"
    raise ValueError(
        "GigaChat credentials are not configured. "
        "For cloud mode set api_key and scope. "
        "For local mode set client_cert and client_key."
    )


def build_gigachat_config(params: Mapping[str, Any]) -> GigaChatBackendConfig:
    """Build a GigaChat backend config from model_params."""
    api_key = _str_or_none(params.get("api_key"))
    scope = _str_or_none(params.get("scope")) or DEFAULT_SCOPE
    client_cert_file = _str_or_none(params.get("client_cert"))
    client_key_file = _str_or_none(params.get("client_key"))

    mode = _resolve_mode(
        mode_value=_str_or_none(params.get("mode")),
        api_key=api_key,
        scope=scope,
        client_cert_file=client_cert_file,
        client_key_file=client_key_file,
    )

    cfg = GigaChatBackendConfig(
        mode=mode,
        model_name=_str_or_none(params.get("model")) or DEFAULT_MODEL,
        base_url=_str_or_none(params.get("api_base")) or DEFAULT_BASE_URL,
        auth_url=_str_or_none(params.get("auth_url")) or DEFAULT_AUTH_URL,
        scope=scope,
        api_key=api_key,
        client_cert_file=client_cert_file,
        client_key_file=client_key_file,
        verify_ssl=_parse_bool(params.get("verify_ssl"), DEFAULT_VERIFY_SSL),
        timeout=_float_or_default(params.get("request_timeout"), DEFAULT_TIMEOUT),
        emulate_tool_calls=_parse_bool(
            params.get("emulate_tool_calls"), False
        ),
    )
    _validate_config(cfg)
    return cfg


def _str_or_none(value: Any) -> str | None:
    if value is None:
        return None
    text = str(value).strip()
    return text if text else None


def _float_or_default(value: Any, default: float) -> float:
    if value is None:
        return default
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _validate_config(cfg: GigaChatBackendConfig) -> None:
    missing: list[str] = []
    if cfg.mode == "cloud":
        if not cfg.api_key:
            missing.append("api_key")
        if not cfg.scope:
            missing.append("scope")
        if not cfg.auth_url:
            missing.append("auth_url")
    else:
        if not cfg.client_cert_file:
            missing.append("client_cert")
        if not cfg.client_key_file:
            missing.append("client_key")

    if not cfg.base_url:
        missing.append("api_base")

    if missing:
        raise ValueError(
            f"GigaChat {cfg.mode} mode is missing required parameters: {', '.join(missing)}"
        )
