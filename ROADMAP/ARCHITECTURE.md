# Архитектура и продуктовый план isobox

Статус: roadmap согласован пользователем 2026-11-12; 2026-11-13 пользователь решил полностью отключить IPv6. Реализованы CLI-каркас, типы профиля, UNIC decode/валидация, host preflight, bwrap runtime, профильные mounts, изолированный HOME и ручные GUI grants; F04-T01/T02/T03 прошли пользовательскую runtime-приёмку на Linux Mint 7. F05 содержит режим `off` с fail-closed egress guard и опубликованными IPv4 портами через pasta, а также `direct` на IPv4-only pasta/slirp с DNS, forwarding и runtime fallback до запуска приложения. F06 содержит SOCKS5 TCP/UDP client, IPv4 TUN data plane, DNS fallback и guarded pasta ingress для объявленных host-портов; F06-T05 добавляет встроенный allowlist egress, resolver и журнал событий, но runtime-приёмка F06-T05 ещё открыта. Podman fixture F06-T04 и TUN/прокси сценарий F06-T03 прошли пользовательскую live-приёмку. F07 содержит cgroup/rlimit backend с fail-closed startup gate; F07-T01 прошла live-приёмку на пользовательском systemd scope. F08 содержит пользовательские desktop launchers; прежние layered XDG settings удалены решением F02-T03.

F09 — согласованный план именованных rootless-сетей с несколькими подключениями и DNS aliases. F09-T01 подтвердил bridge/veth модель rootless на Linux 6.12.116 и bubblewrap 0.12.0; manager namespace остаётся владельцем сети, приложения присоединяются к подготовленному network namespace через `--share-net`. Для F09-T02 добавлены CLI и systemd user manager для постоянного bridge; live create/list/service-restart/remove прошли. Boot recovery, lingering и удаление сети с реально подключённым участником ещё требуют runtime-проверки.

## Назначение и границы

`isobox` — модульное консольное приложение для rootless-запуска Linux-программ в ограниченной среде. Поддерживаются GUI-приложения и терминальные команды. Профиль описывает sandbox и может задавать команду приложения по умолчанию; вызывающий пользователь может заменить её, передав команду и аргументы после `--`.

Целевая среда — Linux с включёнными пользовательскими namespace, `bwrap` и как минимум одним из `pasta` или `slirp4netns`. isobox не устанавливает пакеты и не меняет системные настройки автоматически: при нехватке условий сообщает, что именно нужно исправить. Системная интеграция необязательна.

## Согласованные контракты

### CLI и профиль

```text
isobox run [--profile=/path/to/profile.unic] [--app-id=ID] [-- app [args...]]
isobox config [--profile=/path/to/profile.unic] [--app-id=ID] [-- app [args...]]
isobox ps
```

- Профиль формата UNIC может задавать команду приложения и аргументы по умолчанию как argv-массив `command`. Аргументы после `--` полностью заменяют профильную команду. Если ни одна команда не задана, `run` и `config` завершаются с ошибкой; shell автоматически не запускается.
- Без `--profile` при заданном `--app-id` сначала ищется `~/.isobox/config/<app-id>.unic`; если такой файл отсутствует или app-id не задан, используется `~/.isobox/config/default.unic`, который при отсутствии создаётся с безопасной сетью `off`. App-specific профиль заменяет default целиком. Системные и пользовательские settings слои не подмешиваются. Явный `--profile` используется самостоятельно.
- `config` принимает те же флаги и команду, что `run`, выполняет валидацию/preflight и выводит табличный план запуска, включая все argv bubblewrap; приложение и sandbox supervisor не запускаются.
- `ps` перечисляет активные запуски, зарегистрированные isobox в пользовательском runtime state; PID, рабочая папка и app-id связываются при открытии bwrap gate. Память/CPU читаются из `/proc`; интернет-колонка отражает сетевую политику профиля.
- Типизированная схема профиля описывает mounts, сеть, GUI-разрешения и лимиты. Неизвестные или некорректные настройки не должны незаметно ослаблять изоляцию.
- Профиль может дополнять `PATH` списком абсолютных каталогов `path`; элементы располагаются перед встроенным sandbox `PATH`, host `PATH` не наследуется.
- Профиль может задавать `user <uid> <name>;` и повторяемые `group <gid> <name>;`. Guest UID и GID первой группы отображаются на эффективные UID/GID пользователя, запустившего isobox; остальные группы присутствуют только в приватном `/etc/group`, не назначаются supplementary credentials процесса и не дают доступ к host-ресурсам по GID. При отсутствии настроек сохраняются текущие host UID/GID и имя `isobox`.
- UTS hostname задаётся при каждом запуске: `isobox-<app-id>`, а без app-id — `isobox`; длинный suffix обрезается так, чтобы имя не превышало 64 байта Linux. Приватный `/etc/hosts` сопоставляет `localhost` и sandbox hostname с `127.0.0.1`; только в `egress` там также есть `host.gateway.internal` → выбранный виртуальный gateway (по умолчанию `10.89.0.3`).
- Режим сети по умолчанию — `off`; `network.dns` задаёт упорядоченный IPv4 список upstream-ов. В `proxy` при пустом списке берётся первый IPv4 resolver хоста; если IPv4 resolver не найден, proxy-запуск блокируется. В `egress` host DNS автоматически не используется.
- Сеть isobox полностью IPv4-only: профильные DNS и host bind адреса принимают только IPv4; trusted bootstrap helper задаёт namespace-local `net.ipv6.conf.default/all.disable_ipv6=1`, проверяет отсутствие IPv6 адресов и рабочих IPv6 маршрутов и сбрасывает setup capabilities до приложения. При проверке маршрутов допускается только точная kernel null-discard запись (`RTF_REJECT | RTF_NONEXTHOP`), если она присутствует в `/proc/net/ipv6_route`; она отбрасывает пакеты и не ведёт наружу. Pasta также запускается с `--ipv4-only`, slirp4netns — без `--enable-ipv6`; proxy TUN отбрасывает unexpected IPv6 packets.
- Для proxy mode профиль ссылается на алиас SOCKS5. Секреты хранятся в отдельном пользовательском файле с правами `0600`, без шифрования на диске; пароль и адрес прокси доступны только сетевому компоненту. Если в профиле нет DNS, используется первый IPv4 nameserver из host `/etc/resolv.conf`; отсутствие такого resolver блокирует запуск.
- Для обычного SOCKS5 показывать предупреждение о незашифрованной аутентификации и передаче данных. Пользователь разрешил такой режим.

### Файлы и GUI

- Базовые системные каталоги для выполнения программы подключаются только для чтения. Нестандартные пути хоста доступны только через явные mounts профиля.
- Каталог `HOME` задаётся путём хоста в профиле. Если он не задан, isobox создаёт сохраняемый между запусками `~/.isobox/home/<app-id>/`; в этом случае обязателен `--app-id`. Один app-id использует один managed HOME вне зависимости от пути профиля.
- Управляемые профили хранятся в `~/.isobox/config/`, egress журналы — в `~/.isobox/log/`. Эти пути не зависят от `XDG_CONFIG_HOME`; proxy alias store и системные интеграции продолжают использовать свои стандартные пользовательские каталоги.
- Доступ к Wayland/X11, звуку, GPU и интеграции с рабочим столом включается отдельно в каждом профиле. Полный доступ к D-Bus по умолчанию закрыт. Порталы не используются.

### Сеть и публикация портов

Режимы сети: `off`, `direct`, `proxy`, `egress`.

- Профиль может задавать несколько `network.dns` в порядке приоритета; profile-список заменяет одно значение `settings.defaults.dns`. `egress` перебирает upstream DNS последовательно и допускает только адреса, совпавшие с allowlist на TCP/UDP port 53.
- `off`: приложение не может инициировать запросы наружу, в LAN или на хост, кроме трафика к участникам явно подключённых групповых сетей F09. Хост может подключаться к объявленным в профиле портам приложения; допустимы только ответы по этим соединениям.
- `direct`: rootless IPv4 сеть через `pasta --ipv4-only`; при его отсутствии или startup failure до открытия приложения допускается `slirp4netns` без `--enable-ipv6`, только если preflight подтверждает те же host-forward и DNS требования профиля. Если совместимость не подтверждена или fallback тоже не готов, запуск блокируется.
- `proxy`: текущая реализация требует alias и использует IPv4-only SOCKS5 client с прозрачным TUN-туннелем. Namespace IPv6 отключается до приложения; TCP передаётся через SOCKS5 `CONNECT`, UDP — через `UDP ASSOCIATE`; DNS и QUIC идут этим путём. Unexpected IPv6 packets, ICMP и raw-сокеты блокируются; отказ SOCKS не переключается на direct.
- `egress`: отдельный встроенный policy режим на том же IPv4-only TUN. Host-side TCP/UDP dial разрешается только явными IPv4/CIDR правилами; одно правило может включать несколько адресов через повторяемое поле `ip` или его UNIC-список. Доступ ограничивается опциональным списком портов, включительными диапазонами и протоколом; пустой allowlist блокирует egress. Статические DNS A-записи задаются повторяемыми блоками `record <domain>`, каждый блок принимает несколько `ip`; они отвечаются локально, неизвестные имена идут только на явно заданный upstream и только при разрешающем правиле для upstream IP:53. Host loopback доступен через виртуальный endpoint `10.89.0.3`, который внутри egress sandbox также записывается как `host.gateway.internal` в `/etc/hosts`; для соединения требуется явное правило на целевой loopback IP/порт. `proxy` остаётся отдельным внешним SOCKS5 режимом и всегда требует `proxy_alias`; его потоки можно дополнительно фильтровать теми же allow rules.
- Для `direct`, `proxy` и `egress` профиль может задать positional имя интерфейса и IPv4 CIDR через `network <interface> <CIDR> { ... }`; оба атрибута допускаются отдельно. Случайный IPv4 адрес выбирается из host-части CIDR, а direct pasta также получает случайный отличный gateway. В proxy/egress guest, DNS и host-gateway endpoints выбираются раздельно в заданном CIDR; без CIDR сохраняются `10.89.0.1/24`, `10.89.0.2` и `10.89.0.3`. Пользовательский direct CIDR требует pasta с поддержкой `--ns-ifname`, `--address`, `--netmask`, `--gateway`; slirp не применяется, поскольку назначает фиксированный гостевой адрес. Без атрибутов сохраняются прежние backend defaults.
- При объявленных proxy ingress ports pasta используется только для явного ingress forwarding; nftables и marked policy route разрешают через него только ответы по этим соединениям, а основной default route остаётся в SOCKS TUN.
- Для публикации входящего порта профиль использует повторяемые записи `port <IPv4 host address> <port> [protocol [end_port]];`. Правило может задавать TCP, UDP или оба протокола, один порт или диапазон. Автоматическая публикация не объявленных портов запрещена.
- Для `off` вместе с входящими портами реализация обязана доказать отсутствие исходящего обхода; если выбранный механизм не может это гарантировать, запуск блокируется.

### Именованные сети F09

- Пользователь явно создаёт и удаляет именованную сеть. Предлагаемый CLI: `isobox network create --name=NAME --subnet=IPv4-CIDR`, `isobox network list`, `isobox network remove --name=NAME`; детали хранения уточняются в реализации.
- Сеть держит отдельный user-level manager/bridge, запущенный и включённый через `systemd --user`, даже когда в ней нет контейнеров. Удаление сети с активными участниками завершается отказом. Для старта user manager до интерактивного входа systemd требует lingering; isobox не включает его автоматически.
- Профиль может повторять `group NAME` и `alias NAME`. Каждый контейнер сохраняет собственный network namespace и loopback, получает отдельный уникальный IPv4 адрес в каждой подключённой группе, а его `app-id` и все aliases видны в DNS каждой такой группы. Повторные группы/aliases и адреса обрабатываются без коллизий; пересекающиеся подсети у одновременно подключённых групп отклоняются.
- Явное членство разрешает обмен трафиком внутри подключённых групп, в том числе при `network.mode off`; при `off` за пределы этих групп исходящий трафик остаётся закрытым. При `direct`, `proxy` и `egress` внешние соединения сохраняют свою текущую политику. Межгрупповая маршрутизация и обход host/LAN правил запрещены.
- Rootless bridge/veth подтверждён прототипом от обычного пользователя: отдельный manager user/network namespace владеет bridge, а sandbox получает отдельный network namespace и veth. Проверены IPv4 TCP/UDP между двумя sandbox, изоляция loopback, отсутствие default route, запрет app namespace операций и сброс capabilities. Доказательство ограничено Linux 6.12.116 и bubblewrap 0.12.0; повторить runtime-проверки при интеграции F09-T02—T05. Не добавлять root/setuid helper как неявный fallback.

### Лимиты и интеграция

Профиль может задавать CPU, память, число процессов и открытых файлов. CPU, память и процессы ограничиваются через cgroup v2 в пользовательском transient scope `systemd-run`; open files — через `RLIMIT_NOFILE`. Запуск остаётся заблокированным до проверки значений, а недоступный или ослабленный лимит останавливает запуск. `process_count` использует cgroup task limit и включает потоки; две служебные задачи bwrap резервируются сверх лимита приложения. При `off` с опубликованными портами временный setup allowance нужен Go/nft helper-ам; `systemctl` возвращает точный `process_count+2` до app gate. Для закреплённого сетевого helper thread `RLIMIT_NOFILE` применяется к host TID, который заменится приложением. Live-приёмка F07-T01 подтвердила значения контроллеров, запрет лишней задачи, open-file rlimit и cleanup scope.

Необязательная системная интеграция предоставляет пользовательские desktop launchers и systemd user service units. F08-T01 прошла live-приёмку: desktop entry запущен из графической сессии. Systemd unit запускает `isobox run --profile=... --app-id=...`, поэтому профиль обязан содержать `command`; установка создаёт и перезагружает unit, но не включает и не запускает его. `integration systemd enable/disable` переключают автозапуск в пользовательском `default.target`, не запуская и не останавливая уже работающий сервис.

## Предлагаемая структура Go-кода

```text
cmd/isobox/             точка входа CLI
internal/cli/           разбор команды run и обработка ошибок
internal/profile/       типы UNIC, defaults и валидация
internal/preflight/     проверка ОС, зависимостей и возможностей
internal/sandbox/       сборка и запуск bubblewrap
internal/network/       режимы off/direct и публикация портов
internal/proxy/         SOCKS5, TUN-модуль, алиасы и секреты
internal/limits/        применение лимитов
internal/integration/   необязательные desktop/system defaults
```

`cmd/isobox/`, `internal/cli/`, `internal/profile/`, `internal/preflight/`, `internal/sandbox/`, `internal/limits/` и `internal/integration/` содержат CLI, схемы документов, host capability checks, bwrap root, профильные limits и optional XDG integration. Для `off`, `direct` и `proxy` доверенный helper создаёт network namespace после bwrap setup и до запуска приложения, выключает IPv6 через namespace-local sysctl и сбрасывает временные capabilities. Для `off` helper также устанавливает nftables default-drop guard; proxy создаёт TUN, а при опубликованных портах подготавливает guarded pasta ingress. Supervisor подключает pasta в IPv4-only режиме или совместимый slirp без `--enable-ipv6` и выпускает helper после readiness. Direct фиксирует только IPv4 DNS в read-only resolver file и открывает только объявленные IPv4 host forwards. Proxy/SOCKS5/TUN реализуется в F06.

## Зависимости и проверенные источники

- [Bubblewrap README](https://github.com/containers/bubblewrap/blob/main/README.md) и [man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): bwrap строит файловую и другие namespace-границы из аргументов вызывающей программы; сами аргументы определяют доступы.
- [slirp4netns manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md): rootless-сеть для network namespace; `--outbound-addr` выбирает IPv4/IPv6-адрес или интерфейс для исходящих сокетов, а не SOCKS/HTTP-прокси. DNS по умолчанию доступен как `10.0.2.3`; host forwarding через API IPv4-only, а не указанный `host_addr` по умолчанию равен `0.0.0.0`.
- [passt/pasta manual](https://passt.top/builds/latest/web/passt.1.html): для `pasta` host-to-namespace TCP/UDP forwarding по умолчанию `auto`; isobox должен явно задавать только объявленные правила. Входящий и обратный направления — отдельные настройки.
- [Linux networking sysctl documentation](https://github.com/torvalds/linux/blob/master/Documentation/networking/ip-sysctl.rst): `conf/all/disable_ipv6` applies to all interfaces and default settings; disabling IPv6 removes configured addresses/routes, while `conf/default/disable_ipv6` applies to newly created interfaces.
- [Linux TUN/TAP documentation](https://kernel.org/doc/html/latest/networking/tuntap.html): TUN доставляет IP-пакеты пользовательскому процессу; доступ к `/dev/net/tun` и возможность создать интерфейс нужно проверять отдельно.
- [RFC 1928](https://www.rfc-editor.org/rfc/rfc1928.html): SOCKS5 включает `CONNECT` и `UDP ASSOCIATE`, но не является IP-шлюзом для ICMP. SOCKS UDP relay может молча отбрасывать неподдерживаемые датаграммы, так что fail-closed означает отсутствие прямого обхода, а не гарантированную немедленную ошибку UDP-приложению.
- [RFC 1929](https://www.rfc-editor.org/rfc/rfc1929): SOCKS5 username/password передаёт пароль открыто.
- [sing-box SOCKS inbound](https://sing-box.sagernet.org/configuration/inbound/socks/): выбран только как тестовый SOCKS-сервер в Podman; не runtime-зависимость isobox.
- [systemd-run](https://freedesktop.org/software/systemd/man/latest/systemd-run.html), [systemd resource-control](https://freedesktop.org/software/systemd/man/latest/systemd.resource-control.html) и [systemd.exec](https://freedesktop.org/software/systemd/man/latest/systemd.exec): документируют пользовательские transient scopes, cgroup-лимиты CPU/памяти/tasks и rlimit-параметры, выбранные для F07-T01.
- [go-unic repository](https://github.com/osspkg/go-unic): используется `go.osspkg.com/unic v1.0.2`; проверка исходников показала, что decoder пропускает неизвестные поля структуры, поэтому isobox выполняет отдельную проверку полей по схеме до `unic.Unmarshal`.
- [Linux network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html) и [user namespaces](https://man7.org/linux/man-pages/man7/user_namespaces.7.html): network namespaces изолируют устройства, маршруты, firewall и порты; сетевые операции зависят от user namespace-владельца.
- [Bubblewrap manual](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): поддерживает `--userns FD` для использования существующего user namespace; совместимость этого режима с текущими bwrap helper/security checks isobox требует feasibility proof.
- [passt/pasta overview](https://passt.top/about/?h=2026_05_26.038c51e): rootless L4 forwarding не требует создания host veth, тогда как прямое соединение network namespaces через veth требует `CAP_NET_ADMIN`; не считать текущий per-container pasta механизм общей L2-сетью.
- [systemd loginctl](https://freedesktop.org/software/systemd/man/latest/loginctl.html): `loginctl enable-linger` запускает user manager при загрузке и сохраняет его после logout. F09 не включает lingering без отдельного действия пользователя.

## Порядок зависимостей

Все feature-группы имеют одинаковый приоритет. Порядок ниже отражает зависимости: foundation → profile → host checks → sandbox → network → SOCKS5 proxy; limits зависят от host checks и sandbox; system integration зависит от profile и sandbox. F09 зависит от sandbox F04, адресного контракта F05-T03, egress-политики F06-T05 и пользовательских systemd units F08-T02. Feasibility spike F09-T01 можно выполнять после F04 независимо от незакрытой runtime-приёмки F05-T03/F06-T05; подключение всех сетевых режимов начинается только после закрытия этих зависимостей.
