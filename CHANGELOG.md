# Changelog

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
