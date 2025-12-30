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
import signal
import socket
import sys
import threading
import time
from typing import Dict, Tuple


def _pipe(
    src: socket.socket,
    dst: socket.socket,
    bufsize: int,
    stats: Dict[str, int],
    key: str,
) -> None:
    total = 0
    try:
        while True:
            data = src.recv(bufsize)
            if not data:
                try:
                    dst.shutdown(socket.SHUT_WR)
                except OSError:
                    pass
                break
            total += len(data)
            dst.sendall(data)
    except OSError:
        pass
    finally:
        stats[key] = total
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass


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
    try:
        target = socket.create_connection((target_host, target_port))
        _set_nodelay(client)
        _set_nodelay(target)
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
            args=(client, target, bufsize, stats, "up"),
            daemon=True,
        )
        t2 = threading.Thread(
            target=_pipe,
            args=(target, client, bufsize, stats, "down"),
            daemon=True,
        )
        t1.start()
        t2.start()
        t1.join()
        t2.join()
        sent = stats.get("up", 0)
        received = stats.get("down", 0)
        logger.info(
            "Conn #%d closed (sent=%d bytes, received=%d bytes)",
            conn_id,
            sent,
            received,
        )
    except OSError as exc:
        logger.warning("Conn #%d error for %s:%s: %s", conn_id, addr[0], addr[1], exc)
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
    parser = argparse.ArgumentParser(description="TCP forwarder")
    parser.add_argument("--listen-host", default="0.0.0.0")
    parser.add_argument("--listen-port", type=int, required=True)
    parser.add_argument("--target-host", default="127.0.0.1")
    parser.add_argument("--target-port", type=int, required=True)
    parser.add_argument("--bufsize", type=int, default=65536)
    parser.add_argument("--log-level", default="INFO")
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

    logging.basicConfig(
        level=args.log_level.upper(),
        format="%(asctime)s %(message)s",
        stream=sys.stdout,
    )
    logger = logging.getLogger("tcp_forward")

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
