# Changelog

## [0.22.1](https://github.com/anatolykoptev/go-mcpserver/compare/v0.22.0...v0.22.1) (2026-10-15)


### Fixed

* **keyfile:** scoped-method allowlist, clock-step-safe reload gate, refusal signatures, deploy docs ([#50](https://github.com/anatolykoptev/go-mcpserver/issues/50)) ([7888171](https://github.com/anatolykoptev/go-mcpserver/commit/78881712a5fee5d8b0c3f05e4ade1f4e287ac4cf)), closes [#49](https://github.com/anatolykoptev/go-mcpserver/issues/49)

## [0.22.0](https://github.com/anatolykoptev/go-mcpserver/compare/v0.21.0...v0.22.0) (2026-10-15)


### Added

* **keyfile:** experimental hashed per-key token verifier with tool scoping ([#47](https://github.com/anatolykoptev/go-mcpserver/issues/47)) ([fdf3d28](https://github.com/anatolykoptev/go-mcpserver/commit/fdf3d28a57978c7798c521e8d86cfaec3f6ed994))

## [0.21.0](https://github.com/anatolykoptev/go-mcpserver/compare/v0.20.0...v0.21.0) (2026-10-10)


### Added

* **transport:** cancel tool handlers when the client disconnects ([#42](https://github.com/anatolykoptev/go-mcpserver/issues/42)) ([75d142b](https://github.com/anatolykoptev/go-mcpserver/commit/75d142bc9931122ee14ece29a5e668502f6c1324))


### Fixed

* **schema:** normalize boolean subschemas so every MCP client accepts tools/list ([#41](https://github.com/anatolykoptev/go-mcpserver/issues/41)) ([5f80cfb](https://github.com/anatolykoptev/go-mcpserver/commit/5f80cfb82b5a2143df7ee9f02d83ae3a283d5c42))

## [0.20.0](https://github.com/anatolykoptev/go-mcpserver/compare/v0.19.0...v0.20.0) (2026-10-10)


* release 0.20.0 ([#40](https://github.com/anatolykoptev/go-mcpserver/issues/40)) ([52f7fa8](https://github.com/anatolykoptev/go-mcpserver/commit/52f7fa8ee066e771dc6d72c2624066b51f480f4d))


### Added

* **sdk:** go-sdk v1.8.0 — MCP protocol 2026-07-28 + SupportedProtocolVersions ([#35](https://github.com/anatolykoptev/go-mcpserver/issues/35)) ([b6f4e7a](https://github.com/anatolykoptev/go-mcpserver/commit/b6f4e7a1c1b9958b1b4707880749e9a521cb7952))


### Fixed

* BearerAuth.LoopbackIdentity — trusted edge identity under LoopbackBypass ([#33](https://github.com/anatolykoptev/go-mcpserver/issues/33)) ([#34](https://github.com/anatolykoptev/go-mcpserver/issues/34)) ([18f022a](https://github.com/anatolykoptev/go-mcpserver/commit/18f022a6518426a69a70f0a93ddeea86f63649f9))
