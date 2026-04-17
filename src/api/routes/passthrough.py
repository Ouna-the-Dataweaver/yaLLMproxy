"""Passthrough endpoint for direct upstream proxying.

This module provides passthrough proxying for non-OpenAI compatible upstreams.
Models with api_type: passthrough are accessible via /p1/{model_name}/* paths.
"""

import logging
from typing import Optional

import httpx
from fastapi import HTTPException, Request, Response
from starlette.requests import ClientDisconnect

from ...core.registry import get_router
from ...core.backend import filter_response_headers, HOP_BY_HOP_HEADERS
from ...core.upstream_transport import get_upstream_transport

logger = logging.getLogger("yallmp-proxy")


def _build_passthrough_headers(
    incoming: dict[str, str],
    backend_api_key: str,
) -> dict[str, str]:
    """Build headers for passthrough requests - minimal transformation."""
    headers: dict[str, str] = {}

    # Copy all headers except hop-by-hop ones
    for key, value in incoming.items():
        key_lower = key.lower()
        # Strip hop-by-hop headers only
        if key_lower in HOP_BY_HOP_HEADERS:
            continue
        # Strip headers that httpx will set automatically
        if key_lower in {"host", "content-length"}:
            continue
        headers[key] = value

    # Add authorization only if backend has an API key
    if backend_api_key:
        headers["Authorization"] = f"Bearer {backend_api_key}"

    return headers


async def passthrough_request(request: Request, model_name: str, endpoint_path: str) -> Response:
    """Handle passthrough requests to non-OpenAI compatible upstreams.

    This endpoint forwards requests directly to the upstream without any
    transformation. It's designed for custom APIs that don't follow OpenAI format.

    Path: /p1/{model_name}/{endpoint_path:path}

    Args:
        request: The FastAPI request object
        model_name: The configured model name (from URL path)
        endpoint_path: The upstream endpoint path (from URL path)

    Returns:
        Response from upstream, passed through as-is
    """
    router = get_router()

    # Get the passthrough backend
    backend = router.get_passthrough_backend(model_name)
    if backend is None:
        logger.warning(f"Passthrough model '{model_name}' not found")
        raise HTTPException(status_code=404, detail={"error": {"message": f"Passthrough model '{model_name}' not found", "type": "not_found_error", "code": "model_not_found"}})

    logger.info(f"Handling passthrough request: model={model_name}, endpoint={endpoint_path}, method={request.method}")

    # Build the upstream URL
    base_url = backend.base_url.rstrip("/")
    upstream_path = f"/{endpoint_path}" if endpoint_path else "/"
    upstream_url = f"{base_url}{upstream_path}"

    # Add query string if present
    query = request.url.query
    if query:
        upstream_url = f"{upstream_url}?{query}"

    # Read the request body
    try:
        body = await request.body()
    except ClientDisconnect:
        logger.warning("Client disconnected before request body was fully read")
        return Response(status_code=499)  # Client Closed Request

    # Build headers for upstream - minimal transformation for passthrough
    outbound_headers = _build_passthrough_headers(
        dict(request.headers),
        backend.api_key,
    )

    logger.debug(f"Forwarding passthrough request to: {upstream_url}")

    # Forward the request
    timeout = backend.timeout or 60
    transport = get_upstream_transport(upstream_url)

    try:
        async with httpx.AsyncClient(timeout=timeout, http2=backend.http2, transport=transport, follow_redirects=True) as client:
            # Build request with appropriate method
            method = request.method.upper()
            if method == "GET":
                resp = await client.get(upstream_url, headers=outbound_headers)
            elif method == "POST":
                resp = await client.post(upstream_url, headers=outbound_headers, content=body)
            elif method == "PUT":
                resp = await client.put(upstream_url, headers=outbound_headers, content=body)
            elif method == "DELETE":
                resp = await client.delete(upstream_url, headers=outbound_headers)
            elif method == "PATCH":
                resp = await client.patch(upstream_url, headers=outbound_headers, content=body)
            else:
                # For other methods, use generic request
                resp = await client.request(method, upstream_url, headers=outbound_headers, content=body)
    except httpx.TimeoutException:
        logger.error(f"Timeout calling passthrough backend {model_name} at {upstream_url}")
        raise HTTPException(status_code=504, detail={"error": {"message": "Upstream timeout", "type": "timeout_error", "code": "gateway_timeout"}})
    except httpx.ConnectError as e:
        logger.error(f"Connection error to passthrough backend {model_name}: {e}")
        raise HTTPException(status_code=502, detail={"error": {"message": "Failed to connect to upstream", "type": "connection_error", "code": "bad_gateway"}})
    except httpx.HTTPError as e:
        logger.error(f"HTTP error calling passthrough backend {model_name}: {e}")
        raise HTTPException(status_code=502, detail={"error": {"message": f"Upstream error: {str(e)}", "type": "upstream_error", "code": "bad_gateway"}})

    logger.info(f"Passthrough request completed: model={model_name}, status={resp.status_code}")

    # Return response as-is, filtering only hop-by-hop headers
    filtered_headers = filter_response_headers(resp.headers)
    media_type = resp.headers.get("content-type")

    return Response(content=resp.content, status_code=resp.status_code, headers=filtered_headers, media_type=media_type)
