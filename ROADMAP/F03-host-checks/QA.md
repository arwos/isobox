# F03 QA

## Планируемые сценарии

- Проверить отсутствие каждого обязательного бинарника и отключённые user namespaces; получить точную диагностику без запуска payload.
- Проверить автоматический выбор pasta и fallback на slirp, когда только один backend доступен.
- Проверить, что pasta автоматически не публикует незаявленные порты; проверить TCP/UDP и явные host addresses для отдельных портов и диапазонов.
- Отклонить IPv6 DNS/host bind до выбора backend; требовать pasta `--ipv4-only` и запускать slirp без `--enable-ipv6`.
- Отклонить `off` с ports при отсутствии egress guard, а `proxy` с ports — пока нет совместимого ingress path.
- Проверить TUN недоступен/запрещён и получить отказ до запуска proxy-mode приложения.
- Проверить, что рекомендации не выполняются автоматически с root-правами.

## Выполнение

## Актуальная проверка network capability path

- После завершения F05-T02 runtime проверено: `./tests/f05-network/pasta/run.sh --local-only`, `./tests/f05-network/slirp4netns/run.sh --local-only`, `./tests/f05-network/auto-fallback/run.sh --local-only` и `./tests/f05-network/off-published/run.sh` — прошли. Полные внешние DNS/TCP/UDP проверки см. `ROADMAP/F05-network/QA.md`.
- `./tests/f05-network/profile-validation/run.sh` — прошло; профильные IPv6 DNS, IPv6 host bind и IPv4-mapped IPv6 bind отклоняются до запуска команды приложения.
- Проверка проверила bwrap `--cap-add`, временные `CAP_SYS_ADMIN`/`CAP_NET_ADMIN`, namespace-local IPv6 sysctl и их сброс перед `exec`; `/proc/net/if_inet6` пуст в app.
- Proxy/TUN проверки остаются не запущенными: текущая система не предоставляет `/dev/net/tun`, а F06 ещё не реализована.

Проверено:

- `pasta --help` и `slirp4netns --help` — локальные interfaces прочитаны; установленный pasta обрывает список до всех нужных параметров, поэтому его совместимость в этом окружении не подтверждена. Функция runtime выбора по этим выводам отдельно не запускалась.
- `gofmt -w internal/preflight/backend.go internal/preflight/preflight.go` — успешно.
- `git diff --check` — без замечаний.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM вывел предупреждение о записи stat cache в read-only `/home/user/.gvm/.cache`.
- `gofmt -w internal/preflight/preflight.go internal/preflight/preflight_test.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/preflight` — успешно; тесты проверяют action hint для NETLINK_ROUTE denial и прочих bwrap errors.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — успешно.
- `PATH=/tmp/isobox-f05-off.ec4iL8/bin /tmp/isobox-f05-off.ec4iL8/isobox run --profile=/tmp/isobox-f05-off.ec4iL8/profile.unic --app-id=f05-off -- /usr/bin/true` — exit 2 до payload; новая диагностика предлагает разрешить rootless bwrap loopback setup и указывает отдельно, что установленный pasta не поддерживает необходимые off-mode options.
- В текущей среде `bwrap`, `pasta` и `slirp4netns` найдены через `PATH`; `/dev/net/tun` отсутствует.

Исторические ограничения при запуске из execution sandbox:

- Proxy-mode `isobox run` и фактическая TUN-проба не запускались. `/dev/net/tun` отсутствует, поэтому proxy mode здесь должен закрываться отказом.
- Более ранние direct/off-попытки блокировались до payload из-за host networking policy; runtime-гейт позже был повторён вне execution sandbox и прошёл (см. актуальную проверку выше).

Сборка подтверждает компиляцию, а unit-тесты — отдельные решения backend/preflight. Они не доказывают возможность создавать namespaces/TUN на целевой системе.
