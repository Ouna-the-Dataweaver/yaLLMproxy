import importlib.util
import logging
import os
import socket
import subprocess
import sys
import threading
import time
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


def _start_multi_echo_server(max_clients: int, timeout: float = 5.0):
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind(("127.0.0.1", 0))
    server.listen(max_clients)
    server.settimeout(0.2)
    port = server.getsockname()[1]

    def echo_client(conn: socket.socket):
        conn.settimeout(timeout)
        with conn:
            while True:
                try:
                    data = conn.recv(4096)
                except socket.timeout:
                    break
                if not data:
                    break
                conn.sendall(data)

    def run():
        threads = []
        deadline = time.monotonic() + timeout
        while len(threads) < max_clients and time.monotonic() < deadline:
            try:
                conn, _addr = server.accept()
            except socket.timeout:
                continue
            conn.settimeout(timeout)
            thread = threading.Thread(target=echo_client, args=(conn,), daemon=True)
            thread.start()
            threads.append(thread)
        for thread in threads:
            thread.join(timeout=timeout)
        server.close()

    thread = threading.Thread(target=run, daemon=True)
    thread.start()
    return port, thread


def _unused_port() -> int:
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def _drain_lines(stream, lines):
    if stream is None:
        return
    for line in iter(stream.readline, ""):
        lines.append(line)
    stream.close()


def _start_forwarder_process(
    listen_port: int,
    target_port: int,
    idle_log_seconds: float | None = None,
    accept_timeout: float = 0.1,
):
    repo_root = Path(__file__).resolve().parents[1]
    script = repo_root / "scripts" / "tcp_forward.py"
    args = [
        sys.executable,
        "-u",
        str(script),
        "--listen-host",
        "127.0.0.1",
        "--listen-port",
        str(listen_port),
        "--target-host",
        "127.0.0.1",
        "--target-port",
        str(target_port),
        "--accept-timeout",
        str(accept_timeout),
        "--log-level",
        "INFO",
    ]
    if idle_log_seconds is not None:
        args += ["--idle-log-seconds", str(idle_log_seconds)]

    env = os.environ.copy()
    env["PYTHONUNBUFFERED"] = "1"
    process = subprocess.Popen(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        bufsize=1,
        env=env,
    )
    lines: list[str] = []
    thread = threading.Thread(
        target=_drain_lines,
        args=(process.stdout, lines),
        daemon=True,
    )
    thread.start()
    return process, lines, thread


def _wait_for_log(lines, needle: str, timeout: float = 2.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if any(needle in line for line in lines):
            return True
        time.sleep(0.05)
    return False


def _terminate_process(process: subprocess.Popen) -> None:
    if process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


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


def test_tcp_forwarder_large_payload_roundtrip():
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
            4096,
            _null_logger(),
            1,
        ),
        daemon=True,
    )
    handler_thread.start()

    payload = b"x" * (256 * 1024)
    client.settimeout(5)
    for offset in range(0, len(payload), 4096):
        client.sendall(payload[offset : offset + 4096])
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


def test_tcp_forwarder_handles_multiple_clients():
    forwarder = _load_forwarder_module()
    target_port, target_thread = _start_multi_echo_server(max_clients=2)

    forwarder_server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    forwarder_server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    forwarder_server.bind(("127.0.0.1", 0))
    forwarder_server.listen(2)
    forwarder_server.settimeout(5)
    forwarder_port = forwarder_server.getsockname()[1]

    clients = [
        socket.create_connection(("127.0.0.1", forwarder_port), timeout=5),
        socket.create_connection(("127.0.0.1", forwarder_port), timeout=5),
    ]

    handler_threads = []
    for conn in range(2):
        forwarder_conn, addr = forwarder_server.accept()
        handler_thread = threading.Thread(
            target=forwarder._handle_client,
            args=(
                forwarder_conn,
                addr,
                "127.0.0.1",
                target_port,
                8192,
                _null_logger(),
                conn + 1,
            ),
            daemon=True,
        )
        handler_thread.start()
        handler_threads.append(handler_thread)
    forwarder_server.close()

    payloads = [b"alpha-" * 1024, b"beta-" * 1024]
    responses = [bytearray(), bytearray()]

    def send_and_receive(sock: socket.socket, payload: bytes, output: bytearray):
        sock.settimeout(5)
        for offset in range(0, len(payload), 2048):
            sock.sendall(payload[offset : offset + 2048])
        sock.shutdown(socket.SHUT_WR)
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            output.extend(chunk)
        sock.close()

    client_threads = []
    for idx, client in enumerate(clients):
        thread = threading.Thread(
            target=send_and_receive,
            args=(client, payloads[idx], responses[idx]),
            daemon=True,
        )
        thread.start()
        client_threads.append(thread)

    for thread in client_threads:
        thread.join(timeout=5)
    for thread in handler_threads:
        thread.join(timeout=5)
    target_thread.join(timeout=5)

    assert responses[0] == payloads[0]
    assert responses[1] == payloads[1]


def test_tcp_forwarder_cli_roundtrip():
    forwarder_port = _unused_port()
    target_port, _received, target_thread = _start_echo_server()
    process, lines, _thread = _start_forwarder_process(
        listen_port=forwarder_port,
        target_port=target_port,
        accept_timeout=0.05,
    )
    try:
        assert _wait_for_log(lines, "Forwarding", timeout=2.0)
        if process.poll() is not None:
            raise AssertionError("forwarder process exited early:\n" + "".join(lines))
        client = socket.create_connection(("127.0.0.1", forwarder_port), timeout=5)
        payload = b"cli-forwarder-test"
        client.sendall(payload)
        client.shutdown(socket.SHUT_WR)
        response = bytearray()
        while True:
            chunk = client.recv(4096)
            if not chunk:
                break
            response.extend(chunk)
        client.close()
        assert response == payload
    finally:
        _terminate_process(process)
        target_thread.join(timeout=5)


def test_tcp_forwarder_cli_idle_log():
    forwarder_port = _unused_port()
    target_port = _unused_port()
    process, lines, _thread = _start_forwarder_process(
        listen_port=forwarder_port,
        target_port=target_port,
        idle_log_seconds=0.1,
        accept_timeout=0.05,
    )
    try:
        assert _wait_for_log(lines, "Forwarding", timeout=2.0)
        assert _wait_for_log(lines, "Waiting for connections...", timeout=2.0)
    finally:
        _terminate_process(process)
