#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "${F09_IN_USERNS:-0}" != 1 ]]; then
    if [[ "$(id -u)" == 0 ]]; then
        echo "run as an ordinary user, not root" >&2
        exit 2
    fi
    exec unshare --user --map-root-user --net --fork env F09_IN_USERNS=1 F09_GUEST_UID="$(id -u)" F09_GUEST_GID="$(id -g)" "$0"
fi

work=$(mktemp -d)
pid_a=
pid_b=
server_pid=
loop_server_pid=
cleanup() {
    [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
    [[ -z "$loop_server_pid" ]] || kill "$loop_server_pid" 2>/dev/null || true
    [[ -z "$pid_a" ]] || kill "$pid_a" 2>/dev/null || true
    [[ -z "$pid_b" ]] || kill "$pid_b" 2>/dev/null || true
    [[ -z "$server_pid" ]] || wait "$server_pid" 2>/dev/null || true
    [[ -z "$loop_server_pid" ]] || wait "$loop_server_pid" 2>/dev/null || true
    [[ -z "$pid_a" ]] || wait "$pid_a" 2>/dev/null || true
    [[ -z "$pid_b" ]] || wait "$pid_b" 2>/dev/null || true
    rm -rf "$work"
}
trap cleanup EXIT

ip link add br0 type bridge
ip addr add 192.168.240.1/24 dev br0
ip link set br0 up

unshare --net --fork sleep 120 &
pid_a=$!
unshare --net --fork sleep 120 &
pid_b=$!
sleep 0.1

for pair in "a $pid_a 2" "b $pid_b 3"; do
    read -r suffix pid hostnum <<<"$pair"
    ip link add "br-$suffix" type veth peer name eth0
    ip link set "br-$suffix" master br0
    ip link set "br-$suffix" up
    ip link set eth0 netns "$pid"
    nsenter -t "$pid" -n ip link set lo up
    nsenter -t "$pid" -n ip addr add "192.168.240.$hostnum/24" dev eth0
    nsenter -t "$pid" -n ip link set eth0 up
done

manager_userns=$(readlink "/proc/$$/ns/user")
userns_a=$(readlink "/proc/$pid_a/ns/user")
userns_b=$(readlink "/proc/$pid_b/ns/user")
netns_a=$(readlink "/proc/$pid_a/ns/net")
netns_b=$(readlink "/proc/$pid_b/ns/net")
[[ "$userns_a" == "$manager_userns" && "$userns_b" == "$manager_userns" ]]
[[ "$netns_a" != "$netns_b" ]]
[[ "$(nsenter -t "$pid_a" -n ip -o -4 addr show dev lo | awk '{print $4}')" == "127.0.0.1/8" ]]
[[ "$(nsenter -t "$pid_b" -n ip -o -4 addr show dev lo | awk '{print $4}')" == "127.0.0.1/8" ]]

# The manager namespace has only its bridge; members have no default route.
[[ "$(nsenter -t "$pid_a" -n ip -4 route show default)" == "" ]]
[[ "$(nsenter -t "$pid_b" -n ip -4 route show default)" == "" ]]
echo "manager_userns=$manager_userns"
echo "sandbox_a_netns=$netns_a ipv4=192.168.240.2 lo=127.0.0.1"
echo "sandbox_b_netns=$netns_b ipv4=192.168.240.3 lo=127.0.0.1"
echo "routes_a=$(nsenter -t "$pid_a" -n ip -4 route show | tr '\n' ';')"
echo "routes_b=$(nsenter -t "$pid_b" -n ip -4 route show | tr '\n' ';')"

run_bwrap() {
    local ns_pid=$1
    shift
    local -a cap_drops=(
        CAP_CHOWN CAP_DAC_OVERRIDE CAP_DAC_READ_SEARCH CAP_FOWNER CAP_FSETID
        CAP_KILL CAP_SETGID CAP_SETUID CAP_SETPCAP CAP_LINUX_IMMUTABLE
        CAP_NET_BIND_SERVICE CAP_NET_BROADCAST CAP_NET_ADMIN CAP_NET_RAW
        CAP_IPC_LOCK CAP_IPC_OWNER CAP_SYS_MODULE CAP_SYS_RAWIO CAP_SYS_CHROOT
        CAP_SYS_PTRACE CAP_SYS_PACCT CAP_SYS_ADMIN CAP_SYS_BOOT CAP_SYS_NICE
        CAP_SYS_RESOURCE CAP_SYS_TIME CAP_SYS_TTY_CONFIG CAP_MKNOD CAP_LEASE
        CAP_AUDIT_WRITE CAP_AUDIT_CONTROL CAP_SETFCAP CAP_MAC_OVERRIDE CAP_MAC_ADMIN
        CAP_SYSLOG CAP_WAKE_ALARM CAP_BLOCK_SUSPEND CAP_AUDIT_READ CAP_PERFMON
        CAP_BPF CAP_CHECKPOINT_RESTORE
    )
    local -a cap_args=()
    local capability
    for capability in "${cap_drops[@]}"; do cap_args+=(--cap-drop "$capability"); done
    nsenter -t "$ns_pid" -n -- bwrap \
        --unshare-user --unshare-pid --share-net \
        --disable-userns --assert-userns-disabled --uid "$F09_GUEST_UID" --gid "$F09_GUEST_GID" "${cap_args[@]}" \
        --ro-bind /usr /usr --ro-bind /lib /lib --ro-bind /lib64 /lib64 \
        --proc /proc --dev /dev -- \
        "$@"
}

server_code='import socket; t=socket.socket(); t.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); t.bind(("0.0.0.0",45678)); t.listen(1); c,_=t.accept(); c.sendall(b"tcp-ok"); c.close(); t.close(); u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); u.bind(("0.0.0.0",45679)); data,addr=u.recvfrom(64); u.sendto(b"udp:"+data,addr); u.close()'
run_bwrap "$pid_a" /usr/bin/python3 -u -c "$server_code" >"$work/server.out" 2>"$work/server.err" &
server_pid=$!
sleep 0.3
response=$(run_bwrap "$pid_b" /usr/bin/python3 -c 'import socket; t=socket.create_connection(("192.168.240.2",45678),timeout=3); a=t.recv(64).decode(); t.close(); u=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); u.settimeout(3); u.sendto(b"probe",("192.168.240.2",45679)); b=u.recv(64).decode(); print(a+" "+b); u.close()')
[[ "$response" == "tcp-ok udp:probe" ]]
wait "$server_pid"
echo "tcp_udp_peer_exchange=$response"

# A listener on sandbox A's loopback must not be reachable through B's loopback.
run_bwrap "$pid_a" /usr/bin/python3 -u -c 'import socket,time; s=socket.socket(); s.bind(("127.0.0.1",45680)); s.listen(1); time.sleep(2)' >/dev/null 2>&1 &
loop_server_pid=$!
sleep 0.2
if run_bwrap "$pid_b" /usr/bin/python3 -c 'import socket; s=socket.socket(); s.settimeout(.5); s.connect(("127.0.0.1",45680))' >/dev/null 2>&1; then
    echo "sandbox loopback unexpectedly shared" >&2
    exit 1
fi
wait "$loop_server_pid" || true
echo "loopback_isolation=verified"

# Applications have no CAP_NET_ADMIN and cannot create further user namespaces.
security=$(run_bwrap "$pid_b" /usr/bin/python3 -c 'import re; s=open("/proc/self/status").read(); cap=int(re.search(r"^CapEff:\s+([0-9a-f]+)$",s,re.M).group(1),16); print(cap)')
[[ "$security" == "0" ]]

if run_bwrap "$pid_b" /usr/sbin/ip link add should-fail type dummy >/dev/null 2>&1; then
    echo "application unexpectedly has network administration capability" >&2
    exit 1
fi
if run_bwrap "$pid_b" /usr/bin/unshare --user --map-root-user true >/dev/null 2>&1; then
    echo "application unexpectedly created a nested user namespace" >&2
    exit 1
fi
echo "app_cap_eff=$security network_admin=blocked nested_userns=blocked"
echo "host_lan_isolation=verified by no default route and manager-only bridge topology"
