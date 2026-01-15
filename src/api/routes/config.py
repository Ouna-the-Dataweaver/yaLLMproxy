"""Config management endpoints for web interface."""

import hmac
import logging
from pathlib import Path
from typing import Any

from fastapi import HTTPException, Request
from fastapi.responses import FileResponse

from ...core.registry import get_router
from ...config_store import CONFIG_STORE, _normalize_protected

logger = logging.getLogger("yallmp-proxy")
ADMIN_PASSWORD_HEADER = "x-admin-password"


def _extract_admin_password(request: Request, payload: dict | None = None) -> str | None:
    header = request.headers.get(ADMIN_PASSWORD_HEADER)
    if header and header.strip():
        return header.strip()
    query = request.query_params.get("admin_password")
    if query and query.strip():
        return query.strip()
    if payload:
        value = payload.get("admin_password")
        if isinstance(value, str) and value.strip():
            return value.strip()
    return None


def _require_admin_password(
    request: Request, payload: dict | None = None, detail: str | None = None
) -> None:
    expected = CONFIG_STORE.get_admin_password()
    if not expected:
        raise HTTPException(
            status_code=403,
            detail="Admin password is not configured in .env",
        )
    provided = _extract_admin_password(request, payload)
    if not provided or not hmac.compare_digest(provided, expected):
        raise HTTPException(
            status_code=403,
            detail=detail or "Admin password required for protected model changes",
        )


def _model_is_protected(model: dict[str, Any] | None) -> bool:
    if not model:
        return False
    return _normalize_protected(model.get("protected"), default=True)


def _parse_bool_param(value: str | None) -> bool:
    if not value:
        return False
    return value.strip().lower() in {"1", "true", "yes", "on"}


def _requires_password_for_config_update(
    current_cfg: dict[str, Any], new_cfg: dict[str, Any]
) -> bool:
    def _model_map(cfg: dict[str, Any]) -> dict[str, dict[str, Any]]:
        models = {}
        for model in cfg.get("model_list", []) or []:
            if isinstance(model, dict):
                name = model.get("model_name")
                if name:
                    models[name] = model
        return models

    current_models = _model_map(current_cfg)
    new_models = _model_map(new_cfg)

    for name, current_model in current_models.items():
        if _normalize_protected(current_model.get("protected"), default=True):
            new_model = new_models.get(name)
            if new_model is None or new_model != current_model:
                return True

    for name, new_model in new_models.items():
        if name not in current_models and _normalize_protected(
            new_model.get("protected"), default=True
        ):
            return True
    return False


async def get_full_config() -> dict:
    """Get the full configuration (for editing).
    
    GET /admin/config
    
    Returns:
        The complete configuration dictionary.
    """
    try:
        config = CONFIG_STORE.get_raw()
        # Mask sensitive values for display
        return _mask_sensitive_data(config)
    except Exception as exc:
        logger.error(f"Failed to load config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc


async def update_config(request: Request, new_config: dict) -> dict:
    """Update the full configuration.
    
    PUT /admin/config
    
    Args:
        new_config: The new configuration to save.
    
    Returns:
        Success status.
    """
    try:
        current_cfg = CONFIG_STORE.get_raw()
        new_config = dict(new_config or {})
        new_config.pop("admin_password", None)
        if _requires_password_for_config_update(current_cfg, new_config):
            _require_admin_password(
                request,
                payload=new_config,
                detail="Admin password required to modify protected models",
            )
        CONFIG_STORE.save(new_config)
        logger.info("Configuration updated at %s", CONFIG_STORE.config_path)
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
        Dictionary with protected and unprotected model lists.
    """
    protected_models, unprotected_models = CONFIG_STORE.list_models()

    return {
        "protected": [_mask_sensitive_data(model) for model in protected_models],
        "unprotected": [_mask_sensitive_data(model) for model in unprotected_models],
    }


async def get_models_tree() -> dict:
    """Get the full model inheritance tree.

    GET /admin/models/tree
    """
    tree = CONFIG_STORE.get_model_tree()
    nodes: dict[str, Any] = {}
    for name, node in tree.nodes.items():
        nodes[name] = {
            "config": _mask_sensitive_data(node.config),
            "parent": node.parent,
            "children": list(node.children),
            "protected": node.protected,
            "editable": node.editable,
        }
    return {
        "roots": list(tree.roots),
        "nodes": nodes,
    }


async def get_model_ancestry(model_name: str) -> dict:
    """Get the inheritance chain for a model.

    GET /admin/models/{name}/ancestry
    """
    tree = CONFIG_STORE.get_model_tree()
    chain = tree.get_inheritance_chain(model_name)
    if not chain:
        raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")
    return {
        "model": model_name,
        "chain": chain,
        "inheritance_depth": len(chain),
    }


async def get_model_dependents(model_name: str) -> dict:
    """Get direct and recursive dependents for a model.

    GET /admin/models/{name}/dependents
    """
    tree = CONFIG_STORE.get_model_tree()
    if not tree.get_node(model_name):
        raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")
    direct_children = tree.get_children(model_name)
    all_descendants = tree.get_descendants(model_name)
    return {
        "model": model_name,
        "direct_children": direct_children,
        "all_descendants": all_descendants,
        "descendant_count": len(all_descendants),
    }


async def delete_model(model_name: str, request: Request) -> dict:
    """Delete a model from config and runtime.
    
    DELETE /admin/models/{model_name}
    
    Args:
        model_name: The name of the model to delete.
    
    Returns:
        Success status.
    
    Note:
        Protected models require an admin password to delete.
    """
    cascade = _parse_bool_param(request.query_params.get("cascade"))
    tree = CONFIG_STORE.get_model_tree()
    node = tree.get_node(model_name)
    if not node:
        raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")

    to_delete = [model_name]
    if cascade:
        to_delete.extend(tree.get_descendants(model_name))
    if any(tree.get_node(name).protected for name in to_delete if tree.get_node(name)):
        _require_admin_password(
            request,
            detail="Admin password required to delete protected model(s)",
        )

    result = CONFIG_STORE.delete_model_with_dependents(model_name, cascade=cascade)
    if not result.success:
        if result.error == "Model not found":
            raise HTTPException(status_code=404, detail=f"Model '{model_name}' not found")
        if result.dependents:
            raise HTTPException(
                status_code=409,
                detail={
                    "error": result.error or "Cannot delete model with existing dependents",
                    "dependents": result.dependents,
                    "hint": "Set cascade=true to delete dependents, or update them to use a different parent",
                },
            )
        raise HTTPException(status_code=400, detail=result.error or "Delete failed")

    router = get_router()
    for name in result.deleted:
        if name in router.backends:
            await router.unregister_backend(name)

    logger.info("Deleted model(s): %s", ", ".join(result.deleted))
    return {
        "status": "ok",
        "message": f"Deleted {len(result.deleted)} model(s)",
        "deleted": result.deleted,
    }


async def copy_model(source: str, target: str, request: Request) -> dict:
    """Copy an existing model to a new model with a different name.
    
    POST /admin/models/copy?source={source}&target={target}
    
    Query params:
        source: The name of the model to copy.
        target: The name for the new copied model.
    
    Returns:
        The newly created model entry (with sensitive data masked).
    
    Note:
        Copying a protected model requires an admin password.
    """
    if not source or not source.strip():
        raise HTTPException(status_code=400, detail="'source' query parameter is required")
    if not target or not target.strip():
        raise HTTPException(status_code=400, detail="'target' query parameter is required")
    
    source = source.strip()
    target = target.strip()
    
    try:
        source_model = CONFIG_STORE.find_model(source)
        if _model_is_protected(source_model):
            _require_admin_password(
                request,
                detail=f"Admin password required to copy protected model '{source}'",
            )
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
    """Recursively remove sensitive values like API keys.
    
    Args:
        data: The data to mask.
    
    Returns:
        Data with sensitive values masked.
    """
    if isinstance(data, dict):
        masked: dict[str, Any] = {}
        for key, value in data.items():
            # Remove API keys entirely
            if "api_key" in key.lower():
                continue
            masked[key] = _mask_sensitive_data(value)
        return masked
    elif isinstance(data, list):
        return [_mask_sensitive_data(item) for item in data]
    else:
        return data


# Template directory for jinja templates
TEMPLATES_DIR = Path(__file__).parent.parent.parent.parent / "configs" / "jinja_templates"


async def list_templates():
    """List available jinja templates.

    GET /admin/templates

    Returns:
        List of template filenames in configs/jinja_templates.
    """
    if not TEMPLATES_DIR.exists():
        return {"templates": []}

    templates = []
    for file_path in sorted(TEMPLATES_DIR.iterdir()):
        if file_path.is_file() and file_path.suffix in (".jinja", ".j2", ".jinja2"):
            templates.append({
                "name": file_path.name,
                "path": f"configs/jinja_templates/{file_path.name}"
            })

    return {"templates": templates}


async def upload_template(request: Request):
    """Upload a new jinja template.

    POST /admin/templates

    Expects multipart form data with:
        - file: The template file to upload

    Returns:
        The path to the uploaded template.
    """
    form = await request.form()
    file = form.get("file")

    if not file:
        raise HTTPException(status_code=400, detail="No file provided")

    # Validate filename
    filename = file.filename
    if not filename:
        raise HTTPException(status_code=400, detail="No filename provided")

    # Sanitize filename - only allow safe characters
    import re
    safe_name = re.sub(r'[^\w\-.]', '_', filename)

    # Ensure it has a valid template extension
    if not any(safe_name.endswith(ext) for ext in (".jinja", ".j2", ".jinja2")):
        safe_name += ".jinja"

    # Create templates directory if it doesn't exist
    TEMPLATES_DIR.mkdir(parents=True, exist_ok=True)

    # Write the file
    file_path = TEMPLATES_DIR / safe_name
    content = await file.read()

    # Basic validation - check it's text content
    try:
        content.decode("utf-8")
    except UnicodeDecodeError:
        raise HTTPException(status_code=400, detail="File must be valid UTF-8 text")

    file_path.write_bytes(content)

    logger.info("Uploaded template: %s", safe_name)

    return {
        "name": safe_name,
        "path": f"configs/jinja_templates/{safe_name}"
    }
