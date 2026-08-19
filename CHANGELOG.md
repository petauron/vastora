# Changelog

## [0.1.0-alpha.6](https://github.com/petauron/vastora/compare/v0.1.0-alpha.5...v0.1.0-alpha.6) (2026-08-19)


### Bug Fixes

* accept generated Center install bundle ([#37](https://github.com/petauron/vastora/issues/37)) ([1eae32c](https://github.com/petauron/vastora/commit/1eae32cd637a3af0bc7215c52e92c7ee59120667))
* verify packaged release metadata path ([#36](https://github.com/petauron/vastora/issues/36)) ([b73d9f6](https://github.com/petauron/vastora/commit/b73d9f6b77090ef943c68da6447829b92ae2ca5e))

## [0.1.0-alpha.5](https://github.com/petauron/vastora/compare/v0.1.0-alpha.4...v0.1.0-alpha.5) (2026-08-19)


### Features

* install Headscale from the setup wizard ([#34](https://github.com/petauron/vastora/issues/34)) ([069eb36](https://github.com/petauron/vastora/commit/069eb361a196297db3104fc28e67e2e91ee16fea))


### Bug Fixes

* make installer release selection resilient ([#33](https://github.com/petauron/vastora/issues/33)) ([bc9fae1](https://github.com/petauron/vastora/commit/bc9fae1ecc2ded174d1e21b50628a9eeae93cf13))
* resolve draft release target commit ([#32](https://github.com/petauron/vastora/issues/32)) ([f2a0d20](https://github.com/petauron/vastora/commit/f2a0d20e3606dfdbe2061c5003c84e1bf7d75b32))
* resolve draft releases from the release list ([#31](https://github.com/petauron/vastora/issues/31)) ([72720dd](https://github.com/petauron/vastora/commit/72720dda2885b14653d3f9a808b416fee9226411))
* verify release checksum from dist directory ([#30](https://github.com/petauron/vastora/issues/30)) ([f86e35a](https://github.com/petauron/vastora/commit/f86e35a44fac6ac31600306f9e7c4db540e8ff51))

## [0.1.0-alpha.4](https://github.com/petauron/vastora/compare/v0.1.0-alpha.3...v0.1.0-alpha.4) (2026-08-19)


### Bug Fixes

* harden upgrades and runtime lifecycle ([#28](https://github.com/petauron/vastora/issues/28)) ([7912d1c](https://github.com/petauron/vastora/commit/7912d1c41d83d7634a4e69d8cd23afddf22ddad4))

## [0.1.0-alpha.3](https://github.com/petauron/vastora/compare/v0.1.0-alpha.2...v0.1.0-alpha.3) (2026-08-18)


### Features

* add forward-only database migrations ([#26](https://github.com/petauron/vastora/issues/26)) ([47d84b4](https://github.com/petauron/vastora/commit/47d84b461afcddca2fe2155db468ed53ad4c5ea7))

## [0.1.0-alpha.2](https://github.com/petauron/vastora/compare/v0.1.0-alpha.1...v0.1.0-alpha.2) (2026-08-18)


### Features

* add optional shared 443 gateway ([#24](https://github.com/petauron/vastora/issues/24)) ([976d774](https://github.com/petauron/vastora/commit/976d77457d192bcd21c2d21dbc3af0d64199c04f))

## 0.1.0-alpha.1 (2026-08-18)

### Features

* add guided Center first-run setup ([#7](https://github.com/petauron/vastora/issues/7)) ([20e4da0](https://github.com/petauron/vastora/commit/20e4da09181715259c19c5e5d792ecd9102fdf9f))
* bootstrap Center through an SSH tunnel ([#17](https://github.com/petauron/vastora/issues/17)) ([097e07d](https://github.com/petauron/vastora/commit/097e07d03eb4a2fe5ecd86483a6082b358a3cc3e))
* initialize Petauron Vastora ([2e24f3a](https://github.com/petauron/vastora/commit/2e24f3abc78c55aee6f5a3dbec347ec3b4bc885e))

### Bug Fixes

* bind containerized Center to loopback ([#20](https://github.com/petauron/vastora/issues/20)) ([0bfb835](https://github.com/petauron/vastora/commit/0bfb835f98d9447b8c45151613e08bbfe26e279d))
* publish and package Linux amd64 releases only ([#13](https://github.com/petauron/vastora/issues/13)) ([1e97ea1](https://github.com/petauron/vastora/commit/1e97ea10db4f7e98e9ce35ef31e028b2abbad9fe))

### Security

* scan final container images, upload SARIF reports, and attest released image digests ([#15](https://github.com/petauron/vastora/issues/15)) ([d06ef57](https://github.com/petauron/vastora/commit/d06ef5753d4da237038997048af777c19a098366))
