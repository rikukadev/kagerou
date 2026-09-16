package scaffold

// 生成に使った入力を kagerou.yaml に残す(#207)。
//
// バージョンの印(#212)だけでは「古い」ことしか分からない。**何が変わるのか**を
// 出すには生成し直して比べるしかなく、そのためには生成に使った入力が要る。
// 入力は kagerou.yaml から完全には復元できない(compute / entrypoint / 検出値は
// 設定に出ない)ので、生成時に書き残す。
//
// 置き場所を kagerou.yaml のコメントにしたのは、**このファイルは kagerou が
// 所有している**(--force で上書きする)ため。新しいファイルを増やすと、
// 利用者のリポジトリに .gitignore や review の面倒を持ち込む。
//
// 設定のキーとして足さないのは、CONTRACT の kagerou.yaml スキーマが
// 外部が依存してよい契約だから。コメントなら契約を広げずに済む。

import (
	"bytes"
	"encoding/json"
	"strings"
)

// genRecordPrefix は記録行の目印。
const genRecordPrefix = "# kagerou:generated "

// GenRecord は生成入力を 1 行の JSON にする。テンプレートから呼ぶ。
//
// 検出値(Framework / Port / HealthPath …)も含める。再生成したときに
// **そのときの検出結果ではなく、生成時の入力**で比べたいため — 差分に
// 「リポジトリが変わった」ぶんが混ざると、ツール側の変更が埋もれる。
func (p Params) GenRecord() string {
	b, err := json.Marshal(p.canonical())
	if err != nil {
		return ""
	}
	return genRecordPrefix + string(b)
}

// canonical は「実際に生成に使われた値」に揃える。
//
// 空文字と既定値は同じ意味なので(Compute 未指定 = lambda)、そのまま記録すると
// **等価な指定で記録行だけが変わる**。生成物が同じなのに diff が出ることになり、
// upgrade --check が信用できなくなる。
func (p Params) canonical() Params {
	if p.Driver == "" {
		p.Driver = "stack"
	}
	if !p.Static() && p.Compute == "" {
		p.Compute = "lambda"
	}
	// Entrypoint は ALB() の判定がドメインの有無にも依るので、ここでは埋めない
	// (埋めると「ドメインが無いのに alb」という嘘の記録になる)
	return p
}

// ParseGenRecord は kagerou.yaml から生成入力を読む。
// 記録が無い(v0.13 以前に生成された)なら ok=false。
func ParseGenRecord(body []byte) (Params, bool) {
	for _, line := range bytes.Split(body, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if !strings.HasPrefix(s, genRecordPrefix) {
			continue
		}
		var p Params
		if err := json.Unmarshal([]byte(s[len(genRecordPrefix):]), &p); err != nil {
			return Params{}, false
		}
		return p, true
	}
	return Params{}, false
}

// Owner は生成物の持ち主。upgrade --check の出し分けに使う。
type Owner int

const (
	// OwnerKagerou は kagerou が丸ごと生成し直すファイル(workflow / base / kagerou.yaml)。
	// 差分が出たら**取り込むべき変更**なので、そのまま報告する。
	OwnerKagerou Owner = iota
	// OwnerUser は利用者が手を入れる前提のファイル(template.yaml / Dockerfile)。
	// 差分が出るのは正常なので、件数だけ伝えて中身は出さない。
	// ここを他と同じ声で出すと、毎回鳴って誰も読まなくなる。
	OwnerUser
)

// GeneratedFile は「いま生成するとこうなる」1 ファイル。
type GeneratedFile struct {
	Path  string
	Body  []byte
	Owner Owner
}

// Generate は書き出さずに、生成物の中身だけを返す(upgrade --check 用)。
//
// Run と同じ fileSpecs を引くので、片方だけ増えることがない。Dockerfile は
// 含めない — あれは雛形の新規生成か LWA の 1 行注入で、丸ごと比較する対象ではない。
func Generate(p Params, sel Targets) ([]GeneratedFile, error) {
	var out []GeneratedFile
	for _, f := range fileSpecs(p, sel) {
		if !f.enabled {
			continue
		}
		body, err := render(f.tmpl, p)
		if err != nil {
			return nil, err
		}
		owner := OwnerUser
		if f.overwrite {
			owner = OwnerKagerou
		}
		out = append(out, GeneratedFile{Path: f.path, Body: body, Owner: owner})
	}
	return out, nil
}
