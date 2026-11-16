# F09 Контекст: именованные групповые сети

## Согласованные решения

- Пользователь явно создаёт/удаляет именованные сети; bridge постоянно активен даже без sandbox.
- Сеть обслуживает пользовательский manager под `systemd --user` и автоматически поднимается, когда стартует user manager. Для запуска до интерактивного входа нужен systemd lingering; isobox не должен включать его автоматически.
- Каждая sandbox сохраняет собственный network namespace и loopback. Она может подключаться к нескольким группам; у неё отдельный уникальный IPv4 адрес в каждой группе.
- В профиле `group` и `alias` повторяемые. `app-id` и все aliases публикуются в DNS каждой подключённой группы.
- Явное членство разрешает внутреннюю связь участников, включая `network.mode off`. Внешний трафик остаётся под политикой режима: direct, SOCKS5 proxy или egress allowlist; off не получает выхода из группы.
- Межгрупповая маршрутизация запрещена; пересекающиеся подсети у групп, подключённых к одному sandbox, отклоняются. Неоднозначность IP/имени или ошибка подключения должна завершать запуск fail-closed.
- Предложенный CLI: `isobox network create --name=NAME --subnet=IPv4-CIDR`, `isobox network list`, `isobox network remove --name=NAME`. Предлагаемая форма профиля:

```unic
network {
  mode off;
  group frontend;
  group backend;
  alias api;
  alias web;
}
```

## Ограничения и технический риск

- Реализация должна оставаться rootless и не добавлять root/setuid helper без отдельного согласования.
- Текущий sandbox создаёт user namespace на запуск и отдельный network namespace; direct использует per-container pasta/slirp, proxy/egress — per-container TUN. Это не создаёт общей L2-сети между запусками.
- Linux связывает полномочия сетевых операций с user namespace, владеющим network namespace. `veth` может связать network namespaces, но это требует правильного владельца и `CAP_NET_ADMIN`. Bubblewrap поддерживает `--userns FD`; применение к текущей схеме с запретом дальнейших namespace-операций и capabilities ещё нужно проверить.
- Успешная rootless feasibility проверка — prerequisite для реализации manager и подключения приложений. При невозможности сохранить текущие границы безопасности нельзя незаметно переходить к privileged backend.
- Статические адреса контейнерам пользователь пока не задавал; базовый вариант — выдаваемый manager-ом уникальный IPv4 lease из подсети каждой группы.
- Коллизия app-id/alias в пределах одной группы должна завершать попытку присоединения ошибкой; одно имя может повторяться в разных группах.
- Точное место хранения сетевых описаний внутри `~/.isobox` и детали unit naming остаются решением реализации. За основу брать проверки владельца, symlink и приватных прав из существующих F02/F08 integrations.

## Зависимости

- F04 предоставляет bwrap helper, startup gate и изоляцию sandbox.
- F05-T03 задаёт контракт IPv4 CIDR и адресации; его runtime-приёмка ещё открыта.
- F06-T05 задаёт egress policy для встроенного режима; его runtime-приёмка ещё открыта.
- F08-T02 уже предоставляет пользовательские systemd units и безопасные file operations, которые следует переиспользовать.

## Проверенные источники

- [Linux network namespaces](https://man7.org/linux/man-pages/man7/network_namespaces.7.html): network namespaces изолируют сетевые устройства, маршруты, firewall и порты; `veth` связывает namespaces и может подключаться к bridge.
- [Linux user namespaces](https://man7.org/linux/man-pages/man7/user_namespaces.7.html): capabilities применяются к ресурсам namespaces, принадлежащих соответствующему user namespace; это определяет требования к владельцу общей сети.
- [Bubblewrap manual](https://github.com/containers/bubblewrap/blob/main/bwrap.xml): `--userns FD` использует существующий user namespace при выполнении требований `setns`; проверить совместимость с текущим sandbox helper и запретом user namespace приложения.
- [passt/pasta overview](https://passt.top/about/?h=2026_05_26.038c51e): rootless L4 forwarding не требует host veth; соединение namespaces через veth требует `CAP_NET_ADMIN`, поэтому pasta per-run не является общей LAN.
- [systemd loginctl](https://freedesktop.org/software/systemd/man/latest/loginctl.html): lingering запускает user manager при boot и удерживает его после logout; isobox не включает lingering автоматически.
