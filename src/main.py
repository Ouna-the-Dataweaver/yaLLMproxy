"""Main FastAPI application for the yaLLM proxy."""

import asyncio
import logging
import socket
from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import Response

from .config_loader import load_config
from .core import ProxyRouter
from .core.registry import set_router
from .logging import logger as setup_logger
from .api.routes import chat_completions, list_models, register_model, responses

# Import logging setup
from .logging import setup_logging

# Initialize logging
logger = setup_logging()

# Load configuration
config = load_config()
router = ProxyRouter(config)
logger.info(f"Proxy router initialized with {len(router.backends)} backends")

# Set the router in the registry for routes to access
set_router(router)

# Server configuration - environment variables take priority over config file
# Environment variables: YALLMP_HOST, YALLMP_PORT
import os

# Get host from env var first, then fallback to config
SERVER_HOST = os.getenv("YALLMP_HOST")
if SERVER_HOST is None:
    # Check config file under proxy_settings.server.host
    proxy_settings = config.get("proxy_settings") or {}
    server_cfg = proxy_settings.get("server") or {}
    SERVER_HOST = str(server_cfg.get("host", "127.0.0.1"))

# Get port from env var first, then fallback to config
SERVER_PORT_STR = os.getenv("YALLMP_PORT")
if SERVER_PORT_STR is not None:
    try:
        SERVER_PORT = int(SERVER_PORT_STR)
    except (TypeError, ValueError):
        proxy_settings = config.get("proxy_settings") or {}
        server_cfg = proxy_settings.get("server") or {}
        SERVER_PORT = int(server_cfg.get("port", 8000))
else:
    # Fallback to config file
    proxy_settings = config.get("proxy_settings") or {}
    server_cfg = proxy_settings.get("server") or {}
    try:
        SERVER_PORT = int(server_cfg.get("port", 8000))
    except (TypeError, ValueError):
        SERVER_PORT = 8000

# Check if responses endpoint should be enabled (prefer proxy_settings for current configs)
proxy_settings = config.get("proxy_settings") or {}
general_settings = config.get("general_settings") or {}
if "enable_responses_endpoint" in proxy_settings:
    enable_responses_endpoint = bool(proxy_settings.get("enable_responses_endpoint"))
else:
    enable_responses_endpoint = bool(
        general_settings.get("enable_responses_endpoint", False)
    )

logger.info(f"Responses endpoint enabled: {enable_responses_endpoint}")


# Create FastAPI application
app = FastAPI(title="yaLLMp Proxy")
logger.info("FastAPI application created")


@app.on_event("startup")
async def startup_event():
    """Handle application startup."""
    # Print awesome ASCII art banner
    print("""
╔═════════════════════════════════════════╗
║                                         ║
║    ||  Y(et) A(nother) LLM proxy ||     ║
║                                         ║
║   =(^_^)=                    =(^_^)=    ║
║                    =(^_^)=              ║
║        =(^_^)=                =(^_^)=   ║
╚═════════════════════════════════════════╝
    """)
    
    logger.info("yaLLMp Proxy server starting up...")
    logger.info("Configured bind address %s:%s", SERVER_HOST, SERVER_PORT)
    if SERVER_HOST == "0.0.0.0":
        hostname = socket.gethostname()
        logger.info("Reachable on local network at http://%s:%s", hostname, SERVER_PORT)
        try:
            lan_ip = socket.gethostbyname(hostname)
        except socket.gaierror:
            lan_ip = None
        if lan_ip and not lan_ip.startswith("127."):
            logger.info("Resolved LAN IP:  http://%s:%s", lan_ip, SERVER_PORT)
    logger.info(f"Available backends: {list(router.backends.keys())}")
    for name, backend in router.backends.items():
        logger.info(f"  - {name}: {backend.base_url}")
    logger.info("yaLLMp Proxy server ready to handle requests")


@app.on_event("shutdown")
async def shutdown_event():
    """Handle application shutdown."""
    from .logging.recorder import _PENDING_LOG_TASKS
    
    if not _PENDING_LOG_TASKS:
        return
    logger.info("Waiting for %d pending log flush tasks", len(_PENDING_LOG_TASKS))
    pending = list(_PENDING_LOG_TASKS)
    await asyncio.gather(*pending, return_exceptions=True)
    logger.info("All log flush tasks completed")


# Register routes
app.post("/v1/chat/completions")(chat_completions)
app.get("/v1/models")(list_models)
app.post("/admin/models")(register_model)

# Conditionally register responses endpoint
if enable_responses_endpoint:
    app.post("/v1/responses")(responses)
    logger.info("Responses endpoint enabled")
else:
    logger.info("Responses endpoint is disabled in configuration")


def create_app() -> FastAPI:
    """Factory function to create the FastAPI application.
    
    Returns:
        The configured FastAPI application instance.
    """
    return app


# Export for external use
__all__ = ["app", "create_app", "router", "config", "SERVER_HOST", "SERVER_PORT"]
