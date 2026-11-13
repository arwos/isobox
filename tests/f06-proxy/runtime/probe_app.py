#!/usr/bin/env python3
import socket
import sys


host_address = sys.argv[1]
tcp_echo_port = int(sys.argv[2])
udp_echo_port = int(sys.argv[3])
listen_port = int(sys.argv[4])

try:
    socket.getaddrinfo("isobox-runtime-check.invalid.", 80, socket.AF_INET, socket.SOCK_STREAM)
except socket.gaierror as error:
    if error.errno != socket.EAI_NONAME:
        raise
print("DNS_QUERY_OK", flush=True)

with socket.create_connection((host_address, tcp_echo_port), timeout=8) as tcp:
    tcp.sendall(b"isobox-proxy-tcp")
    if tcp.recv(64) != b"isobox-proxy-tcp":
        raise RuntimeError("TCP response did not match")
print("OUTBOUND_TCP_OK", flush=True)

udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
udp.settimeout(8)
udp.sendto(b"isobox-proxy-udp", (host_address, udp_echo_port))
if udp.recvfrom(64)[0] != b"isobox-proxy-udp":
    raise RuntimeError("UDP response did not match")
udp.close()
print("OUTBOUND_UDP_OK", flush=True)

server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
server.bind(("0.0.0.0", listen_port))
server.listen(4)
print("APP_READY", flush=True)
connection, _ = server.accept()
with connection:
    if connection.recv(64) != b"isobox-proxy-ingress":
        raise RuntimeError("host ingress payload did not match")
    connection.sendall(b"isobox-proxy-ingress-ok")
server.close()
print("INGRESS_RESPONSE_OK", flush=True)
