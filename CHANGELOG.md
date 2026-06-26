# Changelog

## [0.5.2](https://github.com/j0sh3rs/bollard/compare/v0.5.1...v0.5.2) (2026-06-26)


### Bug Fixes

* filter Docker bridge interfaces from IP inference, log container name with short ID and hostname on failures ([8e8433f](https://github.com/j0sh3rs/bollard/commit/8e8433fa1a24960cf217ed7069e65580da1176c6))

## [0.5.1](https://github.com/j0sh3rs/bollard/compare/v0.5.0...v0.5.1) (2026-06-26)


### Bug Fixes

* legacy UniFi API returns raw array and single object, not envelope ([043781e](https://github.com/j0sh3rs/bollard/commit/043781ef0030ced06db416b627e34d0f42c4df51))


### Chores

* add ip-override to dev test-app label for multi-NIC hosts ([62781a8](https://github.com/j0sh3rs/bollard/commit/62781a8725c54f6b539fcd576e78c0d1f4152fff))
* add local dev compose with labeled test container and env example ([e595987](https://github.com/j0sh3rs/bollard/commit/e5959870f2c5a24747eeac180c7ea793745c2359))

## [0.5.0](https://github.com/j0sh3rs/bollard/compare/v0.4.0...v0.5.0) (2026-06-26)


### Features

* add --healthcheck flag and Dockerfile HEALTHCHECK directive ([d1f905d](https://github.com/j0sh3rs/bollard/commit/d1f905d8410df3496f9b689de40d28ca9f28b2c7))


### Bug Fixes

* remove USER 65534 — breaks volume writes on host-mounted paths ([183614d](https://github.com/j0sh3rs/bollard/commit/183614d8ad4ee59eeb115c21242543deb5814bd6))


### Documentation

* add roadmap with phased feature backlog and open questions ([31889a9](https://github.com/j0sh3rs/bollard/commit/31889a9641f57ed5b7abbaa424ad5f6e75b03dc3))

## [0.4.0](https://github.com/j0sh3rs/bollard/compare/v0.3.0...v0.4.0) (2026-06-26)


### Features

* **dep:** Bump all deps to latest ([f0a1c7f](https://github.com/j0sh3rs/bollard/commit/f0a1c7f440bf3590720c85810d514479cca3b001))
* expose Prometheus metrics and /healthz endpoint ([9cd1ba2](https://github.com/j0sh3rs/bollard/commit/9cd1ba2a2fba590579f42d00b63fab6cd8c8c93f))


### Documentation

* add comprehensive monitoring reference with alerting rules and examples ([c35d965](https://github.com/j0sh3rs/bollard/commit/c35d965cc1b1304a16a6441b3435edd1105dfa8a))

## [0.3.0](https://github.com/j0sh3rs/bollard/compare/v0.2.2...v0.3.0) (2026-06-26)


### Features

* add PostgreSQL store backend with migrations and tests ([607f233](https://github.com/j0sh3rs/bollard/commit/607f2337f20df09104e439915177e750860f030a))

## [0.2.2](https://github.com/j0sh3rs/bollard/compare/v0.2.1...v0.2.2) (2026-06-26)


### Documentation

* add comprehensive documentation and examples ([14fe926](https://github.com/j0sh3rs/bollard/commit/14fe926cd440e5b6b76b6843a795e3ca7e006d10))


### Chores

* add renovate config with weekly binpacking and automerge ([f99e2d9](https://github.com/j0sh3rs/bollard/commit/f99e2d98b67b38f7e86f962d1b61c50988f01fe6))

## [0.2.1](https://github.com/j0sh3rs/bollard/compare/v0.2.0...v0.2.1) (2026-06-26)


### Chores

* harden Dockerfile and release pipeline with SBOM, provenance, signing ([1458149](https://github.com/j0sh3rs/bollard/commit/1458149559d294d99503c3d17357d9bc4fb6b517))

## [0.2.0](https://github.com/j0sh3rs/bollard/compare/v0.1.0...v0.2.0) (2026-06-26)


### Features

* add Docker event watcher ([c44bc37](https://github.com/j0sh3rs/bollard/commit/c44bc37e7e1db55ec63cc632b814f9431d00628c))
* add label parser and host IP resolver ([d9fa72e](https://github.com/j0sh3rs/bollard/commit/d9fa72e3b8b580036a12c619a3872c55caa5fd91))
* add main entrypoint with adopt flag and graceful shutdown ([b236de3](https://github.com/j0sh3rs/bollard/commit/b236de3be96055afdc640810f82c2f1eb1b46c5d))
* add module scaffold and config package ([fdeeb4e](https://github.com/j0sh3rs/bollard/commit/fdeeb4efbfb705cb2b61802e1f87bed76fb8e4f7))
* add reconciler with event handling, reconcile loop, and adopt ([d437a10](https://github.com/j0sh3rs/bollard/commit/d437a106c477e4b5bcd4771f4814524541a08258))
* add store interface and SQLite backend with migrations ([a87d1aa](https://github.com/j0sh3rs/bollard/commit/a87d1aad12b6b4b6ded8ac76fc619af7e8a35a99))
* add structured logging package (logfmt/json) ([2929e93](https://github.com/j0sh3rs/bollard/commit/2929e938a328bb4f000724fdcab72ef1a842e758))
* add UniFi DNS provider (modern + legacy API) ([b49a1e4](https://github.com/j0sh3rs/bollard/commit/b49a1e48adbe5b5371e2379e310960e3d7cbdd87))


### Bug Fixes

* add tint dependency and make config test hermetic ([a4f27aa](https://github.com/j0sh3rs/bollard/commit/a4f27aa2cfb2974c88b4d5e63a9d96ce52c49680))
* assign record-type label value to spec and pin go version ([851ddb2](https://github.com/j0sh3rs/bollard/commit/851ddb2f4838a8e34b1f1f9875ee610ac46a9029))
* complete reconcile loop, add Dockerfile and CI workflow ([67b625f](https://github.com/j0sh3rs/bollard/commit/67b625f178100f92fbd78c6b98a9f4cb77fc218f))
* correct go version, propagate time parse errors, atomic delete, remove unfalsifiable test ([657503f](https://github.com/j0sh3rs/bollard/commit/657503fee70f07da7d781b07a4d17f4d4321ba1c))
* pin go 1.23 after docker SDK dependency addition ([db5dcc1](https://github.com/j0sh3rs/bollard/commit/db5dcc1aca14a1a7e4ea9a6af81bd1a0036b3bf4))
* pin go 1.23 and wrap legacy create response error ([d5abc56](https://github.com/j0sh3rs/bollard/commit/d5abc5609d2093d8ffee93102e25f4c5e2f886bf))
* set go directive to 1.23 in go.mod ([a5483f7](https://github.com/j0sh3rs/bollard/commit/a5483f7c8a8ccb1e1fdaac10151b61642e5bc73c))
* skip IP inference when hostIP already set in reconciler ([07bbc12](https://github.com/j0sh3rs/bollard/commit/07bbc12495c858f306d4f616a9c6a96f2f4217b5))
* wrap handleStop error and protect hostIP with sync.Once ([bdb2db5](https://github.com/j0sh3rs/bollard/commit/bdb2db5b6195ad61ee09a721a582efad31167d57))


### Documentation

* add initial scoping document ([4b8e219](https://github.com/j0sh3rs/bollard/commit/4b8e2196435df68a068b325a39bee46c467b94dd))
* add MVP implementation plan ([094f28b](https://github.com/j0sh3rs/bollard/commit/094f28bb7b880f6b1774cfe4a91985e130a2ed2f))
* add README and docker compose example ([76d3ac8](https://github.com/j0sh3rs/bollard/commit/76d3ac8b2bc8dcde21bba7bdb2608018c267ae92))
* resolve open questions in scoping document ([1d7a1c7](https://github.com/j0sh3rs/bollard/commit/1d7a1c746e72efecdebd34ed085eb81ebc25686e))
