# F05 QA

## Планируемые сценарии

- `off`: исходящие TCP/UDP, DNS, IPv4/IPv6 и доступ к host/LAN не проходят; объявленный host port принимает соединение; ответы разрешены только в его рамках.
- `off`: попытки использовать не объявленный host port или соседнее устройство завершаются отказом.
- `direct`: внешний TCP/UDP работает; проверки доступны только объявленные host bind address и диапазоны.
- Убедиться, что pasta auto forwarding отключён; приложение не получает не объявленные входящие порты.
- Сравнить семантику direct mode на pasta/slirp; отказать в fallback при отличии IP/protocol/bind behavior.
- Проверить занятую host-пару адрес/порт, диапазон с конфликтом и низкий порт без возможности bind: точная ошибка, приложение не стартует.
- Проверить отсутствие утечки при отключении backend во время запуска.
- `direct`: внешние IPv4/IPv6 TCP/UDP и profile DNS работают на pasta и slirp; DNS host-loopback и profile override работают без чтения host `/etc/resolv.conf` приложением.
- `direct`: TCP-only, UDP-only, both, одиночный порт и диапазон проходят через выбранный backend; host bind address совпадает с профилем; конфликт bind/API отменяет запуск до приложения.

## Выполнение

- `gofmt -w internal/cli/cli.go internal/preflight/backend.go internal/preflight/preflight.go internal/sandbox/build.go internal/sandbox/forward_linux.go internal/sandbox/network.go internal/sandbox/run_linux.go internal/sandbox/run_other.go internal/sandbox/slirp_linux.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — прошло; GVM вывел предупреждение о недоступном stat cache в `/home/user/.gvm/.cache`, на сборку это не повлияло.
- `gofmt -w internal/sandbox/off_guard_linux.go internal/sandbox/off_guard_linux_test.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/sandbox` — прошло; тесты проверяют нормализацию/отказ port set, точные input/output/forward default-drop rules и ошибку nft helper.
- `gofmt -w internal/preflight/backend_test.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/preflight` — прошло; проверки покрывают отсутствие backend для `off` без ports, требования off guard, приоритет pasta, slirp fallback и отказ от IPv6 host forward на slirp.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — прошло.
- `git diff --check` — прошло.
- `gofmt -w internal/sandbox/forward_linux.go internal/sandbox/slirp_linux.go internal/sandbox/forward_linux_test.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/sandbox` — прошло; новые unit-тесты проверяют native-endian разбор `/proc` endpoints, разворачивание slirp TCP/UDP правил и диапазонов, JSON для `add_hostfwd` и fail-closed разбор ответов API.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — прошло после изменений.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — прошло; GVM сообщил, что stat cache в `/home/user/.gvm/.cache` недоступен для записи.
- `git diff --check` — прошло после изменений.
- Попытка поднять локальный Unix socket для интеграционной проверки slirp API завершилась `listen unix ...: setsockopt: operation not permitted`. Поэтому API socket обмен не проверен в этой среде; новые тесты изолированно проверяют формирование запроса и обработку ответа, но не заменяют runtime-приёмку.
- Проверка `pasta --help` подтвердила DNS и PID options, но не показала все explicit network/forwarding options, которые требует preflight; такой pasta не будет выбран. `slirp4netns --help` показывает configure, IPv6, readiness, exit и API socket options.
- Для T01 ingress и egress-фильтр, а для T02 direct TCP/UDP/DNS и pasta/slirp forwarding остаются не подтверждены.

Дополнительная runtime-попытка T01:

- Профиль `mode off` объявлял `127.0.0.1:39471/tcp`; тестовый `PATH` содержал только symlink на установленные `bwrap`, `pasta`, `nft`. Payload был `/usr/bin/true`.
- `PATH=/tmp/isobox-f05-off.ec4iL8/bin /tmp/isobox-f05-off.ec4iL8/isobox run --profile=/tmp/isobox-f05-off.ec4iL8/profile.unic --app-id=f05-off -- /usr/bin/true` — exit 2 до сборки/старта sandbox. Preflight сообщил две причины: `bwrap: loopback: Failed to create NETLINK_ROUTE socket: Operation not permitted` и `installed pasta lacks options required to enforce off-mode egress and explicit forwarding`.
- nft rules и pasta не стартовали, приложение не запускалось. Попытка подтвердила fail-closed preflight, но не проверила host-to-app ingress или egress filter в работающем namespace. Для live-приёмки нужен совместимый passt/pasta и host policy, разрешающая bwrap настраивать loopback/routes.

Дополнительная runtime-попытка T02:

- Установлены `bwrap 0.12.0` и `slirp4netns 1.2.1`; тестовый `PATH` содержал bwrap и slirp4netns, но не pasta, чтобы выбрать slirp fallback. Профиль использовал `mode direct`, явный DNS `192.0.2.53`, временный HOME и команду `/usr/bin/true`.
- `PATH=/tmp/isobox-t02-bin /tmp/isobox run --profile=/tmp/isobox-t02-profile.unic --app-id=t02 -- /usr/bin/true` — приложение не запущено. Host preflight отклонил bwrap: `loopback: Failed to create NETLINK_ROUTE socket: Operation not permitted`.
- Независимая проверка `unshare --user --map-root-user --net nft list tables` также не смогла открыть netlink (`Unable to initialize Netlink socket: Operation not permitted`). Создание user namespace само по себе проходит.
- Результат не проверяет slirp readiness, DNS или forwarding: bwrap не смог создать пригодную сеть раньше запуска payload. Необходимо повторить live acceptance на хосте, где rootless bwrap разрешено настраивать loopback и маршруты. Fail-closed поведение подтверждено: команда приложения не стартовала.
