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
- На момент первоначальной проверки для T01 ingress/egress filter, а для T02 direct TCP/UDP/DNS и pasta/slirp forwarding оставались не подтверждены; повторная live-проверка ниже закрыла часть direct/slirp сценариев.

Дополнительная runtime-попытка T01:

- Профиль `mode off` объявлял `127.0.0.1:39471/tcp`; тестовый `PATH` содержал только symlink на установленные `bwrap`, `pasta`, `nft`. Payload был `/usr/bin/true`.
- `PATH=/tmp/isobox-f05-off.ec4iL8/bin /tmp/isobox-f05-off.ec4iL8/isobox run --profile=/tmp/isobox-f05-off.ec4iL8/profile.unic --app-id=f05-off -- /usr/bin/true` — exit 2 до сборки/старта sandbox. Preflight сообщил две причины: `bwrap: loopback: Failed to create NETLINK_ROUTE socket: Operation not permitted` и `installed pasta lacks options required to enforce off-mode egress and explicit forwarding`.
- nft rules и pasta не стартовали, приложение не запускалось. Попытка подтвердила fail-closed preflight, но не проверила host-to-app ingress или egress filter в работающем namespace. Для live-приёмки нужен совместимый passt/pasta и host policy, разрешающая bwrap настраивать loopback/routes.

Дополнительная runtime-попытка T02:

- Установлены `bwrap 0.12.0` и `slirp4netns 1.2.1`; тестовый `PATH` содержал bwrap и slirp4netns, но не pasta, чтобы выбрать slirp fallback. Профиль использовал `mode direct`, явный DNS `192.0.2.53`, временный HOME и команду `/usr/bin/true`.
- `PATH=/tmp/isobox-t02-bin /tmp/isobox run --profile=/tmp/isobox-t02-profile.unic --app-id=t02 -- /usr/bin/true` — приложение не запущено. Host preflight отклонил bwrap: `loopback: Failed to create NETLINK_ROUTE socket: Operation not permitted`.
- Независимая проверка `unshare --user --map-root-user --net nft list tables` также не смогла открыть netlink (`Unable to initialize Netlink socket: Operation not permitted`). Создание user namespace само по себе проходит.
- Результат не проверяет slirp readiness, DNS или forwarding: bwrap не смог создать пригодную сеть раньше запуска payload. Необходимо повторить live acceptance на хосте, где rootless bwrap разрешено настраивать loopback и маршруты. Fail-closed поведение подтверждено: команда приложения не стартовала.

Повторная live-проверка с разрешением host NETLINK_ROUTE и локальных Unix sockets:

- `bwrap --unshare-user --unshare-net --ro-bind / / --proc /proc --dev /dev /usr/bin/true` — прошло. Тот же запуск без расширенного доступа ранее завершался `NETLINK_ROUTE: Operation not permitted`; ограничение относилось к обычной test оболочке.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f05-live/isobox ./cmd/isobox` — прошло; GVM сообщил, что stat cache в `/home/user/.gvm/.cache` недоступен для записи.
- `python3 -c 'import ast, pathlib; ast.parse(pathlib.Path("ROADMAP/F05-network/source/slirp_acceptance.py").read_text(encoding="utf-8"))'` — прошло.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-live/isobox` — прошло при запуске бинарного файла от обычного UID 1000. Временный `PATH` содержал только bwrap и slirp4netns, тем самым выбирался fallback. Проверены: `off` блокирует host-loopback TCP, внешний TCP/UDP и DNS; `off` с портом при отсутствии pasta завершает preflight до payload; direct запускается через slirp4netns 1.2.1; TCP и UDP принимаются на каждом порту профилированного диапазона; привязка `127.0.0.1` не доступна на non-loopback адресе; не объявленный TCP порт отклонён; проходят default DNS и profile DNS `1.1.1.1`, внешний IPv4 TCP handshake и один IPv4 UDP DNS query к `1.1.1.1:53`.
- Harness вывел `SKIP external IPv6: host has no IPv6 default route`; `ip -6 route show default` вернул пустой результат. Внешнюю IPv6 доступность не считаем проверенной.
- `pasta --help` не содержит обязательных для `off`/`direct` опций `--config-net`, `--udp-ports`, `--tcp-ns` и `--udp-ns`. Проверочный `off` профиль с опубликованным port и установленным pasta завершился exit 2 с ошибкой `installed pasta lacks options required to enforce off-mode egress and explicit forwarding`; payload marker отсутствовал. Это подтверждает startup fail-closed, но не работу nft guard и ingress.
- Реальный slirp API обмен и host forwarding проверены harness-ом; он успешно открыл диапазон отдельными TCP/UDP `add_hostfwd` правилами. Повторяемая инструкция: `ROADMAP/F05-network/source/README.md`.

F05 остаётся частично непринятой: нужен host с совместимой pasta для off-mode egress guard/ingress и прямого backend comparison; нужен IPv6 default route для внешней IPv6 матрицы.
