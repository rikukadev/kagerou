# Changelog

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
