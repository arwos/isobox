# F03 Контекст

- Поддерживаемая базовая конфигурация: `bwrap`, `pasta` или `slirp4netns`, включённые пользовательские namespace. isobox не устанавливает пакеты и не меняет sysctl автоматически.
- Proxy mode дополнительно зависит от доступа к `/dev/net/tun` и возможности создать TUN-интерфейс; kernel docs указывают, что создание/подключение интерфейса требует соответствующего `CAP_NET_ADMIN` в сетевом namespace.
- `pasta` и `slirp4netns` — разные rootless backend. Автовыбор: pasta, затем slirp только после проверки эквивалентности требуемых гарантий.
- Источники: [bubblewrap](https://github.com/containers/bubblewrap/blob/main/README.md), [pasta manual](https://passt.top/builds/latest/web/passt.1.html), [slirp4netns manual](https://github.com/rootless-containers/slirp4netns/blob/master/slirp4netns.1.md), [Linux TUN/TAP](https://kernel.org/doc/html/latest/networking/tuntap.html).
- Доступность TUN и cgroup/systemd-механизмов различается между дистрибутивами; запуск при отсутствии нужной возможности закрывается ошибкой с remediation.
- Bubblewrap требует user namespace при rootless запуске; опция `--unshare-user` делает требование явным, а `--unshare-net` проверяет доступность отдельного network namespace. Это подтверждено официальными [bwrap README](https://github.com/containers/bubblewrap/blob/main/README.md) и [bwrap man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml).
- Проверка user namespace запускает дочерний процесс isobox с `CLONE_NEWUSER` и отображением текущих uid/gid на namespace uid/gid 0. Она не меняет sysctl и не использует root fallback.
- `internal/preflight.Check` проверяет Linux, `bwrap --version`, минимальный реальный rootless bwrap с `/bin/true`, user namespace и наличие `pasta` или `slirp4netns`; найденные пути возвращаются вызывающему коду. Семантическая совместимость backend и выбор `pasta` относятся к F03-T02.
- Для `proxy` отдельный дочерний процесс создаёт user+network namespace, открывает `/dev/net/tun` и выполняет `TUNSETIFF` для временного TUN. Закрытие fd очищает интерфейс. Это проверяет одновременно device access и kernel permission в namespace, без изменения host network.
- Пока F07 не реализован, любое ненулевое CPU/memory/process/open-files ограничение fail-closed блокирует запуск с перечнем неподдержанных полей. Запрос ограничения не считается проверенным или применённым по одному лишь наличию sysctl/cgroup controller.
- Текущая среда: `bwrap`, `pasta`, `slirp4netns` находятся в `PATH`; `/dev/net/tun` отсутствует. Наличие бинарников не доказывает, что namespace-пробы пройдут на любой целевой системе.
