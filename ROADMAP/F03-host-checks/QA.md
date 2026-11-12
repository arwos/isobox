# F03 QA

## Планируемые сценарии

- Проверить отсутствие каждого обязательного бинарника и отключённые user namespaces; получить точную диагностику без запуска payload.
- Проверить автоматический выбор pasta и fallback на slirp, когда только один backend доступен.
- Проверить, что pasta автоматически не публикует незаявленные порты; проверить TCP/UDP и явные host addresses для отдельных портов и диапазонов.
- Отклонить fallback slirp для IPv6 host bind address, отсутствующего API или DNS override, если helper не может сохранить эти параметры.
- Отклонить `off` с ports при отсутствии egress guard, а `proxy` с ports — пока нет совместимого ingress path.
- Проверить TUN недоступен/запрещён и получить отказ до запуска proxy-mode приложения.
- Проверить, что рекомендации не выполняются автоматически с root-правами.

## Выполнение

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

Не запускалось:

- Proxy-mode `isobox run` и фактическая TUN-проба не запускались. `/dev/net/tun` отсутствует, поэтому proxy mode здесь должен закрываться отказом.
- Не проверялись фактическая доступность host bind addresses/ports, настройка DNS внутри namespace и готовность helper в runtime; текущие direct/off-попытки блокируются до payload из-за host networking policy и несовместимых опций pasta.

Сборка подтверждает компиляцию, а unit-тесты — отдельные решения backend/preflight. Они не доказывают возможность создавать namespaces/TUN на целевой системе.
