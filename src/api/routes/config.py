"""Config management endpoints for web interface."""

import logging
from pathlib import Path
from typing import Any

from fastapi import HTTPException
from fastapi.responses import FileResponse

from ...core.registry import get_router
from ...config_loader import load_config, CONFIG_PATH

logger = logging.getLogger("yallmp-proxy")


async def get_full_config() -> dict:
    """Get the full configuration (for editing).
    
    GET /admin/config
    
    Returns:
        The complete configuration dictionary.
    """
    try:
        config = load_config()
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
    import yaml
    from pathlib import Path
    
    config_path = Path(CONFIG_PATH)
    
    try:
        # Write back to config file
        with config_path.open("w", encoding="utf-8") as f:
            yaml.safe_dump(new_config, f, default_flow_style=False, sort_keys=False)
        
        logger.info(f"Configuration updated at {config_path}")
        return {"status": "ok", "message": "Configuration saved successfully"}
    except Exception as exc:
        logger.error(f"Failed to save config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def get_models_list() -> list[dict]:
    """Get detailed list of all registered models.
    
    GET /admin/models
    
    Returns:
        List of models with their full configuration.
    """
    from ...core.registry import get_router
    
    config = load_config()
    config_models = config.get("model_list", [])
    
    # Get model names from config file
    config_model_names = {m.get("model_name") for m in config_models}
    
    # Get runtime-registered models from router
    router = get_router()
    runtime_models = []
    for backend in router.backends.values():
        if backend.name not in config_model_names:
            # This is a runtime-registered model
            runtime_models.append({
                "model_name": backend.name,
                "model_params": {
                    "api_base": backend.base_url,
                    "api_type": backend.api_type,
                    "model": backend.target_model or "",
                    "request_timeout": backend.timeout,
                    "supports_reasoning": backend.supports_reasoning,
                },
                "editable": backend.editable,
            })
    
    # Combine config models (with editable=False) and runtime models (with editable=True)
    all_models = []
    
    # Add config models
    for model in config_models:
        model_copy = dict(model)
        model_copy["editable"] = False
        all_models.append(model_copy)
    
    # Add runtime models
    all_models.extend(runtime_models)
    
    # Return masked config for security
    return [_mask_sensitive_data(model) for model in all_models]


async def delete_model(model_name: str) -> dict:
    """Delete a runtime-registered model from memory.
    
    DELETE /admin/models/{model_name}
    
    Args:
        model_name: The name of the model to delete.
    
    Returns:
        Success status.
    
    Note:
        This only deletes runtime-registered models (editable=true).
        Config-loaded models cannot be deleted as they are defined in config.yaml.
    """
    config = load_config()
    config_model_names = {m.get("model_name") for m in config.get("model_list", [])}
    
    # Config-loaded models cannot be deleted
    if model_name in config_model_names:
        raise HTTPException(
            status_code=400, 
            detail=f"Model '{model_name}' is loaded from config.yaml and cannot be deleted. "
                   f"Edit the config file and restart the proxy to remove it."
        )
    
    # Only runtime-registered models can be deleted
    router = get_router()
    if model_name not in router.backends:
        raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")
    
    # Remove from router
    removed = await router.unregister_backend(model_name)
    if removed:
        logger.info(f"Runtime model '{model_name}' deleted from memory")
        return {"status": "ok", "message": f"Model '{model_name}' deleted successfully"}
    else:
        raise HTTPException(status_code=500, detail=f"Failed to delete model '{model_name}'")


async def serve_admin_ui():
    """Serve the admin UI frontend.
    
    GET /admin/
    
    Returns:
        The admin HTML page.
    """
    static_dir = Path(__file__).parent.parent.parent.parent / "static" / "admin"
    index_path = static_dir / "admin_new.html"
    
    if not index_path.exists():
        raise HTTPException(
            status_code=404,
            detail="Admin UI not found. Please ensure the static/admin directory exists with admin_new.html"
        )
    
    return FileResponse(index_path)


async def serve_admin_ui_v2():
    """Serve the admin UI v2 frontend.
    
    GET /admin_2/
    
    Returns:
        The admin HTML page.
    """
    static_dir = Path(__file__).parent.parent.parent.parent / "static" / "admin"
    index_path = static_dir / "admin_2.html"
    
    if not index_path.exists():
        raise HTTPException(
            status_code=404,
            detail="Admin UI v2 not found. Please ensure the static/admin directory exists with admin_2.html"
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
