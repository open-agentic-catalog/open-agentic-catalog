"""Finite host-loopback TCP relays for the invocation-owned internal bridge."""

import ipaddress
from contextlib import contextmanager
import signal
import socket
import socketserver
import threading


PORTS = {"api": 8080, "mcp": 9443, "postgres": 5432}
STOP_SIGNALS = {signal.SIGINT, signal.SIGTERM}


@contextmanager
def defer_stop():
    """Finish registering owned resources before delivering a stop signal."""
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, STOP_SIGNALS)
    try:
        yield previous
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous)


def targets(subnet, values):
    """Accept only the three inspected service addresses in this run's subnet."""
    if not isinstance(subnet, str):
        raise ValueError("unexpected fixture subnet")
    network = ipaddress.IPv4Network(subnet)
    if network.prefixlen != 28 or not network.subnet_of(ipaddress.IPv4Network("172.29.0.0/16")):
        raise ValueError("unexpected fixture subnet")
    if not isinstance(values, dict) or set(values) != set(PORTS):
        raise ValueError("incomplete fixture relay targets")
    result = {}
    for name, port in PORTS.items():
        value = values[name]
        if (not isinstance(value, dict) or set(value) != {"address", "port"} or not isinstance(value["address"], str)
                or type(value["port"]) is not int or value["port"] != port):
            raise ValueError("unexpected fixture service port")
        address = ipaddress.IPv4Address(value["address"])
        if address not in network or address in (network.network_address, network.network_address + 1, network.broadcast_address):
            raise ValueError("fixture relay target is outside its service addresses")
        result[name] = (str(address), port)
    if len({address for address, _port in result.values()}) != len(PORTS):
        raise ValueError("fixture services share an unexpected address")
    return result


def _copy(source, destination):
    try:
        while data := source.recv(32768):
            destination.sendall(data)
    except OSError:
        pass  # Closing either owned peer cancels the stream.
    finally:
        try:
            destination.shutdown(socket.SHUT_WR)
        except OSError:
            pass


class _Connection(socketserver.BaseRequestHandler):
    def handle(self):
        upstream = None
        try:
            upstream = socket.create_connection(self.server.target, timeout=3)
            upstream.settimeout(None)
            if not self.server.track(upstream):
                return
            sender = threading.Thread(target=_copy, args=(self.request, upstream))
            sender.start()
            _copy(upstream, self.request)
            sender.join()
        except OSError:
            pass  # A failed fixed target closes the caller; it is never retried.
        finally:
            if upstream is not None:
                self.server.untrack(upstream)
                upstream.close()


class _Listener(socketserver.ThreadingTCPServer):
    request_queue_size = 16

    def __init__(self, target):
        self.target = target
        self.slots = threading.BoundedSemaphore(16)
        self.lock = threading.Lock()
        self.connections = set()
        self.stopping = False
        super().__init__(("127.0.0.1", 0), _Connection)

    def track(self, connection):
        with self.lock:
            if self.stopping:
                return False
            self.connections.add(connection)
            return True

    def untrack(self, connection):
        with self.lock:
            self.connections.discard(connection)

    def process_request(self, request, client_address):
        if not self.slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        if not self.track(request):
            self.slots.release()
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except BaseException:
            self.untrack(request)
            self.slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.untrack(request)
            self.slots.release()

    def cancel(self):
        with self.lock:
            self.stopping = True
            for connection in self.connections:
                try:
                    connection.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass
                connection.close()


class Relays:
    """Own bounded byte streams to already validated fixed service targets."""

    def __init__(self, selected):
        self.listeners = {}
        self.threads = []
        try:
            with defer_stop():
                for name, target in selected.items():
                    self.listeners[name] = _Listener(target)
                for listener in self.listeners.values():
                    thread = threading.Thread(target=listener.serve_forever, kwargs={"poll_interval": 0.05})
                    self.threads.append(thread)
                    thread.start()
        except BaseException:
            self.close()
            raise

    def addresses(self):
        return {name: "127.0.0.1:" + str(listener.server_address[1]) for name, listener in self.listeners.items()}

    def close(self):
        with defer_stop():
            for listener in self.listeners.values():
                listener.cancel()
            for listener, thread in zip(self.listeners.values(), self.threads, strict=False):
                if thread.ident is not None:
                    listener.shutdown()
            for listener in self.listeners.values():
                listener.server_close()
            for thread in self.threads:
                if thread.ident is not None:
                    thread.join()
