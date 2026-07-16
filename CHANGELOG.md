# Changelog

## [1.10.0](https://github.com/anatolykoptev/dozor/compare/v1.9.0...v1.10.0) (2026-07-16)


### Added

* **deploy:** add loadavg backpressure guard for heavy builds (audit P3) ([#134](https://github.com/anatolykoptev/dozor/issues/134)) ([7559b9d](https://github.com/anatolykoptev/dozor/commit/7559b9d8d85ce7abfc8fa50fa88677d560a94862))
* **deploy:** per-repo deploy_on: release option ([#136](https://github.com/anatolykoptev/dozor/issues/136)) ([19e7c4d](https://github.com/anatolykoptev/dozor/commit/19e7c4d2c7346afddd9b5d5fb256f56a78405674))

## [1.9.0](https://github.com/anatolykoptev/dozor/compare/v1.8.1...v1.9.0) (2026-07-16)


### Features

* add alerts-active MCP tool with in-memory alert ring ([#122](https://github.com/anatolykoptev/dozor/issues/122)) ([ce7c35e](https://github.com/anatolykoptev/dozor/commit/ce7c35eaabb6b7c9faf818cb36d635b5d18b3d57))
* **deploy:** serialise heavy builds cross-lane via the box-wide ci-lock (audit P2) ([#129](https://github.com/anatolykoptev/dozor/issues/129)) ([6cf8825](https://github.com/anatolykoptev/dozor/commit/6cf8825d1ba4cd044a47f08be7e157e9c0caaf5d))
* durable Telegram message log + tg-messages MCP tool ([#126](https://github.com/anatolykoptev/dozor/issues/126)) ([72f4e54](https://github.com/anatolykoptev/dozor/commit/72f4e54dc3295103e58f9c6494149a2fb338e3fa))
* **engine:** add cargo target-dir cleanup, unify cleanup age threshold, fix+retier sccache cleanup ([56b3a4b](https://github.com/anatolykoptev/dozor/commit/56b3a4bec33eb36ef96942ec452fa3c199331ac7))


### Bug Fixes

* correct alerts-active jsonschema tags (drop description= prefix) ([#123](https://github.com/anatolykoptev/dozor/issues/123)) ([0de12b2](https://github.com/anatolykoptev/dozor/commit/0de12b2953d475fb6e826282cdfeac9938b11664))
* **engine:** resolve sccache path via Go, not broken shell tilde ([6e2e1e6](https://github.com/anatolykoptev/dozor/commit/6e2e1e6bafb65fb0e0c15d3504a757c509f23259))
* release-event webhooks respect BuildPaths/SkipPaths via a real diff ([#128](https://github.com/anatolykoptev/dozor/issues/128)) ([5fcfb68](https://github.com/anatolykoptev/dozor/commit/5fcfb68f1b3a085a8e45a6111b99f63ea45a0018))
* ring records Telegram-delivery time, not alert StartsAt ([#125](https://github.com/anatolykoptev/dozor/issues/125)) ([792430c](https://github.com/anatolykoptev/dozor/commit/792430c079ade11f713641bd24224118950bbaf4))
* rune-safe Telegram caption truncation (Cyrillic corruption) ([#127](https://github.com/anatolykoptev/dozor/issues/127)) ([664a114](https://github.com/anatolykoptev/dozor/commit/664a1145a3bd4f7997d146d6c4a09bd9448f97f2))

## [1.8.1](https://github.com/anatolykoptev/dozor/compare/v1.8.0...v1.8.1) (2026-06-20)


### Bug Fixes

* **release:** run GoReleaser inside release-please workflow (combined) ([#119](https://github.com/anatolykoptev/dozor/issues/119)) ([c08eb5d](https://github.com/anatolykoptev/dozor/commit/c08eb5d28fe3f19ba094a5e1cba5c20bbd2c3116))

## [1.8.0](https://github.com/anatolykoptev/dozor/compare/v1.7.0...v1.8.0) (2026-06-20)


### Features

* **a2a:** refuse start when DOZOR_A2A_SECRET missing (fail-closed) ([#55](https://github.com/anatolykoptev/dozor/issues/55)) ([4ddc6af](https://github.com/anatolykoptev/dozor/commit/4ddc6af52e9778b9507ad5be9ebdae89afc3f80e))
* alert cards via satori-render sidecar (no LLM) ([#9](https://github.com/anatolykoptev/dozor/issues/9)) ([5aacb06](https://github.com/anatolykoptev/dozor/commit/5aacb06d20ad4c9b30862f5d718ba40c4186c34b))
* **api/logs:** dozor.alias label for service-name resolution ([#36](https://github.com/anatolykoptev/dozor/issues/36)) ([243b2a8](https://github.com/anatolykoptev/dozor/commit/243b2a83c3c70217c2730ba91b56bbecea175fb8)), closes [#35](https://github.com/anatolykoptev/dozor/issues/35)
* **api:** GET /api/logs endpoint for debug_investigate log ingestion ([#32](https://github.com/anatolykoptev/dozor/issues/32)) ([18d80a7](https://github.com/anatolykoptev/dozor/commit/18d80a70c6b0f8e10de44a3920739ba85dd950ad))
* best-effort source-checkout ff-sync on deploy (default-OFF) ([#114](https://github.com/anatolykoptev/dozor/issues/114)) ([c8eef1d](https://github.com/anatolykoptev/dozor/commit/c8eef1dfbbb4b94b16d26c6526a65fec711a2c5e))
* **bind:** default to 127.0.0.1 via DOZOR_BIND_HOST ([#54](https://github.com/anatolykoptev/dozor/issues/54)) ([660ba0b](https://github.com/anatolykoptev/dozor/commit/660ba0ba84b5f5961de10a2489d8310a2fca6b8a))
* **deploy:** auto-pull deploy clone + inject GIT_SHA/BUILD_TIMESTAMP ([#77](https://github.com/anatolykoptev/dozor/issues/77)) ([dbf2780](https://github.com/anatolykoptev/dozor/commit/dbf278097c56ff148fa311b94f29cbdd2718e5f5))
* **deploy:** configurable build concurrency + skip debounce when build active ([#61](https://github.com/anatolykoptev/dozor/issues/61)) ([c89453b](https://github.com/anatolykoptev/dozor/commit/c89453ba61e29ad2d3ef5b2ed2d1efb888ed89db))
* **deploy:** make SkipPaths a real deny-list (was documentation-only) ([#59](https://github.com/anatolykoptev/dozor/issues/59)) ([aeccaf5](https://github.com/anatolykoptev/dozor/commit/aeccaf550a5621bd129304d740d0e591a7699c44))
* **deploy:** multi-branch support — same repo can deploy different services per branch ([19be896](https://github.com/anatolykoptev/dozor/commit/19be896ee576be115e9c301c080c895d23f4f301))
* **deploy:** multi-branch support — same repo can deploy different services per branch ([a5ba97d](https://github.com/anatolykoptev/dozor/commit/a5ba97dbe7f0b3edb33d4a109ce49aeb54ad29b9))
* **deploy:** multi-target deploys per repo (LookupAll + webhook fan-out) ([995b7fb](https://github.com/anatolykoptev/dozor/commit/995b7fb6f47e6a35e28683b72b59b0a7b04ace78))
* **deploy:** observable counter for queue-admission dedup ([#11](https://github.com/anatolykoptev/dozor/issues/11)) ([880707c](https://github.com/anatolykoptev/dozor/commit/880707c13148a3be98d836d3b1de1a2213f07bc8))
* **deploy:** pass DEPLOY_CHANGED_PATHS to static deploy scripts ([#65](https://github.com/anatolykoptev/dozor/issues/65)) ([dc26d47](https://github.com/anatolykoptev/dozor/commit/dc26d47b84a0340210f2152902a4da970ccf9957))
* **deploy:** path-filter and debounce window for webhook builds ([#6](https://github.com/anatolykoptev/dozor/issues/6)) ([e4a83f3](https://github.com/anatolykoptev/dozor/commit/e4a83f3dfe1789b8cf873403f55b792a9c5acf49))
* **deploy:** profile-based build_paths/skip_paths presets ([#7](https://github.com/anatolykoptev/dozor/issues/7)) ([90ef8dd](https://github.com/anatolykoptev/dozor/commit/90ef8ddb8ebb90f4cb10ddde07ed86614b4c3d29))
* **deploy:** static deploy kind for Astro/Vite sites ([#39](https://github.com/anatolykoptev/dozor/issues/39)) ([25bcaf8](https://github.com/anatolykoptev/dozor/commit/25bcaf869810872a21a4f3bdee68b6696bd5b7bd))
* **dev-mode:** bootstrap dev mode from DOZOR_DEV_MODE env var ([#53](https://github.com/anatolykoptev/dozor/issues/53)) ([f8b0d8a](https://github.com/anatolykoptev/dozor/commit/f8b0d8a5584923d7a69f3d124f03fba35efca14d))
* **dozor:** per-repo build_timeout config — fix long Rust rebuild timeouts ([#43](https://github.com/anatolykoptev/dozor/issues/43)) ([aa8532e](https://github.com/anatolykoptev/dozor/commit/aa8532e49c30d3d56a18a54597d96d740dec9844))
* **dozor:** static kind path-filter test coverage + krolik-server caddy sync ([5438fda](https://github.com/anatolykoptev/dozor/commit/5438fda865c543d078b557ce9d67b3fa1c2ec50b))
* **dozor:** tunable remote-confirm threshold and satori timeout ([#30](https://github.com/anatolykoptev/dozor/issues/30)) ([52d7fb2](https://github.com/anatolykoptev/dozor/commit/52d7fb2e33efbd11bf426da397a18c5108608270))
* drop-in httpmw.NewServeMux for code.* OTEL attrs on HTTP routes ([#37](https://github.com/anatolykoptev/dozor/issues/37)) ([d7a2f7a](https://github.com/anatolykoptev/dozor/commit/d7a2f7aa514cc410256a104691256fbdf4ef4b21))
* **engine:** adopt go-kit/score for nuclei severity parsing ([#31](https://github.com/anatolykoptev/dozor/issues/31)) ([7131888](https://github.com/anatolykoptev/dozor/commit/713188895b487b021c9be02d6af13d6eeb58e6b1))
* **follow-ups:** session ChatTime round-trip + prompt-cache observability ([4a06339](https://github.com/anatolykoptev/dozor/commit/4a0633982358df83f81c478b62bc23d0a0775b0f))
* introduce global default debounce window (3m) ([#63](https://github.com/anatolykoptev/dozor/issues/63)) ([9f24060](https://github.com/anatolykoptev/dozor/commit/9f2406069157ad8bc582e8f1429eaefdc1f0c644))
* **llmcfg:** canary check-models default to the production fallback chain ([#96](https://github.com/anatolykoptev/dozor/issues/96)) ([fe8ba63](https://github.com/anatolykoptev/dozor/commit/fe8ba636e364b16c307fd920d888d4c87cde2001))
* **llmcfg:** canonical LLM config + llm_check proxy probe via kitllm.Client.Chat [PR6/6] ([#72](https://github.com/anatolykoptev/dozor/issues/72)) ([0383648](https://github.com/anatolykoptev/dozor/commit/03836481024dc623ae7c62d93611499453d25f4f))
* **llm:** configurable cooldown TTL via LLM_COOLDOWN_SECONDS (default 15m) ([#104](https://github.com/anatolykoptev/dozor/issues/104)) ([bb8dd7c](https://github.com/anatolykoptev/dozor/commit/bb8dd7c3bd74242ee54065e7a937e001a30fbe4c))
* **logs:** noise demotion + error-log enrichment + untracked-tolerant clone pull ([#91](https://github.com/anatolykoptev/dozor/issues/91)) ([5072976](https://github.com/anatolykoptev/dozor/commit/50729768d042a4af9bf52cf2a10a1d277b7290cc))
* **metric-pull:** failures category — cross-source failure digest ([#98](https://github.com/anatolykoptev/dozor/issues/98)) ([d48c9ee](https://github.com/anatolykoptev/dozor/commit/d48c9ee826a56be3bab3a4d4edf94f565dc3ec77))
* **metrics:** export dozor_build_result_total for build outcome tracking ([#10](https://github.com/anatolykoptev/dozor/issues/10)) ([ebd155b](https://github.com/anatolykoptev/dozor/commit/ebd155bcb211c2b0c4672d8ffc9ce708e70c03d8))
* **metrics:** registry-driven Prom/Loki/Jaeger MCP tool ([#81](https://github.com/anatolykoptev/dozor/issues/81)) ([afee616](https://github.com/anatolykoptev/dozor/commit/afee6161e03d637616fadc3e45b4883d978110fe))
* **observability:** OTel spans on hot paths ([3132822](https://github.com/anatolykoptev/dozor/commit/3132822c71137995a8111866ae83a6f8be040a7a))
* **observability:** OTel tracing boilerplate (Setup + httpmw + slogh) ([e997492](https://github.com/anatolykoptev/dozor/commit/e997492df579354ba17bba1f89ed118b0c6231d0))
* **observability:** wrap outgoing HTTP transports with OTel ([5690b3e](https://github.com/anatolykoptev/dozor/commit/5690b3eb4a0254a8d6a96bf05eb1beeef5ae3906))
* **provider/openai:** wire DOZOR_LLM_MODEL_FALLBACK chain ([bfc9b00](https://github.com/anatolykoptev/dozor/commit/bfc9b009737e474849285123f63d44879b128475))
* **provider/openai:** wire DOZOR_LLM_MODEL_FALLBACK chain on primary ([d6cd4dd](https://github.com/anatolykoptev/dozor/commit/d6cd4dd0c734ae7d3582e1093fff5f0a62bab99f))
* **provider:** adopt kitllm.NewOptional pattern — empty key returns ErrUnavailable ([#66](https://github.com/anatolykoptev/dozor/issues/66)) ([2df9cc3](https://github.com/anatolykoptev/dozor/commit/2df9cc30a0b78bdc1758b8b4e54d34f3efe65472))
* **provider:** health-aware LLM model chain (filter dead models via /v1/models) ([#111](https://github.com/anatolykoptev/dozor/issues/111)) ([621b6d6](https://github.com/anatolykoptev/dozor/commit/621b6d6b7517c04ce6947fc2ae03283bfc6d5d51))
* **provider:** hedge.DoFallback for primary→fallback LLM chains ([1ddc648](https://github.com/anatolykoptev/dozor/commit/1ddc6484ca242dcbf42d54de194223928471b151))
* **provider:** MemDB-aligned message timestamps ([d5f0be1](https://github.com/anatolykoptev/dozor/commit/d5f0be150ac706edac5d4b2d81c7922bfc420359))
* **provider:** P1/6 — type adapters between dozor.provider and go-kit/llm ([2b51f63](https://github.com/anatolykoptev/dozor/commit/2b51f637e414a84a30ed357b9917837ba05f89df))
* **provider:** thread context.Context through Provider.Chat [PR2/6] ([#68](https://github.com/anatolykoptev/dozor/issues/68)) ([2b81943](https://github.com/anatolykoptev/dozor/commit/2b81943096430770c69ae967559e0e153e28e0e1))
* **remediate:** cooldown + top-dirs diagnostic + Prometheus counters ([#24](https://github.com/anatolykoptev/dozor/issues/24)) ([6d0816d](https://github.com/anatolykoptev/dozor/commit/6d0816d7dfc50fbff07f7d7cb24066e7cd9d6e3d))
* **remediate:** real df-based freed measurement + suppress empty notifications ([#22](https://github.com/anatolykoptev/dozor/issues/22)) ([8692251](https://github.com/anatolykoptev/dozor/commit/86922510c65f308b9ed26b54cfb34d69758e0549))
* **remediate:** stratified disk-pressure targets — apt/sccache/npm/docker ([#23](https://github.com/anatolykoptev/dozor/issues/23)) ([7054209](https://github.com/anatolykoptev/dozor/commit/705420998ab1e13f7d80587e7a47297c8a435e8d))
* **remediate:** wire disk-pressure auto-remediation into dozor ([#15](https://github.com/anatolykoptev/dozor/issues/15)) ([e431f78](https://github.com/anatolykoptev/dozor/commit/e431f78397f7e0940ef9646a4d0933fa56883628))
* **resilience:** panic recovery for goroutines + HTTP handlers ([ff1c61e](https://github.com/anatolykoptev/dozor/commit/ff1c61e2f38078145c4f37dca0ad29ab76bfc06b))
* skip deploy on no-auto-deploy PR label or commit marker ([#64](https://github.com/anatolykoptev/dozor/issues/64)) ([c3aeb12](https://github.com/anatolykoptev/dozor/commit/c3aeb12a8f48c560097d5d415d30c4fc3cf56542))
* **tg:** transition-only watch alerts + deploy failure-only by default ([#102](https://github.com/anatolykoptev/dozor/issues/102)) ([ddee714](https://github.com/anatolykoptev/dozor/commit/ddee714876cdc1236b30b9aa890994c5599c4b78))
* **tools:** server_deploy_check — aggregate webhook-deploy state per service ([#44](https://github.com/anatolykoptev/dozor/issues/44)) ([b7a27e2](https://github.com/anatolykoptev/dozor/commit/b7a27e2b2b8f4b904c0ef33eab9ceca58dd6e3cc))
* **tools:** server_deploy_check exposes live build-queue state ([#46](https://github.com/anatolykoptev/dozor/issues/46)) ([5b8fe3e](https://github.com/anatolykoptev/dozor/commit/5b8fe3ef8c488dd650d34d5e84910d51f27eef81))
* **watch:** mechanical Telegram report replaces LLM route by default ([#89](https://github.com/anatolykoptev/dozor/issues/89)) ([01116ae](https://github.com/anatolykoptev/dozor/commit/01116aeb7db3de80ca8e553f6548ee58611243ff))
* **watch:** per-hash notify cooldown — 1 alert per hour for persistent issues ([#26](https://github.com/anatolykoptev/dozor/issues/26)) ([cd4f0f6](https://github.com/anatolykoptev/dozor/commit/cd4f0f64d27670e66f72cce11be9372f5c3b4da2))
* **watch:** report header with time + id; gate LLM canary ([#90](https://github.com/anatolykoptev/dozor/issues/90)) ([319fe13](https://github.com/anatolykoptev/dozor/commit/319fe13966f178fedec269e758e8bb9f20cc231b))
* **webhook:** /webhook/alertmanager receives Prometheus Alertmanager v4 ([#17](https://github.com/anatolykoptev/dozor/issues/17)) ([4709cc2](https://github.com/anatolykoptev/dozor/commit/4709cc2b9ab46a377f8a378a0178242a52f83624))
* **webhook:** per-source rate limit on /webhook handler ([e944a30](https://github.com/anatolykoptev/dozor/commit/e944a3082f25828aeccb65107ab5e354ca12231c))


### Bug Fixes

* **a2a:** concurrency cap + LLM timeout + artifacts fallback ([#50](https://github.com/anatolykoptev/dozor/issues/50)) ([152b0cd](https://github.com/anatolykoptev/dozor/commit/152b0cdae16f07041d61a81c0ba357750a360a7a))
* **alerts:** serialize satori renders via renderMu; batch regression test ([#60](https://github.com/anatolykoptev/dozor/issues/60)) ([fc0e6a5](https://github.com/anatolykoptev/dozor/commit/fc0e6a599a6a8ea7b1162126f3347552364867c9))
* **api/logs:** skip handler registration when Docker client is nil (closes [#33](https://github.com/anatolykoptev/dozor/issues/33)) ([#34](https://github.com/anatolykoptev/dozor/issues/34)) ([4396132](https://github.com/anatolykoptev/dozor/commit/4396132b4386d40ec8e8eb3dbd0e9caf4c855472))
* **canary:** per-profile smoke_timeout default — bump rust to 120s ([#28](https://github.com/anatolykoptev/dozor/issues/28)) ([2a89ece](https://github.com/anatolykoptev/dozor/commit/2a89ece6425c8c19b67eae089fbc24f8519321b2))
* **cleanup:** downgrade apt target to unavailable when sudo is structurally blocked ([#106](https://github.com/anatolykoptev/dozor/issues/106)) ([a559d3f](https://github.com/anatolykoptev/dozor/commit/a559d3f01462b2bef5d46b6159dea271339f93ef))
* consolidate service-ops Telegram alerts onto the deterministic alert-card ([#113](https://github.com/anatolykoptev/dozor/issues/113)) ([6f22fbe](https://github.com/anatolykoptev/dozor/commit/6f22fbe3fcb46afbffd352de04b13409273f10b6))
* **deploy:** binary repos auto-populate Services from UserServices ([e409e23](https://github.com/anatolykoptev/dozor/commit/e409e23e6beaf1f40c2bc4b0cbdb601cd1fbd403))
* **deploy:** binary repos auto-populate Services from UserServices ([2afc1c9](https://github.com/anatolykoptev/dozor/commit/2afc1c9f3f12bade0243d91622fd285f6aaf7159))
* **deploy:** branch-scope the shared-clone check (don't break multi-branch) ([ce65220](https://github.com/anatolykoptev/dozor/commit/ce65220a4469db61b0500fb6d619d14d8abf27f2))
* **deploy:** bump up-phase maxOutputLen + add up-phase dump file ([#18](https://github.com/anatolykoptev/dozor/issues/18)) ([4108f02](https://github.com/anatolykoptev/dozor/commit/4108f0208c26ff78fb7777a78abc6a6334eed8fd))
* **deploy:** classify ff-failure into 3 classes + auto-quarantine untracked colliders ([#115](https://github.com/anatolykoptev/dozor/issues/115)) ([ad69d86](https://github.com/anatolykoptev/dozor/commit/ad69d86364b9a734f5fce77e07e13b5caef83650))
* **deploy:** fail-loud guard for colliding multi-target entries + conservative release path ([eb6a42e](https://github.com/anatolykoptev/dozor/commit/eb6a42e5e47b3b5437777e87e801d84a6246f904))
* **deploy:** forward build failures to Telegram notify ([#42](https://github.com/anatolykoptev/dozor/issues/42)) ([78802fa](https://github.com/anatolykoptev/dozor/commit/78802fa322e4bc4edec900f14a3450aba34011ef))
* **deploy:** newest-wins coalescing — never drop a newer commit, always serialize builds ([#12](https://github.com/anatolykoptev/dozor/issues/12)) ([bced058](https://github.com/anatolykoptev/dozor/commit/bced058fbc93be2e95977eaf4b22d50027ce7081))
* **deploy:** pass SSH_PORT to keyscan + ssh config ([#73](https://github.com/anatolykoptev/dozor/issues/73)) ([2a27a4c](https://github.com/anatolykoptev/dozor/commit/2a27a4c246f94d0f3e10fb4baa525f3b3b25f4f2))
* **deploy:** per-repo prune_buildkit_cache knob — invalidate cargo target/ between rebuilds ([#29](https://github.com/anatolykoptev/dozor/issues/29)) ([d4179ad](https://github.com/anatolykoptev/dozor/commit/d4179ada1c2b65de14fc46d8e856825a5e377e31))
* **deploy:** persist debounced builds across dozor restart ([#110](https://github.com/anatolykoptev/dozor/issues/110)) ([35196c4](https://github.com/anatolykoptev/dozor/commit/35196c4142d200070c4378347afcbdb8e1b6de23))
* **deploy:** unify manual deploy onto SHA-pinned worktree pipeline ([#108](https://github.com/anatolykoptev/dozor/issues/108)) ([a186a89](https://github.com/anatolykoptev/dozor/commit/a186a89f86c1aeb9eaf5bd046c28946e3751adb9))
* **deploy:** use per-repo branch from RepoConfig (was hardcoded main) ([#40](https://github.com/anatolykoptev/dozor/issues/40)) ([183abbd](https://github.com/anatolykoptev/dozor/commit/183abbd962925d72f928ec70beb996907879b81e))
* **disk:** unconditional build-cache prune on CRITICAL disk pressure ([5aa7687](https://github.com/anatolykoptev/dozor/commit/5aa768736d30de00c403224dda4916c0cae533f8))
* **disk:** unconditional build-cache prune on CRITICAL disk pressure ([43e1d6f](https://github.com/anatolykoptev/dozor/commit/43e1d6f59ecbb3250876371eb4c724d170c31338))
* **engine:** stable per-entity LLM alert identity — model once per line ([#94](https://github.com/anatolykoptev/dozor/issues/94)) ([5a53e8c](https://github.com/anatolykoptev/dozor/commit/5a53e8c04216befa4aa61c9760307dafe2b01753))
* **llm-check:** chain-aware canary + parser no longer doubles service in description ([#95](https://github.com/anatolykoptev/dozor/issues/95)) ([9da6147](https://github.com/anatolykoptev/dozor/commit/9da6147392e609f4aba0ae72930e3a5aa2b835fc))
* **llm-check:** classify HTTP 503/5xx as upstream warning, not invalid-key error ([#14](https://github.com/anatolykoptev/dozor/issues/14)) ([b25fc48](https://github.com/anatolykoptev/dozor/commit/b25fc4855b39c3393ab06f9abdf79f6c3dc5bc42))
* **log-analyzer:** cloakbrowser noise filter covers shared_memory + video_capture ([#27](https://github.com/anatolykoptev/dozor/issues/27)) ([5023cc1](https://github.com/anatolykoptev/dozor/commit/5023cc104ff42859e8e4a33ed0f6b6deb2face99))
* **loki:** parse logfmt level= key before substring heuristic ([#101](https://github.com/anatolykoptev/dozor/issues/101)) ([cdd4699](https://github.com/anatolykoptev/dozor/commit/cdd4699d79cf410ab7fa02df639e8ec24207d612))
* **metric-pull:** escape log_query for LogQL + warn on empty Loki match ([#83](https://github.com/anatolykoptev/dozor/issues/83)) ([d54c4b1](https://github.com/anatolykoptev/dozor/commit/d54c4b11a267bc4d28f2ffcf78b2cd1262556bac))
* **metric-pull:** Loki window honours range (3h default, was 30m) + limit 200 ([0c2545f](https://github.com/anatolykoptev/dozor/commit/0c2545f351d663f8d54d7a87dc9ea10570756389))
* **metric-pull:** Loki window honours range (default 3h not 30m) + limit 200 ([8dcd238](https://github.com/anatolykoptev/dozor/commit/8dcd2383ad45f6128d702fac8ca2f5c1b8e7f924))
* **metric-pull:** metric filter narrows instead of widening ([49b27cb](https://github.com/anatolykoptev/dozor/commit/49b27cb7b625cddc183a66cf75e4edb228355bda))
* **metric-pull:** metric filter narrows instead of widening ([ca15eed](https://github.com/anatolykoptev/dozor/commit/ca15eedfbbf96eb2a274f0a2f38f31a0d10f9b41))
* **metric-pull:** quality batch — name fallback, surface loki/jaeger URLs, jaeger empty-trace warn, richer no-match warning ([#84](https://github.com/anatolykoptev/dozor/issues/84)) ([a662c4c](https://github.com/anatolykoptev/dozor/commit/a662c4c8d73bf452d85451b2309afb27d06b99b0))
* **metrics:** count error logs by structured level, not substring ([#99](https://github.com/anatolykoptev/dozor/issues/99)) ([cab02f7](https://github.com/anatolykoptev/dozor/commit/cab02f7f8175a67d7ff04774d9757f0a8376e564))
* **metrics:** failures-sweep error count by level, not substring ([#100](https://github.com/anatolykoptev/dozor/issues/100)) ([ef1e948](https://github.com/anatolykoptev/dozor/commit/ef1e948b4ea1d25716e915074d7c39a6c22c24c1))
* **notify:** sort issue services before hash so cooldown survives container order flips ([#41](https://github.com/anatolykoptev/dozor/issues/41)) ([f3ec892](https://github.com/anatolykoptev/dozor/commit/f3ec892e74d8e8f016db06970ccea927032f259f))
* persist + recover the build queue across dozor restarts ([#116](https://github.com/anatolykoptev/dozor/issues/116)) ([e131b33](https://github.com/anatolykoptev/dozor/commit/e131b3315a714fa6cf3e41b03d42ae7fd0cc1c25))
* **provider:** honour kitllm.APIError.RetryAfter in chatBackoff ([#74](https://github.com/anatolykoptev/dozor/issues/74)) ([7a5f5bd](https://github.com/anatolykoptev/dozor/commit/7a5f5bd9e1cabc1040102a2de7d8a82aa58d12d1))
* **review:** address blockers from code review + bump go-kit v0.40.1 ([3803c00](https://github.com/anatolykoptev/dozor/commit/3803c00d75ce7ee07dcaf94d54feb48fd5fa87db))
* **static-deploy:** cap changed-paths union + set cmd.Dir on script runner ([#67](https://github.com/anatolykoptev/dozor/issues/67)) ([6e4b8b2](https://github.com/anatolykoptev/dozor/commit/6e4b8b22646f9c1580cb33e0e2be5e6765b86550))
* **telegram:** remove CompactForTelegram — split, don't truncate alerts ([#107](https://github.com/anatolykoptev/dozor/issues/107)) ([5ce5edd](https://github.com/anatolykoptev/dozor/commit/5ce5edd2ff1023ed14a11c3d73d326b22abf0a58))
* **telegram:** route sendReply through tgfmt.PrepareForTelegram ([#19](https://github.com/anatolykoptev/dozor/issues/19)) ([73d0104](https://github.com/anatolykoptev/dozor/commit/73d01041262d688d32eacc9ce80be75674eebf8a))
* **tools:** add missing window arg in jaeger empty-match warning ([#93](https://github.com/anatolykoptev/dozor/issues/93)) ([bc1101b](https://github.com/anatolykoptev/dozor/commit/bc1101b271b29d997a748d4a370505af33ca31a7))
* **tools:** bump docker ps timeout 3s → 10s in server_deploy_check ([#45](https://github.com/anatolykoptev/dozor/issues/45)) ([2f2d665](https://github.com/anatolykoptev/dozor/commit/2f2d6650037165e022b8bde6e6ce3af828c68a7e))
* **watch:** cosmetic-log allow-list + error-density threshold (alert tuning) ([#57](https://github.com/anatolykoptev/dozor/issues/57)) ([fafef6d](https://github.com/anatolykoptev/dozor/commit/fafef6da44e5aaa7b31c57f27352a78eb77fb4b7))
* **watch:** switch LLM prompt template from Markdown to Telegram HTML ([#21](https://github.com/anatolykoptev/dozor/issues/21)) ([91afc53](https://github.com/anatolykoptev/dozor/commit/91afc535504d88d5bc2496ebdf7d96ecdf1da3dd))
