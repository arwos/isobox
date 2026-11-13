# F06 — SOCKS5 proxy tunnel

## Зависимости

- Требует: F02, F03, F04, F05.
- Использует port forwarding из F05, если он задан профилем.

## Задачи

- [x] F06-T01 — Хранить proxy aliases и секреты для сетевого компонента.
  - Шаги: реализовать пользовательское хранилище с правами `0600`; добавить управление alias; принимать credentials без попадания в логи и аргументы процесса; выдавать секреты только proxy component.
  - Приёмка: профиль ссылается на alias; другой пользователь не читает файл; повреждённые права/файл блокируют proxy mode; при использовании plain SOCKS5 отображается предупреждение о незашифрованном канале.
- [x] F06-T02 — Реализовать Go SOCKS5 client для TCP и UDP.
  - Шаги: поддержать no-auth и username/password, `CONNECT`, `UDP ASSOCIATE`, адреса IPv4/domain; IPv6 address type отклоняется, ошибки handshake/relay и lifecycle ассоциации обрабатываются fail-closed.
  - Приёмка: клиент открывает control connection только к SOCKS server по IPv4; TCP `CONNECT` и UDP datagrams отправляются только в SOCKS tunnel/relay; IPv6 SOCKS addresses отклоняются; отказ или разрыв прокси не приводит к direct fallback. Пакетная фильтрация IPv6 TUN, ICMP и raw traffic проверяется в F06-T03 вместе с TUN data plane.
- [~] F06-T03 — Добавить прозрачный TUN-путь для приложений.
  - Шаги: получить IPv4 packets от sandbox через TUN; namespace-local IPv6 отключается до запуска приложения, а unexpected IPv6 packets отбрасываются; преобразовывать TCP/UDP flows в SOCKS5; направлять DNS к profile/global IPv4 DNS, а если оба не заданы — к первому IPv4 DNS хоста, всегда по тому же SOCKS relay; исключить DNS и data-plane обходы; обеспечить cleanup процессов и FD.
  - Приёмка: приложение не настраивает proxy env и не знает alias/credentials; sandbox не имеет IPv6 адресов или рабочих IPv6 маршрутов (допускается только kernel null-discard запись); при TUN/SOCKS UDP недоступности приложение не стартует; после отказа активной сессии трафик не переключается напрямую.
  - Реализованы TUN/IPv4/TCP/UDP data plane и startup gate. Для опубликованных host-портов добавлен ingress-only pasta путь с nftables egress guard и отдельным marked return route; если pasta, nftables, интерфейс или маршруты не проходят проверки, запуск остаётся закрыт. Proxy mode создаёт network namespace из доверенного helper после bwrap setup, с временными `CAP_SYS_ADMIN` и `CAP_NET_ADMIN`; helper сразу сбрасывает `CAP_SYS_ADMIN`, применяет IPv6/TUN setup и до `exec` приложения устанавливает seccomp запрет namespace-операций. Отчёт `E4RvCn` выявил две kernel null-discard записи в `/proc/net/ipv6_route`; helper теперь принимает только их точную форму, а любые рабочие маршруты по-прежнему блокируют запуск. Отчёт `VKFIqY` подтвердил прохождение IPv6-проверки и выявил отсутствие явного `Dst` у TUN default route; default route теперь передаётся как `0.0.0.0/0` и проверяется через TUN link. Отчёт `UuQLZa` подтвердил эти шаги и выявил CIDR в аргументе pasta `--address`; он заменён отдельными `--address=10.89.1.2` и `--netmask=24`, а host preflight требует поддержку обоих аргументов. Отчёт `q7mSQd` показал, что pasta не может создать default route, пока TUN route уже занимает main table; при proxy ingress TUN default route теперь добавляется после pasta readiness и до release startup gate. Отчёт `5Pmywj` подтвердил pasta readiness и выявил ошибку сравнения host address с network address при проверке `isoboxp0`; проверка теперь отдельно сверяет IP и netmask. Отчёт `Fonl5A` показал, что запись connected/default route в policy table доходит до проверки, но default не распознавался в нормализованной форме; helper теперь задаёт явный IPv4 zero-prefix и принимает обе формы default route, а отказ включает фактический список маршрутов. До нового runtime-прогона startup gate release и SOCKS data plane не подтверждены; приёмка остаётся незавершённой.
- [x] F06-T04 — Подготовить Podman fixture для тестового sing-box SOCKS5 server.
  - Шаги: локальная конфигурация тестового server для TCP и UDP и тестовых credentials; не добавлять sing-box в runtime зависимости или production installer.
  - Приёмка: fixture запускается локально в Podman и позволяет проверить CONNECT, UDP ASSOCIATE, auth failure и остановку сервера. Пользовательская runtime-проверка прошла; отчёт `/tmp/isobox-test-results/f06-sing-box.SNry0x/result.txt`.
