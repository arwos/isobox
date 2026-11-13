# F06 Контекст

- Runtime-протокол — только SOCKS5; sing-box используется исключительно как тестовый server в Podman.
- По решению пользователя IPv6 полностью отключён namespace-local sysctl ещё до запуска приложения; proxy TUN также создаётся без IPv6 адресов и маршрутов. IPv6 packets в обработчике отбрасываются как дополнительная проверка.
- Сетевой компонент isobox должен прозрачно перехватывать TCP и UDP. Обычное приложение не настраивает proxy и не получает credentials.
- SOCKS5 `CONNECT` и `UDP ASSOCIATE` описаны в [RFC 1928](https://www.rfc-editor.org/rfc/rfc1928.html). SOCKS5 не предоставляет IP-layer gateway для ICMP; сервер может молча отбросить неподдержанную UDP datagram.
- SOCKS5 username/password описан в [RFC 1929](https://www.rfc-editor.org/rfc/rfc1929); пароль передаётся открыто. Пользователь разрешил plain SOCKS5 с предупреждением. Сам SOCKS5 также не шифрует передаваемые данные.
- TUN читает/пишет IP packets через `/dev/net/tun`: [Linux kernel documentation](https://kernel.org/doc/html/latest/networking/tuntap.html). Наличие TUN и необходимые права проверяет F03.
- `slirp4netns --outbound-addr` не является proxy client; это binding outbound address/interface: [manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md).
- Точная Go-зависимость для TUN/IP stack не выбрана. Перед выбором проверить поддержку TCP+UDP/IPv4, fail-closed behavior, maintenance и лицензию; собственный SOCKS5 protocol client остаётся компонентом isobox.
- Официальный sing-box SOCKS inbound: https://sing-box.sagernet.org/configuration/inbound/socks/ . Он нужен для локальной проверки и не входит в runtime.

## F06-T01 — хранилище alias и credentials

- Команды управления: `isobox proxy add --alias=ALIAS --server=HOST:PORT [--auth=none|userpass]`, `isobox proxy list`, `isobox proxy remove --alias=ALIAS`. Credentials не принимаются в аргументах процесса; при `userpass` имя пользователя запрашивается с терминала, пароль вводится с отключённым echo. Прерывание сигнала восстанавливает настройки терминала.
- Файл хранится как `$XDG_CONFIG_HOME/isobox/proxies.unic`, либо `$HOME/.config/isobox/proxies.unic`, если `XDG_CONFIG_HOME` отсутствует или не абсолютен. Каталог `isobox` создаётся с точными правами `0700`, файл — `0600`, оба должны принадлежать текущему effective UID. Config-home должен принадлежать пользователю и не быть доступен группе/остальным на запись. Небезопасные права и symlink не исправляются автоматически: операция завершается ошибкой.
- Store использует версионированную UNIC-схему. Перед `unic.Unmarshal` проверяются неизвестные/повторные поля; ошибки декодирования не включают исходный текст, чтобы не вывести пароль. Записи сериализуются во временный файл, fsync-ятся и заменяются атомарно под directory lock. Ограничение размера — 1 MiB.
- Публичный API `internal/proxy` возвращает только alias, server и метод аутентификации. Чтение username/password находится в непубличном `loadRuntimeConfig` того же пакета для последующего SOCKS5 runtime; CLI `run` credentials не получает.
- Proxy-mode `run` проверяет alias и защищённость store до host preflight и печатает предупреждение, что SOCKS5 не шифрует credentials и трафик. Добавление proxy также выводит это предупреждение. Сам data plane F06-T02/T03 ещё не реализован; при текущей версии приложение не получает рабочую proxy-сеть.
- Хост proxy принимается как IPv4 или DNS hostname и TCP port; IPv6 literal отклоняется в соответствии с IPv4-only контрактом isobox. SOCKS5 user/password ограничены 1–255 байтами без NUL, как требуется RFC 1929.
