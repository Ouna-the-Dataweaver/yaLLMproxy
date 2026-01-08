"""Config management endpoints for web interface."""

import logging
from pathlib import Path
from typing import Any

from fastapi import HTTPException
from fastapi.responses import FileResponse

from ...core.registry import get_router
from ...config_store import CONFIG_STORE

logger = logging.getLogger("yallmp-proxy")


async def get_full_config() -> dict:
    """Get the full configuration (for editing).
    
    GET /admin/config
    
    Returns:
        The complete configuration dictionary.
    """
    try:
        config = CONFIG_STORE.get_default_raw()
        # Mask sensitive values for display
        return _mask_sensitive_data(config)
    except Exception as exc:
        logger.error(f"Failed to load config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def update_config(new_config: dict) -> dict:
    """Update the full configuration.
    
    PUT /admin/config
    
    Args:
        new_config: The new configuration to save.
    
    Returns:
        Success status.
    """
    try:
        CONFIG_STORE.save_default(new_config)
        logger.info("Configuration updated at %s", CONFIG_STORE.default_path)
        return {"status": "ok", "message": "Configuration saved successfully"}
    except Exception as exc:
        logger.error(f"Failed to save config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def reload_config() -> dict:
    """Reload configuration from disk without restarting.
    
    POST /admin/config/reload
    
    This endpoint will:
    1. Reload config files from disk
    2. Re-parse all models and backends
    3. Update the router's runtime state
    
    Returns:
        Status message with reload details.
    """
    try:
        # Reload config store from disk
        CONFIG_STORE.reload()

        # Get the new merged config
        new_config = CONFIG_STORE.get_runtime_config()

        # Update router with new config
        router = get_router()
        await router.reload_config(new_config)

        logger.info("Configuration reloaded successfully")
        return {
            "status": "ok",
            "message": "Configuration reloaded successfully",
            "models_count": len(router.backends),
        }
    except Exception as exc:
        logger.error(f"Failed to reload config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def get_models_list() -> dict:
    """Get detailed list of all registered models.
    
    GET /admin/models
    
    Returns:
        Dictionary with default and added model lists.
    """
    default_models, added_models = CONFIG_STORE.list_models()

    return {
        "default": [_mask_sensitive_data(model) for model in default_models],
        "added": [_mask_sensitive_data(model) for model in added_models],
    }


async def delete_model(model_name: str) -> dict:
    """Delete a runtime-registered model from memory.
    
    DELETE /admin/models/{model_name}
    
    Args:
        model_name: The name of the model to delete.
    
    Returns:
        Success status.
    
    Note:
        This only deletes runtime-registered models (editable=true).
        Config-loaded models cannot be deleted as they are defined in config_default.yaml.
    """
    config = CONFIG_STORE.get_default_raw()
    config_model_names = {m.get("model_name") for m in config.get("model_list", [])}
    
    # Config-loaded models cannot be deleted
    if model_name in config_model_names:
        raise HTTPException(
            status_code=400, 
            detail=f"Model '{model_name}' is loaded from config_default.yaml and cannot be deleted. "
                   f"Edit config_default.yaml and restart the proxy to remove it."
        )
    
    # Only runtime-registered models can be deleted
    router = get_router()
    removed_from_config = CONFIG_STORE.delete_added_model(model_name)
    removed_from_router = False
    if model_name in router.backends:
        removed_from_router = await router.unregister_backend(model_name)
    if not removed_from_config and not removed_from_router:
        raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")
    
    if removed_from_router or removed_from_config:
        logger.info(f"Runtime model '{model_name}' deleted from memory")
        return {"status": "ok", "message": f"Model '{model_name}' deleted successfully"}
    raise HTTPException(status_code=500, detail=f"Failed to delete model '{model_name}'")


async def copy_model(source: str, target: str) -> dict:
    """Copy an existing model to a new model with a different name.
    
    POST /admin/models/copy?source={source}&target={target}
    
    Query params:
        source: The name of the model to copy.
        target: The name for the new copied model.
    
    Returns:
        The newly created model entry (with sensitive data masked).
    
    Note:
        The copied model is always saved to config_added.yaml (editable).
        Source model can come from either default or added config.
    """
    if not source or not source.strip():
        raise HTTPException(status_code=400, detail="'source' query parameter is required")
    if not target or not target.strip():
        raise HTTPException(status_code=400, detail="'target' query parameter is required")
    
    source = source.strip()
    target = target.strip()
    
    try:
        new_model = CONFIG_STORE.copy_model(source, target)
        logger.info(f"Model '{source}' copied to '{target}'")
        return {
            "status": "ok",
            "message": f"Model '{source}' copied to '{target}'",
            "model": _mask_sensitive_data(new_model),
        }
    except ValueError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
    except Exception as exc:
        logger.error(f"Failed to copy model: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def serve_admin_ui():
    """Serve the admin UI frontend.
    
    GET /admin/
    
    Returns:
        The admin HTML page.
    """
    static_dir = Path(__file__).parent.parent.parent.parent / "static" / "admin"
    index_path = static_dir / "admin.html"
    
    if not index_path.exists():
        raise HTTPException(
            status_code=404,
            detail="Admin UI not found. Please ensure the static/admin directory exists with admin.html"
        )
    
    return FileResponse(index_path)


def _mask_sensitive_data(data: Any) -> Any:
    """Recursively mask sensitive values like API keys.
    
    Args:
        data: The data to mask.
    
    Returns:
        Data with sensitive values masked.
    """
    if isinstance(data, dict):
        masked = {}
        for key, value in data.items():
            # Mask API keys
            if "api_key" in key.lower() and isinstance(value, str) and len(value) > 8:
                masked[key] = value[:4] + "****" + value[-4:]
            else:
                masked[key] = _mask_sensitive_data(value)
        return masked
    elif isinstance(data, list):
        return [_mask_sensitive_data(item) for item in data]
    else:
        return data
