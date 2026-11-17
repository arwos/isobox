# F09 Контекст: именованные групповые сети

## Согласованные решения

- Пользователь явно создаёт/удаляет именованные сети; bridge постоянно активен даже без sandbox.
- Сеть обслуживает пользовательский manager под `systemd --user` и автоматически поднимается, когда стартует user manager. Для запуска до интерактивного входа нужен systemd lingering; isobox не должен включать его автоматически.
- Каждая sandbox сохраняет собственный network namespace и loopback. Она может подключаться к нескольким группам; у неё отдельный уникальный IPv4 адрес в каждой группе.
- В профиле `group` и `alias` повторяемые. `app-id` и все aliases публикуются в DNS каждой подключённой группы.
- Явное членство разрешает внутреннюю связь участников, включая `network.mode off`. Внешний трафик остаётся под политикой режима: direct, SOCKS5 proxy или egress allowlist; off не получает выхода из группы.
- Межгрупповая маршрутизация запрещена; пересекающиеся подсети у групп, подключённых к одному sandbox, отклоняются. Неоднозначность IP/имени или ошибка подключения должна завершать запуск fail-closed.
- Предложенный CLI: `isobox network create --name=NAME --subnet=IPv4-CIDR`, `isobox network list`, `isobox network remove --name=NAME`. Предлагаемая форма профиля:

```unic
network {
  mode off;
  group frontend;
  group backend;
  alias api;
  alias web;
}
```

## Ограничения и технический риск

- Реализация должна оставаться rootless и не добавлять root/setuid helper без отдельного согласования.
- Текущий sandbox создаёт user namespace на запуск и отдельный network namespace; direct использует per-container pasta/slirp, proxy/egress — per-container TUN. Это не создаёт общей L2-сети между запусками.
- Linux связывает полномочия сетевых операций с user namespace, владеющим network namespace. `veth` может связать network namespaces, но это требует правильного владельца и `CAP_NET_ADMIN`. Bubblewrap поддерживает `--userns FD`; применение к текущей схеме с запретом дальнейших namespace-операций и capabilities ещё нужно проверить.
- Успешная rootless feasibility проверка — prerequisite для реализации manager и подключения приложений. При невозможности сохранить текущие границы безопасности нельзя незаметно переходить к privileged backend.
- Статические адреса контейнерам пользователь пока не задавал; базовый вариант — выдаваемый manager-ом уникальный IPv4 lease из подсети каждой группы.
- Коллизия app-id/alias в пределах одной группы должна завершать попытку присоединения ошибкой; одно имя может повторяться в разных группах.
- Точное место хранения сетевых описаний внутри `~/.isobox` и детали unit naming остаются решением реализации. За основу брать проверки владельца, symlink и приватных прав из существующих F02/F08 integrations.
- Одна sandbox может одновременно состоять в нескольких группах, поэтому все group bridges и member network namespaces должны иметь общий владеющий rootless user namespace. Отдельный user namespace на каждый bridge несовместим с подключением одного member namespace к нескольким bridge; все bridge принадлежат общему пользовательскому service namespace, управляемому одним `isobox-network-manager.service`.

## Решение F09-T01: rootless bridge

- Feasibility подтверждена прототипом `tests/f09-network-groups/rootless-bridge/run.sh` на Linux 6.12.116 и bubblewrap 0.12.0 от обычного пользователя без `sudo`.
- Долгоживущий manager должен создать собственные user и network namespaces и держать bridge внутри них. Он владеет bridge и создаёт отдельный network namespace для каждого участника как потомка своего user namespace; veth связывает namespace участника с bridge. Host network namespace к bridge не подключается.
- Запуск приложения: `nsenter -n` входит только в подготовленный network namespace участника, затем bubblewrap создаёт sandbox user namespace и использует `--share-net`. Передачу user namespace manager-а через `--userns FD` применять не следует: prototype bubblewrap отклонил присоединение с сообщением, что namespace не является потомком текущего. Отдельный app user namespace также сохраняет текущую модель UID/GID и позволяет запретить дальнейшее создание user namespace через `--disable-userns --assert-userns-disabled`.
- До подключения внешнего backend-а manager topology не устанавливает default route и сама по себе не даёт host/LAN/Internet egress. F09-T05 добавляет default route для `direct` или policy TUN и сохраняет более специфичные connected routes групп.
- Участник не получает полномочий owner user namespace: bridge и veth остаются под контролем manager-а. В прототипе bubblewrap дополнительно сбрасывает все известные на проверенном ядре capabilities; проверены `CapEff=0`, отказ `CAP_NET_ADMIN` и запрет вложенного user namespace. Перечень capability names при реализации следует формировать/проверять с учётом `cap_last_cap`, чтобы новые kernel capabilities не остались включёнными.
- Это доказывает feasibility на проверенных версиях, но не заменяет повторную runtime-приёмку при интеграции с network modes, IPv4 allocation и systemd lifecycle. Успех ограниченной tool sandbox-среды не гарантируется: runner нужно запускать на целевом Linux, где доступны rootless user/network namespaces и netlink bridge/veth операции.

## F09-T02: постоянный network manager

- `isobox network create --name=NAME --subnet=IPv4-CIDR`, `list` и `remove --name=NAME` управляют сетями; `create` требует обычного пользователя, доступный `unshare`, `XDG_RUNTIME_DIR` и отвечающий `systemd --user` до создания постоянного состояния.
- Конфигурация хранится как приватный JSON `~/.isobox/networks/<name>.json` (0600), каталоги — 0700. Имя ограничено ASCII буквами/цифрами/`_`/`-`. CIDR IPv4 канонизируется и должен быть от `/8` до `/29`; первый usable IP резервируется bridge gateway. Linux interface name детерминированный короткий hash сети, не зависит от пользовательского имени.
- Устанавливается общий `isobox-network-manager.service` в XDG user unit directory. Unit `Type=notify`, включается в `default.target`, запускается сразу и перезапускается после сбоя; manager сообщает `READY=1` только после создания настроенных bridge, назначения gateway и открытия control sockets. Каждый bridge имеет отдельный приватный control socket. Последний `remove` останавливает/отключает unit; удаление одной сети при других настроенных сетях оставляет общий manager работающим. До входа пользователя user manager запускается только если настроен lingering; isobox не включает его.
- Status socket находится в проверенном приватном `$XDG_RUNTIME_DIR/isobox/`, имеет mode 0600 и предоставляет read-only сведения о bridge и числе присоединённых интерфейсов. `remove` отказывает при активных интерфейсах; если активный manager нельзя опросить или найден stale socket, удаление fail-closed. F09-T03 должен сохранить атомарность между этой проверкой и присоединением участника.
- Live-приёмка через пользовательский systemd прошла для нескольких `create`, list, service restart и remove. `default.target` enablement подтверждён. Перезапуск manager service восстановил сохранённые bridge. Удаление сети при реальном активном veth-участнике теперь проверено. Перезапуск всего user manager, boot recovery с lingering и завершение/восстановление при аварийной остановке остаются открытыми; F09-T02 пока `[~]`.

## F09-T03: несколько групп у sandbox

- Профиль принимает повторяемые `network.group` и `network.alias`; максимум 16 групп. Имя группы валидируется, дубликаты групп/aliases и некорректные DNS aliases отклоняются до запуска.
- Все bridge обслуживает общий manager, который выделяет участнику один IPv4 из каждой выбранной подсети и создаёт отдельный `veth` к bridge. Членское network namespace — потомок manager user namespace; `bwrap` запускается из него с `--share-net`, отдельными loopback и pid namespaces. В sandbox сохраняется исходный host UID/GID mapping, а capabilities сбрасываются существующим sandbox runner.
- F09-T03 первоначально поддерживала grouped профили только с `network.mode off`; F09-T05 добавляет `direct`, `proxy` и `egress`. Групповые профили пока не поддерживают опубликованные порты.
- Адреса и интерфейсы освобождаются при нормальном выходе или ошибке запуска; удаление сети проверяет bridge ports под тем же manager lock, что сериализует attach/detach. Manager restart завершает unit cgroup вместе с участниками; автоматическое восстановление активных участников не выполняется.

## F09-T04: DNS участников и aliases

- При grouped запуске обязателен DNS-совместимый `--app-id`; он и все повторяемые `network.alias` публикуются во всех назначенных группах.
- Каждый member namespace получает loopback DNS listener и `resolv.conf` с `127.0.0.1`. Listener запрашивает shared manager по приватному Unix socket; manager подтверждает peer PID/network namespace, затем возвращает только записи участников с хотя бы одной общей group.
- A-запросы возвращают адрес участника в каждой общей группе; известные имена на AAAA получают пустой ответ (IPv4-only). Неизвестные однословные имена, включая завершившие работу app-id и alias участников, получают локальный NXDOMAIN во всех режимах. Другие неизвестные имена при `off` получают NXDOMAIN, а при `direct`, `proxy` и `egress` передаются DNS upstream-ам соответствующего backend/policy.
- Если запрос к manager или внешний DNS upstream завершается ошибкой, loopback resolver возвращает DNS `SERVFAIL` по UDP и TCP вместо молчаливого сброса запроса и клиентского timeout.
- Нормализованные имена уникальны внутри каждой группы; конфликт app-id/alias отклоняет новый launch до запуска приложения. Одинаковое имя допустимо в непересекающихся группах. Удаление member state при выходе автоматически убирает его DNS-записи.

## F09-T05: маршруты групп и внешняя policy

- Групповой sandbox сохраняет все connected routes, выданные manager-ом. В `direct` pasta/slirp добавляет внешний маршрут; в `proxy` и `egress` создаётся policy TUN с default route. Connected route группы специфичнее default route, поэтому адреса участников идут через veth и не попадают во внешний policy TUN.
- Для grouped `direct` inner CLI передаёт manager-owned namespace-holder PID внешнему launcher-у. Внешний launcher остаётся в host network namespace и запускает pasta/slirp до продолжения sandbox запуска; запуск helper-а из inner CLI ошибочен, потому что тогда его исходящие sockets наследуют изолированную member network namespace и теряют host egress. Внешний helper настраивает только свою direct link, а более специфичные connected routes групп остаются через veth.
- В grouped `proxy`/`egress` TUN создаётся доверенной частью isobox в manager-owned member namespace до запуска bwrap. Его descriptor по SCM_RIGHTS передаётся исходному CLI-процессу на хосте; там запускается policy data plane, чтобы разрешённые dial-ы и host loopback использовали host network namespace, а не возвращались в member TUN. Manager ждёт подтверждения готовности policy до продолжения запуска. После этого bwrap присоединяется к уже настроенному namespace через `--share-net`; приложение не получает сетевые capabilities. Выбранная TUN `/24` не пересекается с подсетями подключённых групп.
- Loopback DNS сначала проверяет динамические записи общих групп. Отсутствующие однословные имена завершаются локальным NXDOMAIN; прочие неизвестные имена при `off` тоже получают NXDOMAIN, а для `direct` resolver использует адреса из выбранной resolver-конфигурации, для `proxy`/`egress` запрос отправляется в policy resolver endpoint внутри TUN. DNS и обычный внешний трафик остаются под политикой выбранного режима.
- Групповые published ports остаются отклонёнными до отдельного решения по их совместной адресации и ingress policy.
- Сборка и частичная live acceptance прошли; результаты и ограничения перечислены в `QA.md`. Grouped `direct` external IPv4 egress и TCP/UDP обмен между `direct` и `off` участниками подтверждены после запуска backend из host namespace. Позитивный grouped `proxy` TCP-путь подтверждён локальным SOCKS5 test endpoint. Proxy UDP application traffic и host/LAN isolation ещё не подтверждены.

## Зависимости

- F04 предоставляет bwrap helper, startup gate и изоляцию sandbox.
- F05-T03 задаёт контракт IPv4 CIDR и адресации; live acceptance закрыта в commit `f0541e8`.
- F06-T05 задаёт egress policy для встроенного режима; live acceptance и host-gateway allowlist fix закрыты в commit `1e99530`.
- F08-T02 предоставляет проверенные шаблоны пользовательских systemd units и безопасные file operations.

## Проверенные источники

- [Linux network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html): network namespaces изолируют сетевые устройства, маршруты, firewall и порты; `veth` связывает namespaces и может подключаться к bridge.
- [Linux user namespaces](https://man7.org/linux/man-pages/man7/user_namespaces.7.html): capabilities применяются к ресурсам namespaces, принадлежащих соответствующему user namespace; это определяет требования к владельцу общей сети.
- [Bubblewrap manual](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): `--userns FD` использует существующий user namespace при выполнении требований `setns`; проверить совместимость с текущим sandbox helper и запретом user namespace приложения.
- [passt/pasta overview](https://passt.top/about/?h=2026_05_26.038c51e): rootless L4 forwarding не требует host veth; соединение namespaces через veth требует `CAP_NET_ADMIN`, поэтому pasta per-run не является общей LAN.
- [systemd loginctl](https://freedesktop.org/software/systemd/man/latest/loginctl.html): lingering запускает user manager при boot и удерживает его после logout; isobox не включает lingering автоматически.
