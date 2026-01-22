#!/usr/bin/env python3
"""Simple TCP forwarder.

Usage:
  python tcp_forward.py --listen-host 0.0.0.0 --listen-port 7979 \
    --target-host 127.0.0.1 --target-port 7978
"""

from __future__ import annotations

import argparse
import itertools
import logging
import os
import signal
import socket
import sys
import threading
import time
from pathlib import Path
from typing import Dict, Optional, Tuple

# Add project root to path for imports
_SCRIPT_DIR = Path(__file__).resolve().parent
_PROJECT_ROOT = _SCRIPT_DIR.parent
sys.path.insert(0, str(_PROJECT_ROOT))

# Log file path
TCP_FORWARDER_LOG_PATH = _PROJECT_ROOT / "logs" / "console_forwarder.log"


def _load_forwarder_config() -> dict:
    """Load forwarder settings from config file.

    Returns:
        forwarder_settings dict from config, or empty dict if unavailable.
    """
    try:
        from src.config_loader import load_config
        cfg = load_config(substitute_env=False)
        return cfg.get("forwarder_settings") or {}
    except Exception:
        return {}


def _pipe(
    src: socket.socket,
    dst: socket.socket,
    bufsize: int,
    stats: Dict[str, int],
    key: str,
    logger: logging.Logger,
    conn_id: int,
) -> None:
    total = 0
    chunk_count = 0
    try:
        while True:
            data = src.recv(bufsize)
            if not data:
                if logger.isEnabledFor(logging.DEBUG):
                    logger.debug(
                        "Conn #%d pipe %s: EOF received after %d chunks, %d bytes total",
                        conn_id,
                        key,
                        chunk_count,
                        total,
                    )
                break
            total += len(data)
            chunk_count += 1
            dst.sendall(data)
    except OSError as exc:
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Conn #%d pipe %s: OSError after %d chunks, %d bytes - %s (errno=%s)",
                conn_id,
                key,
                chunk_count,
                total,
                exc,
                getattr(exc, 'errno', 'N/A'),
            )
    finally:
        stats[key] = total


def _set_nodelay(sock: socket.socket) -> None:
    try:
        sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
    except OSError:
        pass


def _handle_client(
    client: socket.socket,
    addr: Tuple[str, int],
    target_host: str,
    target_port: int,
    bufsize: int,
    logger: logging.Logger,
    conn_id: int,
) -> None:
    target = None
    stats: Dict[str, int] = {}
    start_time = time.perf_counter()
    try:
        # Connect to target with timing
        connect_start = time.perf_counter()
        target = socket.create_connection((target_host, target_port))
        connect_elapsed = time.perf_counter() - connect_start

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Conn #%d target connection established in %.3fs",
                conn_id,
                connect_elapsed,
            )

        _set_nodelay(client)
        _set_nodelay(target)

        if logger.isEnabledFor(logging.DEBUG):
            logger.debug("Conn #%d TCP_NODELAY set on both sockets", conn_id)

        logger.info(
            "Conn #%d established %s:%s -> %s:%s",
            conn_id,
            addr[0],
            addr[1],
            target_host,
            target_port,
        )
        t1 = threading.Thread(
            target=_pipe,
            args=(client, target, bufsize, stats, "up", logger, conn_id),
            daemon=True,
        )
        t2 = threading.Thread(
            target=_pipe,
            args=(target, client, bufsize, stats, "down", logger, conn_id),
            daemon=True,
        )
        t1.start()
        t2.start()
        t1.join()
        t2.join()
        sent = stats.get("up", 0)
        received = stats.get("down", 0)
        elapsed = time.perf_counter() - start_time
        logger.info(
            "Conn #%d closed after %.3fs (sent=%d bytes, received=%d bytes)",
            conn_id,
            elapsed,
            sent,
            received,
        )
    except OSError as exc:
        elapsed = time.perf_counter() - start_time
        logger.warning(
            "Conn #%d error for %s:%s after %.3fs: %s",
            conn_id,
            addr[0],
            addr[1],
            elapsed,
            exc,
        )
        if logger.isEnabledFor(logging.DEBUG):
            logger.debug(
                "Conn #%d error details: target=%s:%s, errno=%s, type=%s",
                conn_id,
                target_host,
                target_port,
                getattr(exc, 'errno', 'N/A'),
                type(exc).__name__,
            )
    finally:
        try:
            client.close()
        except OSError:
            pass
        if target is not None:
            try:
                target.close()
            except OSError:
                pass


def main() -> None:
    # Load config first for debug flag and defaults
    forwarder_cfg = _load_forwarder_config()
    debug_from_config = bool(forwarder_cfg.get("debug", False))

    # Get defaults from config
    listen_cfg = forwarder_cfg.get("listen") or {}
    target_cfg = forwarder_cfg.get("target") or {}
    default_listen_host = str(listen_cfg.get("host", "0.0.0.0"))
    default_listen_port = int(listen_cfg.get("port", 6969)) if listen_cfg.get("port") else None
    default_target_host = str(target_cfg.get("host", "127.0.0.1"))
    default_target_port = int(target_cfg.get("port", 7979)) if target_cfg.get("port") else None

    parser = argparse.ArgumentParser(description="TCP forwarder")
    parser.add_argument("--listen-host", default=default_listen_host)
    parser.add_argument(
        "--listen-port",
        type=int,
        default=default_listen_port,
        required=default_listen_port is None,
    )
    parser.add_argument("--target-host", default=default_target_host)
    parser.add_argument(
        "--target-port",
        type=int,
        default=default_target_port,
        required=default_target_port is None,
    )
    parser.add_argument("--bufsize", type=int, default=65536)
    parser.add_argument("--log-level", default="INFO")
    parser.add_argument(
        "--debug",
        action="store_true",
        help="Enable debug logging (overrides config and --log-level)",
    )
    parser.add_argument(
        "--accept-timeout",
        type=float,
        default=1.0,
        help="Server accept timeout in seconds (default: 1.0)",
    )
    parser.add_argument(
        "--idle-log-seconds",
        type=float,
        default=0.0,
        help="Log a heartbeat every N seconds while idle (default: off)",
    )
    args = parser.parse_args()

    # Debug from CLI overrides config
    is_debug = args.debug or debug_from_config
    log_level = logging.DEBUG if is_debug else getattr(logging, args.log_level.upper(), logging.INFO)

    # Set up logging with optional file handler
    handlers: list[logging.Handler] = [logging.StreamHandler(sys.stdout)]
    if is_debug:
        TCP_FORWARDER_LOG_PATH.parent.mkdir(parents=True, exist_ok=True)
        file_handler = logging.FileHandler(TCP_FORWARDER_LOG_PATH, mode='w', encoding='utf-8')
        file_handler.setLevel(logging.DEBUG)
        handlers.append(file_handler)

    logging.basicConfig(
        level=log_level,
        format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        handlers=handlers,
    )
    logger = logging.getLogger("tcp_forward")

    if is_debug:
        logger.debug("Debug mode enabled - logging to %s", TCP_FORWARDER_LOG_PATH)

    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((args.listen_host, args.listen_port))
    server.listen(128)
    server.settimeout(args.accept_timeout)
    logger.info(
        "Forwarding %s:%s -> %s:%s",
        args.listen_host,
        args.listen_port,
        args.target_host,
        args.target_port,
    )
    logger.info("Press Ctrl+C to stop.")
    conn_ids = itertools.count(1)
    stop_event = threading.Event()

    def _handle_signal(signum: int, _frame) -> None:
        logger.info("Shutdown requested (signal=%s)", signum)
        stop_event.set()
        try:
            server.close()
        except OSError:
            pass

    signal.signal(signal.SIGINT, _handle_signal)
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, _handle_signal)

    try:
        last_idle = time.monotonic()
        while not stop_event.is_set():
            try:
                client, addr = server.accept()
            except socket.timeout:
                if args.idle_log_seconds:
                    now = time.monotonic()
                    if now - last_idle >= args.idle_log_seconds:
                        logger.info("Waiting for connections...")
                        last_idle = now
                continue
            except OSError as exc:
                if stop_event.is_set():
                    break
                logger.warning("Accept failed: %s", exc)
                continue
            conn_id = next(conn_ids)
            logger.info("Conn #%d accepted from %s:%s", conn_id, addr[0], addr[1])
            thread = threading.Thread(
                target=_handle_client,
                args=(
                    client,
                    addr,
                    args.target_host,
                    args.target_port,
                    args.bufsize,
                    logger,
                    conn_id,
                ),
                daemon=True,
            )
            thread.start()
    finally:
        try:
            server.close()
        except OSError:
            pass
        logger.info("Forwarder stopped")


if __name__ == "__main__":
    main()
