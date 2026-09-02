# Changelog

## [0.1.0-alpha.93](https://github.com/petauron/vastora/compare/v0.1.0-alpha.92...v0.1.0-alpha.93) (2026-09-02)


### Features

* **catalog:** update 3x-ui to 3.7.0 ([21dccd0](https://github.com/petauron/vastora/commit/21dccd0bc96a785195103e497117c03011f8697d))


### Bug Fixes

* **catalog:** version the updated CPA package ([2c9127a](https://github.com/petauron/vastora/commit/2c9127a3647b2170699c6d9484d985e695a14a9a))

## [0.1.0-alpha.92](https://github.com/petauron/vastora/compare/v0.1.0-alpha.91...v0.1.0-alpha.92) (2026-09-02)


### Features

* **release:** publish GitHub-generated release notes ([93ab48a](https://github.com/petauron/vastora/commit/93ab48a1946b68b1cca465e076cc4ad8fd9fabe2))


### Bug Fixes

* address PR review and CI contract findings ([2e51600](https://github.com/petauron/vastora/commit/2e51600796bd2094d8d34e2aa164303d151de939))
* **assistant:** isolate runtime diagnostics and inspect tool values ([4fc9c69](https://github.com/petauron/vastora/commit/4fc9c69bf2d260451ffdbc9b625ae82176faaf0e))
* **assistant:** reject credential-like chat input ([ed4b866](https://github.com/petauron/vastora/commit/ed4b86644f0e9ce4039aaeb301bf3a5fb5c2bb7f))
* **reality:** enforce pinned target network policy ([4eaf159](https://github.com/petauron/vastora/commit/4eaf1599f4611d49dc5bac054957c78b72df23e1))
* **reality:** make ASN advisory behind HAProxy ([2e5fed5](https://github.com/petauron/vastora/commit/2e5fed5f68d8a7a71b730c15e1abc429b6b99b00))
* **security:** close Headscale alternate HTTP routes ([e10d6ae](https://github.com/petauron/vastora/commit/e10d6ae41fe55eb7087025a0b50f56c01775882b))
* **security:** construct a single-route Headscale transport ([b6721ed](https://github.com/petauron/vastora/commit/b6721edd74f4bdc50cc3f58927ad61eac60cad72))
* **security:** pin Headscale HTTP destinations ([d3256f0](https://github.com/petauron/vastora/commit/d3256f0a05511befcaf4282ab24d2af9a3667671))
* **tailscale:** repair fixed endpoint runtime drift ([d137a4b](https://github.com/petauron/vastora/commit/d137a4b0d14973753a906bf360a735a77a5bc03c))
* **tailscale:** verify managed DERP map at runtime ([1bfe0a1](https://github.com/petauron/vastora/commit/1bfe0a1d5faad79d443b337ae5880694e3defaaf))
* **web:** explain managed REALITY shared 443 ([6865d8f](https://github.com/petauron/vastora/commit/6865d8ffd5d8c6383fb08913fdbd8cc044cb83c5))

## [0.1.0-alpha.91](https://github.com/petauron/vastora/compare/v0.1.0-alpha.90...v0.1.0-alpha.91) (2026-09-02)


### Features

* automate CPA credential lifecycle ([f4b353a](https://github.com/petauron/vastora/commit/f4b353af85ed0c1656f8a9c0f1ebcd37ea19d68f))
* keep only the active installer in R2 ([9476af8](https://github.com/petauron/vastora/commit/9476af8360ff4d28badf6b22ce66d1130ff787e6))
* make bundled Headscale DNS explicit ([34d0d50](https://github.com/petauron/vastora/commit/34d0d50c11510fbf9fa69a3cdb445d21cf158425))
* make external helpers explicit ([067e218](https://github.com/petauron/vastora/commit/067e218b6cda4b500447fb976370d1e9d44070d2))


### Bug Fixes

* align catalog contract boundary validation ([2d6d859](https://github.com/petauron/vastora/commit/2d6d85906e7f402be633d86fb6940dd5ac4f66ca))
* cancel pending Agent updates before uninstall ([324128c](https://github.com/petauron/vastora/commit/324128c7e369ed4e70d95076597773d4b91f9be2))
* cancel pending host cleanup during local uninstall ([6475e05](https://github.com/petauron/vastora/commit/6475e0536eb1481175b5890c23a63b60130abe58))
* complete secure task recovery ([d1a1ec4](https://github.com/petauron/vastora/commit/d1a1ec4f832d3d8cd427de4a297bd42943f0c679))
* fence redirected catalog revalidation ([0cc7c1d](https://github.com/petauron/vastora/commit/0cc7c1d6214e0832126422a5bde634283810e8b3))
* generate subscription IDs for initial VLESS clients ([54d264d](https://github.com/petauron/vastora/commit/54d264dfcc810c8d8d6cfac8a926cbdfd14dc6d7))
* harden Center backup recovery ([d7dab95](https://github.com/petauron/vastora/commit/d7dab955ce3d19e0cca85d3f46b02e26f2e9fe69))
* keep shared subscriptions on the public URL ([42933ae](https://github.com/petauron/vastora/commit/42933aea16aa863eef47f4fcb37ebcd9aa886f6f))
* load Center Access state in apps ([45347c5](https://github.com/petauron/vastora/commit/45347c571cddc3ea37ed7048bf824fcd71f788ff))
* make host cleanup finalization independently resumable ([7dc6eef](https://github.com/petauron/vastora/commit/7dc6eef4840d35f622694b5facfec01af5d8bcd7))
* normalize Headscale DNS inputs ([c60c4d2](https://github.com/petauron/vastora/commit/c60c4d2083bec9fc814fc1c919e6bf5419f87fb9))
* persist completed host cleanup before acknowledgement ([e65548d](https://github.com/petauron/vastora/commit/e65548dc909120dd1325245c8e4d5a3f32292843))
* preserve Keeper login during CPA rotation ([e171aa7](https://github.com/petauron/vastora/commit/e171aa76205f93ede5a15ffac75ef7d270c8110f))
* preserve uninstall ownership across interrupted cleanup ([161bf87](https://github.com/petauron/vastora/commit/161bf87e72c4d40534760dbf75387c5b00863616))
* reconcile uncertain application deployments ([b274d79](https://github.com/petauron/vastora/commit/b274d79575217e964a67945a0726e5f5356a125f))
* remove deployment-specific routing defaults ([8cd45b3](https://github.com/petauron/vastora/commit/8cd45b39de45c12990ecba4c18fc4ef7a41d0773))
* report Agent cleanup through bootstrap endpoint ([c2449ae](https://github.com/petauron/vastora/commit/c2449aee85fcee134a584b5f85f52bb8c7d9fee5))
* resolve CPA lifecycle CI regressions ([5684cf5](https://github.com/petauron/vastora/commit/5684cf5279f7dd40b8ab5e9a1cfad2f1d824b034))
* resolve lifecycle CI regressions ([ed07b70](https://github.com/petauron/vastora/commit/ed07b70aa598881921e48c85b361c4a448cef233))
* restore CI contract consistency ([a29bb5f](https://github.com/petauron/vastora/commit/a29bb5f23ec59d2832436d0046df9d3132af04d4))
* satisfy CPA lifecycle checks ([8571649](https://github.com/petauron/vastora/commit/8571649f7335c5a861a5b42ce2ed575950f49bda))
* satisfy decommission integration static checks ([3248337](https://github.com/petauron/vastora/commit/3248337916016af0b6309f2f423c6d33866767cd))
* validate host dependency ownership before uninstall ([f5b5af0](https://github.com/petauron/vastora/commit/f5b5af07e8e9e9b4533a5f9e50bdb3c33fe9bc47))

## [0.1.0-alpha.90](https://github.com/petauron/vastora/compare/v0.1.0-alpha.89...v0.1.0-alpha.90) (2026-09-01)


### Bug Fixes

* **3x-ui:** serve VLESS directly from each node ([0fb6dc7](https://github.com/petauron/vastora/commit/0fb6dc702134e08fb21493b47a553fb260dfd030))
* **center:** preserve shared 443 collision guard ([4e2a6c2](https://github.com/petauron/vastora/commit/4e2a6c2c4534b0ccb9e3247efdc95ab28a313c55))

## [0.1.0-alpha.89](https://github.com/petauron/vastora/compare/v0.1.0-alpha.88...v0.1.0-alpha.89) (2026-09-01)


### Features

* **agent:** update Agents through Center ([4269591](https://github.com/petauron/vastora/commit/4269591a11e5cc8a31dc490bc47e0ae427848f29))

## [0.1.0-alpha.88](https://github.com/petauron/vastora/compare/v0.1.0-alpha.87...v0.1.0-alpha.88) (2026-09-01)


### Bug Fixes

* **agent:** accept REALITY region in encrypted tasks ([32fcca3](https://github.com/petauron/vastora/commit/32fcca335dec82f5d18512bb712ff486e9fec7a7))

## [0.1.0-alpha.87](https://github.com/petauron/vastora/compare/v0.1.0-alpha.86...v0.1.0-alpha.87) (2026-09-01)


### Bug Fixes

* **3x-ui:** allow automatic REALITY target discovery ([70e0142](https://github.com/petauron/vastora/commit/70e01428054060ab96af160bd599fed4712d846b))

## [0.1.0-alpha.86](https://github.com/petauron/vastora/compare/v0.1.0-alpha.85...v0.1.0-alpha.86) (2026-09-01)


### Features

* **3x-ui:** model VPS traffic as monthly plans ([df8a1df](https://github.com/petauron/vastora/commit/df8a1df41d08a91e3acef8eab2fb00ceae48754a))


### Bug Fixes

* **agent:** start unused gateway nodes without Caddy ([2b195e1](https://github.com/petauron/vastora/commit/2b195e187915b2518ac2f14e9d010d904856e73a))
* **center:** refresh the page after updating ([f6cef20](https://github.com/petauron/vastora/commit/f6cef20948a1112864c900b6b5e409d594fa6261))
* **web:** show domain blockers inside the dialog ([0067161](https://github.com/petauron/vastora/commit/006716114f1a3b51b43e6bc4a599541cd327b78b))

## [0.1.0-alpha.85](https://github.com/petauron/vastora/compare/v0.1.0-alpha.84...v0.1.0-alpha.85) (2026-09-01)


### Features

* **3x-ui:** simplify VLESS REALITY creation ([9fbfd3d](https://github.com/petauron/vastora/commit/9fbfd3dc49e8ec484dfef612100bb86e24c18617))


### Bug Fixes

* **3x-ui:** align REALITY automation checks ([de45f85](https://github.com/petauron/vastora/commit/de45f85bf1405222b555d95cb9567d3e9b5a9d6a))

## [0.1.0-alpha.84](https://github.com/petauron/vastora/compare/v0.1.0-alpha.83...v0.1.0-alpha.84) (2026-09-01)


### Features

* **gateway:** randomize public hostnames ([381eeba](https://github.com/petauron/vastora/commit/381eeba569a767b18f5b05b0ad552ca05514e788))

## [0.1.0-alpha.83](https://github.com/petauron/vastora/compare/v0.1.0-alpha.82...v0.1.0-alpha.83) (2026-09-01)


### Features

* **3x-ui:** allow protected credential retrieval ([0ac217a](https://github.com/petauron/vastora/commit/0ac217a9c4e738c8a164626d225590c81a21f38e))

## [0.1.0-alpha.82](https://github.com/petauron/vastora/compare/v0.1.0-alpha.81...v0.1.0-alpha.82) (2026-09-01)


### Bug Fixes

* **gateway:** restore dedicated public hostnames ([23e4f2c](https://github.com/petauron/vastora/commit/23e4f2c92263359a9f3dd15e91dd58822027f706))

## [0.1.0-alpha.81](https://github.com/petauron/vastora/compare/v0.1.0-alpha.80...v0.1.0-alpha.81) (2026-08-31)


### Bug Fixes

* **gateway:** route colocated 3x-ui through Docker DNS ([f651096](https://github.com/petauron/vastora/commit/f6510962c599325bfcd79e1e992acee21c1dab27))

## [0.1.0-alpha.80](https://github.com/petauron/vastora/compare/v0.1.0-alpha.79...v0.1.0-alpha.80) (2026-08-31)


### Bug Fixes

* **gateway:** keep tunnel origins on plaintext HTTP ([0da66f4](https://github.com/petauron/vastora/commit/0da66f4f21c27998b0fe7ec980df7eebc6dc6f7a))

## [0.1.0-alpha.79](https://github.com/petauron/vastora/compare/v0.1.0-alpha.78...v0.1.0-alpha.79) (2026-08-31)


### Bug Fixes

* restore protected panel publication ([1d504d2](https://github.com/petauron/vastora/commit/1d504d24da5a7deb5e273ad5edc3eb9cfebfacbf))

## [0.1.0-alpha.78](https://github.com/petauron/vastora/compare/v0.1.0-alpha.77...v0.1.0-alpha.78) (2026-08-31)


### Features

* unify public services behind shared gateway ([f503703](https://github.com/petauron/vastora/commit/f503703e9f196bde56f957778f8e152e0ea92982))

## [0.1.0-alpha.77](https://github.com/petauron/vastora/compare/v0.1.0-alpha.76...v0.1.0-alpha.77) (2026-08-31)


### Features

* **network:** detect agent public egress at startup ([5d2ae30](https://github.com/petauron/vastora/commit/5d2ae30f9b5bf9c83a7aa6220a95fa2d4df219f3))

## [0.1.0-alpha.76](https://github.com/petauron/vastora/compare/v0.1.0-alpha.75...v0.1.0-alpha.76) (2026-08-31)


### Features

* **network:** detect verified cloud NAT ingress ([#260](https://github.com/petauron/vastora/issues/260)) ([cc433a2](https://github.com/petauron/vastora/commit/cc433a2dcea772272028bbc060de466495a5f3f1))

## [0.1.0-alpha.75](https://github.com/petauron/vastora/compare/v0.1.0-alpha.74...v0.1.0-alpha.75) (2026-08-31)


### Bug Fixes

* **deployer:** normalize Headscale key commit prefixes ([f059f7d](https://github.com/petauron/vastora/commit/f059f7d7c530196d7527acb8a2ee8d85fc5168c6))

## [0.1.0-alpha.74](https://github.com/petauron/vastora/compare/v0.1.0-alpha.73...v0.1.0-alpha.74) (2026-08-31)


### Bug Fixes

* **deployer:** normalize Headscale API key prefixes ([95f3554](https://github.com/petauron/vastora/commit/95f35546cdba03df9da39a16a8dee2b5206fa435))

## [0.1.0-alpha.73](https://github.com/petauron/vastora/compare/v0.1.0-alpha.72...v0.1.0-alpha.73) (2026-08-30)


### Bug Fixes

* **center:** recover updates and report progress ([a5bebbd](https://github.com/petauron/vastora/commit/a5bebbd3fae5ab5cdf17a64392e1e3bef682df8b))

## [0.1.0-alpha.72](https://github.com/petauron/vastora/compare/v0.1.0-alpha.71...v0.1.0-alpha.72) (2026-08-30)


### Bug Fixes

* **upgrade:** migrate legacy runtime network ([ac5a3e3](https://github.com/petauron/vastora/commit/ac5a3e3cfb928b96b25b7b6b49c4933b9c5de109))

## [0.1.0-alpha.71](https://github.com/petauron/vastora/compare/v0.1.0-alpha.70...v0.1.0-alpha.71) (2026-08-30)


### Features

* **catalog:** freeze v0.1 interoperability contract ([#242](https://github.com/petauron/vastora/issues/242)) ([2c454a5](https://github.com/petauron/vastora/commit/2c454a588d1317de0d5baf8ca1f6e47b194fd0d5)), closes [#1](https://github.com/petauron/vastora/issues/1)
* complete verified catalog lifecycle ([e9bbd02](https://github.com/petauron/vastora/commit/e9bbd026f03f53b72809988f277b1f315447c7fc))
* secure Agent control plane and node runtime ([2829d4e](https://github.com/petauron/vastora/commit/2829d4ec9e7a95b2c1bd00d8122816d847d7cb54))


### Bug Fixes

* complete secure Center recovery ([4c635fd](https://github.com/petauron/vastora/commit/4c635fd2da8ffd3623724dbc570b794a9f10f29e))
* enforce canonical catalog contracts ([678c2e2](https://github.com/petauron/vastora/commit/678c2e2b6c99ddcd202676d3f2a8795d64132bfc))
* harden deployment and gateway defaults ([d553d1b](https://github.com/petauron/vastora/commit/d553d1bd0f932f56c05d4be34f58bb8bc9946d97))
* make Center updates recoverable ([49caab1](https://github.com/petauron/vastora/commit/49caab14e5ff98c457f7bac6f2b148fe206bd757))

## [0.1.0-alpha.70](https://github.com/petauron/vastora/compare/v0.1.0-alpha.69...v0.1.0-alpha.70) (2026-08-29)


### Bug Fixes

* use a flat Center remote hostname ([#203](https://github.com/petauron/vastora/issues/203)) ([be068b3](https://github.com/petauron/vastora/commit/be068b39c57217020fc9ab68e4a112916732143c))

## [0.1.0-alpha.69](https://github.com/petauron/vastora/compare/v0.1.0-alpha.68...v0.1.0-alpha.69) (2026-08-29)


### Features

* show update progress and fix Cloudflare authorization ([#201](https://github.com/petauron/vastora/issues/201)) ([4cf3ef5](https://github.com/petauron/vastora/commit/4cf3ef59e374d54b7f9f6dfff27eb08a4346067a))

## [0.1.0-alpha.68](https://github.com/petauron/vastora/compare/v0.1.0-alpha.67...v0.1.0-alpha.68) (2026-08-29)


### Features

* **security:** prevent REALITY fallback relay abuse ([#199](https://github.com/petauron/vastora/issues/199)) ([4a73e63](https://github.com/petauron/vastora/commit/4a73e63566c653aa4079b8781bf448f4c27f9b5f)), closes [#198](https://github.com/petauron/vastora/issues/198)

## [0.1.0-alpha.67](https://github.com/petauron/vastora/compare/v0.1.0-alpha.66...v0.1.0-alpha.67) (2026-08-29)


### Bug Fixes

* **web:** keep remote access sheet responsive ([#195](https://github.com/petauron/vastora/issues/195)) ([eceba45](https://github.com/petauron/vastora/commit/eceba45a1e3fcce070fd73decebf4ab6bc9ecd28))

## [0.1.0-alpha.66](https://github.com/petauron/vastora/compare/v0.1.0-alpha.65...v0.1.0-alpha.66) (2026-08-29)


### Features

* **center:** add Access-protected remote fallback ([#193](https://github.com/petauron/vastora/issues/193)) ([575bd33](https://github.com/petauron/vastora/commit/575bd33d6904ca487ce4045ec5591b2ab14708b5))

## [0.1.0-alpha.65](https://github.com/petauron/vastora/compare/v0.1.0-alpha.64...v0.1.0-alpha.65) (2026-08-29)


### Bug Fixes

* **gateway:** restore system service protection ([#191](https://github.com/petauron/vastora/issues/191)) ([cc9f1a4](https://github.com/petauron/vastora/commit/cc9f1a4480e55ace97b4fa63276dbc08a86e82cd))

## [0.1.0-alpha.64](https://github.com/petauron/vastora/compare/v0.1.0-alpha.63...v0.1.0-alpha.64) (2026-08-29)


### Bug Fixes

* **gateway:** recover full runtime state ([#189](https://github.com/petauron/vastora/issues/189)) ([e04dc41](https://github.com/petauron/vastora/commit/e04dc41492aa6552491cdcd1d5cb860f95a29b55))

## [0.1.0-alpha.63](https://github.com/petauron/vastora/compare/v0.1.0-alpha.62...v0.1.0-alpha.63) (2026-08-29)


### Bug Fixes

* **center:** restore private gateway listeners ([#187](https://github.com/petauron/vastora/issues/187)) ([3a7231a](https://github.com/petauron/vastora/commit/3a7231a3d116cb05950ce96d6676d602888df037))

## [0.1.0-alpha.62](https://github.com/petauron/vastora/compare/v0.1.0-alpha.61...v0.1.0-alpha.62) (2026-08-29)


### Bug Fixes

* recover NAT 1:1 gateways with split DNS ([#185](https://github.com/petauron/vastora/issues/185)) ([e43e60f](https://github.com/petauron/vastora/commit/e43e60f9bc61cd4c7f40d2020c60594ebe51e551))

## [0.1.0-alpha.61](https://github.com/petauron/vastora/compare/v0.1.0-alpha.60...v0.1.0-alpha.61) (2026-08-29)


### Bug Fixes

* complete Tailscale direct endpoint upgrades ([#183](https://github.com/petauron/vastora/issues/183)) ([a0c0072](https://github.com/petauron/vastora/commit/a0c007206569c6df2ded4a19bdf0e97ffa29a8fe))

## [0.1.0-alpha.60](https://github.com/petauron/vastora/compare/v0.1.0-alpha.59...v0.1.0-alpha.60) (2026-08-28)


### Bug Fixes

* make release retries immutable ([#180](https://github.com/petauron/vastora/issues/180)) ([7471d2c](https://github.com/petauron/vastora/commit/7471d2c1d577cea1212aa2fc8cf33b47e8332ba2))

## [0.1.0-alpha.59](https://github.com/petauron/vastora/compare/v0.1.0-alpha.58...v0.1.0-alpha.59) (2026-08-28)


### Features

* add managed Tailscale direct endpoints ([#179](https://github.com/petauron/vastora/issues/179)) ([e3fb376](https://github.com/petauron/vastora/commit/e3fb3768e9d171f39d45ec577845cc9b6a8f2726)), closes [#178](https://github.com/petauron/vastora/issues/178)


### Bug Fixes

* use immutable R2 releases for Center updates ([#176](https://github.com/petauron/vastora/issues/176)) ([56e235a](https://github.com/petauron/vastora/commit/56e235a46e92dc6a6ca9cddee0ba4b00c2d52597))

## [0.1.0-alpha.58](https://github.com/petauron/vastora/compare/v0.1.0-alpha.57...v0.1.0-alpha.58) (2026-08-28)


### Features

* isolate managed application runtimes ([#174](https://github.com/petauron/vastora/issues/174)) ([155991d](https://github.com/petauron/vastora/commit/155991d013b4cfed85034e53caeb3254981d55f1))

## [0.1.0-alpha.57](https://github.com/petauron/vastora/compare/v0.1.0-alpha.56...v0.1.0-alpha.57) (2026-08-27)


### Features

* use IPv4-only managed networking ([#172](https://github.com/petauron/vastora/issues/172)) ([d20e76b](https://github.com/petauron/vastora/commit/d20e76b8c688bd8c11ac405901ce9c20140100c2))

## [0.1.0-alpha.56](https://github.com/petauron/vastora/compare/v0.1.0-alpha.55...v0.1.0-alpha.56) (2026-08-27)


### Bug Fixes

* read release version from R2 installer ([#171](https://github.com/petauron/vastora/issues/171)) ([c4dda9a](https://github.com/petauron/vastora/commit/c4dda9afa5e3ea1c95e198b4ce6dfa8971c89062))
* use current tooling for release retries ([#167](https://github.com/petauron/vastora/issues/167)) ([4266443](https://github.com/petauron/vastora/commit/42664435b81866d5c1a6ee211b3392ffc668b586))

## [0.1.0-alpha.55](https://github.com/petauron/vastora/compare/v0.1.0-alpha.54...v0.1.0-alpha.55) (2026-08-27)


### Bug Fixes

* isolate Headscale clients from Tailscale services ([#163](https://github.com/petauron/vastora/issues/163)) ([5b7d104](https://github.com/petauron/vastora/commit/5b7d104aa9b34482f85a2f895757386fccdc683c))

## [0.1.0-alpha.54](https://github.com/petauron/vastora/compare/v0.1.0-alpha.53...v0.1.0-alpha.54) (2026-08-27)


### Features

* harden network and host lifecycle ([#161](https://github.com/petauron/vastora/issues/161)) ([93f0f84](https://github.com/petauron/vastora/commit/93f0f849116f86b340fc0167c0218a9bdea75a89)), closes [#159](https://github.com/petauron/vastora/issues/159) [#160](https://github.com/petauron/vastora/issues/160)

## [0.1.0-alpha.53](https://github.com/petauron/vastora/compare/v0.1.0-alpha.52...v0.1.0-alpha.53) (2026-08-26)


### Features

* add guided Center uninstall and domain recovery ([#157](https://github.com/petauron/vastora/issues/157)) ([4824e15](https://github.com/petauron/vastora/commit/4824e15ada3e9b7b59f718d40cf83bea5854d755))

## [0.1.0-alpha.52](https://github.com/petauron/vastora/compare/v0.1.0-alpha.51...v0.1.0-alpha.52) (2026-08-26)


### Bug Fixes

* **ci:** allow release PR comments ([#154](https://github.com/petauron/vastora/issues/154)) ([87fa002](https://github.com/petauron/vastora/commit/87fa002297c78bd47ff726459f7a7ebb436266ab))
* keep Center settings consistent after changes ([#156](https://github.com/petauron/vastora/issues/156)) ([dead798](https://github.com/petauron/vastora/commit/dead798d37aa8aa3310002b8650c99d7521c10de))

## [0.1.0-alpha.51](https://github.com/petauron/vastora/compare/v0.1.0-alpha.50...v0.1.0-alpha.51) (2026-08-26)


### Features

* add safe control plane migrations ([#147](https://github.com/petauron/vastora/issues/147)) ([3eb4a44](https://github.com/petauron/vastora/commit/3eb4a44b5ffe736912e635ce0ba66c77fde6758f))

## [0.1.0-alpha.50](https://github.com/petauron/vastora/compare/v0.1.0-alpha.49...v0.1.0-alpha.50) (2026-08-25)


### Bug Fixes

* support browsers without media query events ([#145](https://github.com/petauron/vastora/issues/145)) ([e1c1d0f](https://github.com/petauron/vastora/commit/e1c1d0f7a9c84636d0ff44bd65bb9de6e37da8b1))

## [0.1.0-alpha.49](https://github.com/petauron/vastora/compare/v0.1.0-alpha.48...v0.1.0-alpha.49) (2026-08-25)


### Features

* polish setup controls and REALITY defaults ([#143](https://github.com/petauron/vastora/issues/143)) ([9f39459](https://github.com/petauron/vastora/commit/9f394593e665dedd46e0f67b39907913a617ffc5))

## [0.1.0-alpha.48](https://github.com/petauron/vastora/compare/v0.1.0-alpha.47...v0.1.0-alpha.48) (2026-08-25)


### Features

* add built-in Center updates ([#141](https://github.com/petauron/vastora/issues/141)) ([b11054b](https://github.com/petauron/vastora/commit/b11054b5625cb216d54623494d0747940b11ccc2))

## [0.1.0-alpha.47](https://github.com/petauron/vastora/compare/v0.1.0-alpha.46...v0.1.0-alpha.47) (2026-08-24)


### Features

* verify public ingress before installation ([#139](https://github.com/petauron/vastora/issues/139)) ([d0b6388](https://github.com/petauron/vastora/commit/d0b63882edfa9b13a611857056027cdcd1f94d6c))

## [0.1.0-alpha.46](https://github.com/petauron/vastora/compare/v0.1.0-alpha.45...v0.1.0-alpha.46) (2026-08-24)


### Features

* support amd64 and arm64 deployments ([#137](https://github.com/petauron/vastora/issues/137)) ([65dc87c](https://github.com/petauron/vastora/commit/65dc87cc32c5219ce105d4b794e1fdf5fb1d0c6f))

## [0.1.0-alpha.45](https://github.com/petauron/vastora/compare/v0.1.0-alpha.44...v0.1.0-alpha.45) (2026-08-24)


### Bug Fixes

* harden 3x-ui lifecycle reliability ([#135](https://github.com/petauron/vastora/issues/135)) ([457d3b3](https://github.com/petauron/vastora/commit/457d3b3d7a2f55fd4a3cef42d57758b0497686b8))

## [0.1.0-alpha.44](https://github.com/petauron/vastora/compare/v0.1.0-alpha.43...v0.1.0-alpha.44) (2026-08-23)


### Bug Fixes

* normalize 3x-ui subscription node names ([#132](https://github.com/petauron/vastora/issues/132)) ([0088105](https://github.com/petauron/vastora/commit/00881055df6d23a48bb29300d6ea4bea83f3550a))

## [0.1.0-alpha.43](https://github.com/petauron/vastora/compare/v0.1.0-alpha.42...v0.1.0-alpha.43) (2026-08-23)


### Features

* add independent VLESS traffic plans ([#130](https://github.com/petauron/vastora/issues/130)) ([4b7a7f8](https://github.com/petauron/vastora/commit/4b7a7f8a8df09d48a26ce946f539aa49c76373df))

## [0.1.0-alpha.42](https://github.com/petauron/vastora/compare/v0.1.0-alpha.41...v0.1.0-alpha.42) (2026-08-23)


### Features

* deliver agent tasks instantly ([#128](https://github.com/petauron/vastora/issues/128)) ([7f8c423](https://github.com/petauron/vastora/commit/7f8c42329e138399b26bb749e5a990017e5db319))

## [0.1.0-alpha.41](https://github.com/petauron/vastora/compare/v0.1.0-alpha.40...v0.1.0-alpha.41) (2026-08-23)


### Bug Fixes

* harden migrations and app workflows ([#126](https://github.com/petauron/vastora/issues/126)) ([9aee583](https://github.com/petauron/vastora/commit/9aee583daa92d9536f72b8c0fc137e21eb9a0bb3))

## [0.1.0-alpha.40](https://github.com/petauron/vastora/compare/v0.1.0-alpha.39...v0.1.0-alpha.40) (2026-08-23)


### Features

* share site certificates and localize node names ([#124](https://github.com/petauron/vastora/issues/124)) ([0c38e5b](https://github.com/petauron/vastora/commit/0c38e5b58551a175fb58c17b9881af4b6d3a7a16))

## [0.1.0-alpha.39](https://github.com/petauron/vastora/compare/v0.1.0-alpha.38...v0.1.0-alpha.39) (2026-08-23)


### Features

* add VLESS region prefixes ([#122](https://github.com/petauron/vastora/issues/122)) ([e8d4c4e](https://github.com/petauron/vastora/commit/e8d4c4ee2129961473c817eb8e144b6a91899e97))

## [0.1.0-alpha.38](https://github.com/petauron/vastora/compare/v0.1.0-alpha.37...v0.1.0-alpha.38) (2026-08-23)


### Features

* improve multi-node VLESS management ([#120](https://github.com/petauron/vastora/issues/120)) ([4fd2e06](https://github.com/petauron/vastora/commit/4fd2e066494c38a8f7f21bd9407d1b9b974a7cbb))

## [0.1.0-alpha.37](https://github.com/petauron/vastora/compare/v0.1.0-alpha.36...v0.1.0-alpha.37) (2026-08-23)


### Bug Fixes

* synchronize 3x-ui clients across nodes ([#118](https://github.com/petauron/vastora/issues/118)) ([443b8c3](https://github.com/petauron/vastora/commit/443b8c3494f941634162d921f80989bad8e08173))

## [0.1.0-alpha.36](https://github.com/petauron/vastora/compare/v0.1.0-alpha.35...v0.1.0-alpha.36) (2026-08-23)


### Bug Fixes

* link release metadata check to workflow logs ([#116](https://github.com/petauron/vastora/issues/116)) ([1525dda](https://github.com/petauron/vastora/commit/1525dda38860c38008ed94767e41c24e35e1a814))

## [0.1.0-alpha.35](https://github.com/petauron/vastora/compare/v0.1.0-alpha.34...v0.1.0-alpha.35) (2026-08-23)


### Features

* support resilient 3x-ui site controllers ([#113](https://github.com/petauron/vastora/issues/113)) ([d570a0e](https://github.com/petauron/vastora/commit/d570a0e76b0c89754fb0a864b4528ae393cc7b64))

## [0.1.0-alpha.34](https://github.com/petauron/vastora/compare/v0.1.0-alpha.33...v0.1.0-alpha.34) (2026-08-22)


### Bug Fixes

* dispatch trusted release checks automatically ([#96](https://github.com/petauron/vastora/issues/96)) ([2d98e4a](https://github.com/petauron/vastora/commit/2d98e4a71643d183861a37255584d889b290030b))

## [0.1.0-alpha.33](https://github.com/petauron/vastora/compare/v0.1.0-alpha.32...v0.1.0-alpha.33) (2026-08-22)


### Bug Fixes

* restore co-located gateways after upgrades ([#94](https://github.com/petauron/vastora/issues/94)) ([bcedf56](https://github.com/petauron/vastora/commit/bcedf56753de479067ded4d5bb702a48838f3237))

## [0.1.0-alpha.32](https://github.com/petauron/vastora/compare/v0.1.0-alpha.31...v0.1.0-alpha.32) (2026-08-22)


### Bug Fixes

* synchronize co-located upgrades and tailnet DNS ([#92](https://github.com/petauron/vastora/issues/92)) ([72105f9](https://github.com/petauron/vastora/commit/72105f977baa6b2804ff0b7dc12c4b3607d03b7d))

## [0.1.0-alpha.31](https://github.com/petauron/vastora/compare/v0.1.0-alpha.30...v0.1.0-alpha.31) (2026-08-22)


### Features

* keep Center private behind Headscale ([#90](https://github.com/petauron/vastora/issues/90)) ([31330e5](https://github.com/petauron/vastora/commit/31330e5b7e7ad710fbee46bd5b3b2f5e4bf5d529))

## [0.1.0-alpha.30](https://github.com/petauron/vastora/compare/v0.1.0-alpha.29...v0.1.0-alpha.30) (2026-08-22)


### Bug Fixes

* support Mihomo Reality clients ([#87](https://github.com/petauron/vastora/issues/87)) ([e984040](https://github.com/petauron/vastora/commit/e9840403b6c1a904411cb9f920cf8aca0c8e5b91))

## [0.1.0-alpha.29](https://github.com/petauron/vastora/compare/v0.1.0-alpha.28...v0.1.0-alpha.29) (2026-08-22)


### Bug Fixes

* export public Reality endpoints in subscriptions ([#85](https://github.com/petauron/vastora/issues/85)) ([1f2d617](https://github.com/petauron/vastora/commit/1f2d61719f18403c301d0c3eafd9ac85dec80351))

## [0.1.0-alpha.28](https://github.com/petauron/vastora/compare/v0.1.0-alpha.27...v0.1.0-alpha.28) (2026-08-22)


### Bug Fixes

* accept in-process 3x-ui reloads ([#82](https://github.com/petauron/vastora/issues/82)) ([ee7c13f](https://github.com/petauron/vastora/commit/ee7c13f35269416d88fb4918fd772202e860b0eb))

## [0.1.0-alpha.27](https://github.com/petauron/vastora/compare/v0.1.0-alpha.26...v0.1.0-alpha.27) (2026-08-22)


### Bug Fixes

* reload 3x-ui subscription routes ([#80](https://github.com/petauron/vastora/issues/80)) ([5948c44](https://github.com/petauron/vastora/commit/5948c44cec80af640944b0304feeb5c69baa4fed))

## [0.1.0-alpha.26](https://github.com/petauron/vastora/compare/v0.1.0-alpha.25...v0.1.0-alpha.26) (2026-08-22)


### Bug Fixes

* support OpenClash subscriptions ([#78](https://github.com/petauron/vastora/issues/78)) ([1b5313d](https://github.com/petauron/vastora/commit/1b5313d5a2279cfee328abd63f128d69d6e0f50f))

## [0.1.0-alpha.25](https://github.com/petauron/vastora/compare/v0.1.0-alpha.24...v0.1.0-alpha.25) (2026-08-22)


### Features

* manage 3x-ui clients in center ([#76](https://github.com/petauron/vastora/issues/76)) ([6f179a7](https://github.com/petauron/vastora/commit/6f179a73ffefa48df84b058a4998ff75f3213787))

## [0.1.0-alpha.24](https://github.com/petauron/vastora/compare/v0.1.0-alpha.23...v0.1.0-alpha.24) (2026-08-22)


### Bug Fixes

* protect system gateway from stale state ([#74](https://github.com/petauron/vastora/issues/74)) ([5cf09d8](https://github.com/petauron/vastora/commit/5cf09d83a261746eb4793ee7ad51f913d3fe2174))

## [0.1.0-alpha.23](https://github.com/petauron/vastora/compare/v0.1.0-alpha.22...v0.1.0-alpha.23) (2026-08-22)


### Bug Fixes

* unify co-located gateway runtime ([#72](https://github.com/petauron/vastora/issues/72)) ([29ef235](https://github.com/petauron/vastora/commit/29ef2358b52c2a313f3bcd43be225f187a09f6bf))

## [0.1.0-alpha.22](https://github.com/petauron/vastora/compare/v0.1.0-alpha.21...v0.1.0-alpha.22) (2026-08-21)


### Features

* add private HTTPS and 3x-ui subscriptions ([#70](https://github.com/petauron/vastora/issues/70)) ([913d205](https://github.com/petauron/vastora/commit/913d20521e769a396c96622b29ec46108e849173))

## [0.1.0-alpha.21](https://github.com/petauron/vastora/compare/v0.1.0-alpha.20...v0.1.0-alpha.21) (2026-08-20)


### Bug Fixes

* bootstrap HAProxy config on writable tmpfs ([#68](https://github.com/petauron/vastora/issues/68)) ([ca6c970](https://github.com/petauron/vastora/commit/ca6c9707fcc165b48d65d63f9d34e0dd1addf20d))

## [0.1.0-alpha.20](https://github.com/petauron/vastora/compare/v0.1.0-alpha.19...v0.1.0-alpha.20) (2026-08-20)


### Bug Fixes

* verify private services through gateway addresses ([#66](https://github.com/petauron/vastora/issues/66)) ([48883ac](https://github.com/petauron/vastora/commit/48883ac47c8088cad203eb1789d48a0f7b662815))

## [0.1.0-alpha.19](https://github.com/petauron/vastora/compare/v0.1.0-alpha.18...v0.1.0-alpha.19) (2026-08-20)


### Features

* add one-click 3x-ui Reality access ([#64](https://github.com/petauron/vastora/issues/64)) ([367a5fe](https://github.com/petauron/vastora/commit/367a5fef6e2082daecaa9d985a2882dcc47685d6))

## [0.1.0-alpha.18](https://github.com/petauron/vastora/compare/v0.1.0-alpha.17...v0.1.0-alpha.18) (2026-08-20)


### Bug Fixes

* reserve private addresses for co-located gateways ([#62](https://github.com/petauron/vastora/issues/62)) ([1cd2ca5](https://github.com/petauron/vastora/commit/1cd2ca54a593276faf04e0c049663a724b5130e4))

## [0.1.0-alpha.17](https://github.com/petauron/vastora/compare/v0.1.0-alpha.16...v0.1.0-alpha.17) (2026-08-20)


### Features

* support co-located Center and Agent ([#59](https://github.com/petauron/vastora/issues/59)) ([c75cac9](https://github.com/petauron/vastora/commit/c75cac9e5132c4fae9ff409e9e3555bb52553e21))


### Bug Fixes

* use triggering commit for release validation ([#60](https://github.com/petauron/vastora/issues/60)) ([86a6f15](https://github.com/petauron/vastora/commit/86a6f1530f187ba9a8995f6ce3971b218bab7eb4))

## [0.1.0-alpha.16](https://github.com/petauron/vastora/compare/v0.1.0-alpha.15...v0.1.0-alpha.16) (2026-08-20)


### Bug Fixes

* run Agent installer as an executable ([#57](https://github.com/petauron/vastora/issues/57)) ([9b72800](https://github.com/petauron/vastora/commit/9b728008dbe59b9bfc886424cb065d802fc73e71))

## [0.1.0-alpha.15](https://github.com/petauron/vastora/compare/v0.1.0-alpha.14...v0.1.0-alpha.15) (2026-08-20)


### Features

* simplify agent enrollment ([#55](https://github.com/petauron/vastora/issues/55)) ([395929d](https://github.com/petauron/vastora/commit/395929dc22c0650dc99ea0d82f8c9b081bf73443))

## [0.1.0-alpha.14](https://github.com/petauron/vastora/compare/v0.1.0-alpha.13...v0.1.0-alpha.14) (2026-08-20)


### Bug Fixes

* avoid bundled headscale DNS race ([#53](https://github.com/petauron/vastora/issues/53)) ([2fc2df7](https://github.com/petauron/vastora/commit/2fc2df7c39a1939c988103523f44fed56e50cb2a))

## [0.1.0-alpha.13](https://github.com/petauron/vastora/compare/v0.1.0-alpha.12...v0.1.0-alpha.13) (2026-08-20)


### Bug Fixes

* migrate legacy setup domain drafts ([#51](https://github.com/petauron/vastora/issues/51)) ([2495b0a](https://github.com/petauron/vastora/commit/2495b0a0b22e6c63f566b8c8bbf58cd8cd3880bf))

## [0.1.0-alpha.12](https://github.com/petauron/vastora/compare/v0.1.0-alpha.11...v0.1.0-alpha.12) (2026-08-20)


### Features

* namespace Vastora service hostnames ([#49](https://github.com/petauron/vastora/issues/49)) ([a3da00d](https://github.com/petauron/vastora/commit/a3da00d0303cce157e088a3b63b5abdb68f66bff))

## [0.1.0-alpha.11](https://github.com/petauron/vastora/compare/v0.1.0-alpha.10...v0.1.0-alpha.11) (2026-08-20)


### Features

* simplify Center operations and user flows ([#47](https://github.com/petauron/vastora/issues/47)) ([6f16e4a](https://github.com/petauron/vastora/commit/6f16e4a3d0246c654b711c43cf17d90676a99faf))

## [0.1.0-alpha.10](https://github.com/petauron/vastora/compare/v0.1.0-alpha.9...v0.1.0-alpha.10) (2026-08-19)


### Bug Fixes

* use standard HTTPS for bundled gateway ([#45](https://github.com/petauron/vastora/issues/45)) ([0824a05](https://github.com/petauron/vastora/commit/0824a05ca73c8584ab9eaa7ff123ac7e568b9c63))

## [0.1.0-alpha.9](https://github.com/petauron/vastora/compare/v0.1.0-alpha.8...v0.1.0-alpha.9) (2026-08-19)


### Bug Fixes

* make Cloudflare authorization recoverable ([#43](https://github.com/petauron/vastora/issues/43)) ([db1d177](https://github.com/petauron/vastora/commit/db1d177988706ba813c8bd04cb88a3e471b57d3a))

## [0.1.0-alpha.8](https://github.com/petauron/vastora/compare/v0.1.0-alpha.7...v0.1.0-alpha.8) (2026-08-19)


### Bug Fixes

* correct Cloudflare OAuth and simplify setup ([#41](https://github.com/petauron/vastora/issues/41)) ([6748daf](https://github.com/petauron/vastora/commit/6748daf9d651b9fe051b6f7d79c7354f7db9fa03))

## [0.1.0-alpha.7](https://github.com/petauron/vastora/compare/v0.1.0-alpha.6...v0.1.0-alpha.7) (2026-08-19)


### Features

* connect Cloudflare with OAuth ([#39](https://github.com/petauron/vastora/issues/39)) ([bf4c3ba](https://github.com/petauron/vastora/commit/bf4c3bac6de08fdfb12417da248c770e56f44704))

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
