"""Models listing endpoint - OpenAI compatible."""

import logging
from pathlib import Path

from ...core.registry import get_router

logger = logging.getLogger("yallmp-proxy")


async def list_models() -> dict:
    """List available models in OpenAI API format.
    
    GET /v1/models
    
    Returns:
        A dictionary containing the list of available models.
    """
    logger.info("Received models list request")
    
    router = get_router()
    models = []
    for model_name in await router.list_model_names():
        models.append({
            "id": model_name,
            "object": "model",
            "created": int(Path(__file__).with_name("..").with_name("..").with_name("..").stat().st_ctime),
            "owned_by": "yallmp-proxy"
        })
    
    return {
        "object": "list",
        "data": models
    }
