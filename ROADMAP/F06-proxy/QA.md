# F06 QA

## Планируемые сценарии

- SOCKS5 no-auth и username/password: успешный и неуспешный handshake; секреты не попадают в stdout/stderr/log/argv.
- TCP CONNECT к IPv4 и domain; IPv6 address type и IPv6 TUN packets отклоняются; прокси отказал, недоступен или завершился во время потока — direct connection не появляется.
- UDP ASSOCIATE для DNS и произвольной UDP datagram; проверка ответа и тихого drop; прокси без UDP не запускает profile.
- DNS общего настроечного resolver и профильного resolver проходит только через SOCKS; при packet capture на хосте прямого DNS нет.
- QUIC-like UDP flow проходит SOCKS UDP; TCP/UDP отказ прокси не вызывает direct fallback.
- ICMP/raw socket отклоняются; IPv6 отключается namespace-local sysctl до приложения, IPv6 address/route tables пусты, а TUN дополнительно отбрасывает unexpected IPv6 packets.
- TUN fd закрывается после выхода; аварийный exit proxy process оставляет egress закрытым.
- Podman sing-box fixture покрывает TCP, UDP, auth и отказ relay; runtime может запускаться без sing-box.
- Plain SOCKS5 показывает явное предупреждение; файл secrets имеет mode `0600`.

## Выполнение

### F06-T01 — proxy aliases и credentials

- `gofmt -w internal/profile/strict_unic.go internal/profile/decode.go internal/proxy/*.go internal/cli/cli.go internal/cli/proxy*.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/proxy ./internal/cli ./internal/profile` — прошло. Проверены UNIC round-trip, точные права `0700/0600`, credentials только через непубличный runtime loader, metadata без secrets, дубликаты и некорректные server/credentials, corrupted/unknown/duplicate fields, symlink/FIFO, небезопасные права config/store, отсутствие утечки секрета из ошибок, отказ `run` до host preflight и terminal echo off/restore.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test -race ./internal/proxy ./internal/cli ./internal/profile` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go vet ./...` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0. GVM вывел предупреждение о недоступном stat cache в read-only `/home/user/.gvm/.cache`; на результат сборки это не повлияло.
- `git diff --check` — без замечаний.

Не проверялось: чтение файла под отдельным UID вручную и полная интерактивная установка credentials в пользовательской TTY. Тесты проверяют owner/mode, отказ другим правам и фактическое отключение/восстановление ECHO через pseudo-terminal. SOCKS handshake, сетевой runtime и sing-box fixture относятся к F06-T02/T03/T04.

### F06-T02 — SOCKS5 TCP и UDP client

- `gofmt -w internal/proxy/client_test.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/proxy -count=1` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./... -count=1` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test -race ./internal/proxy -count=1` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go vet ./...` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM напечатал предупреждение о недоступном stat cache в `/home/user/.gvm/.cache` на read-only filesystem.
- `bash -n tests/f06-proxy/client/run.sh` и `git diff --check` — без замечаний.
- `./tests/f06-proxy/client/run.sh` — прошло; отчёт создан в `/tmp/isobox-test-results/f06-proxy-client.QZnh34/result.txt`. Runner записывает metadata и raw output и может быть запущен пользователем повторно.

Проверены no-auth и username/password negotiation, отказ auth без утечки credentials, malformed method/reply, TCP CONNECT с IPv4/domain, запрет downgrade, IPv6/invalid destinations, SOCKS refusal и недоступность без direct retry, отмена handshake, UDP ASSOCIATE/auth, relay-only UDP send, игнорирование datagrams от другого источника, FRAG/IPv6 rejection и завершение association вместе с control connection/context. Использовались `net.Pipe` и UDP mock; реальные сетевые сокеты, sing-box/Podman, TUN packet conversion, внешний DNS/Internet capture и IPv6/ICMP/raw rejection на TUN уровне здесь не проверялись и остаются в T03/T04.

### F06-T03 — прозрачный proxy TUN (частично)

- `gofmt -w internal/proxy/tunnel_linux.go internal/proxy/tunnel_linux_test.go internal/sandbox/tun_helper_linux.go internal/sandbox/run_linux.go internal/sandbox/build.go internal/sandbox/tun_gate_linux.go internal/preflight/backend.go internal/preflight/preflight.go cmd/isobox/main.go` — успешно.
- `go mod tidy` — успешно; добавлены gVisor, netlink и `x/sys` зависимости.
- `go test ./... -count=1` — прошло.
- `go test -race ./internal/proxy ./internal/sandbox ./internal/preflight -count=1` — прошло.
- `go vet ./...` — прошло.
- `go build ./...` — exit 0; GVM вывел предупреждение, что stat cache нельзя записать в read-only `/home/user/.gvm/.cache`.
- `git diff --check` — без замечаний.
- Unit-проверки подтверждают фильтр IPv4 TCP/UDP (IPv6, ICMP, raw protocol, malformed short packet отбрасываются), двунаправленный TCP bridge, завершение channel-backed packet workers и запрет новых flow после начала shutdown.

Не проверено на runtime: `/dev/net/tun` отсутствует в текущем окружении; TUN interface setup, `SCM_RIGHTS` startup gate, bwrap helper capabilities/sysctl/seccomp, передача DNS/TCP/UDP через sing-box/Podman, packet capture на хосте, активный отказ proxy и опубликованные host ports. Дополнительную локальную проверку Unix control socket нельзя выполнить в sandbox: `net.ListenUnix`/`net.FileConn` возвращают `operation not permitted`. Guarded proxy ingress добавлен, но его поведение с pasta и nftables пока не подтверждено runtime-приёмкой. F06-T03 помечен `[~]` до такой проверки на обычном Linux host с доступным TUN.

В последней итерации добавлены DNS fallback на первый IPv4 nameserver host `/etc/resolv.conf`, внутренний DNS endpoint `10.89.0.2` с заменой DNS destination перед SOCKS5, и guarded proxy ingress: pasta с фиксированным tap/gateway, nftables default-drop и marked policy route. Для proxy mode с портами startup gate ждёт готовности pasta sockets и подтверждения маршрутов от helper. Добавлены unit checks для DNS mapping, firewall rules, аргументов helper, BuildCommand и pasta options.

- `gofmt -w internal/limits/limits.go internal/preflight/backend.go internal/preflight/backend_test.go internal/preflight/preflight.go internal/profile/decode.go internal/profile/decode_test.go internal/proxy/tunnel_linux.go internal/proxy/tunnel_linux_test.go internal/sandbox/build.go internal/sandbox/build_test.go internal/sandbox/forward_linux.go internal/sandbox/forward_linux_test.go internal/sandbox/network.go internal/sandbox/network_test.go internal/sandbox/run_linux.go internal/sandbox/tun_gate_linux.go internal/sandbox/tun_helper_linux.go internal/sandbox/proxy_ingress_linux.go internal/sandbox/proxy_ingress_linux_test.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — прошло; GVM вывел предупреждение о read-only stat cache `/home/user/.gvm/.cache`.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go vet ./...` — прошло.
- `git diff --check` — прошло.
- `bash -n tests/f06-proxy/runtime/run.sh` и Python AST parse `echo.py`/`probe_app.py` — прошло.
- Go unit tests в этой итерации не запускались; новые unit checks добавлены, но требуют запуска владельцем checkout.
- `tests/f06-proxy/runtime/run.sh` добавлен для live acceptance, но здесь не запускался: среда не предоставляет `/dev/net/tun`, а rootless Podman ранее завершился из-за read-only `/run/user/1000/libpod`.

Пользовательский отчёт `/tmp/isobox-test-results/f06-proxy-runtime.3mArip/result.txt` дошёл до запуска proxy helper, но runtime-приёмка остановилась до TUN setup: запись `/proc/sys/net/ipv6/conf/default/disable_ipv6` завершилась `permission denied`, а supervisor затем получил timeout startup gate. Первоначальная гипотеза связывала это с bwrap `--disable-userns`; отчёт после коммита `8a61742` показал тот же отказ, поэтому гипотеза была отвергнута. По исходнику bubblewrap, использованному при анализе, `--dev` включает devpts и второй user namespace, тогда как `--unshare-net` создаёт сеть при первоначальном clone; следовательно capability helper-а не принадлежала user namespace-владельцу этой сети.

Исправлено: proxy helper теперь создаёт network namespace после bwrap setup, сбрасывает `CAP_SYS_ADMIN`, оставляет `CAP_NET_ADMIN`, применяет IPv6/TUN setup и поднимает loopback. До `exec` приложения он устанавливает seccomp для блокировки namespace-операций. Runtime-приёмка после этой схемы ещё не запускалась. Проверка создания TUN, IPv6 sysctl, DNS/TCP/UDP через sing-box, negative egress capture и guarded ingress ports остаётся открытой. После статических проверок запустите на целевом Linux host `./tests/f06-proxy/runtime/run.sh` и пришлите новый `result.txt`.

В этой итерации `gofmt -w internal/sandbox/build.go internal/sandbox/build_test.go internal/sandbox/tun_helper_linux.go internal/preflight/preflight.go` завершился успешно; `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` и аналогичный `go vet ./...` завершились с exit 0; GVM вывел предупреждение о read-only stat cache; `git diff --check` прошёл. Go tests и runtime runner в этой итерации не запускались. Новый runtime-отчёт: `/tmp/isobox-test-results/f06-proxy-runtime.1hUOS7/result.txt`.

### F06-T04 — sing-box Podman fixture (приёмка пройдена)

- `gofmt -w internal/proxy/podman_fixture_test.go` — успешно.
- `go test ./internal/proxy -count=1` и `go test ./... -count=1` — прошли; fixture integration checks пропускаются без `ISOBOX_SINGBOX_FIXTURE`.
- `go vet ./...` — прошло.
- `go build ./...` — exit 0; GVM напечатал предупреждение о записи stat cache в read-only `/home/user/.gvm/.cache`.
- `bash -n tests/f06-proxy/sing-box/run.sh`, `python3 -m json.tool tests/f06-proxy/sing-box/config.json` и `git diff --check` — без замечаний.
- Первый запуск `tests/f06-proxy/sing-box/run.sh` в Codex-среде не дошёл до контейнера: `podman info` сообщил `Failed to obtain podman configuration: set sticky bit on: chmod /run/user/1000/libpod: read-only file system`. Повторная проверка с `XDG_RUNTIME_DIR`, `--root` и `--runroot` в `/tmp` затем уткнулась в sandbox `newuidmap ... write to uid_map failed: Operation not permitted`.
- Пользовательская runtime-приёмка прошла: `fixture_acceptance=passed`; Go integration checks подтвердили TCP CONNECT, UDP ASSOCIATE, отказ неправильной auth-пары и остановку сервера. Отчёт: `/tmp/isobox-test-results/f06-sing-box.SNry0x/result.txt`.

Runner проверяет TCP CONNECT, UDP ASSOCIATE, неверный пароль и недоступность listener после `podman stop`; тестовые назначения — loopback echo endpoints, внешний Интернет не нужен. Runtime acceptance пройдена на Linux Mint 7 / kernel 6.12.116 / Podman 5.4.2.
