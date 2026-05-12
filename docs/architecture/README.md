# dozor — архитектурные диаграммы

LikeC4 source. Build → static SPA → `https://m.krolik.run/c4/dozor/`.

systemctl --user status dozor

## Файлы
- `catalog-info.yaml` — Backstage descriptor
- `dozor.c4` — LikeC4 source (skeleton — расширь)

## Edit + publish
```bash
$EDITOR dozor.c4
~/bin/c4-publish dozor
```

**TODO**: обогатить containers / components / dynamic views.
