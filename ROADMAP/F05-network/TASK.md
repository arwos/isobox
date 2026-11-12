# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [!] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. bwrap удерживает приложение до PID readiness-файла pasta; procfs ownership-проверка выполняется дополнительно, если доступна. Внутренний helper устанавливает default-drop nftables для input/output/forward, оставляет loopback, established ответы и новые входящие TCP/UDP только на объявленные порты; затем сбрасывает capability и запрещает приложению создавать namespace. Если firewall, helper, socket readiness или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[!]`: `off` без объявленных портов прошёл runtime-проверку запрета host-loopback, внешних TCP/UDP и DNS. Для `off` с портами host acceptance не завершён: обычный запуск harness доходит до nft helper, но nft получает `netlink: Error: cache initialization failed: Operation not permitted`; повторный запуск с расширенным доступом останавливается раньше, потому что pasta не может войти в bwrap namespaces (`Couldn't switch to pasta namespaces: Operation not permitted`). В обоих случаях payload не запускается. Fail-closed подтверждён; применение nft правил, host ingress и ответы на TCP/UDP соединения ещё не приняты. Нужен хост, разрешающий rootless pasta namespace setup и nft netlink операции. Детали в `QA.md`.
- [!] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: прямой режим доступен приложению; wildcard bind не выбирается неявно; портовая конфигурация одинакова при выборе поддерживаемого fallback backend; не объявленные порты не открыты.
  - Реализация: pasta конфигурирует direct namespace, отключает auto/reverse port forwarding, применяет только host forwards из профиля и готовит DNS через read-only `/etc/resolv.conf`. Если pasta не подходит, slirp4netns используется с `ready-fd`; host forwards добавляются отдельными API-запросами по каждому порту. Его host-forward API IPv4-only, поэтому IPv6 host bind без подходящего pasta блокирует запуск.
  - Реализация: при совместимой конфигурации preflight сохраняет slirp fallback; если pasta не запускается до открытия bwrap startup gate, isobox останавливает pasta и пытается поднять slirp. Для fallback заранее формируется read-only resolver с DNS-адресами обоих backend. Несовместимый IPv6 host bind и loopback DNS override не ослабляются; если fallback тоже не поднимается, приложение не запускается.
  - Статус `[!]`: полный direct acceptance прошёл на pasta и slirp для внешнего IPv4 TCP/UDP и DNS (default и профильный override), TCP/UDP диапазона, отдельных TCP-only/UDP-only портов, точного loopback bind и отказа не объявленного порта. Injected pasta startup failure проверил переход на slirp до открытия gate с теми же forwarding/DNS проверками. Внешний IPv6 пропущен: у хоста нет IPv6 default route. Для полного закрытия нужен host с внешним IPv6 маршрутом; детали в `QA.md` и `source/README.md`.
