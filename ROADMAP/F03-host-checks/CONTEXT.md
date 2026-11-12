# F03 Контекст

- Поддерживаемая базовая конфигурация: `bwrap`, `pasta` или `slirp4netns`, включённые пользовательские namespace. isobox не устанавливает пакеты и не меняет sysctl автоматически.
- Proxy mode дополнительно зависит от доступа к `/dev/net/tun` и возможности создать TUN-интерфейс; kernel docs указывают, что создание/подключение интерфейса требует соответствующего `CAP_NET_ADMIN` в сетевом namespace.
- `pasta` и `slirp4netns` — разные rootless backend. Автовыбор: pasta, затем slirp только после проверки эквивалентности требуемых гарантий.
- Источники: [bubblewrap](https://github.com/containers/bubblewrap/blob/main/README.md), [pasta manual](https://passt.top/builds/latest/web/passt.1.html), [slirp4netns manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md), [Linux TUN/TAP](https://kernel.org/doc/html/latest/networking/tuntap.html).
- Доступность TUN и cgroup/systemd-механизмов различается между дистрибутивами; запуск при отсутствии нужной возможности закрывается ошибкой с remediation.
