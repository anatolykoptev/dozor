# Changelog

## [0.3.0](https://github.com/anatolykoptev/go-stt/compare/v0.2.0...v0.3.0) (2026-07-18)


### Features

* add WithRetryWithMaxDelay for configurable backoff cap ([#41](https://github.com/anatolykoptev/go-stt/issues/41)) ([77a3799](https://github.com/anatolykoptev/go-stt/commit/77a37995530d9d6932e44efc5d4c40b26a44a529))


### Bug Fixes

* apply StreamParams VAD/Punctuate defaults (nil → true) via *bool ([#36](https://github.com/anatolykoptev/go-stt/issues/36)) ([c2b5e6d](https://github.com/anatolykoptev/go-stt/commit/c2b5e6dbcb2885d591490986de0d92cf60f144fc))
* explicit circuit breaker half-open state with single probe + per-attempt check ([#35](https://github.com/anatolykoptev/go-stt/issues/35)) ([9bfb705](https://github.com/anatolykoptev/go-stt/commit/9bfb70510cfe5714d1cddd3500bbf11460e853c3))
* harden StreamClient lifecycle — Close() timeout, conn race, double-Connect guard ([#29](https://github.com/anatolykoptev/go-stt/issues/29)) ([f0bf0cd](https://github.com/anatolykoptev/go-stt/commit/f0bf0cd4cdd35f1cff00adeef59f512d7b545736))
* increase TestStreamFileCancel timeout for race detector ([9e13341](https://github.com/anatolykoptev/go-stt/commit/9e13341ddf615d50abc0641df7c374f9f91b4b03))
* inject clock into circuit breaker + cap baseDelay to maxDelay + property tests ([#43](https://github.com/anatolykoptev/go-stt/issues/43)) ([33f7e16](https://github.com/anatolykoptev/go-stt/commit/33f7e161f200898fab2235076e9409cb4d66ecb4))
* pass all StreamParams from StreamFile + propagate json.Marshal error ([#42](https://github.com/anatolykoptev/go-stt/issues/42)) ([4b332b8](https://github.com/anatolykoptev/go-stt/commit/4b332b8be7b4508860f980779e4e85fcf62a28f6))
* propagate multipart WriteField errors instead of ignoring them ([#32](https://github.com/anatolykoptev/go-stt/issues/32)) ([e8d7a5c](https://github.com/anatolykoptev/go-stt/commit/e8d7a5ce6d3877555d01d43eb10362ebba348666))
* set WebSocket read limit to prevent memory bomb from large server messages ([#33](https://github.com/anatolykoptev/go-stt/issues/33)) ([6fa8e19](https://github.com/anatolykoptev/go-stt/commit/6fa8e194130d251dbbd75c493956344cc4e336b7))
* use configured HTTP client + temp dir for downloadToTemp ([#40](https://github.com/anatolykoptev/go-stt/issues/40)) ([35c32f1](https://github.com/anatolykoptev/go-stt/commit/35c32f189db2079dab70ab94ec7411458228d24a))
* use net/url for WebSocket URL scheme conversion instead of naive string replace ([#34](https://github.com/anatolykoptev/go-stt/issues/34)) ([15bfb56](https://github.com/anatolykoptev/go-stt/commit/15bfb562ca4a832b6bc83394bd86c0e03f4df642))
* validate retry maxAttempts and baseDelay bounds ([#31](https://github.com/anatolykoptev/go-stt/issues/31)) ([f2f3125](https://github.com/anatolykoptev/go-stt/commit/f2f312591cae0bef67ce9c504e647886a0cdc494))
