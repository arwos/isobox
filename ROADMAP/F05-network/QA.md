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

На момент этой записи F05-T01 требовала host acceptance для nft ingress, а F05-T02 — runtime comparison pasta/slirp и IPv6 route. Runtime comparison IPv4 выполнено в последующем прогоне ниже; nft ingress и внешняя IPv6-приёмка остаются открыты.

## Повторная проверка F05-T01 и regression acceptance

Старые runtime записи выше описывают проверки до исправления обнаружения обрезанного pasta --help; актуальный результат указан здесь.

Проверка выполнена 2026-11-13 после правок preflight, pasta readiness и off-mode port rules:

- Установленный pasta возвращает `--help` длиной 4096 байт, где часть поддерживаемых flags обрезана. Preflight теперь дополнительно вызывает parser probes с каждым скрытым flag и `--help` последним; runtime проходит capability check и запускает pasta с проверенными explicit options.
- Pasta `--pid` используется как startup readiness: по документации pasta пишет свой PID после завершения инициализации. isobox сверяет PID; если `/proc/<pid>/fd` и `/proc/<pid>/net` доступны, дополнительно подтверждает ownership всех ожидаемых forwarding sockets. Недоступность procfs не подменяет PID handshake.
- `go test ./...` — прошло, включая тесты parser probes, протокольных отключателей `--no-tcp`/`--no-udp`, числовых nft port sets, PID readiness и защиты supervisor от сигнализации собственной process group.
- `go build -o /tmp/isobox-f05-current ./cmd/isobox` — прошло. GVM сообщил, что его stat cache в `/home/user/.gvm/.cache` недоступен для записи; exit code сборки 0.
- `python3 -c 'import ast, pathlib; ast.parse(pathlib.Path("ROADMAP/F05-network/source/pasta_off_acceptance.py").read_text(encoding="utf-8"))'` — прошло.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-current` — прошло: `off` без ports блокирует host-loopback, внешний TCP/UDP и DNS; direct/slirp подтверждает DNS override, IPv4 TCP/UDP и явный host-forward диапазон. Внешний IPv6 пропущен, потому что host не имеет IPv6 default route.
- `python3 ROADMAP/F05-network/source/pasta_off_acceptance.py /tmp/isobox-f05-current` — harness запустил pasta и дошёл до nft helper, но завершился до `READY`: `nft -f - failed: exit status 1 (netlink: Error: cache initialization failed: Operation not permitted)`. Payload не был запущен. Это подтверждает fail-closed при запрете netlink, но не подтверждает работу правил, host ingress или ответы на входящие TCP/UDP.
- Сценарий успешной приёмки off+ports сохранён в `source/pasta_off_acceptance.py`; запускать его нужно на хосте, где nft разрешено изменять firewall внутри rootless network namespace. Пока эта проверка не проходит до payload, F05-T01 остаётся `[!]`.

Повторная попытка с расширенным доступом:

- `python3 ROADMAP/F05-network/source/pasta_off_acceptance.py /tmp/isobox-f05-current` — завершилась с ошибкой до запуска payload: `pasta exited during startup: Couldn't switch to pasta namespaces: Operation not permitted`.
- Этот запуск не дошёл до nft helper и не проверил ingress/egress; ранее обычный запуск доходил до nft и завершался `netlink: Error: cache initialization failed: Operation not permitted`. Оба сценария подтверждают отказ до приложения, но не заменяют успешную сетевую приёмку.
- Для полного acceptance нужен host environment, в котором pasta может подключиться к bwrap network namespace и nft может применить правила внутри него.

## Direct backend comparison and runtime fallback

Выполнено после добавления переключения на заранее проверенный slirp, если pasta не готов до открытия bwrap gate:

- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — прошло; покрыты выбор совместимого fallback, отказ fallback для loopback DNS/IPv6 host bind, подготовка общих resolver-адресов, cleanup pasta при переключении и fail-closed при ошибке обоих backend.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f05-fallback ./cmd/isobox` — прошло. GVM сообщил, что stat cache в `/home/user/.gvm/.cache` недоступен для записи; exit code сборки 0.
- Парсинг `ROADMAP/F05-network/source/slirp_acceptance.py` через `ast.parse` и `git diff --check` — прошли.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-fallback --backend slirp` — прошло: `off` без ports блокирует host-loopback/external TCP/UDP/DNS; `off` с ports fail-closed без pasta; direct проверяет TCP+UDP диапазон, TCP-only/UDP-only одиночные порты, точный `127.0.0.1` bind, undeclared-port отказ, default/profile DNS и внешний IPv4 TCP/UDP.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-fallback --backend auto` — прошло с обычным pasta-first выбором (оба executable были доступны, preflight capability test выбирает pasta); direct port/DNS/IPv4 TCP/UDP сценарии прошли без fallback warning.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-fallback --backend auto --simulate-pasta-failure --local-only` — прошло. Wrapper пропускает реальные pasta capability probes, но отказывает при runtime start; isobox пишет fallback diagnostic и до открытия приложения поднимает slirp. Диапазон и оба protocol-specific single ports доступны только по своим правилам.
- `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-fallback --backend auto --simulate-pasta-failure --dns-only` — прошло: после injected pasta startup failure через slirp работают host-default DNS, profile DNS `1.1.1.1`, внешний IPv4 TCP и UDP.
- Во всех полных прогонах внешний IPv6 пропущен: host не имеет IPv6 default route. Это остаётся незакрытой частью T02.
- Один повтор `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-fallback --backend pasta` прошёл диапазон, но остановился на TCP-only setup из-за `Couldn't switch to pasta namespaces: Operation not permitted`; приложение не запустилось. В некоторых отдельных запусках slirp также возвращал `setns(CLONE_NEWNET): Operation not permitted`; запуск оставался за startup gate. Полные slirp и auto pasta-first прогоны, а также локальный и DNS acceptance injected fallback выше прошли. Если оба backend откажут, запуск остаётся fail-closed.

## F05-T01: итоговая off-mode acceptance

Первоначальный `nft ... Operation not permitted` был связан с namespace topology bwrap на этом host: при `--dev /dev` bwrap входил во вложенный user namespace, тогда как созданный ранее network namespace принадлежал предыдущему namespace. Для off+ports network namespace теперь создаёт внутренний helper после завершения bwrap mount setup. Он применяет firewall, сбрасывает capabilities и ждёт на временном Unix socket; supervisor обнаруживает host TID с изолированным netns, поднимает pasta и только после его PID/socket readiness открывает helper gate.

- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f05-final ./cmd/isobox` — прошло; GVM сообщил, что его stat cache в `/home/user/.gvm/.cache` недоступен для записи, exit code сборки 0.
- Python `ast.parse` для `source/pasta_off_acceptance.py` — прошло.
- `python3 ROADMAP/F05-network/source/pasta_off_acceptance.py /tmp/isobox-f05-final` — прошло на обычном UID с разрешённым runtime namespace acceptance. Проверены отказ TCP к host listener и `1.1.1.1:443`, блокировка UDP/DNS, невозможность создать ICMP datagram socket (`EPERM`), host TCP/UDP запросы к объявленному `127.0.0.1` порту с ответами приложения и отказ host TCP/UDP к соседнему не объявленному порту.
- Runtime-результат: `PASS sandbox process could not create an ICMP socket`; `PASS off mode blocked host/Internet TCP and external UDP/DNS`; `PASS host TCP/UDP requests reached the announced port and received replies`; `PASS host could not reach the unannounced guest TCP/UDP port`.
- Ранние неуспешные прогоны выше остаются историей fail-closed поведения до изменения порядка создания namespace; они не описывают текущий результат.

F05-T01 принята. F05-T02 остаётся `[!]`: внешний IPv6 недоступен из-за отсутствия default route, а IPv6 host-bind acceptance не прошла до запуска приложения.

## F05-T02: IPv6 host-bind probe

Acceptance harness теперь проверяет объявленный `::1` TCP+UDP bind через pasta, если host routes позволяют pasta выбрать IPv6 interface. slirp-прогон явно сообщает, что IPv6 host bind не поддержан его IPv4-only host-forward API.

- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f05-ipv6 ./cmd/isobox` — прошло; GVM сообщил, что stat cache в `/home/user/.gvm/.cache` недоступен для записи.
- Python `ast.parse` для финальной `source/slirp_acceptance.py`, проверка route predicates (`default route=False`, `routed interface=False`) и `git diff --check` — прошли.
- Host Python смог создать TCP sockets на `::1` и `::`; `/proc/net/ipv6_route` содержит link-local routes на `tun0` и `wlp3s0`, но не IPv6 default route. Pasta manual описывает автоматический выбор единственного routed interface при отсутствии default route; у этого host несколько таких interface, поэтому harness считает IPv6 network setup недоступным.
- Первый запуск `python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-f05-ipv6 --backend pasta --local-only` прошёл IPv4 range и TCP/UDP single-port проверки, затем IPv6 bind probe завершился до `READY`: pasta вернула `Failed to bind port ... for option '-t ::1/...'`; приложение не запустилось. Этот результат не считается IPv6 forwarding pass.
- Повторные локальные acceptance прогоны остановились раньше отдельных protocol checks: pasta вернула `Couldn't switch to pasta namespaces: Operation not permitted`, а при auto fallback slirp вернул `setns(CLONE_NEWNET): Operation not permitted`. Payload остался за startup gate.
- Внешний IPv6 также остаётся непроверенным: default route отсутствует, отдельная попытка connect завершилась `Network is unreachable`. F05-T02 остаётся `[!]`; для закрытия нужны host routes, позволяющие pasta поднять IPv6, успешный `::1` TCP/UDP host bind и внешние IPv6 DNS/TCP/UDP пробы.

## Повторная попытка создать локальную IPv6 acceptance-среду

- Временный `bwrap --unshare-user --unshare-net --cap-add CAP_NET_ADMIN` namespace был создан, но `ip link set lo up`, `ip link add ... type dummy`, назначение IPv6 address и route завершились `RTNETLINK answers: Operation not permitted`. Host namespace не изменялся; synthetic routed interface создать не удалось.
- Одноразовый wrapper, добавляющий `--ipv6-only` только к runtime вызову pasta, и IPv6-only payload с объявленным `::1` TCP+UDP портом завершились до `READY`: `Couldn't switch to pasta namespaces: Operation not permitted`. Это не forwarding acceptance pass; приложение не запускалось.
- Отдельный `pasta --ipv6-only --config-net ... /usr/bin/sleep 1` завершился с кодом 0, но без клиентского TCP/UDP probe не считается проверкой host forwarding.
- В этой среде нет безопасного локального способа создать маршрутизируемый IPv6 test interface. Требуется внешний host environment с настроенным IPv6 маршрутом и разрешёнными rootless namespace/netlink операциями.

## Повторная проверка IPv6 `::1` host forwarding

- Повторное чтение таблицы маршрутов подтвердило: глобального IPv6 адреса и default route нет; доступны только `fe80::/64` на `tun0` и `wlp3s0`.
- Точечный вызов IPv6 loopback acceptance harness с временным wrapper вокруг pasta и `--outbound-if6=lo` остановился до `READY`: `External interface not usable`.
- Вариант с `--interface=lo` остановился до `READY`: pasta не смогла открыть объявленный host listener `::1` (`Failed to bind port ... for option '-t ::1/...'`).
- Варианты `--interface=tun0|wlp3s0 --ipv6-only` остановились до `READY` с `Couldn't switch to pasta namespaces: Operation not permitted`; `--outbound-if6=tun0|wlp3s0 --ipv6-only` — с `External interface not usable`.
- Wrapper и тестовые каталоги были временными; host routes и интерфейсы не менялись. Ни одна из этих попыток не является IPv6 forwarding acceptance pass. Нужна среда, где pasta может настроить IPv6 namespace и host `::1` listener; external IPv6 проверки также требуют рабочего default route.
