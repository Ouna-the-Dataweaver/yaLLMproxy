# Config Reload Bug

## Summary

When updating model configuration through the admin UI (PUT /admin/config), parameter overrides (such as `tool_choice: none`) stop being applied until the "Reload Config" button is pressed. This occurs because the router's in-memory state is not updated after saving the config.

## Root Cause

The bug exists in `src/api/routes/config.py` in the `update_config` function.

### Problem Location

**File:** `src/api/routes/config.py:108-134`

```python
async def update_config(request: Request, new_config: dict) -> dict:
    """Update the full configuration."""
    try:
        current_cfg = CONFIG_STORE.get_raw()
        new_config = dict(new_config or {})
        new_config.pop("admin_password", None)
        if _requires_password_for_config_update(current_cfg, new_config):
            _require_admin_password(...)
        CONFIG_STORE.save(new_config)  # Only saves to disk
        # MISSING: router.reload_config() call!
        logger.info("Configuration updated at %s", CONFIG_STORE.config_path)
        return {"status": "ok", "message": "Configuration saved successfully"}
```

When `CONFIG_STORE.save()` is called, it:
1. Writes the new configuration to disk (`configs/config.yaml`)
2. Updates the internal `_raw` config dictionary
3. Rebuilds the model tree via `_rebuild_model_tree_locked()`

However, it does **NOT** update the router's in-memory `Backend` objects. The router continues to use the old backend instances with stale parameter configurations.

### Why Parameters Stop Working

1. **Backend objects store parameters in memory:** Each `Backend` instance in `router.backends` contains a `parameters: dict[str, ParameterConfig]` field that is set at creation time via `ProxyRouter._parse_backends()`.

2. **Parameters are applied during request processing:** The `build_backend_body()` function in `src/core/backend.py:191-267` uses `backend.parameters` to apply parameter overrides to outgoing requests.

3. **Stale backend objects:** When the config is updated via PUT /admin/config, new `Backend` objects are never created. The router keeps using the old instances with outdated parameter configurations.

### Contrast with Working Reload Flow

The "Reload Config" button works correctly because `reload_config()` (`src/api/routes/config.py:137-169`) properly updates the router:

```python
async def reload_config() -> dict:
    CONFIG_STORE.reload()  # Reload from disk
    new_config = CONFIG_STORE.get_runtime_config()
    router = get_router()
    await router.reload_config(new_config)  # Properly updates router
    return {"status": "ok", ...}
```

## Secondary Issue: POST /admin/models Missing Parameters

When models are added/updated via POST /admin/models, parameter configurations can be lost.

### Problem Location

**File:** `src/api/routes/admin.py:118-128`

```python
backend = Backend(
    name=model_name,
    base_url=api_base,
    api_key=api_key,
    timeout=timeout_val,
    target_model=target_model,
    api_type=api_type,
    anthropic_version=anthropic_version,
    supports_reasoning=supports_reasoning,
    supports_responses_api=supports_responses_api,
    http2=http2,
    editable=editable,
    # MISSING: parameters=param_configs
)
```

The `parameters` field is never passed when creating the `Backend` object in `register_model()`. If a model already exists in the config with parameter overrides (like `tool_choice: none`), updating it via POST /admin/models will create a new backend without those parameters.

## Reproduction Tests

### Test 1: PUT /admin/config Does Not Update Router

```python
import pytest
from fastapi.testclient import TestClient
from yaLLMproxy.src.main import app
from yaLLMproxy.src.config_store import CONFIG_STORE
from yaLLMproxy.src.core.registry import get_router

client = TestClient(app)

def test_config_update_updates_router():
    """Test that PUT /admin/config updates router backends immediately."""
    # Get initial model config
    initial_cfg = CONFIG_STORE.get_raw()
    model_name = "glm_local_AWQ"
    
    # Modify parameters for the model
    updated_cfg = initial_cfg.copy()
    for model in updated_cfg.get("model_list", []):
        if model.get("model_name") == model_name:
            if "parameters" not in model:
                model["parameters"] = {}
            model["parameters"]["tool_choice"] = {"default": "auto", "allow_override": True}
            break
    
    # Update config via API
    response = client.put("/admin/config", json=updated_cfg)
    assert response.status_code == 200
    
    # Router should have updated parameters
    router = get_router()
    backend = router.backends.get(model_name)
    assert backend is not None
    assert "tool_choice" in backend.parameters
    assert backend.parameters["tool_choice"].default == "auto"
```

### Test 2: Request Uses Updated Parameters

```python
def test_request_uses_updated_parameters():
    """Test that parameter overrides are applied to requests after config update."""
    # Update config to set tool_choice = none
    config = CONFIG_STORE.get_raw()
    for model in config.get("model_list", []):
        if model.get("model_name") == "glm_local_AWQ":
            model["parameters"] = {"tool_choice": {"default": "none", "allow_override": False}}
            break
    
    response = client.put("/admin/config", json=config)
    assert response.status_code == 200
    
    # Make a chat completion request
    response = client.post(
        "/v1/chat/completions",
        json={
            "model": "glm_local_AWQ",
            "messages": [{"role": "user", "content": "test"}],
            "tool_choice": "auto"  # Should be overridden to "none"
        }
    )
    
    # Verify the backend received the overridden parameter
    # This requires mocking the upstream or checking request logs
```

### Test 3: POST /admin/models Preserves Parameters

```python
def test_register_model_preserves_parameters():
    """Test that POST /admin/models preserves existing parameter config."""
    model_name = "glm_local_AWQ"
    
    # First, ensure model has parameters in config
    config = CONFIG_STORE.get_raw()
    for model in config.get("model_list", []):
        if model.get("model_name") == model_name:
            model["parameters"] = {"tool_choice": {"default": "none", "allow_override": False}}
            break
    CONFIG_STORE.save(config)
    
    # Update model via POST /admin/models
    response = client.post(
        "/admin/models",
        json={
            "model_name": model_name,
            "model_params": {
                "api_base": "http://nid-sc-28:16161/v1",
                "model": "GLM_air_awq"
            }
        }
    )
    assert response.status_code == 200
    
    # Router backend should still have parameters
    router = get_router()
    backend = router.backends.get(model_name)
    assert backend is not None
    assert backend.parameters.get("tool_choice") is not None
```

### Test 4: Reload Config Works as Expected

```python
def test_reload_config_updates_router():
    """Test that POST /admin/config/reload properly updates router."""
    # Modify config directly
    config = CONFIG_STORE.get_raw()
    for model in config.get("model_list", []):
        if model.get("model_name") == "glm_local_AWQ":
            model["parameters"] = {"tool_choice": {"default": "auto", "allow_override": True}}
            break
    CONFIG_STORE.save(config)
    
    # Reload config via API
    response = client.post("/admin/config/reload")
    assert response.status_code == 200
    
    # Router should have updated parameters
    router = get_router()
    backend = router.backends.get("glm_local_AWQ")
    assert backend is not None
    assert "tool_choice" in backend.parameters
    assert backend.parameters["tool_choice"].default == "auto"
```

## Proposed Fixes

### Fix 1: Update Router in update_config (Primary Fix)

In `src/api/routes/config.py`, modify the `update_config` function to also reload the router:

```python
async def update_config(request: Request, new_config: dict) -> dict:
    """Update the full configuration."""
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
        
        # Also reload the router to apply changes immediately
        router = get_router()
        await router.reload_config(CONFIG_STORE.get_runtime_config())
        
        logger.info("Configuration updated at %s", CONFIG_STORE.config_path)
        return {"status": "ok", "message": "Configuration saved successfully"}
    except Exception as exc:
        logger.error(f"Failed to save config: {exc}")
        raise HTTPException(status_code=500, detail=str(exc)) from exc
```

### Fix 2: Preserve Parameters in register_model (Secondary Fix)

In `src/api/routes/admin.py`, parse and pass parameters when creating the Backend:

```python
from .backend import ParameterConfig, _parse_bool

def _backend_from_runtime_payload(
    payload: dict[str, Any]
) -> tuple[Backend, Optional[list[str]], dict[str, Any]]:
    # ... existing code ...
    
    # Parse parameter overrides
    param_configs: dict[str, ParameterConfig] = {}
    if "parameters" in payload:
        raw_params = payload.get("parameters") or {}
        if not isinstance(raw_params, dict):
            raw_params = {}
        for param_name, param_config in raw_params.items():
            if isinstance(param_config, dict):
                default = param_config.get("default")
                allow_override = _parse_bool(param_config.get("allow_override", True))
                param_configs[param_name] = ParameterConfig(
                    default=default,
                    allow_override=allow_override,
                )
    
    backend = Backend(
        name=model_name,
        base_url=api_base,
        api_key=api_key,
        timeout=timeout,
        target_model=target_model,
        api_type=api_type,
        anthropic_version=anthropic_version,
        supports_reasoning=supports_reasoning,
        editable=True,
        parameters=param_configs,  # ADD THIS
    )
    # ... rest of function ...
```

### Alternative Fix: Always Use Config Store for Parameters

Instead of storing parameters in the Backend object, always read them from the ConfigStore during request processing. This would require:

1. Modifying `build_backend_body()` to accept the config store or model name
2. Looking up parameters from the ConfigStore each time
3. This approach is more complex but ensures consistency

## Impact

- **Severity:** Medium - Parameters silently fail to apply until manual reload
- **Affected Operations:** PUT /admin/config, POST /admin/models
- **Workaround:** Press "Reload Config" button after making changes

## Files Affected

1. `src/api/routes/config.py` - Missing router.reload_config() in update_config()
2. `src/api/routes/admin.py` - Missing parameters parsing in register_model()
3. `src/config_store.py` - save() method only updates store, not router
