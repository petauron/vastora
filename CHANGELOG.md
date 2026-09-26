# Changelog

## [0.1.0-alpha.220](https://github.com/petauron/vastora/compare/v0.1.0-alpha.219...v0.1.0-alpha.220) (2026-09-26)


### Bug Fixes

* **meridian:** complete node scoring and unlock status handling ([#667](https://github.com/petauron/vastora/issues/667)) ([f126758](https://github.com/petauron/vastora/commit/f126758ac4de1ef6bd12a151f468350ed19fbcd4))

## [0.1.0-alpha.219](https://github.com/petauron/vastora/compare/v0.1.0-alpha.218...v0.1.0-alpha.219) (2026-09-26)


### Bug Fixes

* **meridian:** show node scores and all unlock statuses ([#665](https://github.com/petauron/vastora/issues/665)) ([258de34](https://github.com/petauron/vastora/commit/258de34705d14442ffb49d87b8f425e50be1095f))

## [0.1.0-alpha.218](https://github.com/petauron/vastora/compare/v0.1.0-alpha.217...v0.1.0-alpha.218) (2026-09-25)


### Bug Fixes

* **meridian:** simplify missing IPQS score labels ([#663](https://github.com/petauron/vastora/issues/663)) ([80ffef8](https://github.com/petauron/vastora/commit/80ffef8ecfca95c22dc3da1b9c054636a2ca38b4))

## [0.1.0-alpha.217](https://github.com/petauron/vastora/compare/v0.1.0-alpha.216...v0.1.0-alpha.217) (2026-09-25)


### Features

* **meridian:** score conservatively when IPQS is missing ([#660](https://github.com/petauron/vastora/issues/660)) ([63dbeb5](https://github.com/petauron/vastora/commit/63dbeb52ba30bf1c15790417f3756aa70138327e))

## [0.1.0-alpha.216](https://github.com/petauron/vastora/compare/v0.1.0-alpha.215...v0.1.0-alpha.216) (2026-09-25)


### Features

* **meridian:** assess exit quality and measure landing links ([5adaf05](https://github.com/petauron/vastora/commit/5adaf058e71b9cbaf3bb2ccb5b3f153b112e4ad4))

## [0.1.0-alpha.215](https://github.com/petauron/vastora/compare/v0.1.0-alpha.214...v0.1.0-alpha.215) (2026-09-24)


### Bug Fixes

* **agent:** allow measured TCP egress probe latency ([#653](https://github.com/petauron/vastora/issues/653)) ([1040461](https://github.com/petauron/vastora/commit/1040461e50a3296ac289a6c227c1e523812d3f5f))

## [0.1.0-alpha.214](https://github.com/petauron/vastora/compare/v0.1.0-alpha.213...v0.1.0-alpha.214) (2026-09-24)


### Bug Fixes

* **agent:** advance identical Meridian revisions without Xray reload ([#651](https://github.com/petauron/vastora/issues/651)) ([c08b18d](https://github.com/petauron/vastora/commit/c08b18d2fb29d5d968dde0d4d098f40f73c14151))
* restore full CI baseline after Meridian cutover ([#648](https://github.com/petauron/vastora/issues/648)) ([ef8c34e](https://github.com/petauron/vastora/commit/ef8c34e21097fb16ec61f3ac218ad2d95e25837f))

## [0.1.0-alpha.213](https://github.com/petauron/vastora/compare/v0.1.0-alpha.212...v0.1.0-alpha.213) (2026-09-24)


### Bug Fixes

* remove closed landing gates that mask Meridian routes ([#646](https://github.com/petauron/vastora/issues/646)) ([61f684e](https://github.com/petauron/vastora/commit/61f684e48d8eaf03395965cd230a209395387e47))

## [0.1.0-alpha.212](https://github.com/petauron/vastora/compare/v0.1.0-alpha.211...v0.1.0-alpha.212) (2026-09-24)


### Bug Fixes

* **agent:** snapshot Xray worker state before mutations ([#643](https://github.com/petauron/vastora/issues/643)) ([0a97384](https://github.com/petauron/vastora/commit/0a973849a6b0f2b8faaccd7780720e8451092f72))
* **center:** accept zero-valued Meridian usage counters ([#645](https://github.com/petauron/vastora/issues/645)) ([4f3c76f](https://github.com/petauron/vastora/commit/4f3c76f45de2ba98339cd593a26d952bd7936ce7))

## [0.1.0-alpha.211](https://github.com/petauron/vastora/compare/v0.1.0-alpha.210...v0.1.0-alpha.211) (2026-09-24)


### Bug Fixes

* **meridian:** verify legacy route receipt identities ([417329e](https://github.com/petauron/vastora/commit/417329e6e298c9434a89b5f8e2a8a7643973fc17))

## [0.1.0-alpha.210](https://github.com/petauron/vastora/compare/v0.1.0-alpha.209...v0.1.0-alpha.210) (2026-09-23)


### Bug Fixes

* **meridian:** distinguish heartbeat projection failures from authentication ([#637](https://github.com/petauron/vastora/issues/637)) ([3338890](https://github.com/petauron/vastora/commit/3338890a6788f4ef5bd5d869ecd91300b2009575))

## [0.1.0-alpha.209](https://github.com/petauron/vastora/compare/v0.1.0-alpha.208...v0.1.0-alpha.209) (2026-09-23)


### Bug Fixes

* **meridian:** unblock native-only cutover from legacy landing drift ([#634](https://github.com/petauron/vastora/issues/634)) ([4271021](https://github.com/petauron/vastora/commit/42710218ba42805f4165a571a8aa5737c26c590b))

## [0.1.0-alpha.208](https://github.com/petauron/vastora/compare/v0.1.0-alpha.207...v0.1.0-alpha.208) (2026-09-23)


### Bug Fixes

* **meridian:** expose explicit recovery for uncertain legacy runtime ([#631](https://github.com/petauron/vastora/issues/631)) ([c0db82e](https://github.com/petauron/vastora/commit/c0db82e3a005dbb61b710d4c9e58d987489ef518))

## [0.1.0-alpha.207](https://github.com/petauron/vastora/compare/v0.1.0-alpha.206...v0.1.0-alpha.207) (2026-09-23)


### Bug Fixes

* **center:** claim Meridian deployments with text JSON config ([#629](https://github.com/petauron/vastora/issues/629)) ([606ddab](https://github.com/petauron/vastora/commit/606ddab1b5963c322720c5272f6db197a71d8e51))

## [0.1.0-alpha.206](https://github.com/petauron/vastora/compare/v0.1.0-alpha.205...v0.1.0-alpha.206) (2026-09-23)


### Bug Fixes

* **center:** distinguish Agent claim failures from authentication ([#627](https://github.com/petauron/vastora/issues/627)) ([09a8152](https://github.com/petauron/vastora/commit/09a8152acb625b86f6efe3364bf44060c4c16a9e))

## [0.1.0-alpha.205](https://github.com/petauron/vastora/compare/v0.1.0-alpha.204...v0.1.0-alpha.205) (2026-09-23)


### Bug Fixes

* **center:** keep Meridian subscription origin stable ([#625](https://github.com/petauron/vastora/issues/625)) ([f29dc3d](https://github.com/petauron/vastora/commit/f29dc3def872f76fe53ba9df0d76cf1e9b2f6e75))

## [0.1.0-alpha.204](https://github.com/petauron/vastora/compare/v0.1.0-alpha.203...v0.1.0-alpha.204) (2026-09-23)


### Bug Fixes

* accept verified pinned REALITY targets in Meridian import ([f90cef8](https://github.com/petauron/vastora/commit/f90cef85f3d2e55292244879ec815acb613b82ea))

## [0.1.0-alpha.203](https://github.com/petauron/vastora/compare/v0.1.0-alpha.202...v0.1.0-alpha.203) (2026-09-23)


### Bug Fixes

* unblock Meridian export publication verification ([d48edee](https://github.com/petauron/vastora/commit/d48edeea1e6524d91f1ef4f13cfb5c29ecde6150))

## [0.1.0-alpha.202](https://github.com/petauron/vastora/compare/v0.1.0-alpha.201...v0.1.0-alpha.202) (2026-09-23)


### Bug Fixes

* **agent:** keep read-only Meridian export out of landing write queue ([#619](https://github.com/petauron/vastora/pull/619)) ([bcd8be1](https://github.com/petauron/vastora/commit/bcd8be163efd63d7f704cf86b37af508aacde63d))

## [0.1.0-alpha.201](https://github.com/petauron/vastora/compare/v0.1.0-alpha.200...v0.1.0-alpha.201) (2026-09-23)


### Bug Fixes

* **center:** dispatch Meridian command receipts by stored task ([#616](https://github.com/petauron/vastora/issues/616)) ([f5cf472](https://github.com/petauron/vastora/commit/f5cf472f460af0769d4c946e0b638621ba30e8b2))
* **meridian:** native-only MVP cutover ([#618](https://github.com/petauron/vastora/issues/618)) ([ad1dd99](https://github.com/petauron/vastora/commit/ad1dd998763d6845c0b0b2d73d4c168f39bb098f))

## [0.1.0-alpha.200](https://github.com/petauron/vastora/compare/v0.1.0-alpha.199...v0.1.0-alpha.200) (2026-09-23)


### Bug Fixes

* **center:** allow Meridian backup receipt confirmation ([#614](https://github.com/petauron/vastora/issues/614)) ([5c70eda](https://github.com/petauron/vastora/commit/5c70eda5e0beb27243d6b0202c08e27234ebd02c))

## [0.1.0-alpha.199](https://github.com/petauron/vastora/compare/v0.1.0-alpha.198...v0.1.0-alpha.199) (2026-09-23)


### Bug Fixes

* **center:** unblock Meridian cutover restore-point task ([#612](https://github.com/petauron/vastora/issues/612)) ([3f54ec6](https://github.com/petauron/vastora/commit/3f54ec60493fc55a1518e2d8fa29b8549f20ec4c))

## [0.1.0-alpha.198](https://github.com/petauron/vastora/compare/v0.1.0-alpha.197...v0.1.0-alpha.198) (2026-09-23)


### Bug Fixes

* **center:** verify landing and assistant secrets in backups ([#610](https://github.com/petauron/vastora/issues/610)) ([e2724cd](https://github.com/petauron/vastora/commit/e2724cde0e2a4dfc2c0a94540b9f4d9cc880f942))

## [0.1.0-alpha.197](https://github.com/petauron/vastora/compare/v0.1.0-alpha.196...v0.1.0-alpha.197) (2026-09-23)


### Bug Fixes

* exclude retired shared children from Meridian import ([#608](https://github.com/petauron/vastora/issues/608)) ([d3732ce](https://github.com/petauron/vastora/commit/d3732ce27423cdf07af00efccd16dea33b171997))

## [0.1.0-alpha.196](https://github.com/petauron/vastora/compare/v0.1.0-alpha.195...v0.1.0-alpha.196) (2026-09-23)


### Bug Fixes

* keep landing grants pending until child preparation ([#606](https://github.com/petauron/vastora/issues/606)) ([64bdf63](https://github.com/petauron/vastora/commit/64bdf634f4fe4c256418e635b19fd5e0648d7411))

## [0.1.0-alpha.195](https://github.com/petauron/vastora/compare/v0.1.0-alpha.194...v0.1.0-alpha.195) (2026-09-23)


### Bug Fixes

* **center:** retain confirmed landing retirements across cleanup conflicts ([#603](https://github.com/petauron/vastora/issues/603)) ([07e8e9f](https://github.com/petauron/vastora/commit/07e8e9f8c73faa2c84639581d7e988aac17732a5))

## [0.1.0-alpha.194](https://github.com/petauron/vastora/compare/v0.1.0-alpha.193...v0.1.0-alpha.194) (2026-09-23)


### Bug Fixes

* **landing:** commit proxy receipts before pool reconciliation ([#601](https://github.com/petauron/vastora/issues/601)) ([d5e475d](https://github.com/petauron/vastora/commit/d5e475dc6947634d708093452ea20c06077837bf))

## [0.1.0-alpha.193](https://github.com/petauron/vastora/compare/v0.1.0-alpha.192...v0.1.0-alpha.193) (2026-09-23)


### Bug Fixes

* **landing:** retire failed grants for removed landing servers ([#599](https://github.com/petauron/vastora/issues/599)) ([59ce932](https://github.com/petauron/vastora/commit/59ce932656459ec3359864fa527fe3347d0b4398))

## [0.1.0-alpha.192](https://github.com/petauron/vastora/compare/v0.1.0-alpha.191...v0.1.0-alpha.192) (2026-09-23)


### Bug Fixes

* **landing:** retain DNS bind across host resolver remounts ([#597](https://github.com/petauron/vastora/issues/597)) ([0be3263](https://github.com/petauron/vastora/commit/0be3263d46c21cb0d44fdeecee91406ca90514a6))

## [0.1.0-alpha.191](https://github.com/petauron/vastora/compare/v0.1.0-alpha.190...v0.1.0-alpha.191) (2026-09-23)


### Features

* **meridian:** preserve entry traffic caps during cutover ([#595](https://github.com/petauron/vastora/issues/595)) ([4f3f29a](https://github.com/petauron/vastora/commit/4f3f29aadacf5b7e63eec9377e04c7dcdcf46414))

## [0.1.0-alpha.190](https://github.com/petauron/vastora/compare/v0.1.0-alpha.189...v0.1.0-alpha.190) (2026-09-23)


### Bug Fixes

* **updates:** recover task channel and retire unclaimed stale update targets ([#593](https://github.com/petauron/vastora/issues/593)) ([163a5c8](https://github.com/petauron/vastora/commit/163a5c82d20ea172812ae3b31908debf4487dd9c))

## [0.1.0-alpha.189](https://github.com/petauron/vastora/compare/v0.1.0-alpha.188...v0.1.0-alpha.189) (2026-09-23)


### Bug Fixes

* recover landing execution fences and repair Meridian validation ([#591](https://github.com/petauron/vastora/issues/591)) ([45d9bc0](https://github.com/petauron/vastora/commit/45d9bc03c0dbd44a12038b3976acf8242403da18))

## [0.1.0-alpha.188](https://github.com/petauron/vastora/compare/v0.1.0-alpha.187...v0.1.0-alpha.188) (2026-09-22)


### Bug Fixes

* **center:** fence legacy landing during Meridian cutover ([#589](https://github.com/petauron/vastora/issues/589)) ([41045af](https://github.com/petauron/vastora/commit/41045afa34cc3946a3e35082faf404ae97105554))

## [0.1.0-alpha.187](https://github.com/petauron/vastora/compare/v0.1.0-alpha.186...v0.1.0-alpha.187) (2026-09-22)


### Bug Fixes

* **center:** accept applied landing revisions ([#587](https://github.com/petauron/vastora/issues/587)) ([634a9d6](https://github.com/petauron/vastora/commit/634a9d659e7c3523a632f3ee6b40a7f3a7d59136))

## [0.1.0-alpha.186](https://github.com/petauron/vastora/compare/v0.1.0-alpha.185...v0.1.0-alpha.186) (2026-09-22)


### Bug Fixes

* **center:** recover previously expired retained results ([#585](https://github.com/petauron/vastora/issues/585)) ([b3b54a5](https://github.com/petauron/vastora/commit/b3b54a5dd0e3b34072813f0bae69c0fdc4fa997b))

## [0.1.0-alpha.185](https://github.com/petauron/vastora/compare/v0.1.0-alpha.184...v0.1.0-alpha.185) (2026-09-22)


### Bug Fixes

* **center:** recover expired retained results on startup ([#583](https://github.com/petauron/vastora/issues/583)) ([6423d2a](https://github.com/petauron/vastora/commit/6423d2a8a60c377cf34135f5c74f396ee154a46b))

## [0.1.0-alpha.184](https://github.com/petauron/vastora/compare/v0.1.0-alpha.183...v0.1.0-alpha.184) (2026-09-22)


### Bug Fixes

* **center:** converge landing task results safely ([#580](https://github.com/petauron/vastora/issues/580)) ([510998a](https://github.com/petauron/vastora/commit/510998adc88d7092d0d1261213de222458a28257))
* **center:** fence Meridian cutover on unresolved executions ([#582](https://github.com/petauron/vastora/issues/582)) ([64ccf04](https://github.com/petauron/vastora/commit/64ccf04184365789a6f1f7529ac598bc08fd2a6a))

## [0.1.0-alpha.183](https://github.com/petauron/vastora/compare/v0.1.0-alpha.182...v0.1.0-alpha.183) (2026-09-22)


### Bug Fixes

* **subscriptions:** isolate stale landing state ([#578](https://github.com/petauron/vastora/issues/578)) ([ea2735c](https://github.com/petauron/vastora/commit/ea2735c2d88e68429b97a7446f56ceff64d7a3fd))

## [0.1.0-alpha.182](https://github.com/petauron/vastora/compare/v0.1.0-alpha.181...v0.1.0-alpha.182) (2026-09-22)


### Features

* replace 3x-ui authority with Meridian ([#573](https://github.com/petauron/vastora/issues/573)) ([5970e62](https://github.com/petauron/vastora/commit/5970e624a97f9b0bc6065546cd54135cb7e6b8fb))


### Bug Fixes

* **agent:** compile legacy worker recovery ([#574](https://github.com/petauron/vastora/issues/574)) ([d69d8f3](https://github.com/petauron/vastora/commit/d69d8f347825896def0c29ed7445d2d393a290e7))
* **catalog:** declare Meridian empty config ([#575](https://github.com/petauron/vastora/issues/575)) ([bc4e954](https://github.com/petauron/vastora/commit/bc4e9541ef4b2ce4cdad45d3dda047df87bc12f2))

## [0.1.0-alpha.181](https://github.com/petauron/vastora/compare/v0.1.0-alpha.180...v0.1.0-alpha.181) (2026-09-21)


### Bug Fixes

* **updates:** release migration claim pause ([#571](https://github.com/petauron/vastora/issues/571)) ([2cf9a86](https://github.com/petauron/vastora/commit/2cf9a8615768e5c25622fd25ca13ac72a5a06f2d))

## [0.1.0-alpha.180](https://github.com/petauron/vastora/compare/v0.1.0-alpha.179...v0.1.0-alpha.180) (2026-09-20)


### Bug Fixes

* recover proxy deployment and subscription convergence ([#565](https://github.com/petauron/vastora/issues/565)) ([aede43e](https://github.com/petauron/vastora/commit/aede43e171f6acee7fd7f592cb0ad8d351f8c032))

## [0.1.0-alpha.179](https://github.com/petauron/vastora/compare/v0.1.0-alpha.178...v0.1.0-alpha.179) (2026-09-20)


### Bug Fixes

* **center:** record deployment rejection reasons ([#563](https://github.com/petauron/vastora/issues/563)) ([8faa252](https://github.com/petauron/vastora/commit/8faa2521f389631b4ec3adbbee158b8c2918b4f1))

## [0.1.0-alpha.178](https://github.com/petauron/vastora/compare/v0.1.0-alpha.177...v0.1.0-alpha.178) (2026-09-20)


### Bug Fixes

* **agent:** persist native subscription hosts ([#561](https://github.com/petauron/vastora/issues/561)) ([2d449d3](https://github.com/petauron/vastora/commit/2d449d39f908e98150370e8313fd8d0e9779bf79))

## [0.1.0-alpha.177](https://github.com/petauron/vastora/compare/v0.1.0-alpha.176...v0.1.0-alpha.177) (2026-09-20)


### Bug Fixes

* **web:** allow Xray recovery task disposition ([#559](https://github.com/petauron/vastora/issues/559)) ([3cab708](https://github.com/petauron/vastora/commit/3cab7082ec50f2780b51af5ae30e7297a09f2482))

## [0.1.0-alpha.176](https://github.com/petauron/vastora/compare/v0.1.0-alpha.175...v0.1.0-alpha.176) (2026-09-20)


### Bug Fixes

* **center:** finalize Xray recovery inspection ([#557](https://github.com/petauron/vastora/issues/557)) ([8e185e0](https://github.com/petauron/vastora/commit/8e185e0d5d024d7cd4158aa4af71ff7f8b76ca04))

## [0.1.0-alpha.175](https://github.com/petauron/vastora/compare/v0.1.0-alpha.174...v0.1.0-alpha.175) (2026-09-20)


### Bug Fixes

* **agent:** surface blocked Xray recovery ([#555](https://github.com/petauron/vastora/issues/555)) ([0ad2368](https://github.com/petauron/vastora/commit/0ad2368e90fb47d65d25001f39d7617c15562d54))

## [0.1.0-alpha.174](https://github.com/petauron/vastora/compare/v0.1.0-alpha.173...v0.1.0-alpha.174) (2026-09-20)


### Bug Fixes

* **center:** authorize work after superseded updates ([#553](https://github.com/petauron/vastora/issues/553)) ([dfd33f9](https://github.com/petauron/vastora/commit/dfd33f98d79875a34f37cdce8908cbb98e2ce03e))

## [0.1.0-alpha.173](https://github.com/petauron/vastora/compare/v0.1.0-alpha.172...v0.1.0-alpha.173) (2026-09-20)


### Bug Fixes

* **center:** release superseded update session fence ([#551](https://github.com/petauron/vastora/issues/551)) ([8620e0d](https://github.com/petauron/vastora/commit/8620e0d2fef8934ae625339cb9c7ad8774b236f8))

## [0.1.0-alpha.172](https://github.com/petauron/vastora/compare/v0.1.0-alpha.171...v0.1.0-alpha.172) (2026-09-20)


### Bug Fixes

* **apps:** converge superseded update tasks ([#549](https://github.com/petauron/vastora/issues/549)) ([fcdb0cf](https://github.com/petauron/vastora/commit/fcdb0cf2b469502f05dae587d04c2cc263fac329))

## [0.1.0-alpha.171](https://github.com/petauron/vastora/compare/v0.1.0-alpha.170...v0.1.0-alpha.171) (2026-09-20)


### Bug Fixes

* **updates:** unblock installed agent versions ([#547](https://github.com/petauron/vastora/issues/547)) ([d317242](https://github.com/petauron/vastora/commit/d317242854c6eed06c1be5a1c1bf84dab014a2f9))

## [0.1.0-alpha.170](https://github.com/petauron/vastora/compare/v0.1.0-alpha.169...v0.1.0-alpha.170) (2026-09-20)


### Bug Fixes

* **updates:** isolate agent rollout failures ([#545](https://github.com/petauron/vastora/issues/545)) ([3835090](https://github.com/petauron/vastora/commit/3835090f50e900d21157db0d8a74da41ffa4380f))

## [0.1.0-alpha.169](https://github.com/petauron/vastora/compare/v0.1.0-alpha.168...v0.1.0-alpha.169) (2026-09-20)


### Features

* **recovery:** add explicit Xray configuration reconciliation ([34eda98](https://github.com/petauron/vastora/commit/34eda98f792de36f06f06e92153670db2e92fd7f))

## [0.1.0-alpha.168](https://github.com/petauron/vastora/compare/v0.1.0-alpha.167...v0.1.0-alpha.168) (2026-09-20)


### Bug Fixes

* **agent:** recover stale Xray apply receipt ([#541](https://github.com/petauron/vastora/issues/541)) ([afdf6f9](https://github.com/petauron/vastora/commit/afdf6f99b09df2a6f16f9cc50b274e17480522e9))

## [0.1.0-alpha.167](https://github.com/petauron/vastora/compare/v0.1.0-alpha.166...v0.1.0-alpha.167) (2026-09-20)


### Bug Fixes

* **agent:** reconcile restored Xray runtime state ([#539](https://github.com/petauron/vastora/issues/539)) ([ee32d20](https://github.com/petauron/vastora/commit/ee32d2081564cfd04a6f96ad2eef659909cf37b9))

## [0.1.0-alpha.166](https://github.com/petauron/vastora/compare/v0.1.0-alpha.165...v0.1.0-alpha.166) (2026-09-20)


### Bug Fixes

* **agent:** reconcile recreated landing runtime ([#538](https://github.com/petauron/vastora/issues/538)) ([0551166](https://github.com/petauron/vastora/commit/055116606df72b3130a4dc2159a02f16cf2f8cec))
* **catalog:** version Mihomo-compatible proxy runtime ([#536](https://github.com/petauron/vastora/issues/536)) ([d890b94](https://github.com/petauron/vastora/commit/d890b946b5a2776b68593c2d86af3eae7dd8496d))

## [0.1.0-alpha.165](https://github.com/petauron/vastora/compare/v0.1.0-alpha.164...v0.1.0-alpha.165) (2026-09-19)


### Bug Fixes

* **proxy:** restore Mihomo Reality compatibility ([#534](https://github.com/petauron/vastora/issues/534)) ([c3932be](https://github.com/petauron/vastora/commit/c3932bebc0eb03876eec2bee6f707b7cd27810d7))

## [0.1.0-alpha.164](https://github.com/petauron/vastora/compare/v0.1.0-alpha.163...v0.1.0-alpha.164) (2026-09-19)


### Bug Fixes

* **proxy:** extract token from legacy worker secrets ([#532](https://github.com/petauron/vastora/issues/532)) ([2231f4f](https://github.com/petauron/vastora/commit/2231f4f7f2e8bd877f270edb29c519d0735c729e))

## [0.1.0-alpha.163](https://github.com/petauron/vastora/compare/v0.1.0-alpha.162...v0.1.0-alpha.163) (2026-09-19)


### Bug Fixes

* **proxy:** carry worker token into Xray migration ([#530](https://github.com/petauron/vastora/issues/530)) ([1b5e4e6](https://github.com/petauron/vastora/commit/1b5e4e690137983f3c028e13e9a242dd1755c6c8))

## [0.1.0-alpha.162](https://github.com/petauron/vastora/compare/v0.1.0-alpha.161...v0.1.0-alpha.162) (2026-09-19)


### Bug Fixes

* **proxy:** migrate workers with trusted Xray manifest ([#528](https://github.com/petauron/vastora/issues/528)) ([9484a6d](https://github.com/petauron/vastora/commit/9484a6d881ac495992078f484dfffc606f438640))

## [0.1.0-alpha.161](https://github.com/petauron/vastora/compare/v0.1.0-alpha.160...v0.1.0-alpha.161) (2026-09-19)


### Bug Fixes

* **proxy:** sequence Xray worker cutover safely ([#526](https://github.com/petauron/vastora/issues/526)) ([fcd5418](https://github.com/petauron/vastora/commit/fcd5418ba856968a5d1786ced1da20fdb1c21a35))

## [0.1.0-alpha.160](https://github.com/petauron/vastora/compare/v0.1.0-alpha.159...v0.1.0-alpha.160) (2026-09-19)


### Bug Fixes

* **update:** fence tasks behind agent rollout ([#524](https://github.com/petauron/vastora/issues/524)) ([fb28938](https://github.com/petauron/vastora/commit/fb28938a349a8526d8b5926125d4ddeb93b8e871))

## [0.1.0-alpha.159](https://github.com/petauron/vastora/compare/v0.1.0-alpha.158...v0.1.0-alpha.159) (2026-09-19)


### Bug Fixes

* **proxy:** name managed workers as Vastora Xray ([#522](https://github.com/petauron/vastora/issues/522)) ([a20aeac](https://github.com/petauron/vastora/commit/a20aeacf24d14a91c8e57e85d21c82ae6fc7e05c))

## [0.1.0-alpha.158](https://github.com/petauron/vastora/compare/v0.1.0-alpha.157...v0.1.0-alpha.158) (2026-09-19)


### Bug Fixes

* **landing:** isolate failed exits without restarting Xray ([#516](https://github.com/petauron/vastora/issues/516)) ([bedf8ff](https://github.com/petauron/vastora/commit/bedf8ff6d2df615b9db8d1bd87966ec1e38320cb)), closes [#513](https://github.com/petauron/vastora/issues/513)
* **proxy:** isolate Xray workers on runtime bridge ([afbcae3](https://github.com/petauron/vastora/commit/afbcae36aee8df7114064e3520398c0bda8ae9f0))

## [0.1.0-alpha.157](https://github.com/petauron/vastora/compare/v0.1.0-alpha.156...v0.1.0-alpha.157) (2026-09-19)


### Features

* **proxy:** replace 3x-ui workers with managed Xray ([#514](https://github.com/petauron/vastora/issues/514)) ([fee0fe8](https://github.com/petauron/vastora/commit/fee0fe81b964d628db7f2636f3f051f7336abd22))

## [0.1.0-alpha.156](https://github.com/petauron/vastora/compare/v0.1.0-alpha.155...v0.1.0-alpha.156) (2026-09-16)


### Bug Fixes

* **agent:** recover native landing subscriptions ([#510](https://github.com/petauron/vastora/issues/510)) ([2f45f53](https://github.com/petauron/vastora/commit/2f45f531b84a1dbfd89872983eb20b25062d407c))

## [0.1.0-alpha.155](https://github.com/petauron/vastora/compare/v0.1.0-alpha.154...v0.1.0-alpha.155) (2026-09-16)


### Bug Fixes

* **landing:** reconcile split 3x-ui quota state ([#508](https://github.com/petauron/vastora/issues/508)) ([76d3afa](https://github.com/petauron/vastora/commit/76d3afaeb6af3c8b5a5329c88bf8763275d2b573))

## [0.1.0-alpha.154](https://github.com/petauron/vastora/compare/v0.1.0-alpha.153...v0.1.0-alpha.154) (2026-09-16)


### Features

* **landing:** add native multi-egress subscriptions ([#506](https://github.com/petauron/vastora/issues/506)) ([4df2b4e](https://github.com/petauron/vastora/commit/4df2b4e60ca9f515f7661abbc0bde3407c4d3617))

## [0.1.0-alpha.153](https://github.com/petauron/vastora/compare/v0.1.0-alpha.152...v0.1.0-alpha.153) (2026-09-16)


### Bug Fixes

* **landing:** stop periodic Xray reloads from unchanged quota sync ([24ef0ff](https://github.com/petauron/vastora/commit/24ef0fff8e1dd1fbc7bba498aaae09778571d4cd))

## [0.1.0-alpha.152](https://github.com/petauron/vastora/compare/v0.1.0-alpha.151...v0.1.0-alpha.152) (2026-09-15)


### Bug Fixes

* stabilize updates diagnostics and subscriptions ([#501](https://github.com/petauron/vastora/issues/501)) ([613f203](https://github.com/petauron/vastora/commit/613f203a0dc495dd063ca52e36a8fbc3ea5164e3))

## [0.1.0-alpha.151](https://github.com/petauron/vastora/compare/v0.1.0-alpha.150...v0.1.0-alpha.151) (2026-09-15)


### Features

* **nodes:** add native host diagnostics dashboard ([#499](https://github.com/petauron/vastora/issues/499)) ([d114f68](https://github.com/petauron/vastora/commit/d114f68358be918ecca119f3c2c5f06194b1e350))

## [0.1.0-alpha.150](https://github.com/petauron/vastora/compare/v0.1.0-alpha.149...v0.1.0-alpha.150) (2026-09-15)


### Bug Fixes

* **pulse:** auto-fill missing settings during legacy upgrade ([#497](https://github.com/petauron/vastora/issues/497)) ([c652da8](https://github.com/petauron/vastora/commit/c652da8675cab17e43ef057a171e26886cca86fe))

## [0.1.0-alpha.149](https://github.com/petauron/vastora/compare/v0.1.0-alpha.148...v0.1.0-alpha.149) (2026-09-15)


### Bug Fixes

* stabilize diagnostics and compact node labels ([#495](https://github.com/petauron/vastora/issues/495)) ([56b4ac0](https://github.com/petauron/vastora/commit/56b4ac02d786e063e70e7e722cf338dbc00f8939))

## [0.1.0-alpha.148](https://github.com/petauron/vastora/compare/v0.1.0-alpha.147...v0.1.0-alpha.148) (2026-09-15)


### Bug Fixes

* sanitize IP quality unlock results ([#493](https://github.com/petauron/vastora/issues/493)) ([a6c247e](https://github.com/petauron/vastora/commit/a6c247ee0f04084d0fd4c60b5a867594fd055d5a))

## [0.1.0-alpha.147](https://github.com/petauron/vastora/compare/v0.1.0-alpha.146...v0.1.0-alpha.147) (2026-09-15)


### Bug Fixes

* **center:** recover incomplete schema 80 marker ([#491](https://github.com/petauron/vastora/issues/491)) ([7af9e2b](https://github.com/petauron/vastora/commit/7af9e2b6cb3c48f7413d4300ac9c15d7425bcd36))

## [0.1.0-alpha.146](https://github.com/petauron/vastora/compare/v0.1.0-alpha.145...v0.1.0-alpha.146) (2026-09-15)


### Features

* complete node diagnostics and agent recovery ([#489](https://github.com/petauron/vastora/issues/489)) ([f89c785](https://github.com/petauron/vastora/commit/f89c78514a51ed2dd8c47ba25f2792ba977c6b38)), closes [#488](https://github.com/petauron/vastora/issues/488)

## [0.1.0-alpha.145](https://github.com/petauron/vastora/compare/v0.1.0-alpha.144...v0.1.0-alpha.145) (2026-09-14)


### Features

* **ui:** add compact fleet overview ([#486](https://github.com/petauron/vastora/issues/486)) ([b53ef4e](https://github.com/petauron/vastora/commit/b53ef4e0305d80caa082fc94cc86f128d243fef6))

## [0.1.0-alpha.144](https://github.com/petauron/vastora/compare/v0.1.0-alpha.143...v0.1.0-alpha.144) (2026-09-14)


### Features

* **landing:** replace node exits with global pool ([#485](https://github.com/petauron/vastora/issues/485)) ([48d2372](https://github.com/petauron/vastora/commit/48d23728fcd814cca2eba6aac71bb8d18610c1b6))


### Bug Fixes

* **agent:** make IP quality cleanup deterministic ([#483](https://github.com/petauron/vastora/issues/483)) ([24f587b](https://github.com/petauron/vastora/commit/24f587bf84c2ee5a90a1cd7b3a1e0fc34fbd9c00))

## [0.1.0-alpha.143](https://github.com/petauron/vastora/compare/v0.1.0-alpha.142...v0.1.0-alpha.143) (2026-09-14)


### Bug Fixes

* **landing:** simplify subscription combination names ([#479](https://github.com/petauron/vastora/issues/479)) ([c7df89f](https://github.com/petauron/vastora/commit/c7df89fbff0d16ca262d1f9d44d039c107451ab2))

## [0.1.0-alpha.142](https://github.com/petauron/vastora/compare/v0.1.0-alpha.141...v0.1.0-alpha.142) (2026-09-14)


### Bug Fixes

* **landing:** show per-exit flags and latency ([#477](https://github.com/petauron/vastora/issues/477)) ([4f6fadd](https://github.com/petauron/vastora/commit/4f6fadde1336d0e4feef4f8bc7ca87408695c7ac))

## [0.1.0-alpha.141](https://github.com/petauron/vastora/compare/v0.1.0-alpha.140...v0.1.0-alpha.141) (2026-09-14)


### Bug Fixes

* **updates:** verify Center image identity ([#475](https://github.com/petauron/vastora/issues/475)) ([dabdd69](https://github.com/petauron/vastora/commit/dabdd6935930fce46abc8eb012b5837b083593a3))

## [0.1.0-alpha.140](https://github.com/petauron/vastora/compare/v0.1.0-alpha.139...v0.1.0-alpha.140) (2026-09-14)


### Features

* add node IP quality diagnostics ([#471](https://github.com/petauron/vastora/issues/471)) ([e03861b](https://github.com/petauron/vastora/commit/e03861b9bbc6ded6f7aa3eb11082394eb822f314))


### Bug Fixes

* **landing:** recover routing after agent restart ([#469](https://github.com/petauron/vastora/issues/469)) ([a17e36f](https://github.com/petauron/vastora/commit/a17e36fae175b366f03775e2860b2bb0171074c0)), closes [#467](https://github.com/petauron/vastora/issues/467)

## [0.1.0-alpha.139](https://github.com/petauron/vastora/compare/v0.1.0-alpha.138...v0.1.0-alpha.139) (2026-09-14)


### Bug Fixes

* **ui:** simplify 3x-ui node and exit management ([#465](https://github.com/petauron/vastora/issues/465)) ([fda3755](https://github.com/petauron/vastora/commit/fda375586468c6749fbefb413543cbe7cb6ae79a))

## [0.1.0-alpha.138](https://github.com/petauron/vastora/compare/v0.1.0-alpha.137...v0.1.0-alpha.138) (2026-09-14)


### Bug Fixes

* release stopped Agent update staging and start unloaded helpers ([#463](https://github.com/petauron/vastora/issues/463)) ([aec7fea](https://github.com/petauron/vastora/commit/aec7feaf06a686b74ad5cc7a7c35f7bd99de921c))

## [0.1.0-alpha.137](https://github.com/petauron/vastora/compare/v0.1.0-alpha.136...v0.1.0-alpha.137) (2026-09-14)


### Bug Fixes

* publish landing combinations through confirmed tunnel origins ([#460](https://github.com/petauron/vastora/issues/460)) ([20444d3](https://github.com/petauron/vastora/commit/20444d334b59dc423a4c7c529bc7b47ddd578df2))
* use native Docker IPAM prefix type ([#461](https://github.com/petauron/vastora/issues/461)) ([0c7e620](https://github.com/petauron/vastora/commit/0c7e620d407136b26e75ab417e4496214592e63e))

## [0.1.0-alpha.136](https://github.com/petauron/vastora/compare/v0.1.0-alpha.135...v0.1.0-alpha.136) (2026-09-13)


### Bug Fixes

* restore task reception and stop heartbeat queue churn ([#443](https://github.com/petauron/vastora/issues/443)) ([8609a61](https://github.com/petauron/vastora/commit/8609a61a0e9e533d3da55dbc3fb0a248d15e75ae))

## [0.1.0-alpha.135](https://github.com/petauron/vastora/compare/v0.1.0-alpha.134...v0.1.0-alpha.135) (2026-09-13)


### Bug Fixes

* **agent:** bind landing child to validated inbound at creation ([#441](https://github.com/petauron/vastora/issues/441)) ([768400b](https://github.com/petauron/vastora/commit/768400b347ea11182e64d4fd2b3b216426548a68))

## [0.1.0-alpha.134](https://github.com/petauron/vastora/compare/v0.1.0-alpha.133...v0.1.0-alpha.134) (2026-09-13)


### Bug Fixes

* **agent:** recover failed updates with explicit operator confirmation ([#437](https://github.com/petauron/vastora/issues/437)) ([7835910](https://github.com/petauron/vastora/commit/78359101302d92454eaef6512546bf759ce50b50))
* clarify blocked landing exits and guard release baseline ([#440](https://github.com/petauron/vastora/issues/440)) ([a962789](https://github.com/petauron/vastora/commit/a96278909d937241f626589dc80f9339bb604566))


### Performance Improvements

* **release:** reuse compilation and retry only missing uploads ([#438](https://github.com/petauron/vastora/issues/438)) ([d0bd025](https://github.com/petauron/vastora/commit/d0bd0254836e68423f7767dd04d73cb324547961))

## [0.1.0-alpha.133](https://github.com/petauron/vastora/compare/v0.1.0-alpha.131...v0.1.0-alpha.133) (2026-09-14)

### Features

* Configure multiple exit combinations directly from application node rows (#433).

### Bug Fixes

* Remove obsolete client landing editor reset that blocked the alpha.132 build (#435).

## [0.1.0-alpha.132](https://github.com/petauron/vastora/compare/v0.1.0-alpha.131...v0.1.0-alpha.132) (2026-09-13)


### Features

* **apps:** configure exit combinations from node rows ([#433](https://github.com/petauron/vastora/issues/433)) ([51db2db](https://github.com/petauron/vastora/commit/51db2db1bc54e8b5afdb317460922a9386db4317))

## [0.1.0-alpha.131](https://github.com/petauron/vastora/compare/v0.1.0-alpha.130...v0.1.0-alpha.131) (2026-09-13)


### Features

* 多选落地生成独立组合订阅节点 ([#431](https://github.com/petauron/vastora/issues/431)) ([5b2a44d](https://github.com/petauron/vastora/commit/5b2a44de7ccaed1b2f99733fdbb3a5b3e85711f1))

## [0.1.0-alpha.130](https://github.com/petauron/vastora/compare/v0.1.0-alpha.129...v0.1.0-alpha.130) (2026-09-13)


### Features

* Agent execution MVP and local HTTP lifecycle fix ([#429](https://github.com/petauron/vastora/issues/429)) ([b3129ee](https://github.com/petauron/vastora/commit/b3129eea776fd5de06f8deeb0effc1127ca4730a))

## [0.1.0-alpha.129](https://github.com/petauron/vastora/compare/v0.1.0-alpha.128...v0.1.0-alpha.129) (2026-09-13)


### Bug Fixes

* remove unvalidated release compiler cache integration ([#426](https://github.com/petauron/vastora/issues/426)) ([c9967fa](https://github.com/petauron/vastora/commit/c9967fa6ed3018f4f86923f6962237398cc9adb3))
* simplify app update status and reuse release compiler caches ([#425](https://github.com/petauron/vastora/issues/425)) ([17139e8](https://github.com/petauron/vastora/commit/17139e83808aa31be3ec460e45f3d7de004867d2))

## [0.1.0-alpha.128](https://github.com/petauron/vastora/compare/v0.1.0-alpha.127...v0.1.0-alpha.128) (2026-09-13)


### Bug Fixes

* **apps:** 列表直接更新应用并清理残留提示 ([#423](https://github.com/petauron/vastora/issues/423)) ([ae3294c](https://github.com/petauron/vastora/commit/ae3294cbdb48a9b5ec84866d1cf6c409e372280a))

## [0.1.0-alpha.127](https://github.com/petauron/vastora/compare/v0.1.0-alpha.126...v0.1.0-alpha.127) (2026-09-13)


### Bug Fixes

* **nodes:** 修复离线节点移除并精简 Alpha 发布检查 ([#421](https://github.com/petauron/vastora/issues/421)) ([e450c86](https://github.com/petauron/vastora/commit/e450c86d3436d5acc3fed4854d150ff8b4ccc75d))

## [0.1.0-alpha.126](https://github.com/petauron/vastora/compare/v0.1.0-alpha.125...v0.1.0-alpha.126) (2026-09-12)


### Features

* **nodes:** 永久移除到期离线节点及关联记录 ([#419](https://github.com/petauron/vastora/issues/419)) ([1a31b5a](https://github.com/petauron/vastora/commit/1a31b5a1d1be11916704d350e50170b6d8a96a7a))

## [0.1.0-alpha.125](https://github.com/petauron/vastora/compare/v0.1.0-alpha.124...v0.1.0-alpha.125) (2026-09-12)


### Bug Fixes

* **agent:** isolate self-update recovery and stop stalled rollout indicators ([#417](https://github.com/petauron/vastora/issues/417)) ([0f7bcb3](https://github.com/petauron/vastora/commit/0f7bcb3f094a52dd538c90b4c16a71a37ac41644))
* **release:** preserve independent catalog during R2 cleanup ([#415](https://github.com/petauron/vastora/issues/415)) ([7c2af2f](https://github.com/petauron/vastora/commit/7c2af2f0d4113d42347597bc98393393dcc23ff4))

## [0.1.0-alpha.124](https://github.com/petauron/vastora/compare/v0.1.0-alpha.123...v0.1.0-alpha.124) (2026-09-12)


### Features

* **landing:** add client-scoped subscription combinations MVP ([81c39be](https://github.com/petauron/vastora/commit/81c39be8f172516b547df3daa4102d7c8043cf65))


### Bug Fixes

* **center:** skip offline agents in update progress ([382ced6](https://github.com/petauron/vastora/commit/382ced6faf590db82a4f1339b46bdbe4ddb1536e))


### Performance Improvements

* **agent:** bound task receipt maintenance and index hot queries ([bcdad97](https://github.com/petauron/vastora/commit/bcdad97a354aaba16370289e67db5b2a716dae34))

## [0.1.0-alpha.123](https://github.com/petauron/vastora/compare/v0.1.0-alpha.122...v0.1.0-alpha.123) (2026-09-12)


### Features

* **catalog:** publish independently signed official catalogs ([#404](https://github.com/petauron/vastora/issues/404)) ([175d969](https://github.com/petauron/vastora/commit/175d969b7cd828b6a0f36dfee576f6ab255426a5))
* **nodes:** stop offline agent access without removing workloads ([#408](https://github.com/petauron/vastora/issues/408)) ([d533d88](https://github.com/petauron/vastora/commit/d533d887ca6d56090bb2021272c708c2b4bcf7b7))
* **pulse:** integrate authenticated service and collector settings ([#405](https://github.com/petauron/vastora/issues/405)) ([f25c096](https://github.com/petauron/vastora/commit/f25c0969adbb982eb03f15bd7108031f0ae79a57))


### Bug Fixes

* **catalog:** provision approved production trust root ([#410](https://github.com/petauron/vastora/issues/410)) ([0922279](https://github.com/petauron/vastora/commit/09222794bb908b10140fdd1269e854cb152eea68))
* preserve scoped recovery and assistant conversation safety ([#403](https://github.com/petauron/vastora/issues/403)) ([cae7bbf](https://github.com/petauron/vastora/commit/cae7bbf4c5b63cc054615c858ebc2dd941813796))
* **web:** simplify subscription client cards and HY2 guidance ([#407](https://github.com/petauron/vastora/issues/407)) ([d7e778e](https://github.com/petauron/vastora/commit/d7e778ef540cdba867516c6d0c7c794a997baa64))

## [0.1.0-alpha.122](https://github.com/petauron/vastora/compare/v0.1.0-alpha.121...v0.1.0-alpha.122) (2026-09-11)


### Bug Fixes

* preserve Pulse default network during recovery ([#395](https://github.com/petauron/vastora/issues/395)) ([28caeea](https://github.com/petauron/vastora/commit/28caeea64fc509c6564bd975f72af90a26d1e896))

## [0.1.0-alpha.121](https://github.com/petauron/vastora/compare/v0.1.0-alpha.120...v0.1.0-alpha.121) (2026-09-11)


### Bug Fixes

* restore Pulse containers and unblock pending agent updates ([#392](https://github.com/petauron/vastora/issues/392)) ([5ca2c9e](https://github.com/petauron/vastora/commit/5ca2c9e6a7e77cc2976a2e22767316203230c09e))

## [0.1.0-alpha.120](https://github.com/petauron/vastora/compare/v0.1.0-alpha.119...v0.1.0-alpha.120) (2026-09-10)


### Bug Fixes

* repair Pulse enrollment and preserve retry node ([#390](https://github.com/petauron/vastora/issues/390)) ([bbec6f4](https://github.com/petauron/vastora/commit/bbec6f4a5889b80ff5034ef048e9dbd636ca7ce2))

## [0.1.0-alpha.119](https://github.com/petauron/vastora/compare/v0.1.0-alpha.118...v0.1.0-alpha.119) (2026-09-10)


### Bug Fixes

* **web:** refine app store and identify official Pulse apps ([#388](https://github.com/petauron/vastora/issues/388)) ([5c633c5](https://github.com/petauron/vastora/commit/5c633c5cc0e14a9dba22055040cc31632467e259))

## [0.1.0-alpha.118](https://github.com/petauron/vastora/compare/v0.1.0-alpha.117...v0.1.0-alpha.118) (2026-09-10)


### Features

* integrate Pulse monitoring in the app store ([#386](https://github.com/petauron/vastora/issues/386)) ([3a025c5](https://github.com/petauron/vastora/commit/3a025c5f4d27ca6c8cc086cda834cd8d89d493bf))

## [0.1.0-alpha.117](https://github.com/petauron/vastora/compare/v0.1.0-alpha.116...v0.1.0-alpha.117) (2026-09-10)


### Features

* add optional native HY2 node protocols ([#383](https://github.com/petauron/vastora/issues/383)) ([7a3130c](https://github.com/petauron/vastora/commit/7a3130ce04de4826ca0be6d5be8f6bbb26f6e07c))

## [0.1.0-alpha.116](https://github.com/petauron/vastora/compare/v0.1.0-alpha.115...v0.1.0-alpha.116) (2026-09-09)


### Features

* remove local controller VLESS nodes and recover unavailable entries ([#379](https://github.com/petauron/vastora/issues/379)) ([a4fef2e](https://github.com/petauron/vastora/commit/a4fef2e00580547d1620373db55e8ad97a66b3b3))

## [0.1.0-alpha.115](https://github.com/petauron/vastora/compare/v0.1.0-alpha.114...v0.1.0-alpha.115) (2026-09-09)


### Bug Fixes

* **landing:** distinguish applied exits and confirm route changes ([#374](https://github.com/petauron/vastora/issues/374)) ([2e7c1ae](https://github.com/petauron/vastora/commit/2e7c1ae951fa7964e7a779b0de0a5c82a2922525))

## [0.1.0-alpha.114](https://github.com/petauron/vastora/compare/v0.1.0-alpha.113...v0.1.0-alpha.114) (2026-09-09)


### Bug Fixes

* **landing:** stream per-target latency results immediately ([#372](https://github.com/petauron/vastora/issues/372)) ([e73d934](https://github.com/petauron/vastora/commit/e73d9348d17b0f2f1d7c55e9880f3653186d7557))

## [0.1.0-alpha.113](https://github.com/petauron/vastora/compare/v0.1.0-alpha.112...v0.1.0-alpha.113) (2026-09-09)


### Features

* support multiple landing servers and redesign app workspace ([#370](https://github.com/petauron/vastora/issues/370)) ([b20fc8f](https://github.com/petauron/vastora/commit/b20fc8fbe92e9caef29ce0e4a4d35b00d8db46c7))

## [0.1.0-alpha.112](https://github.com/petauron/vastora/compare/v0.1.0-alpha.111...v0.1.0-alpha.112) (2026-09-09)


### Bug Fixes

* improve initial landing activation and probe deadlines ([#368](https://github.com/petauron/vastora/issues/368)) ([acedcdd](https://github.com/petauron/vastora/commit/acedcddb65cc149cdf5d4c905dc1d8734c8c763d))

## [0.1.0-alpha.111](https://github.com/petauron/vastora/compare/v0.1.0-alpha.110...v0.1.0-alpha.111) (2026-09-09)


### Features

* configurable Cloudflare Access session duration ([#366](https://github.com/petauron/vastora/issues/366)) ([cd4be28](https://github.com/petauron/vastora/commit/cd4be28117d5cda9a8b158cdda50d431f7c37c29))

## [0.1.0-alpha.110](https://github.com/petauron/vastora/compare/v0.1.0-alpha.109...v0.1.0-alpha.110) (2026-09-08)


### Features

* 删除已停用节点与落地延迟体验修复 ([#363](https://github.com/petauron/vastora/issues/363)) ([a0661d4](https://github.com/petauron/vastora/commit/a0661d482df79960e4ad89393ffed280d2c258df))

## [0.1.0-alpha.109](https://github.com/petauron/vastora/compare/v0.1.0-alpha.108...v0.1.0-alpha.109) (2026-09-08)


### Bug Fixes

* 修复落地切换重启循环并显示节点到落地机延迟 ([#361](https://github.com/petauron/vastora/issues/361)) ([63ac409](https://github.com/petauron/vastora/commit/63ac409a8ab60820db7c91f842c89d8362deed48))

## [0.1.0-alpha.108](https://github.com/petauron/vastora/compare/v0.1.0-alpha.107...v0.1.0-alpha.108) (2026-09-08)


### Features

* add native Dante landing MVP and recovery fixes ([#359](https://github.com/petauron/vastora/issues/359)) ([291c402](https://github.com/petauron/vastora/commit/291c40279827952fc48131787928f6fea6ed3be7))

## [0.1.0-alpha.107](https://github.com/petauron/vastora/compare/v0.1.0-alpha.106...v0.1.0-alpha.107) (2026-09-07)


### Bug Fixes

* **security:** refresh CodeQL alerts on main pushes ([#342](https://github.com/petauron/vastora/issues/342)) ([926db7a](https://github.com/petauron/vastora/commit/926db7a65bbff70e8d98392ebdbb540c14b02610)), closes [#304](https://github.com/petauron/vastora/issues/304)

## [0.1.0-alpha.106](https://github.com/petauron/vastora/compare/v0.1.0-alpha.105...v0.1.0-alpha.106) (2026-09-07)


### Bug Fixes

* **3x-ui:** shorten subscription region labels ([#355](https://github.com/petauron/vastora/issues/355)) ([362262f](https://github.com/petauron/vastora/commit/362262fef7a1413722ba16461da0fe334dff6604))
* **agent:** keep remote updates schema compatible ([#344](https://github.com/petauron/vastora/issues/344)) ([01d9391](https://github.com/petauron/vastora/commit/01d939140a2eb561fb9f388da0e5fdf2c73fad70)), closes [#341](https://github.com/petauron/vastora/issues/341)

## [0.1.0-alpha.105](https://github.com/petauron/vastora/compare/v0.1.0-alpha.104...v0.1.0-alpha.105) (2026-09-03)


### Features

* **center:** add REALITY behavior security check ([#343](https://github.com/petauron/vastora/issues/343)) ([bc7adb4](https://github.com/petauron/vastora/commit/bc7adb496763e6820e14b7279717c7cc5daaecb7)), closes [#321](https://github.com/petauron/vastora/issues/321)
* **center:** harden direct Tunnel login ([#338](https://github.com/petauron/vastora/issues/338)) ([49825cf](https://github.com/petauron/vastora/commit/49825cfe8437f5825a98be55804856b1d66cdf79))
* **cpa:** publish authenticated client API ([#345](https://github.com/petauron/vastora/issues/345)) ([452ec06](https://github.com/petauron/vastora/commit/452ec060eb75ac4644d53f573f352771f3a465f9))

## [0.1.0-alpha.104](https://github.com/petauron/vastora/compare/v0.1.0-alpha.103...v0.1.0-alpha.104) (2026-09-03)


### Features

* **agent:** reconnect offline nodes in place ([#336](https://github.com/petauron/vastora/issues/336)) ([fca9f89](https://github.com/petauron/vastora/commit/fca9f895820d6d6c0bae837159ec0f941ed87fc4))

## [0.1.0-alpha.103](https://github.com/petauron/vastora/compare/v0.1.0-alpha.102...v0.1.0-alpha.103) (2026-09-03)


### Bug Fixes

* **center:** update agents concurrently ([#334](https://github.com/petauron/vastora/issues/334)) ([3565a1b](https://github.com/petauron/vastora/commit/3565a1b94c32443d48ded1503fcc9891d1559c66))

## [0.1.0-alpha.102](https://github.com/petauron/vastora/compare/v0.1.0-alpha.101...v0.1.0-alpha.102) (2026-09-03)


### Bug Fixes

* **agent:** recover interrupted migration tasks ([#332](https://github.com/petauron/vastora/issues/332)) ([63468d0](https://github.com/petauron/vastora/commit/63468d0a259234556f5f9441c574b777029e20dd))

## [0.1.0-alpha.101](https://github.com/petauron/vastora/compare/v0.1.0-alpha.100...v0.1.0-alpha.101) (2026-09-03)


### Features

* streamline alpha application and node management ([#330](https://github.com/petauron/vastora/issues/330)) ([d78a3aa](https://github.com/petauron/vastora/commit/d78a3aad7d3725cab28d638dcefac7f0b6670bb3)), closes [#329](https://github.com/petauron/vastora/issues/329)

## [0.1.0-alpha.100](https://github.com/petauron/vastora/compare/v0.1.0-alpha.99...v0.1.0-alpha.100) (2026-09-03)


### Features

* **install:** support Debian and Ubuntu node installers ([#327](https://github.com/petauron/vastora/issues/327)) ([c396209](https://github.com/petauron/vastora/commit/c396209e983cf771e270a32156e96e67626e4da6))

## [0.1.0-alpha.99](https://github.com/petauron/vastora/compare/v0.1.0-alpha.98...v0.1.0-alpha.99) (2026-09-03)


### Features

* **center:** cascade updates to remote agents ([4a283d9](https://github.com/petauron/vastora/commit/4a283d9a202bbcd91ba242a1ed12d38074193ac6))
* **network:** separate node-local protocol ingress ([ef1bfaa](https://github.com/petauron/vastora/commit/ef1bfaa993676362709f3cbc496da9327f5eb329))

## [0.1.0-alpha.98](https://github.com/petauron/vastora/compare/v0.1.0-alpha.97...v0.1.0-alpha.98) (2026-09-02)


### Bug Fixes

* **center:** acknowledge superseded gateway results ([3445c16](https://github.com/petauron/vastora/commit/3445c16fc2495d5667e8d0b1c8c48245b281d920))

## [0.1.0-alpha.97](https://github.com/petauron/vastora/compare/v0.1.0-alpha.96...v0.1.0-alpha.97) (2026-09-02)


### Bug Fixes

* **center:** preserve private DNS during Agent restarts ([8a845b3](https://github.com/petauron/vastora/commit/8a845b3adf13933d5eff3dfe5c51e85fd7ee49c3))

## [0.1.0-alpha.96](https://github.com/petauron/vastora/compare/v0.1.0-alpha.95...v0.1.0-alpha.96) (2026-09-02)


### Bug Fixes

* **agent:** reconcile stale protected gateway state ([33f5dd8](https://github.com/petauron/vastora/commit/33f5dd834b6e8ae6881c61c09ae1a2f94d59595f))

## [0.1.0-alpha.95](https://github.com/petauron/vastora/compare/v0.1.0-alpha.94...v0.1.0-alpha.95) (2026-09-02)


### Bug Fixes

* **upgrade:** defer loopback Center verification ([1b521bc](https://github.com/petauron/vastora/commit/1b521bc596ab4eb0ef4000b840119545a5c03a40))

## [0.1.0-alpha.94](https://github.com/petauron/vastora/compare/v0.1.0-alpha.93...v0.1.0-alpha.94) (2026-09-02)


### Bug Fixes

* **upgrade:** recover an unhealthy Center ([ca92b99](https://github.com/petauron/vastora/commit/ca92b991579f02f561db4a6cd487192b5935864b))

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
