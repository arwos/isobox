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
