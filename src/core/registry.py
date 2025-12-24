"""Router registry for breaking circular imports.

This module holds the router instance so that routes can import it
without causing circular imports with the main module.
"""

# Global router instance - set by main.py during initialization
router = None


def set_router(router_instance):
    """Set the global router instance."""
    global router
    router = router_instance


def get_router():
    """Get the global router instance."""
    if router is None:
        raise RuntimeError("Router not initialized. Did you call set_router?")
    return router

