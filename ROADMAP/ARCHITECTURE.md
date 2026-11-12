# Архитектура и продуктовый план isobox

Статус: roadmap согласован пользователем 2026-11-12. Реализованы CLI-каркас, типы профиля, UNIC decode/валидация, host preflight, bwrap runtime, профильные mounts, изолированный HOME и ручные GUI grants. F05-T01 содержит off-mode egress guard и pasta host forwarding; F05-T02 содержит direct runtime на pasta/slirp и profile DNS/forwarding. Runtime acceptance обеих задач ещё не выполнена, поэтому они остаются `[~]`. Proxy runtime, limits и desktop system integration ещё не реализованы.

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
- Значения по умолчанию пользователь может переопределить в профиле. Режим сети по умолчанию — `off`; профиль может переопределить общий DNS-сервер.
- Для proxy mode профиль ссылается на алиас SOCKS5. Секреты хранятся в отдельном пользовательском файле с правами `0600`, без шифрования на диске; пароль и адрес прокси доступны только сетевому компоненту.
- Для обычного SOCKS5 показывать предупреждение о незашифрованной аутентификации и передаче данных. Пользователь разрешил такой режим.

### Файлы и GUI

- Базовые системные каталоги для выполнения программы подключаются только для чтения. Нестандартные пути хоста доступны только через явные mounts профиля.
- Каталог `HOME` задаётся путём хоста в профиле. Если он не задан, isobox создаёт сохраняемый между запусками каталог под `/tmp`; в этом случае обязателен `--app-id`. ОС может очистить `/tmp`, поэтому это не гарантирует долговременное хранение.
- Доступ к Wayland/X11, звуку, GPU и интеграции с рабочим столом включается отдельно в каждом профиле. Полный доступ к D-Bus по умолчанию закрыт. Порталы не используются.

### Сеть и публикация портов

Режимы сети: `off`, `direct`, `proxy`.

- `off`: приложение не может инициировать запросы наружу, в LAN или на хост. Хост может подключаться к объявленным в профиле портам приложения; допустимы только ответы по этим соединениям.
- `direct`: rootless-сеть через `pasta`; при его отсутствии допускается `slirp4netns`, только если соблюдены те же требования профиля.
- `proxy`: собственный сетевой модуль isobox с SOCKS5 и прозрачным TUN-туннелем. TCP передаётся через SOCKS5 `CONNECT`, UDP — через `UDP ASSOCIATE`; DNS и QUIC идут этим путём. Прямого fallback нет. ICMP и raw-сокеты блокируются. Если прокси или обязательные возможности недоступны, сеть не включается и приложение не запускается.
- Для публикации входящего порта обязателен адрес привязки на хосте. Правило может задавать TCP, UDP или оба протокола, один порт или диапазон. Автоматическая публикация не объявленных портов запрещена.
- Для `off` вместе с входящими портами реализация обязана доказать отсутствие исходящего обхода; если выбранный механизм не может это гарантировать, запуск блокируется.

### Лимиты и интеграция

Профиль может задавать CPU, память, число процессов и открытых файлов. CPU, память и процессы ограничиваются через cgroup v2 в пользовательском transient scope `systemd-run`; open files — через `RLIMIT_NOFILE`. Запуск остаётся заблокированным до проверки значений, а недоступный или ослабленный лимит останавливает запуск. `process_count` использует cgroup task limit и включает потоки. Runtime-приёмка F07-T01 остаётся открытой.

Необязательная системная интеграция может предоставить desktop launchers и дополнительные системные значения по умолчанию. Эти значения пользователь может переопределять; они не считаются принудительной административной политикой.

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

`cmd/isobox/`, `internal/cli/`, `internal/profile/`, `internal/preflight/` и `internal/sandbox/` содержат CLI, схему документов, host capability checks и bwrap root. Для `off` с объявленными портами runtime удерживает приложение до pasta и устанавливает nftables default-drop guard. Для `direct` он выбирает pasta, допускает slirp при сохранении возможностей профиля, фиксирует DNS в read-only resolver file и открывает только объявленные host forwards. Runtime acceptance F05 остаётся открытой. Proxy/SOCKS5/TUN, limits и system integration остаются отдельными roadmap задачами.

## Зависимости и проверенные источники

- [Bubblewrap README](https://github.com/containers/bubblewrap/blob/main/README.md) и [man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): bwrap строит файловую и другие namespace-границы из аргументов вызывающей программы; сами аргументы определяют доступы.
- [slirp4netns manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md): rootless-сеть для network namespace; `--outbound-addr` выбирает IPv4/IPv6-адрес или интерфейс для исходящих сокетов, а не SOCKS/HTTP-прокси. DNS по умолчанию доступен как `10.0.2.3`; host forwarding через API IPv4-only, а не указанный `host_addr` по умолчанию равен `0.0.0.0`.
- [passt/pasta manual](https://passt.top/builds/latest/web/passt.1.html): для `pasta` host-to-namespace TCP/UDP forwarding по умолчанию `auto`; isobox должен явно задавать только объявленные правила. Входящий и обратный направления — отдельные настройки.
- [Linux TUN/TAP documentation](https://kernel.org/doc/html/latest/networking/tuntap.html): TUN доставляет IP-пакеты пользовательскому процессу; доступ к `/dev/net/tun` и возможность создать интерфейс нужно проверять отдельно.
- [RFC 1928](https://www.rfc-editor.org/rfc/rfc1928.html): SOCKS5 включает `CONNECT` и `UDP ASSOCIATE`, но не является IP-шлюзом для ICMP. SOCKS UDP relay может молча отбрасывать неподдерживаемые датаграммы, так что fail-closed означает отсутствие прямого обхода, а не гарантированную немедленную ошибку UDP-приложению.
- [RFC 1929](https://www.rfc-editor.org/rfc/rfc1929): SOCKS5 username/password передаёт пароль открыто.
- [sing-box SOCKS inbound](https://sing-box.sagernet.org/configuration/inbound/socks/): выбран только как тестовый SOCKS-сервер в Podman; не runtime-зависимость isobox.
- [systemd-run](https://freedesktop.org/software/systemd/man/latest/systemd-run.html), [systemd resource-control](https://freedesktop.org/software/systemd/man/latest/systemd.resource-control.html) и [systemd.exec](https://freedesktop.org/software/systemd/man/latest/systemd.exec): документируют пользовательские transient scopes, cgroup-лимиты CPU/памяти/tasks и rlimit-параметры, выбранные для F07-T01.
- [go-unic repository](https://github.com/osspkg/go-unic): используется `go.osspkg.com/unic v1.0.2`; проверка исходников показала, что decoder пропускает неизвестные поля структуры, поэтому isobox выполняет отдельную проверку полей по схеме до `unic.Unmarshal`.

## Порядок зависимостей

Все feature-группы имеют одинаковый приоритет. Порядок ниже отражает зависимости: foundation → profile → host checks → sandbox → network → SOCKS5 proxy; limits зависят от host checks и sandbox; system integration зависит от profile и sandbox.
