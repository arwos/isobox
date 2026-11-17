# F09 QA: именованные групповые сети

План проверок. Реальные результаты добавлять только после выполнения соответствующей задачи; во время планирования проверки не запускались.

## Profile и address allocation

- Decode повторяемых `group` и `alias`, включая один элемент, несколько элементов и отсутствующие значения.
- Отклонять пустые/некорректные имена, дубликаты в одном профиле, неизвестные группы, IPv6/некорректные CIDR, пересекающиеся CIDR у групп одного sandbox, конфликтующие app-id/aliases и исчерпание адресного пула.
- При конкурентном запуске нескольких sandbox в одной группе выдавать разные usable IPv4; при старте и завершении корректно занимать и освобождать lease.

## Rootless network manager и systemd user

- Провести F09-T01 proof от обычного пользователя без `sudo`: отдельные loopback/network namespaces, bridge/veth, взаимная IPv4 TCP/UDP связь и отсутствие доступа в host/LAN вне внешнего режима.
- Создать сеть, проверить активный manager/bridge без контейнеров, перезапустить user manager, проверить recovery enabled unit; проверить поведение с lingering и без него.
- Удаление пустой сети останавливает только принадлежащий isobox unit и удаляет состояние; удаление сети с работающими участниками завершается понятным отказом.
- Небезопасный владелец/режим каталогов, symlink, повреждённый state, отсутствующий user manager, отказ systemd или невозможность создать bridge блокируют операцию без частичной рабочей сети.

## Connectivity, names и policy

- Подключить две sandbox к одной группе; проверить уникальные IP, отдельные `lo`, соединение TCP и UDP по IP в обе стороны, а также DNS по `app-id` и всем aliases.
- Подключить sandbox к двум группам; проверить отдельный адрес на каждой сети, доступ к участникам каждой группы, отсутствие связи из не подключённой группы, отсутствие маршрутизации между группами и отсутствие конфликтов alias между независимыми группами.
- После штатного завершения и принудительного выхода удалить интерфейс, lease и DNS records; после рестарта manager восстановить только актуальных участников.
- В `off` подтвердить межконтейнерную связь только внутри явно подключённой группы и запрет внешнего DNS, host, LAN и Internet.
- В `direct` проверить внешний IPv4 доступ отдельно от межгруппового маршрута; в `proxy` подтвердить отсутствие direct fallback; в `egress` проверить, что внешние назначения по-прежнему под allowlist, а разрешённая связь внутри группы не уходит в TUN.
- Подтвердить, что подключение к общей группе само по себе не даёт доступа к host loopback или другим группам.

## Фактические проверки

- Для F09-T02 выполнены `gofmt` по изменённым Go-файлам, `git diff --check`, `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f09-t02 ./cmd/isobox`, `go vet ./...` и `/tmp/isobox-f09-t02 network --help` — успешно. GVM выдал предупреждение о read-only stat cache под `/home/user/.gvm`; команды завершились с кодом 0.
- `make lint` после исправления замечаний завершился с кодом 0; `golangci-lint` сообщил `0 issues`. В ходе запуска `govulncheck` не смог скачать базу `vuln.go.dev` из-за недоступного proxy socket, поэтому vulnerability scan не подтверждён; goppy отметил его предупреждением.
- Live acceptance от обычного пользователя без `sudo` выполнялась с доступом к user bus: `systemctl --user is-system-running` → `running`. Временная команда `/tmp/isobox-f09-t02 network create --name=f09-t02-verify-20261116 --subnet=192.0.2.1/29` создала сеть `192.0.2.0/29`, gateway `192.0.2.1`, bridge `ibb4aaa3f134d9`; `isobox network list` показал `active`, `MEMBERS=0`; `systemctl --user is-enabled ...` показал `enabled`; `systemctl --user restart ...` завершился успешно, после него `network list` снова показал `active`; `network remove` удалил сеть. После cleanup конфиг, unit и socket отсутствовали, `is-enabled` ответил `not-found`, `network list` показал только заголовок.
- Первый live start выявил недоступность `/proc/1/ns/user` в service окружении. Проверку worker изменили на собственный `/proc/self/uid_map`; после этого полный цикл create/start/list/restart/remove прошёл.
- `loginctl show-user 1000 --property=Linger --value` вернул `no`; это подтверждает, что в проверяемой сессии запуск user manager до login не включён. Lingering не менялся.
- Дополнительно создана временная сеть `f09-manager-reexec` (`198.18.0.0/29`), после `systemctl --user daemon-reexec` manager восстановил bridge в состоянии `active`, `MEMBERS=0`; тестовая сеть удалена, `network list` пуст. Это проверяет re-exec работающего user manager, но не reboot/запуск до login.
- В F09-T03 подтверждён отказ `remove` при активном veth и последующее удаление после освобождения участника. `systemctl --user daemon-reexec` восстановил активный bridge, а restart network manager service также прошёл. Lingering в окружении выключен (`no`); boot до login не проверялся, для него требуется отдельное включение lingering пользователем.
- `bash -n tests/f09-network-groups/rootless-bridge/run.sh` — passed.
- `bash tests/f09-network-groups/rootless-bridge/run.sh` — passed от обычного пользователя без `sudo` на Linux `6.12.116+deb13-amd64`, bubblewrap `0.12.0`; для выполнения внутри этой ограниченной agent-среды потребовалось разрешение на запуск временного network namespace/netlink прототипа. Скрипт не выполнялся от root и удалил созданные namespace-процессы и временные файлы по завершении.
- Результат прототипа: bridge и две veth-пары созданы в rootless user/network namespaces; участники получили `192.168.240.2/24` и `192.168.240.3/24`, разные netns inode и отдельные `lo`; взаимный TCP и UDP обмен прошёл через bubblewrap `--share-net`; loopback между участниками недоступен; у участников нет default route и host network не подключена; `CAP_NET_ADMIN` не позволяет управлять bridge/veth, вложенный user namespace заблокирован, `CapEff=0` при явном сбросе capabilities.
- Передача user namespace manager-а через `bwrap --userns FD` — не подходит для проверенной модели: bubblewrap завершился сообщением `Joining the specified user namespace failed, it might not be a descendant of the current user namespace`. Выбрана модель отдельного app user namespace с присоединением к уже настроенному network namespace через `--share-net`.
- Не запускались: весь Go unit suite, F05/F06 runtime-приёмка, интеграция режимов direct/proxy/egress и внешняя host/LAN connectivity acceptance. Для systemd сетей прошли create/list/service-restart/remove; restart всего user manager, lingering/boot recovery и отказ remove при реальном активном участнике остаются открытыми.

## F09-T03: фактические проверки

- `gofmt` по изменённым Go-файлам, `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...`, сборка `/tmp/isobox-f09-t03` и `git diff --check` — passed. Go unit tests не запускались.
- Runtime запуск двух временных сетей через общий `isobox-network-manager.service` прошёл от обычного пользователя с user systemd: сети `192.0.2.0/29` и `198.51.100.0/29` создали два интерфейса `g0`/`g1` с `192.0.2.2/29` и `198.51.100.2/29`; внутри виден собственный `lo`, только два connected routes, default route отсутствует, `id -u` вернул `1000`, `CapEff=0`. После завершения `MEMBERS=0`; временные сети удалены.
- Две одновременно запущенные sandbox одной сети обменялись TCP: клиент увидел `ok:192.0.2.3`, то есть manager выдал второму участнику отдельный адрес. `remove` при активном veth завершился отказом с `1 active member interface(s)`; после выхода обоих процессов снова `MEMBERS=0`, сеть удалена.
- Первый concurrency run обнаружил коллизию host-side veth имени; генерация имени переведена на случайный токен и повторная проверка прошла.
- Live fail-closed проверки: неизвестная группа и пересекающиеся сети завершились с кодом `125` без запуска приложения; в `/29` пять процессов получили lease, шестой получил `no free IPv4 addresses`, маркер команды приложения не создался. Завершение пяти клиентов по сигналу очистило участников до `MEMBERS=0`; сеть затем удалилась.
- После включения случайного выбора адреса повторный запуск с двумя группами выдал `198.51.100.6/29` и `203.0.113.6/29`; connected routes и `lo` присутствовали, default route отсутствовал, внутри сохранились UID `1000` и `CapEff=0`. После выхода у обеих сетей было `MEMBERS=0`, сети удалены.
- На момент завершения F09-T03 не проверялись UDP peer traffic, `direct`, `proxy`, `egress`, опубликованные порты и host/LAN isolation; grouped launch тогда принимал только `mode off`. Runtime-проверки реализации F09-T05 перечислены ниже.

## F09-T04: фактические проверки

- `go build ./...`, `go vet ./...` и `git diff --check` завершились с кодом `0`; Go unit tests не запускались.
- Дополнительная `GOOS=darwin CGO_ENABLED=0 go build ./...` не прошла из-за `undefined: validateCredentials` в неизменённом `internal/proxy/client.go`; Linux build и vet проходят.
- Runtime от обычного пользователя: sandbox в общей группе разрешил два aliases и app-id server процесса в `198.51.100.3`; TCP connection к адресу, полученному по alias, вернул `dns-tcp-ok`.
- Sandbox только в другой сети получил NXDOMAIN для alias; после завершения DNS owner следующий lookup также получил NXDOMAIN. Подключение второго участника с уже занятым alias завершилось с кодом `125`, команда приложения не выполнилась.
- На момент приёмки F09-T04 групповой DNS был проверен с `network.mode off`; TCP DNS transport, UDP peer traffic, aliases при нескольких группах и direct/proxy/egress не проверялись. Позднейшие F09-T05 результаты перечислены ниже.

## F09-T05: фактические проверки

- `gofmt` по изменённым Go-файлам, `go build ./...`, `go vet ./...` и `git diff --check` — passed. Сборка использовала текущий Go `1.26.8` и сохранённый GVM module cache; unit tests не запускались.
- Сборка отдельного CLI `/tmp/isobox-f09-t05` — passed.
- Runtime acceptance выполнялась от обычного пользователя без `sudo`; для доступа к host user bus и rootless netlink operations потребовалось разрешение execution environment. Созданы сети `f09t05-20261116` (`198.51.100.0/29`) и `f09t05-other` (`203.0.113.0/29`), обе показали `active`.
- `off` server в первой группе и `egress` client в той же группе: DNS app-id вернул `198.51.100.3`, TCP connection вернул `peer-ok`. `egress` sandbox route table содержала `default dev isobox0` и более специфичный `198.51.100.0/29 dev g0`; это подтверждает, что group flow проходит напрямую, а внешний маршрут остаётся в policy TUN.
- Для egress профиля с allowlist только на `203.0.113.0/24:9/tcp` connect к `203.0.113.7:443` не прошёл; запрос `example.com` также не разрешился. `~/.isobox/log/group-egress-policy.log` записал блокировки destination `203.0.113.7:443` и DNS upstream `1.1.1.1:53`, включая причину отсутствия allow rule.
- Host loopback проверен через `host.gateway.internal` → `10.123.123.3`: egress профиль с явным правилом `127.0.0.1:45678/tcp` получил ответ `host-ok` от временного host-loopback listener.
- Вторая sandbox, подключённая только к `f09t05-other`, получила NXDOMAIN для `group-server`; TCP к известному IP `198.51.100.3` также завершился ошибкой (`DNS_ISOLATED`, `ROUTE_ISOLATED`). Это подтверждает group DNS scope и отсутствие межгруппового маршрута.
- `proxy` с временным SOCKS5 alias на закрытом порту завершился до запуска приложения: host-side TUN readiness вернул `connection refused`; `APP_STARTED` не появился. Direct fallback не предпринимался.
- Первая grouped `direct` реализация запускала `pasta`/`slirp4netns` из inner CLI, уже работающего в manager-owned member network namespace. Это ломало host egress: обычный direct и slirp egress без группы проходили, а grouped direct нет. Backend supervision перенесён во внешний CLI через manager handoff; он запускает helper из host network namespace и удерживает приложение за startup gate. После этого grouped `direct` разрешил `example.com` и установил TCP к `1.1.1.1:443`.
- После переноса backend grouped `direct` и `off` участники одной группы обменялись DNS/TCP (`direct-peer-ok`) и UDP (`direct-udp-peer-ok`). Это подтверждает, что внешний direct backend не перехватил connected route группы.
- `off` участник в отдельной временной группе получил только connected route `198.18.5.0/29 dev g0`; соединения к `127.0.0.1`, host IPv4 `192.168.103.160`, LAN gateway `192.168.111.1` и Internet `1.1.1.1` завершились ошибкой. Сеть удалена, `network list` пуст.
- DNS-over-TCP к loopback resolver внутри `off` sandbox вернул один A record для app-id другого участника (`198.18.2.4`); временная сеть удалена, `network list` пуст.
- Host baseline также разрешил `example.com` и установил TCP к `1.1.1.1:443`, что позволило отделить исходную grouped direct регрессию от ограничения внешней сети среды.
- Позитивный `proxy` проверен grouped sandbox с временным локальным SOCKS5 endpoint: SOCKS5 CONNECT и UDP ASSOCIATE были направлены на локальные TCP/UDP echo endpoints; приложение получило `proxy-tcp-ok` и `proxy-udp-ok`. Отдельная readiness-проверка с закрытым proxy портом завершалась до запуска приложения; direct fallback не предпринимался.
- Временный direct acceptance-контейнер завершён, сеть `f09-direct-audit` удалена; повторный `network list` показал пустой список.
- При штатном завершении участника manager показал `MEMBERS=0`. `systemctl --user restart isobox-network-manager.service` восстановил обе сохранённые сети с `active`; после этого alias удалён, обе сети удалены, `network list` пуст.
- Проверен конфликт стандартной TUN-подсети: при group `10.123.123.0/24` grouped `egress` сохранил route `10.123.123.0/24 dev g0`, выбрал TUN `10.89.1.0/24 dev isobox0` и запустил приложение; тестовая сеть удалена.
- Group DNS по UDP и TCP, direct external IPv4 egress, proxy TCP/UDP через SOCKS5, egress allowlist, межконтейнерный TCP/UDP и межгрупповая изоляция подтверждены. Для `off` при активном host/LAN uplink доказано отсутствие default route; connect attempts к host loopback, host IPv4, LAN gateway и Internet были заблокированы. Все acceptance-сети, aliases, listeners и созданные home/log артефакты удалены; `network list` пуст и proxy store пуст.
- Финальная `go build ./...`, `go vet ./...`, `git diff --check` и `gofmt -d` — passed; unit tests не запускались. После сборки отдельный grouped direct запуск успешно подключился к `1.1.1.1:443`, затем временная сеть удалена и `network list` снова пуст.
- `make lint` после исправления 5 замечаний завершился с кодом `0`, `golangci-lint` сообщил `0 issues`. `govulncheck` не получил базу `vuln.go.dev` из-за недоступного proxy socket и остался неподтверждённым; GVM и golangci-lint также выдали предупреждения о read-only cache каталогах вне workspace.

## F09 DNS resolver follow-up: upstream failure response

- Пользователь сообщил, что после остановки второго контейнера первая sandbox сохраняет `MEMBERS=1`, но lookup его удалённого app-id `shell1` в режиме `egress` получал `REFUSED`: resolver пересылал отсутствующее однословное имя в policy resolver без разрешённого DNS upstream.
- Отсутствующие однословные имена (app-id/alias) теперь завершаются локальным NXDOMAIN во всех режимах. Остальные неизвестные имена сохраняют прежний upstream policy; ошибки manager/upstream возвращают `SERVFAIL` по UDP и TCP вместо молчаливого сброса.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — passed (exit code 0); Go вывел предупреждение о недоступном stat cache во внешнем read-only GVM cache. `gofmt -d` для изменённых Go-файлов и `git diff --check` — passed.
- Runtime reproduction и Go tests для этих DNS follow-up правок не запускались.
