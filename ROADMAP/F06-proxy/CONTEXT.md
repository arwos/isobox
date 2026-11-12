# F06 Контекст

- Runtime-протокол — только SOCKS5; sing-box используется исключительно как тестовый server в Podman.
- Сетевой компонент isobox должен прозрачно перехватывать TCP и UDP. Обычное приложение не настраивает proxy и не получает credentials.
- SOCKS5 `CONNECT` и `UDP ASSOCIATE` описаны в [RFC 1928](https://www.rfc-editor.org/rfc/rfc1928.html). SOCKS5 не предоставляет IP-layer gateway для ICMP; сервер может молча отбросить неподдержанную UDP datagram.
- SOCKS5 username/password описан в [RFC 1929](https://www.rfc-editor.org/rfc/rfc1929); пароль передаётся открыто. Пользователь разрешил plain SOCKS5 с предупреждением. Сам SOCKS5 также не шифрует передаваемые данные.
- TUN читает/пишет IP packets через `/dev/net/tun`: [Linux kernel documentation](https://kernel.org/doc/html/latest/networking/tuntap.html). Наличие TUN и необходимые права проверяет F03.
- `slirp4netns --outbound-addr` не является proxy client; это binding outbound address/interface: [manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md).
- Точная Go-зависимость для TUN/IP stack не выбрана. Перед выбором проверить поддержку TCP+UDP/IPv6, fail-closed behavior, maintenance и лицензию; собственный SOCKS5 protocol client остаётся компонентом isobox.
- Официальный sing-box SOCKS inbound: https://sing-box.sagernet.org/configuration/inbound/socks/ . Он нужен для локальной проверки и не входит в runtime.
