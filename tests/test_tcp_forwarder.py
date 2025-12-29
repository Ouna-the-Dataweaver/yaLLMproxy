import importlib.util
import logging
import socket
import threading
from functools import lru_cache
from pathlib import Path


@lru_cache(maxsize=1)
def _load_forwarder_module():
    repo_root = Path(__file__).resolve().parents[1]
    forwarder_path = repo_root / "scripts" / "tcp_forward.py"
    spec = importlib.util.spec_from_file_location("tcp_forward", forwarder_path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def _null_logger() -> logging.Logger:
    logger = logging.getLogger("tcp_forward_test")
    if not logger.handlers:
        logger.addHandler(logging.NullHandler())
    logger.propagate = False
    logger.setLevel(logging.DEBUG)
    return logger


def _start_echo_server():
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("127.0.0.1", 0))
    server.listen(1)
    server.settimeout(5)
    port = server.getsockname()[1]
    received = bytearray()

    def run():
        try:
            conn, _addr = server.accept()
        except socket.timeout:
            server.close()
            return
        conn.settimeout(5)
        with conn:
            while True:
                try:
                    data = conn.recv(4096)
                except socket.timeout:
                    break
                if not data:
                    break
                received.extend(data)
                conn.sendall(data)
        server.close()

    thread = threading.Thread(target=run, daemon=True)
    thread.start()
    return port, received, thread


def _unused_port() -> int:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def test_tcp_forwarder_roundtrip():
    forwarder = _load_forwarder_module()
    target_port, _received, target_thread = _start_echo_server()

    forwarder_server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    forwarder_server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    forwarder_server.bind(("127.0.0.1", 0))
    forwarder_server.listen(1)
    forwarder_server.settimeout(5)
    forwarder_port = forwarder_server.getsockname()[1]

    client = socket.create_connection(("127.0.0.1", forwarder_port), timeout=5)
    forwarder_conn, addr = forwarder_server.accept()
    forwarder_server.close()

    handler_thread = threading.Thread(
        target=forwarder._handle_client,
        args=(
            forwarder_conn,
            addr,
            "127.0.0.1",
            target_port,
            65536,
            _null_logger(),
            1,
        ),
        daemon=True,
    )
    handler_thread.start()

    payload = b"hello from forwarder test"
    client.settimeout(5)
    client.sendall(payload)
    client.shutdown(socket.SHUT_WR)

    response = bytearray()
    while True:
        chunk = client.recv(4096)
        if not chunk:
            break
        response.extend(chunk)
    client.close()

    handler_thread.join(timeout=5)
    target_thread.join(timeout=5)

    assert not handler_thread.is_alive()
    assert response == payload


def test_tcp_forwarder_unreachable_target_closes_client():
    forwarder = _load_forwarder_module()
    target_port = _unused_port()

    forwarder_server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    forwarder_server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    forwarder_server.bind(("127.0.0.1", 0))
    forwarder_server.listen(1)
    forwarder_server.settimeout(5)
    forwarder_port = forwarder_server.getsockname()[1]

    client = socket.create_connection(("127.0.0.1", forwarder_port), timeout=5)
    forwarder_conn, addr = forwarder_server.accept()
    forwarder_server.close()

    handler_thread = threading.Thread(
        target=forwarder._handle_client,
        args=(
            forwarder_conn,
            addr,
            "127.0.0.1",
            target_port,
            65536,
            _null_logger(),
            1,
        ),
        daemon=True,
    )
    handler_thread.start()
    handler_thread.join(timeout=5)
    assert not handler_thread.is_alive()

    client.settimeout(2)
    try:
        data = client.recv(1)
    except OSError:
        data = None
    assert data in (b"", None)
    client.close()
