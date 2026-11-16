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

## Решение F09-T01: rootless bridge

- Feasibility подтверждена прототипом `tests/f09-network-groups/rootless-bridge/run.sh` на Linux 6.12.116 и bubblewrap 0.12.0 от обычного пользователя без `sudo`.
- Долгоживущий manager должен создать собственные user и network namespaces и держать bridge внутри них. Он владеет bridge и создаёт отдельный network namespace для каждого участника как потомка своего user namespace; veth связывает namespace участника с bridge. Host network namespace к bridge не подключается.
- Запуск приложения: `nsenter -n` входит только в подготовленный network namespace участника, затем bubblewrap создаёт sandbox user namespace и использует `--share-net`. Передачу user namespace manager-а через `--userns FD` применять не следует: prototype bubblewrap отклонил присоединение с сообщением, что namespace не является потомком текущего. Отдельный app user namespace также сохраняет текущую модель UID/GID и позволяет запретить дальнейшее создание user namespace через `--disable-userns --assert-userns-disabled`.
- У app namespace не должно быть default route; только адрес своей группы и connected route. Поэтому manager topology сама по себе не даёт host/LAN/Internet egress. Режимы direct/proxy/egress подключаются позднее отдельными маршрутами и правилами F09-T05, без обхода их policy.
- Участник не получает полномочий owner user namespace: bridge и veth остаются под контролем manager-а. В прототипе bubblewrap дополнительно сбрасывает все известные на проверенном ядре capabilities; проверены `CapEff=0`, отказ `CAP_NET_ADMIN` и запрет вложенного user namespace. Перечень capability names при реализации следует формировать/проверять с учётом `cap_last_cap`, чтобы новые kernel capabilities не остались включёнными.
- Это доказывает feasibility на проверенных версиях, но не заменяет повторную runtime-приёмку при интеграции с network modes, IPv4 allocation и systemd lifecycle. Успех ограниченной tool sandbox-среды не гарантируется: runner нужно запускать на целевом Linux, где доступны rootless user/network namespaces и netlink bridge/veth операции.

## F09-T02: постоянный network manager

- `isobox network create --name=NAME --subnet=IPv4-CIDR`, `list` и `remove --name=NAME` управляют сетями; `create` требует обычного пользователя, доступный `unshare`, `XDG_RUNTIME_DIR` и отвечающий `systemd --user` до создания постоянного состояния.
- Конфигурация хранится как приватный JSON `~/.isobox/networks/<name>.json` (0600), каталоги — 0700. Имя ограничено ASCII буквами/цифрами/`_`/`-`. CIDR IPv4 канонизируется и должен быть от `/8` до `/29`; первый usable IP резервируется bridge gateway. Linux interface name детерминированный короткий hash сети, не зависит от пользовательского имени.
- Для сети устанавливается `isobox-network-<name>.service` в XDG user unit directory. Unit `Type=notify`, включается в `default.target`, запускается сразу и перезапускается после сбоя; manager сообщает `READY=1` только после создания bridge, назначения gateway и открытия control socket. Stop/disable удаляет socket, после чего net namespace и bridge исчезают. До входа пользователя user manager запускается только если настроен lingering; isobox не включает его.
- Status socket находится в проверенном приватном `$XDG_RUNTIME_DIR/isobox/`, имеет mode 0600 и предоставляет read-only сведения о bridge и числе присоединённых интерфейсов. `remove` отказывает при активных интерфейсах; если активный manager нельзя опросить или найден stale socket, удаление fail-closed. F09-T03 должен сохранить атомарность между этой проверкой и присоединением участника.
- Live-приёмка через пользовательский systemd прошла для create/enable/start, list, service restart и remove. `default.target` enablement подтверждён. Перезапуск всего user manager, boot recovery с lingering и отказ remove при реальном активном veth-участнике ещё не проверены; последнее будет доступно после F09-T03. До этих проверок F09-T02 остаётся `[~]`.

## Зависимости

- F04 предоставляет bwrap helper, startup gate и изоляцию sandbox.
- F05-T03 задаёт контракт IPv4 CIDR и адресации; его runtime-приёмка ещё открыта.
- F06-T05 задаёт egress policy для встроенного режима; его runtime-приёмка ещё открыта.
- F08-T02 уже предоставляет пользовательские systemd units и безопасные file operations, которые следует переиспользовать.

## Проверенные источники

- [Linux network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html): network namespaces изолируют сетевые устройства, маршруты, firewall и порты; `veth` связывает namespaces и может подключаться к bridge.
- [Linux user namespaces](https://man7.org/linux/man-pages/man7/user_namespaces.7.html): capabilities применяются к ресурсам namespaces, принадлежащих соответствующему user namespace; это определяет требования к владельцу общей сети.
- [Bubblewrap manual](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): `--userns FD` использует существующий user namespace при выполнении требований `setns`; проверить совместимость с текущим sandbox helper и запретом user namespace приложения.
- [passt/pasta overview](https://passt.top/about/?h=2026_05_26.038c51e): rootless L4 forwarding не требует host veth; соединение namespaces через veth требует `CAP_NET_ADMIN`, поэтому pasta per-run не является общей LAN.
- [systemd loginctl](https://freedesktop.org/software/systemd/man/latest/loginctl.html): lingering запускает user manager при boot и удерживает его после logout; isobox не включает lingering автоматически.
