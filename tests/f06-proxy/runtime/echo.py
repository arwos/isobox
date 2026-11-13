#!/usr/bin/env python3
import socket
import sys
import threading


host = sys.argv[1]
tcp_port = int(sys.argv[2])
udp_port = int(sys.argv[3])


def serve_tcp(listener):
    while True:
        connection, _ = listener.accept()
        threading.Thread(target=echo_connection, args=(connection,), daemon=True).start()


def echo_connection(connection):
    with connection:
        while True:
            payload = connection.recv(65536)
            if not payload:
                return
            connection.sendall(payload)


def serve_udp(listener):
    while True:
        payload, address = listener.recvfrom(65536)
        listener.sendto(payload, address)


tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
tcp.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
tcp.bind((host, tcp_port))
tcp.listen(16)
udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
udp.bind((host, udp_port))
threading.Thread(target=serve_tcp, args=(tcp,), daemon=True).start()
threading.Thread(target=serve_udp, args=(udp,), daemon=True).start()
print("ECHO_READY", flush=True)
threading.Event().wait()
