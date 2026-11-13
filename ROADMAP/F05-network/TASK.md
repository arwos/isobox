# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [x] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные IPv4 адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. После подготовки bwrap mounts helper создаёт network namespace с краткоживущими `CAP_SYS_ADMIN` и `CAP_NET_ADMIN`, отключает IPv6 через namespace-local sysctl и проверяет отсутствие IPv6 адресов, устанавливает default-drop nftables, затем сбрасывает capabilities и запрещает приложению создавать namespace. Helper сообщает supervisor-у о готовности через временный private Unix socket; supervisor находит host TID этого netns, запускает pasta и только после PID/socket readiness выпускает приложение. Если `process_count` задан, временный task allowance для Go/nft setup снижается до точного `process_count+2`, а `RLIMIT_NOFILE` устанавливается именно для host TID, который станет приложением. Если firewall, IPv6-настройка, helper, socket readiness, лимит или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[x]`: live acceptance прошла на текущем rootless host. Проверены запрет host-listener и внешнего TCP, UDP/DNS, отказ в создании ICMP socket, доступ host к объявленному TCP+UDP порту с ответами, отказ TCP/UDP на соседнем не объявленном порту, отсутствие IPv6 адресов и сброс setup capabilities до приложения. Ограничения ранних запусков и полный вывод приведены в `QA.md`.
- [x] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: direct доступен приложению только по IPv4; IPv6 полностью отключён в network namespace; wildcard bind не выбирается неявно; портовая конфигурация одинакова для совместимых backend; не объявленные порты не открыты. Профильные IPv6 DNS и host bind адреса отклоняются до запуска.
  - Реализация: bwrap mounts готовятся до запуска доверенного helper-а, который поздно создаёт отдельный network namespace, выставляет `net.ipv6.conf.default.disable_ipv6=1` и `net.ipv6.conf.all.disable_ipv6=1`, проверяет отсутствие IPv6 адресов, сообщает supervisor-у namespace TID и ждёт сетевую готовность. Supervisor подключает pasta или slirp; только после readiness helper сбрасывает capabilities, включает `no_new_privs` и заменяет себя приложением. Pasta работает с `--ipv4-only`; slirp4netns запускается без `--enable-ipv6`; host forwards добавляются отдельными API-запросами. DNS host resolver фильтруется до IPv4; если IPv4 nameserver отсутствует, запуск завершается ошибкой.
  - Реализация: preflight сохраняет совместимый slirp fallback; если pasta не запускается до release gate, isobox останавливает pasta и пытается поднять slirp с теми же IPv4 forwards и resolver. Если fallback тоже не поднимается, приложение не запускается.
  - Статус `[x]`: live acceptance прошла на pasta, slirp и auto fallback с injected pasta failure. Проверены default/profile IPv4 DNS и внешний TCP/UDP, TCP-only, UDP-only, TCP+UDP range, точный loopback bind, отказ не объявленным портам, отсутствие любых IPv6 адресов включая `::1`, внешняя IPv6 недоступность и нулевые capability sets у приложения. Полные команды в `QA.md`; пользовательские runners находятся в `tests/f05-network/`.
