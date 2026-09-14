# Changelog

## [0.10.0](https://github.com/rikukadev/kagerou/compare/v0.9.0...v0.10.0) (2026-09-14)


### Features

* **alb:** make the ALB base per-app (teams outgrow a single shared one) ([#142](https://github.com/rikukadev/kagerou/issues/142)) ([39ad708](https://github.com/rikukadev/kagerou/commit/39ad708815dff9c4cbb7422d02a2ab6ce3f29d36))
* **config:** resolve {base_domain} from the preview base SSM contract ([#138](https://github.com/rikukadev/kagerou/issues/138)) ([6044b54](https://github.com/rikukadev/kagerou/commit/6044b5487773040ec96eafb58aebdb4783de27f1))
* **init:** put lambda behind the shared ALB so custom domains are the default ([#131](https://github.com/rikukadev/kagerou/issues/131)) ([6107233](https://github.com/rikukadev/kagerou/commit/61072330fbe5b05025a0aa7aa8098d83a173d37c))
* **init:** scaffold {base_domain} when the base was found in SSM ([#145](https://github.com/rikukadev/kagerou/issues/145)) ([7729014](https://github.com/rikukadev/kagerou/commit/772901444540ca026d0daa138d6d9dcb97aa908b))


### Bug Fixes

* **basedomain:** look for the base domain in the app's region too ([#143](https://github.com/rikukadev/kagerou/issues/143)) ([31cfea4](https://github.com/rikukadev/kagerou/commit/31cfea403a2297c792100d52367f5899826b3c5e))
* **dist:** npm の license 表記を Apache-2.0 に直す ([f5f4623](https://github.com/rikukadev/kagerou/commit/f5f46239cea721fa35120fbd27460b686f3e1cf9))
* **iam-policy:** derive the permissions that only fail under a scoped CI role ([#134](https://github.com/rikukadev/kagerou/issues/134)) ([d4984f6](https://github.com/rikukadev/kagerou/commit/d4984f6a20e976408c426ab3e17662993c6bf788))
* **iam-policy:** drift 検出を CI に入れ、残る生成器の穴を塞ぐ ([6f32793](https://github.com/rikukadev/kagerou/commit/6f32793b424c7c9425756b58975e213343bf7be4)), closes [#135](https://github.com/rikukadev/kagerou/issues/135)


### Documentation

* 姉妹ツールを mahoroba と名指しする ([8dc5d85](https://github.com/rikukadev/kagerou/commit/8dc5d85766d9e98c48e20325482fe6cc20e33a6a))

## [0.9.0](https://github.com/rikukadev/kagerou/compare/v0.8.0...v0.9.0) (2026-09-13)


### Features

* **appscan:** detect the URL layout (cross-origin / path-based) — [#109](https://github.com/rikukadev/kagerou/issues/109) v1 ([#110](https://github.com/rikukadev/kagerou/issues/110)) ([fcd0c2b](https://github.com/rikukadev/kagerou/commit/fcd0c2b01ded721878dceccc06a26606c09ee048))
* detect and scaffold SNS (topic, fan-out wiring, IAM) ([#98](https://github.com/rikukadev/kagerou/issues/98)) ([9fc479a](https://github.com/rikukadev/kagerou/commit/9fc479aebc88ae549f9be8c31092d69a65fa4e02))
* **dist:** npm から入れられるようにする ([588d05c](https://github.com/rikukadev/kagerou/commit/588d05ce5ba096a5838b889486f5004acdf34273))
* **ecs:** let tasks run in existing private subnets (base option) ([#108](https://github.com/rikukadev/kagerou/issues/108)) ([2fc2c22](https://github.com/rikukadev/kagerou/commit/2fc2c224d9735de59c53127d6a4b1673dff45ab3))
* **init:** compute option — lambda (default) | ecs (Fargate + shared ALB) ([#106](https://github.com/rikukadev/kagerou/issues/106)) ([0075f62](https://github.com/rikukadev/kagerou/commit/0075f627fba8cd33d3079b6e92b81d8d74ee589e))
* **init:** plan に「前提(作らない)」の節を足す ([bb872d1](https://github.com/rikukadev/kagerou/commit/bb872d167a4a1f497a87696b32571a301c7341c8)), closes [#123](https://github.com/rikukadev/kagerou/issues/123)
* **init:** recommend the entrypoint from repository facts ([eb88463](https://github.com/rikukadev/kagerou/commit/eb88463210865befd5f55ddb9da84c7d26e9ae42))
* **init:** static 構成を見分けて、compute 用の成果物を出さない ([#104](https://github.com/rikukadev/kagerou/issues/104)) ([119795c](https://github.com/rikukadev/kagerou/commit/119795cb2f6849b0549450484593945c4d90a9b9))
* kagerou diagnose — read-only recommendation for any repository ([549ddef](https://github.com/rikukadev/kagerou/commit/549ddef2a91c55099108be38f1927a8d8a7e5738))
* peer linking — resolve the partner environment by name at up ([#99](https://github.com/rikukadev/kagerou/issues/99)/[#109](https://github.com/rikukadev/kagerou/issues/109)) ([#117](https://github.com/rikukadev/kagerou/issues/117)) ([c397982](https://github.com/rikukadev/kagerou/commit/c397982124672f96a639ebf42dabd0718663ce96))
* **static:** preview base に routing を足して SPA のディープリンクを直す ([#97](https://github.com/rikukadev/kagerou/issues/97)) ([2ab89db](https://github.com/rikukadev/kagerou/commit/2ab89db3dc3c8a2f9a39c70d9adc4e9de93cdb5e))


### Bug Fixes

* **dist:** npm パッケージから wrapper が抜けていた ([afd616a](https://github.com/rikukadev/kagerou/commit/afd616a9d409eee9da8fa408df3ebdcadbbad4e1))
* **scaffold:** sashiki hook の `|| true` 回避策を外す ([#114](https://github.com/rikukadev/kagerou/issues/114)) ([b6adbcd](https://github.com/rikukadev/kagerou/commit/b6adbcd24677e708af7d615b252d009e2c204372))
* **stack:** name the failed resource and reason on create/update failure ([#116](https://github.com/rikukadev/kagerou/issues/116)) ([53d482e](https://github.com/rikukadev/kagerou/commit/53d482e5f98650765a2ef2ae6b0b2a9416a26280))
* **static:** pass the configured prefix to Sync ([833fed7](https://github.com/rikukadev/kagerou/commit/833fed73d74c0d5f1946c69080b2a09a617aba4d))


### Documentation

* make per-app the default preview base, with reasoning ([593f2b2](https://github.com/rikukadev/kagerou/commit/593f2b20a43f9f5f98a89ebb742f93e5f1b39675))

## [0.8.0](https://github.com/rikukadev/kagerou/compare/v0.7.0...v0.8.0) (2026-09-13)


### Features

* appscan — repo scanning as a standalone component + deeper inference ([#90](https://github.com/rikukadev/kagerou/issues/90)) ([527b64b](https://github.com/rikukadev/kagerou/commit/527b64b4a42251a14967ce0c9bfaa046bed0b6dd))
* **appscan:** scan recursively for monorepos ([#92](https://github.com/rikukadev/kagerou/issues/92)) ([30c3cef](https://github.com/rikukadev/kagerou/commit/30c3cefb01848217a32423a360a9a3227b0dbb7f))


### Bug Fixes

* **init:** use the scaffolded Dockerfile port in template.yaml too ([#93](https://github.com/rikukadev/kagerou/issues/93)) ([13f0d62](https://github.com/rikukadev/kagerou/commit/13f0d6263457e53713ba20bb51386000421b223c))

## [0.7.0](https://github.com/rikukadev/kagerou/compare/v0.6.0...v0.7.0) (2026-09-13)


### Features

* **init:** choose an AWS profile and check permissions before creating anything ([1bc17a3](https://github.com/rikukadev/kagerou/commit/1bc17a37c7dc210ae72f3771181f01cf2e88dae4))

## [0.6.0](https://github.com/rikukadev/kagerou/compare/v0.5.0...v0.6.0) (2026-09-12)


### Features

* add kagerou serve (read-only HTTP for environments) ([#16](https://github.com/rikukadev/kagerou/issues/16)) ([#66](https://github.com/rikukadev/kagerou/issues/66)) ([e04868e](https://github.com/rikukadev/kagerou/commit/e04868e72dd2e58a44cd8d531136b2506af3f888))
* base↔env data contract over SSM + Terraform reference base ([#75](https://github.com/rikukadev/kagerou/issues/75)) ([#82](https://github.com/rikukadev/kagerou/issues/82)) ([95e82c1](https://github.com/rikukadev/kagerou/commit/95e82c1da6387599c4aa532679a77e23286c2a57))
* generate OIDC trust policy and permissions boundary (iam-policy --doc) ([#63](https://github.com/rikukadev/kagerou/issues/63)) ([d4d4fa1](https://github.com/rikukadev/kagerou/commit/d4d4fa18e84d915ef25c43f86fc90131b8856691))
* iam-policy execution role and drift check (completes [#53](https://github.com/rikukadev/kagerou/issues/53)) ([#69](https://github.com/rikukadev/kagerou/issues/69)) ([954d0ce](https://github.com/rikukadev/kagerou/commit/954d0ce9f150c3d5319fbb03aeb12b1e37398db4))
* **init:** AWS に作る前に構成と費用を見せて y/n を取る ([#85](https://github.com/rikukadev/kagerou/issues/85)) ([31134e5](https://github.com/rikukadev/kagerou/commit/31134e527b25b013d2fe54378d22ab531f72846b))
* **init:** preflight auth check with in-place login ([#73](https://github.com/rikukadev/kagerou/issues/73)) ([0352824](https://github.com/rikukadev/kagerou/commit/0352824c7062469444a1aceb7a30d2a574dbb457))
* make the preview base per-app ({name}.{project}.&lt;zone&gt;) ([#77](https://github.com/rikukadev/kagerou/issues/77)) ([9844d1a](https://github.com/rikukadev/kagerou/commit/9844d1a41e68f8d06896c2972c581e4350669e7f))
* reject up onto another user's environment (kagerou:owner) ([#78](https://github.com/rikukadev/kagerou/issues/78)) ([5c9c530](https://github.com/rikukadev/kagerou/commit/5c9c5309c5fa22443886bf3698180ad8a13c04ca))
* scaffold a framework-specific Dockerfile in init ([#61](https://github.com/rikukadev/kagerou/issues/61)) ([#67](https://github.com/rikukadev/kagerou/issues/67)) ([a9a8756](https://github.com/rikukadev/kagerou/commit/a9a875616f8c96dde38781762180c892b84a3a3a))
* stream stack progress during up/down waits ([#47](https://github.com/rikukadev/kagerou/issues/47)) ([#65](https://github.com/rikukadev/kagerou/issues/65)) ([b119bf5](https://github.com/rikukadev/kagerou/commit/b119bf595161d30bf47712cf1237461f00bfd480))


### Bug Fixes

* chip away at v0.3 review follow-ups ([#51](https://github.com/rikukadev/kagerou/issues/51)) ([#68](https://github.com/rikukadev/kagerou/issues/68)) ([8043435](https://github.com/rikukadev/kagerou/commit/804343511146a2393d7ea93e9ad9c2820287b592))
* **static:** resolve the bucket's region before uploading ([#83](https://github.com/rikukadev/kagerou/issues/83)) ([e8a5a62](https://github.com/rikukadev/kagerou/commit/e8a5a6272adb155ba1a83a812ee9e08965c083c8))
* **static:** run post_up before the content sync ([#84](https://github.com/rikukadev/kagerou/issues/84)) ([37bba56](https://github.com/rikukadev/kagerou/commit/37bba568f4fee6225be8bb0efdd9660797505b7e))


### Documentation

* README刷新(ECSペルソナ)+ 配信基盤/static driver 設計案 ([#62](https://github.com/rikukadev/kagerou/issues/62)) ([389194e](https://github.com/rikukadev/kagerou/commit/389194e69ffd38a12f5daf90624dde3d5604f054))

## [0.5.0](https://github.com/rikukadev/kagerou/compare/v0.4.0...v0.5.0) (2026-09-12)


### Features

* kagerou iam-policy — generate a least-privilege CI role policy ([#59](https://github.com/rikukadev/kagerou/issues/59)) ([5c326a9](https://github.com/rikukadev/kagerou/commit/5c326a966eda921b6a7bbc6703888cd3bd5f7aca)), closes [#27](https://github.com/rikukadev/kagerou/issues/27)
* readiness_path — don't report ready until the app responds ([#57](https://github.com/rikukadev/kagerou/issues/57)) ([ca7259e](https://github.com/rikukadev/kagerou/commit/ca7259ec5752efe400755cc0fc19d55a04057339))


### Bug Fixes

* pin action refs to moving [@v0](https://github.com/v0) tag and reap via action ([#49](https://github.com/rikukadev/kagerou/issues/49), [#50](https://github.com/rikukadev/kagerou/issues/50)) ([#58](https://github.com/rikukadev/kagerou/issues/58)) ([3830bb0](https://github.com/rikukadev/kagerou/commit/3830bb0200fc814615be32ecb5a1b70c5925b1d9))
* scope reap/list to kagerou:project to stop cross-project deletion ([#48](https://github.com/rikukadev/kagerou/issues/48)) ([#55](https://github.com/rikukadev/kagerou/issues/55)) ([734e65a](https://github.com/rikukadev/kagerou/commit/734e65a76994ee5dfad473f2996ba98c8f11cd10))

## [0.4.0](https://github.com/rikukadev/kagerou/compare/v0.3.0...v0.4.0) (2026-09-12)


### Features

* post_up hook — deliver stack outputs as KAGEROU_* env vars ([#45](https://github.com/rikukadev/kagerou/issues/45)) ([d18ce2e](https://github.com/rikukadev/kagerou/commit/d18ce2ed7e9eca21335124713eb86acdbcc749dd)), closes [#23](https://github.com/rikukadev/kagerou/issues/23)
* url_template — decide the environment URL before creation ([#46](https://github.com/rikukadev/kagerou/issues/46)) ([0677415](https://github.com/rikukadev/kagerou/commit/06774154c06cf5330f9e3f6581c6f4a921bb0360)), closes [#25](https://github.com/rikukadev/kagerou/issues/25)


### Bug Fixes

* lint (QF1001 De Morgan) — [#42](https://github.com/rikukadev/kagerou/issues/42) を CI 未確認のままマージした修正 ([#43](https://github.com/rikukadev/kagerou/issues/43)) ([8ea6e09](https://github.com/rikukadev/kagerou/commit/8ea6e09b2eb2454d3fc7424e68f8bca4f69c88f3))

## [0.3.0](https://github.com/rikukadev/kagerou/compare/v0.2.0...v0.3.0) (2026-09-12)


### Features

* **init:** terser prompts + AWS setup question (run now / script / skip) ([#41](https://github.com/rikukadev/kagerou/issues/41)) ([c561d27](https://github.com/rikukadev/kagerou/commit/c561d27ca7fa49831b0eca1ee23dc43a5f5c1231))
* **init:** リポジトリ検出 + ウィザード形式の対話 UI ([#38](https://github.com/rikukadev/kagerou/issues/38)) ([5ef9953](https://github.com/rikukadev/kagerou/commit/5ef9953357dd95b47831ee6997c817d6b55f0e61)), closes [#37](https://github.com/rikukadev/kagerou/issues/37)
* user-facing strings in English ([#40](https://github.com/rikukadev/kagerou/issues/40)) ([732a4e7](https://github.com/rikukadev/kagerou/commit/732a4e79c02b3bbd5bbdef22c113b3759f7b5f3d))

## [0.2.0](https://github.com/rikukadev/kagerou/compare/v0.1.0...v0.2.0) (2026-09-12)


### Features

* kagerou init — 導入スキャフォールドの完成(TUI チェックリスト付き) ([#35](https://github.com/rikukadev/kagerou/issues/35)) ([816291b](https://github.com/rikukadev/kagerou/commit/816291bd3d22c7c6198127ef861574fa1f81fad4))


### Bug Fixes

* **action:** インストール先を RUNNER_TEMP + GITHUB_PATH に変更 ([#30](https://github.com/rikukadev/kagerou/issues/30)) ([73f4a1f](https://github.com/rikukadev/kagerou/commit/73f4a1faa790f76c02e69af402c354d7b2e426f8))
* main の CI を green に戻す(stdlib CVE と lint) ([#36](https://github.com/rikukadev/kagerou/issues/36)) ([c3c191a](https://github.com/rikukadev/kagerou/commit/c3c191ab11462f1827aee4bdc90092cec87c34a9))

## 0.1.0 (2026-09-11)


### Features

* composite action と PR プレビュー/reap のサンプル workflow ([#20](https://github.com/rikukadev/kagerou/issues/20)) ([571a971](https://github.com/rikukadev/kagerou/commit/571a971aa5eb6fd10a7ce3bbe316ce6522c68cb8)), closes [#8](https://github.com/rikukadev/kagerou/issues/8)
* hooks — pre_up / post_down の実行 ([#19](https://github.com/rikukadev/kagerou/issues/19)) ([d8603bc](https://github.com/rikukadev/kagerou/commit/d8603bc2046878e96494aa676645a24437abb9a2)), closes [#7](https://github.com/rikukadev/kagerou/issues/7)
* kagerou.yaml ローダと {name} 展開 (config package) ([#14](https://github.com/rikukadev/kagerou/issues/14)) ([b186fc1](https://github.com/rikukadev/kagerou/commit/b186fc1b827f63b407ac9388085e4dc1b6f12415)), closes [#3](https://github.com/rikukadev/kagerou/issues/3)
* list — タグ走査による状態レス一覧 ([#17](https://github.com/rikukadev/kagerou/issues/17)) ([f6fcaf4](https://github.com/rikukadev/kagerou/commit/f6fcaf4f6bf56b59e742edd8ee47e9e95593087c)), closes [#5](https://github.com/rikukadev/kagerou/issues/5)
* reap — TTL 切れ環境の回収と touch 上限 ([#18](https://github.com/rikukadev/kagerou/issues/18)) ([1a93b49](https://github.com/rikukadev/kagerou/commit/1a93b499b1f84ca6b202a9bb314306a717f80139)), closes [#6](https://github.com/rikukadev/kagerou/issues/6)
* **release:** publish binaries and install them in the action ([#29](https://github.com/rikukadev/kagerou/issues/29)) ([91e7385](https://github.com/rikukadev/kagerou/commit/91e738528e3fd48dcaa99dee330149cebd6c5d60))
* stack driver (up/down/url) と公開契約 CONTRACT.md ([#15](https://github.com/rikukadev/kagerou/issues/15)) ([45bf843](https://github.com/rikukadev/kagerou/commit/45bf84366d341828a12ccca60d733f05517f2f14)), closes [#4](https://github.com/rikukadev/kagerou/issues/4) [#13](https://github.com/rikukadev/kagerou/issues/13)


### Bug Fixes

* kagerou:source を URI 形式の opaque 文字列に変更 ([#22](https://github.com/rikukadev/kagerou/issues/22)) ([683f64b](https://github.com/rikukadev/kagerou/commit/683f64b7385829c1d2ee462491be24129e3831d2))


### Documentation

* 設計ドラフト v0 ([#1](https://github.com/rikukadev/kagerou/issues/1)) ([6bb708d](https://github.com/rikukadev/kagerou/commit/6bb708d306d088a9715db4d1f943d959ff996d29))


### Chores

* start versioning at 0.1.0 ([0da842a](https://github.com/rikukadev/kagerou/commit/0da842a4643273eae1d639722983f049ff3cecad))
