# Архитектура и продуктовый план isobox

Статус: roadmap согласован пользователем 2026-11-12; 2026-11-13 пользователь решил полностью отключить IPv6. Реализованы CLI-каркас, типы профиля, UNIC decode/валидация, host preflight, bwrap runtime, профильные mounts, изолированный HOME и ручные GUI grants. F04-T01 (base root/lifecycle) и F04-T02 (mounts/HOME) прошли пользовательскую runtime-приёмку на Linux Mint 7; в F04-T03 повторный GUI-grants runner подтвердил grants до cleanup, затем выявил ошибку unset `x11_socket`; исправление внесено, ожидается полный повторный прогон. F05 содержит режим `off` с fail-closed egress guard и опубликованными IPv4 портами через pasta, а также `direct` на IPv4-only pasta/slirp с DNS, forwarding и runtime fallback до запуска приложения. F06 содержит SOCKS5 TCP/UDP client, IPv4 TUN data plane, DNS fallback и guarded pasta ingress для объявленных host-портов; Podman fixture F06-T04 и TUN/прокси сценарий F06-T03 прошли пользовательскую live-приёмку. F07 содержит cgroup/rlimit backend с fail-closed startup gate; F07-T01 прошла live-приёмку на пользовательском systemd scope. F08 содержит пользовательские desktop launchers и layered XDG settings; live-приёмка F08-T01 прошла в пользовательской X11-сессии.

## Назначение и границы

`isobox` — модульное консольное приложение для rootless-запуска Linux-программ в ограниченной среде. Поддерживаются GUI-приложения и терминальные команды. Приложение запускается с хоста; профиль описывает sandbox, а команду и её аргументы передаёт вызывающий пользователь.

Целевая среда — Linux с включёнными пользовательскими namespace, `bwrap` и как минимум одним из `pasta` или `slirp4netns`. isobox не устанавливает пакеты и не меняет системные настройки автоматически: при нехватке условий сообщает, что именно нужно исправить. Системная интеграция необязательна.

## Согласованные контракты

### CLI и профиль

```text
isobox run --profile=/path/to/profile.unic [--app-id=ID] -- app [args...]
```

- Профиль формата UNIC не задаёт команду приложения.
- Типизированная схема профиля описывает mounts, сеть, GUI-разрешения и лимиты. Неизвестные или некорректные настройки не должны незаметно ослаблять изоляцию.
- Значения по умолчанию пользователь может переопределить в профиле. Режим сети по умолчанию — `off`; DNS выбирается из профиля, затем общих настроек isobox, а при отсутствии обоих — из первого IPv4 DNS resolver хоста. Если IPv4 resolver не найден, proxy-запуск блокируется. В proxy mode DNS-запросы приложения идут через SOCKS5 relay.
- Сеть isobox полностью IPv4-only: профильные DNS и host bind адреса принимают только IPv4; trusted bootstrap helper задаёт namespace-local `net.ipv6.conf.default/all.disable_ipv6=1`, проверяет отсутствие IPv6 адресов и рабочих IPv6 маршрутов и сбрасывает setup capabilities до приложения. При проверке маршрутов допускается только точная kernel null-discard запись (`RTF_REJECT | RTF_NONEXTHOP`), если она присутствует в `/proc/net/ipv6_route`; она отбрасывает пакеты и не ведёт наружу. Pasta также запускается с `--ipv4-only`, slirp4netns — без `--enable-ipv6`; proxy TUN отбрасывает unexpected IPv6 packets.
- Для proxy mode профиль ссылается на алиас SOCKS5. Секреты хранятся в отдельном пользовательском файле с правами `0600`, без шифрования на диске; пароль и адрес прокси доступны только сетевому компоненту. Если в профиле и общих настройках нет DNS, используется первый IPv4 nameserver из host `/etc/resolv.conf`; отсутствие такого resolver блокирует запуск.
- Для обычного SOCKS5 показывать предупреждение о незашифрованной аутентификации и передаче данных. Пользователь разрешил такой режим.

### Файлы и GUI

- Базовые системные каталоги для выполнения программы подключаются только для чтения. Нестандартные пути хоста доступны только через явные mounts профиля.
- Каталог `HOME` задаётся путём хоста в профиле. Если он не задан, isobox создаёт сохраняемый между запусками каталог под `/tmp`; в этом случае обязателен `--app-id`. ОС может очистить `/tmp`, поэтому это не гарантирует долговременное хранение.
- Доступ к Wayland/X11, звуку, GPU и интеграции с рабочим столом включается отдельно в каждом профиле. Полный доступ к D-Bus по умолчанию закрыт. Порталы не используются.

### Сеть и публикация портов

Режимы сети: `off`, `direct`, `proxy`.

- `off`: приложение не может инициировать запросы наружу, в LAN или на хост. Хост может подключаться к объявленным в профиле портам приложения; допустимы только ответы по этим соединениям.
- `direct`: rootless IPv4 сеть через `pasta --ipv4-only`; при его отсутствии или startup failure до открытия приложения допускается `slirp4netns` без `--enable-ipv6`, только если preflight подтверждает те же host-forward и DNS требования профиля. Если совместимость не подтверждена или fallback тоже не готов, запуск блокируется.
- `proxy`: собственный IPv4-only сетевой модуль isobox с SOCKS5 и прозрачным TUN-туннелем. Namespace IPv6 отключается до приложения; TCP передаётся через SOCKS5 `CONNECT`, UDP — через `UDP ASSOCIATE`; DNS и QUIC идут этим путём. Sandbox использует внутренний DNS endpoint в TUN, который host-side компонент перенаправляет на выбранный IPv4 resolver через SOCKS5, включая resolver на loopback. Unexpected IPv6 packets, ICMP и raw-сокеты блокируются. Прямого fallback нет. Если прокси или обязательные возможности недоступны, сеть не включается и приложение не запускается. При объявленных входящих портах pasta используется только для явного ingress forwarding; nftables и marked policy route разрешают через него только ответы по этим соединениям, а основной default route остаётся в SOCKS TUN.
- Для публикации входящего порта обязателен IPv4 адрес привязки на хосте. Правило может задавать TCP, UDP или оба протокола, один порт или диапазон. Автоматическая публикация не объявленных портов запрещена.
- Для `off` вместе с входящими портами реализация обязана доказать отсутствие исходящего обхода; если выбранный механизм не может это гарантировать, запуск блокируется.

### Лимиты и интеграция

Профиль может задавать CPU, память, число процессов и открытых файлов. CPU, память и процессы ограничиваются через cgroup v2 в пользовательском transient scope `systemd-run`; open files — через `RLIMIT_NOFILE`. Запуск остаётся заблокированным до проверки значений, а недоступный или ослабленный лимит останавливает запуск. `process_count` использует cgroup task limit и включает потоки; две служебные задачи bwrap резервируются сверх лимита приложения. При `off` с опубликованными портами временный setup allowance нужен Go/nft helper-ам; `systemctl` возвращает точный `process_count+2` до app gate. Для закреплённого сетевого helper thread `RLIMIT_NOFILE` применяется к host TID, который заменится приложением. Live-приёмка F07-T01 подтвердила значения контроллеров, запрет лишней задачи, open-file rlimit и cleanup scope.

Необязательная системная интеграция может предоставить desktop launchers и дополнительные системные значения по умолчанию. Эти значения пользователь может переопределять; они не считаются принудительной административной политикой. F08-T01 прошла live-приёмку: desktop entry запущен из графической сессии, а XDG overrides проверены через обычный CLI запуск.

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

## Порядок зависимостей

Все feature-группы имеют одинаковый приоритет. Порядок ниже отражает зависимости: foundation → profile → host checks → sandbox → network → SOCKS5 proxy; limits зависят от host checks и sandbox; system integration зависит от profile и sandbox.
