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
- Live acceptance от обычного пользователя без `sudo` выполнялась с доступом к user bus: `systemctl --user is-system-running` → `running`. Временная команда `/tmp/isobox-f09-t02 network create --name=f09-t02-verify-20261116 --subnet=192.0.2.1/29` создала сеть `192.0.2.0/29`, gateway `192.0.2.1`, bridge `ibb4aaa3f134d9`; `isobox network list` показал `active`, `MEMBERS=0`; `systemctl --user is-enabled ...` показал `enabled`; `systemctl --user restart ...` завершился успешно, после него `network list` снова показал `active`; `network remove` удалил сеть. После cleanup конфиг, unit и socket отсутствовали, `is-enabled` ответил `not-found`, `network list` показал только заголовок.
- Первый live start выявил недоступность `/proc/1/ns/user` в service окружении. Проверку worker изменили на собственный `/proc/self/uid_map`; после этого полный цикл create/start/list/restart/remove прошёл.
- Не проверялись: отказ удаления при реальном подключённом veth (подключение sandbox реализуется в F09-T03), restart самого user manager/boot recovery, запуск до login с lingering и длительная стабильность bridge. Эти acceptance checks остаются открытыми.
- `bash -n tests/f09-network-groups/rootless-bridge/run.sh` — passed.
- `bash tests/f09-network-groups/rootless-bridge/run.sh` — passed от обычного пользователя без `sudo` на Linux `6.12.116+deb13-amd64`, bubblewrap `0.12.0`; для выполнения внутри этой ограниченной agent-среды потребовалось разрешение на запуск временного network namespace/netlink прототипа. Скрипт не выполнялся от root и удалил созданные namespace-процессы и временные файлы по завершении.
- Результат прототипа: bridge и две veth-пары созданы в rootless user/network namespaces; участники получили `192.168.240.2/24` и `192.168.240.3/24`, разные netns inode и отдельные `lo`; взаимный TCP и UDP обмен прошёл через bubblewrap `--share-net`; loopback между участниками недоступен; у участников нет default route и host network не подключена; `CAP_NET_ADMIN` не позволяет управлять bridge/veth, вложенный user namespace заблокирован, `CapEff=0` при явном сбросе capabilities.
- Передача user namespace manager-а через `bwrap --userns FD` — не подходит для проверенной модели: bubblewrap завершился сообщением `Joining the specified user namespace failed, it might not be a descendant of the current user namespace`. Выбрана модель отдельного app user namespace с присоединением к уже настроенному network namespace через `--share-net`.
- Не запускались: весь Go unit suite, F05/F06 runtime-приёмка, интеграция режимов direct/proxy/egress и внешняя host/LAN connectivity acceptance. Для systemd сетей прошли create/list/service-restart/remove; restart всего user manager, lingering/boot recovery и отказ remove при реальном активном участнике остаются открытыми.
