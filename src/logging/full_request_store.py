"""Canonical file-based request payload storage."""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from threading import Lock
from typing import Any
from uuid import UUID

from ..config_store import CONFIG_STORE

logger = logging.getLogger("yallmp-proxy")


@dataclass(frozen=True, slots=True)
class FullRequestStorageSettings:
    enabled: bool = True
    path: Path = Path("logs/requests")
    retention_hours: int = 48
    cleanup_on_startup: bool = True
    cleanup_interval_hours: int = 24


def _parse_bool(value: Any, default: bool) -> bool:
    if value is None:
        return default
    if isinstance(value, str):
        return value.strip().lower() in {"1", "true", "yes", "on"}
    return bool(value)


def get_full_request_storage_settings(
    config: dict[str, Any] | None = None,
) -> FullRequestStorageSettings:
    runtime_config = config or CONFIG_STORE.get_runtime_config()
    proxy_settings = runtime_config.get("proxy_settings") or {}
    logging_cfg = proxy_settings.get("logging") or {}
    storage_cfg = logging_cfg.get("full_request_storage") or {}

    root = Path(__file__).resolve().parent.parent.parent
    path_value = storage_cfg.get("path", "logs/requests")
    path = Path(path_value)
    if not path.is_absolute():
        path = root / path

    retention_hours = max(1, int(storage_cfg.get("retention_hours", 48)))
    cleanup_interval_hours = max(1, int(storage_cfg.get("cleanup_interval_hours", 24)))

    return FullRequestStorageSettings(
        enabled=_parse_bool(storage_cfg.get("enabled"), True),
        path=path,
        retention_hours=retention_hours,
        cleanup_on_startup=_parse_bool(storage_cfg.get("cleanup_on_startup"), True),
        cleanup_interval_hours=cleanup_interval_hours,
    )


class FullRequestStore:
    """Stores canonical full request payloads on disk."""

    def __init__(self, settings: FullRequestStorageSettings) -> None:
        self._settings = settings
        self._root = settings.path

    @property
    def settings(self) -> FullRequestStorageSettings:
        return self._settings

    def build_payload_path(self, request_id: UUID | str, request_time: datetime) -> Path:
        ts = request_time.astimezone(timezone.utc)
        request_uuid = str(request_id)
        return self._root / ts.strftime("%Y") / ts.strftime("%m") / ts.strftime("%d") / f"{request_uuid}.json"

    def build_expiration(self, request_time: datetime) -> datetime:
        return request_time.astimezone(timezone.utc) + timedelta(hours=self._settings.retention_hours)

    def write_payload(
        self,
        request_id: UUID | str,
        request_time: datetime,
        payload: dict[str, Any],
    ) -> tuple[Path | None, datetime | None]:
        if not self._settings.enabled:
            return None, None

        path = self.build_payload_path(request_id, request_time)
        path.parent.mkdir(parents=True, exist_ok=True)
        expires_at = self.build_expiration(request_time)
        serialized = dict(payload)
        serialized["expires_at"] = expires_at.isoformat()

        tmp_path = path.with_suffix(".json.tmp")
        tmp_path.write_text(json.dumps(serialized, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        tmp_path.replace(path)
        return path, expires_at

    def read_payload(self, full_request_path: str | None) -> dict[str, Any] | None:
        if not full_request_path:
            return None
        path = Path(full_request_path)
        if not path.exists():
            return None
        try:
            return json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            logger.warning("Failed to read full request payload %s: %s", path, exc)
            return None

    @staticmethod
    def payload_expires_at(payload: dict[str, Any]) -> datetime | None:
        raw = payload.get("expires_at")
        if not isinstance(raw, str):
            return None
        try:
            expires_at = datetime.fromisoformat(raw)
        except ValueError:
            return None
        if expires_at.tzinfo is None:
            expires_at = expires_at.replace(tzinfo=timezone.utc)
        return expires_at

    def delete_payload_and_sidecars(self, full_request_path: str | Path) -> bool:
        path = Path(full_request_path)
        deleted = False
        for candidate in (path, path.with_suffix(".log"), path.with_suffix(".parsed.log")):
            try:
                if candidate.exists():
                    candidate.unlink()
                    deleted = True
            except OSError as exc:
                logger.warning("Failed to delete request artifact %s: %s", candidate, exc)
        return deleted

    def cleanup_expired(self, now: datetime | None = None) -> int:
        if not self._settings.enabled or not self._root.exists():
            return 0

        utc_now = now or datetime.now(timezone.utc)
        deleted = 0
        for payload_path in self._root.rglob("*.json"):
            try:
                payload = json.loads(payload_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                logger.warning("Failed to inspect request payload %s during cleanup: %s", payload_path, exc)
                continue

            expires_at = self.payload_expires_at(payload)
            if expires_at is None:
                continue
            if expires_at <= utc_now and self.delete_payload_and_sidecars(payload_path):
                deleted += 1

        return deleted

    def search_request_ids(
        self,
        term: str,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
    ) -> set[str]:
        if not self._settings.enabled or not term or not self._root.exists():
            return set()

        term_lower = term.lower()
        matches: set[str] = set()
        for payload_path in self._root.rglob("*.json"):
            try:
                payload = json.loads(payload_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                continue

            expires_at = self.payload_expires_at(payload)
            if expires_at is not None and expires_at <= datetime.now(timezone.utc):
                continue

            request_time_raw = payload.get("request_time")
            request_time: datetime | None = None
            if isinstance(request_time_raw, str):
                try:
                    request_time = datetime.fromisoformat(request_time_raw)
                except ValueError:
                    request_time = None
            if request_time is not None and request_time.tzinfo is None:
                request_time = request_time.replace(tzinfo=timezone.utc)

            if start_time and request_time and request_time < start_time:
                continue
            if end_time and request_time and request_time > end_time:
                continue

            serialized = json.dumps(payload, ensure_ascii=False).lower()
            if term_lower in serialized:
                request_id = payload.get("id")
                if isinstance(request_id, str):
                    matches.add(request_id)
        return matches


_STORE_LOCK = Lock()
_FULL_REQUEST_STORE: FullRequestStore | None = None
_FULL_REQUEST_STORE_KEY: tuple[str, int, bool, bool, int] | None = None


def get_full_request_store(
    config: dict[str, Any] | None = None,
) -> FullRequestStore:
    global _FULL_REQUEST_STORE
    global _FULL_REQUEST_STORE_KEY

    settings = get_full_request_storage_settings(config)
    key = (
        str(settings.path),
        settings.retention_hours,
        settings.enabled,
        settings.cleanup_on_startup,
        settings.cleanup_interval_hours,
    )
    with _STORE_LOCK:
        if _FULL_REQUEST_STORE is None or _FULL_REQUEST_STORE_KEY != key:
            _FULL_REQUEST_STORE = FullRequestStore(settings)
            _FULL_REQUEST_STORE_KEY = key
        return _FULL_REQUEST_STORE
