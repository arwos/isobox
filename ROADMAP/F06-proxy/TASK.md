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
  - Приёмка: приложение не настраивает proxy env и не знает alias/credentials; sandbox не имеет IPv6 адресов/маршрутов; при TUN/SOCKS UDP недоступности приложение не стартует; после отказа активной сессии трафик не переключается напрямую.
  - Реализованы TUN/IPv4/TCP/UDP data plane и startup gate. Для опубликованных host-портов добавлен ingress-only pasta путь с nftables egress guard и отдельным marked return route; если pasta, nftables, интерфейс или маршруты не проходят проверки, запуск остаётся закрыт. Proxy mode не использует bwrap `--disable-userns`: helper должен оставаться в user namespace, владеющем network namespace, чтобы применять IPv6 sysctl и создавать TUN; запрет namespace-операций для приложения устанавливается helper seccomp до `exec`. Runtime-проверка на целевом хосте завершилась отказом записи IPv6 sysctl; после этого исправления T03 ждёт повторной runtime acceptance.
- [x] F06-T04 — Подготовить Podman fixture для тестового sing-box SOCKS5 server.
  - Шаги: локальная конфигурация тестового server для TCP и UDP и тестовых credentials; не добавлять sing-box в runtime зависимости или production installer.
  - Приёмка: fixture запускается локально в Podman и позволяет проверить CONNECT, UDP ASSOCIATE, auth failure и остановку сервера. Пользовательская runtime-проверка прошла; отчёт `/tmp/isobox-test-results/f06-sing-box.SNry0x/result.txt`.
