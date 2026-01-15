"""Main FastAPI application for the yaLLM proxy."""

import asyncio
import logging
import socket
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from fastapi.responses import Response

# Import logging setup FIRST before anything that might log
from .logging import setup_logging

# Initialize logging BEFORE importing config_store
logger = setup_logging()

# Import database module (after logging is set up)
try:
    from .database.factory import get_database, reset_database_instance
    from .database.logger import _PENDING_DB_TASKS
    DATABASE_AVAILABLE = True
except ImportError:
    DATABASE_AVAILABLE = False
    logger.debug("Database module not available")

# Now import config store (it will log config paths during initialization)
from .config_store import CONFIG_STORE
from .core import ProxyRouter
from .core.registry import set_router
from .api.routes import chat_completions, list_models, register_model, responses, config as config_routes, usage, logs

# Load configuration
config = CONFIG_STORE.get_runtime_config()
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
@asynccontextmanager
async def lifespan(app: FastAPI):
    await startup_event()
    try:
        yield
    finally:
        await shutdown_event()


app = FastAPI(title="yaLLMp Proxy", lifespan=lifespan)
logger.info("FastAPI application created")

# Mount static files for admin UI
static_dir = Path(__file__).parent.parent / "static"
if static_dir.exists():
    from fastapi.staticfiles import StaticFiles
    app.mount("/static", StaticFiles(directory=str(static_dir)), name="static")
    logger.info(f"Static files mounted from {static_dir}")
else:
    logger.warning(f"Static directory not found at {static_dir}, admin UI will not be available")


async def startup_event():
    """Handle application startup."""
    import sys
    # Set UTF-8 encoding for Windows
    if sys.platform == "win32":
        import codecs
        try:
            sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass
    
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

    # Initialize database if available
    if DATABASE_AVAILABLE:
        try:
            db_config = config.get("database")
            if db_config:
                logger.info("Initializing database...")
                db = get_database(db_config)
                db.initialize()
                logger.info(f"Database initialized: {db.backend_name}")
            else:
                logger.info("Database not configured (skipping initialization)")
        except Exception as e:
            logger.warning(f"Failed to initialize database: {e}")
    else:
        logger.debug("Database module not available")

    logger.info("yaLLMp Proxy server ready to handle requests")


async def shutdown_event():
    """Handle application shutdown."""
    from .logging.recorder import _PENDING_LOG_TASKS

    # Wait for pending log tasks first
    if _PENDING_LOG_TASKS:
        logger.info("Waiting for %d pending log flush tasks", len(_PENDING_LOG_TASKS))
        pending = list(_PENDING_LOG_TASKS)
        await asyncio.gather(*pending, return_exceptions=True)
        logger.info("All log flush tasks completed")

    # Close database connection if available
    if DATABASE_AVAILABLE:
        try:
            from .database.factory import reset_database_instance
            reset_database_instance()
            logger.info("Database connections closed")
        except Exception as e:
            logger.warning(f"Error closing database connections: {e}")


# Register routes
app.post("/v1/chat/completions")(chat_completions)
app.get("/v1/models")(list_models)
app.post("/admin/models")(register_model)

# Config management routes for admin UI
app.get("/admin/config")(config_routes.get_full_config)
app.put("/admin/config")(config_routes.update_config)
app.post("/admin/config/reload")(config_routes.reload_config)
app.get("/admin/models")(config_routes.get_models_list)
app.get("/admin/models/tree")(config_routes.get_models_tree)
app.get("/admin/models/{model_name}/ancestry")(config_routes.get_model_ancestry)
app.get("/admin/models/{model_name}/dependents")(config_routes.get_model_dependents)
app.delete("/admin/models/{model_name}")(config_routes.delete_model)
app.post("/admin/models/copy")(config_routes.copy_model)
app.get("/admin/")(config_routes.serve_admin_ui)
app.get("/admin/templates")(config_routes.list_templates)
app.post("/admin/templates")(config_routes.upload_template)

# Usage statistics route
app.get("/usage")(usage.usage_page)
app.get("/api/usage")(usage.get_usage)
app.get("/api/usage/page")(usage.usage_page)

# Logs viewer route
app.get("/logs")(logs.logs_page)
app.include_router(logs.router, prefix="/api")

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
