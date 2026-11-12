# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [x] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. После подготовки bwrap mounts helper создаёт network namespace с краткоживущим `CAP_SYS_ADMIN`, устанавливает default-drop nftables с `CAP_NET_ADMIN`, затем сбрасывает capabilities и запрещает приложению создавать namespace. Helper сообщает supervisor-у о готовности через временный private Unix socket; supervisor находит host TID этого netns, запускает pasta и только после PID/socket readiness выпускает приложение. Если firewall, helper, socket readiness или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[x]`: live acceptance прошла на текущем rootless host. Проверены запрет host-listener и внешнего TCP, UDP/DNS, отказ в создании ICMP socket, доступ host к объявленному TCP+UDP порту с ответами и отказ TCP/UDP на соседнем не объявленном порту. Ограничения ранних запусков и полный вывод приведены в `QA.md`.
- [!] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: прямой режим доступен приложению; wildcard bind не выбирается неявно; портовая конфигурация одинакова при выборе поддерживаемого fallback backend; не объявленные порты не открыты.
  - Реализация: pasta конфигурирует direct namespace, отключает auto/reverse port forwarding, применяет только host forwards из профиля и готовит DNS через read-only `/etc/resolv.conf`. Если pasta не подходит, slirp4netns используется с `ready-fd`; host forwards добавляются отдельными API-запросами по каждому порту. Его host-forward API IPv4-only, поэтому IPv6 host bind без подходящего pasta блокирует запуск.
  - Реализация: при совместимой конфигурации preflight сохраняет slirp fallback; если pasta не запускается до открытия bwrap startup gate, isobox останавливает pasta и пытается поднять slirp. Для fallback заранее формируется read-only resolver с DNS-адресами обоих backend. Несовместимый IPv6 host bind и loopback DNS override не ослабляются; если fallback тоже не поднимается, приложение не запускается.
  - Статус `[!]`: полный direct acceptance прошёл на pasta и slirp для внешнего IPv4 TCP/UDP и DNS (default и профильный override), TCP/UDP диапазона, отдельных TCP-only/UDP-only портов, точного loopback bind и отказа не объявленного порта. Injected pasta startup failure проверил переход на slirp до открытия gate с теми же forwarding/DNS проверками. Внешний IPv6 пропущен: у хоста нет IPv6 default route. Для полного закрытия нужен host с внешним IPv6 маршрутом; детали в `QA.md` и `source/README.md`.
