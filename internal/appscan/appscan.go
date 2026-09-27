// Package appscan は既存アプリのリポジトリを読み、「このアプリは何か」を
// 事実(Facts)として返す。
//
// 境界の約束(独立コンポーネントとして切り出せる形を保つ):
//   - リポジトリの **ファイルしか読まない**。exec も AWS も環境変数も見ない
//   - kagerou の他パッケージに依存しない(標準ライブラリのみ)
//   - ベストエフォート: 読めない/無いものは零値のまま。エラーで止まらない
//
// モノレポ対応: マーカー(package.json / go.mod / Dockerfile / compose)は
// ルートに無いことがある(例: api/ と web/ に分かれた 3 層構成)。走査は
// 再帰で、見つかった事実を 1 つの Facts にマージする(規則は merge 参照)。
//
// 環境側の検出(AWS アカウント・Route53・gh Variables 等)は scaffold.Detect が
// この Facts に重ねる。
package appscan

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Facts はリポジトリから読み取れた事実。
type Facts struct {
	Owner, Repo   string // .git/config の remote origin(ルートのみ)
	Region        string // samconfig.toml(ルートのみ・ファイル由来)
	Framework     string // next / remix / react-router / astro / nuxt / sveltejs / node / go / yii / laravel / php / spring-boot / quarkus / micronaut / java
	DBDriver      string // mysql2 / pg / go-sql-driver/mysql など(空 = DB 依存なし)
	HasDockerfile bool
	HasLWA        bool   // いずれかの Dockerfile に Lambda Web Adapter が入っているか
	DockerfileDir string // Dockerfile のあるディレクトリ(ルートからの相対。ルート直下なら "")
	// DockerfileName は注入対象のファイル名(既定 "Dockerfile")。`Dockerfile.lambda`
	// のようにビルド対象で分ける構成があるので、ディレクトリだけでは足りない(#151)。
	DockerfileName string
	// Dockerfiles はそのディレクトリで見つかった Dockerfile 全部(ソート済み)。
	// 複数あるとき「どれに注入するか」を利用者に選ばせるために持つ。
	Dockerfiles  []string
	AppPort      string   // Dockerfile の EXPOSE / compose の ports・expose から検出した listen ポート
	HasTemplate  bool     // ルートに template.yaml があるか
	Services     int      // サービス数(compose services / cmd/*/main.go / Dockerfile の最大)
	ServiceNames []string // 分かる場合のサービス名(cmd/<name>/main.go / compose の services)
	// Services2 は **サービスごとの事実**(#184)。ServiceNames は名前しか持たず、
	// 「どのサービスがどの Dockerfile から来るか」を表現できなかった。
	//
	// モノレポ(api/ と web/ が別アプリ)では、生成物が全サービスで同じイメージを
	// 指してしまう。名前の配列では、そこを直しようがない。
	//
	// ServiceNames は残す。1 イメージ複数バイナリ(cmd/*/main.go)の構成では
	// ディレクトリが分かれないので、こちらには載らない。
	ServiceFacts []ServiceFact
	// WithoutImage は「フレームワークは検出できたが Dockerfile が無い」ディレクトリ
	// (ルートは除く)。複数サービス構成では **環境に載らない** — framework には
	// 出るのに生成物のどこにも現れない、という黙った脱落を防ぐために持つ(#227)。
	WithoutImage []ServiceFact
	Realtime     bool // WebSocket / SSE の痕跡(30 秒上限のある入口を避ける根拠)
	// HealthPath は アプリのヘルスチェック用パス(#165)。LWA の readiness 設定 /
	// Dockerfile の HEALTHCHECK / compose の healthcheck から拾う。
	// 空なら見つからなかった("/" しか無い場合も空 = 既定と同じで言う価値がない)
	HealthPath string
	// PublishesImage は CI が既にイメージを作って公開していること。
	// ImageRegistry は分かれば "ghcr.io" / "ecr"。
	PublishesImage bool
	ImageRegistry  string
	Wants          Wants // 依存から推定した「アプリが使うもの」(全ディレクトリの OR)

	// URLShape は設定ファイルから推定した URL 構成(kagerou#109 v1):
	//   "cross" = フロントと API が別オリジン(traefik Host ラベル / nginx
	//             server_name / CORS 依存 / *_API_URL 系 env 名)
	//   "path"  = 同一オリジンのパス分割(vite server.proxy / next rewrites)
	//   ""      = 単一オリジン or 不明。コードのリテラルは読まない(ノイズ > 価値)
	URLShape string
	Hosts    []string // traefik / nginx に書かれていた実ホスト名(最大 4、参考表示用)

	// 走査中の中間信号。URLShape は Scan の最後に確定する。
	//   pathHint     本番のパス分割(next rewrites)。デプロイ後もそう配られる
	//   crossHint    別オリジン前提の傍証(CORS の依存 / 手書きの CORS ヘッダ / *_API_URL)
	//   devProxyHint 開発時だけのパス分割(vite server.proxy)。localhost の話でしかない
	pathHint, crossHint, devProxyHint bool
}

// ServiceFact は 1 サービスぶんの事実。ディレクトリが分かれている構成
// (モノレポ)でだけ埋まる。
//
// **1 イメージ複数バイナリ(cmd/<name>/main.go)はここに載らない。** あれは
// サービスごとに Dockerfile が分かれないので、ServiceNames のままで足りる。
// 両方の形が実在するので、どちらかに寄せず並べて持つ。
type ServiceFact struct {
	Name string // サービス名(ディレクトリ名、または compose の services のキー)
	Dir  string // リポジトリルートからの相対。ルート直下なら ""
	// Dockerfile はこのサービスをビルドするファイル名。LWA が入っているものが
	// あればそれを優先する(素の Dockerfile が ECS 用、という分け方があるため)。
	Dockerfile string
	HasLWA     bool
	Framework  string // このディレクトリで検出したフレームワーク
	Port       string // EXPOSE / compose の ports
	HealthPath string // HEALTHCHECK 等から読めたパス
}

// Wants は依存関係(package.json / go.mod / compose)から推定した、アプリが
// 使う周辺リソース。preview 環境の形の推論に使う: DynamoDB / SNS / SQS / S3 は
// per-env に置いてもアイドル $0 なのでテンプレートに同梱でき、Redis /
// OpenSearch は常時課金なので共有ベース側に置く(利用側が判断する材料)。
type Wants struct {
	DynamoDB   bool
	SNS        bool
	SQS        bool
	S3         bool
	Redis      bool
	OpenSearch bool
}

// Any はどれか 1 つでも検出されたか。
func (w Wants) Any() bool {
	return w.DynamoDB || w.SNS || w.SQS || w.S3 || w.Redis || w.OpenSearch
}

func (w Wants) or(o Wants) Wants {
	return Wants{
		DynamoDB:   w.DynamoDB || o.DynamoDB,
		SNS:        w.SNS || o.SNS,
		SQS:        w.SQS || o.SQS,
		S3:         w.S3 || o.S3,
		Redis:      w.Redis || o.Redis,
		OpenSearch: w.OpenSearch || o.OpenSearch,
	}
}

// 再帰走査の安全弁。preview 対象のアプリで踏み抜くことはまず無い値にしてある。
const (
	maxDepth = 4   // ルートを 0 としてこの深さまで
	maxDirs  = 512 // 訪問ディレクトリ総数の上限
)

// skipDirs は中を見ないディレクトリ。依存の実体や生成物は「このアプリの事実」ではない。
var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "out": true,
	"coverage": true, "testdata": true, "tmp": true, "target": true,
}

// Scan は dir を再帰的に走査して Facts を返す。
func Scan(dir string) Facts {
	f := Facts{}
	f.Owner, f.Repo = gitRemote(filepath.Join(dir, ".git", "config"))
	f.Region = samconfigRegion(dir)
	f.HasTemplate = exists(filepath.Join(dir, "template.yaml"))
	visited := 0
	walk(dir, "", 0, &f, &visited)

	// CI の workflow はルートにしか無いので walk の外で読む
	f.PublishesImage, f.ImageRegistry = scanWorkflows(dir)

	// 走査順は os.ReadDir 依存で安定しない。生成物の並び(サービスごとの
	// パラメータやリスナールール)が実行のたびに入れ替わると差分が読めないので、
	// ここで固定する。
	sort.Slice(f.ServiceFacts, func(i, j int) bool {
		return f.ServiceFacts[i].Dir < f.ServiceFacts[j].Dir
	})
	// Services は「1 ディレクトリで見えた最大値」なので、**ディレクトリをまたぐ
	// モノレポを数えられない**(api/ と web/ がそれぞれ 1 で、最大は 1)。
	// サービスごとの事実が 2 件以上あるなら、そちらが実際の数。
	//
	// 名前も同じ理由で入れ替える。ServiceNames は ALB のホスト規約
	// <service>-<env> に使われるので、ここがずれると URL がずれる。
	if len(f.ServiceFacts) > 1 && len(f.ServiceFacts) > f.Services {
		f.Services = len(f.ServiceFacts)
		names := make([]string, 0, len(f.ServiceFacts))
		for _, sf := range f.ServiceFacts {
			names = append(names, sf.Name)
		}
		f.ServiceNames = names
	}

	// URL 構成の確定。証拠が「デプロイ後の形」をどれだけ直接に語るかの順に見る:
	//   明示のホスト名 > 本番のパス分割 > 別オリジンの傍証 > 開発時だけのパス分割
	//
	// vite の server.proxy が最後なのは、あれが localhost の話でしかないため。
	// SPA + API のリポジトリはほぼ必ず持っているので、これを上に置くと
	// 「本番は別オリジン、開発だけ同一オリジン」の構成を全部取り違える。
	switch {
	case len(f.Hosts) > 0:
		f.URLShape = "cross"
	case f.pathHint:
		f.URLShape = "path"
	case f.crossHint:
		f.URLShape = "cross"
	case f.devProxyHint:
		f.URLShape = "path"
	}
	return f
}

// walk はルート優先の深さ優先でディレクトリを回り、事実をマージしていく。
func walk(root, rel string, depth int, f *Facts, visited *int) {
	scanDir(filepath.Join(root, rel), rel, f)
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || skipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		if *visited >= maxDirs {
			return
		}
		*visited++
		walk(root, filepath.Join(rel, name), depth+1, f, visited)
	}
}

// frameworkRank はマージ時の優先度。具体的なフレームワーク > go > 素の node。
// モノレポ(web=素の React SPA + api=Go)ではコンテナ化対象の Go が勝つ。
func frameworkRank(fw string) int {
	switch {
	case fw == "":
		return 0
	case fw == "node", fw == "php":
		return 1
	case fw == "go", fw == "java":
		return 2
	case isPHPFramework(fw), isJavaFramework(fw):
		// composer.json / pom.xml はデプロイされるアプリそのものを指す。JS の
		// フレームワークは同梱された assets バンドルとしても出てくるので、同点にしない。
		return 4
	default: // next / remix-run / react-router / astro / nuxt / sveltejs
		return 3
	}
}

func isJavaFramework(fw string) bool {
	switch fw {
	case "spring-boot", "quarkus", "micronaut":
		return true
	}
	return false
}

func isPHPFramework(fw string) bool {
	for _, f := range phpFrameworks {
		if f[1] == fw {
			return true
		}
	}
	return false
}

// scanDir は 1 ディレクトリの事実を f にマージする。
// - Framework: frameworkRank が高い方が勝つ(同点は先着 = ルート優先)
// - DBDriver / AppPort: 先着(ルート優先)
// - Wants: OR
// - Dockerfile: 先着。場所を DockerfileDir に覚える(LWA 注入先になる)
func scanDir(dir, rel string, f *Facts) {
	fw, db, wants := depsOf(dir)
	if frameworkRank(fw) > frameworkRank(f.Framework) {
		f.Framework = fw
	}
	if f.DBDriver == "" {
		f.DBDriver = db
	}
	f.Wants = f.Wants.or(wants)

	// サービスごとの事実。**マージ前に**、そのディレクトリで見えたものを記録する。
	// 既存のフィールドは「先着が勝つ」で 1 つに畳むので、ここを後から復元できない。
	recordService(dir, rel, fw, f)
	// フレームワークの痕跡はあるのにコンテナにできないディレクトリを覚える。
	// ルート(rel=="")は「リポジトリ全体」の話なのでサービス扱いしない
	if rel != "" && fw != "" && len(dockerfilesIn(dir)) == 0 {
		f.WithoutImage = append(f.WithoutImage, ServiceFact{
			Name: filepath.Base(rel), Dir: rel, Framework: fw,
		})
	}

	if !f.HasDockerfile {
		if names := dockerfilesIn(dir); len(names) > 0 {
			f.HasDockerfile = true
			f.DockerfileDir = rel
			f.Dockerfiles = names
			// LWA は **どれか 1 つにでも**入っていれば導入済み。素の Dockerfile が
			// ECS 用で、Lambda 用が別ファイル、という分け方は珍しくない(#151)
			for _, n := range names {
				b, err := os.ReadFile(filepath.Join(dir, n))
				if err != nil {
					continue
				}
				src := string(b)
				if strings.Contains(src, "lambda-adapter") {
					f.HasLWA = true
					f.DockerfileName = n // 既に入っているファイル = Lambda 用
				}
				if f.AppPort == "" {
					if m := exposeRe.FindAllStringSubmatch(src, -1); len(m) > 0 {
						f.AppPort = m[len(m)-1][1] // マルチステージなら最後の EXPOSE
					}
				}
			}
			if f.DockerfileName == "" {
				f.DockerfileName = names[0]
			}
			if f.HealthPath == "" {
				f.HealthPath = healthcheckPath(dir, names)
			}
		}
	}
	if f.AppPort == "" {
		f.AppPort = composePort(dir)
	}
	if f.HealthPath == "" {
		f.HealthPath = healthcheckPath(dir, nil)
	}
	if n := serviceCount(dir); n > f.Services {
		f.Services = n
		// 名前が取れるなら覚える(ALB のホスト規約 <service>-<env> に使う)。
		// cmd/*/main.go を優先(1 イメージ複数バイナリの形がそのまま出る)
		if names := cmdMainNames(dir); len(names) > 0 {
			f.ServiceNames = names
		} else if names := composeServiceNames(dir); len(names) > 0 {
			f.ServiceNames = names
		}
	}
	if !f.Realtime {
		f.Realtime = realtimeUsed(dir)
	}
	scanURLShape(dir, f)
}

// recordService はこのディレクトリを 1 サービスとして記録する。
//
// 条件は **Dockerfile を持っていること**。持たないディレクトリまで数えると、
// ただの置き場(docs/ や scripts/)がサービスになってしまう。
//
// ルート直下は記録しない。ルートに Dockerfile があるのは「リポジトリ全体で
// 1 アプリ」の形で、それは ServiceFacts で表現するものではない。
func recordService(dir, rel, framework string, f *Facts) {
	if rel == "" {
		return
	}
	names := dockerfilesIn(dir)
	if len(names) == 0 {
		return
	}
	sf := ServiceFact{Name: filepath.Base(rel), Dir: rel, Framework: framework}
	// LWA の入っているものを優先する。素の Dockerfile が ECS 用で、Lambda 用が
	// 別ファイル、という分け方があるため(#151)
	sf.Dockerfile = names[0]
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		src := string(b)
		if strings.Contains(src, "lambda-adapter") {
			sf.HasLWA = true
			sf.Dockerfile = n
		}
		if sf.Port == "" {
			if m := exposeRe.FindAllStringSubmatch(src, -1); len(m) > 0 {
				sf.Port = m[len(m)-1][1]
			}
		}
	}
	if sf.Port == "" {
		sf.Port = composePort(dir)
	}
	sf.HealthPath = healthcheckPath(dir, names)
	f.ServiceFacts = append(f.ServiceFacts, sf)
}

// serviceCount は「このディレクトリにいくつサービスがあるか」を数える。
// compose の services、cmd/*/main.go(Go の複数バイナリ)、Dockerfile の数の最大を取る。
// 正確な数より「1 つか、複数か」が判定に効く。
func serviceCount(dir string) int {
	n := composeServiceCount(dir)
	if m := cmdMainCount(dir); m > n {
		n = m
	}
	if n == 0 && (exists(filepath.Join(dir, "Dockerfile")) || goModuleMain(dir) || javaModule(dir)) {
		n = 1
	}
	return n
}

// javaModule は「このディレクトリが Java アプリ 1 個か」。pom.xml か build.gradle が
// あればサービスとして数える。packaging=pom(モノレポの集約 pom)はアプリでは
// ないので除く。これが無いと Dockerfile の無い Spring Boot が services 0 になり、
// recommend が「サーバが見つからない → static」と自信満々に間違える(#228)。
func javaModule(dir string) bool {
	if b, err := os.ReadFile(filepath.Join(dir, "pom.xml")); err == nil {
		return !strings.Contains(string(b), "<packaging>pom<")
	}
	return exists(filepath.Join(dir, "build.gradle")) || exists(filepath.Join(dir, "build.gradle.kts"))
}

var packageMainRe = regexp.MustCompile(`(?m)^package\s+main\b`)

// goModuleMain は「go.mod の直下に package main がある」= コンテナ化されていない
// Go のサーバか。cmd/<name>/ に分けず main.go を直置きする構成は普通にあり、
// Dockerfile も compose も無い(zip Lambda 等)とサービス数が 0 に見えてしまう。
// go.mod を要求するのは、サービスではない main.go(生成スクリプト等)を
// 数えないため。
func goModuleMain(dir string) bool {
	if !exists(filepath.Join(dir, "go.mod")) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "main.go"))
	return err == nil && packageMainRe.Match(b)
}

var composeServiceEntryRe = regexp.MustCompile(`(?m)^  ([a-zA-Z0-9_.-]+):\s*$`)

func composeServiceCount(dir string) int {
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.Index(s, "\nservices:")
		if i < 0 && !strings.HasPrefix(s, "services:") {
			continue
		}
		if i < 0 {
			i = 0
		}
		rest := s[i:]
		// 次のトップレベルキー(volumes: など)まで
		if j := regexp.MustCompile(`(?m)^[a-zA-Z]`).FindStringIndex(rest[1:]); j != nil {
			if k := regexp.MustCompile(`(?m)^(volumes|networks|configs|secrets):`).FindStringIndex(rest); k != nil && k[0] > 0 {
				rest = rest[:k[0]]
			}
		}
		if m := composeServiceEntryRe.FindAllString(rest, -1); len(m) > 0 {
			return len(m)
		}
	}
	return 0
}

// cmdMainCount は Go の「cmd/<name>/main.go」の数(複数バイナリ = 複数サービス)。
func cmdMainCount(dir string) int {
	return len(cmdMainNames(dir))
}

// cmdMainNames は cmd/<name>/main.go の <name>(= サービス名)を並べる。
// ALB の入口では「<service>-<env>」というホスト名の規約に使う。
func cmdMainNames(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "cmd"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && exists(filepath.Join(dir, "cmd", e.Name(), "main.go")) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// composeServiceNames は compose の services: 直下のキーを並べる。
func composeServiceNames(dir string) []string {
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.Index(s, "\nservices:")
		if i < 0 && !strings.HasPrefix(s, "services:") {
			continue
		}
		if i < 0 {
			i = 0
		}
		rest := s[i:]
		if k := regexp.MustCompile(`(?m)^(volumes|networks|configs|secrets):`).FindStringIndex(rest); k != nil && k[0] > 0 {
			rest = rest[:k[0]]
		}
		var names []string
		for _, m := range composeServiceEntryRe.FindAllStringSubmatch(rest, -1) {
			names = append(names, m[1])
		}
		if len(names) > 0 {
			return names
		}
	}
	return nil
}

// realtimeUsed は WebSocket / SSE の痕跡を探す。これがあると、オリジン応答に
// 上限のある入口(API Gateway の 30 秒、CloudFront の ~60 秒)では動かない。
func realtimeUsed(dir string) bool {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		s := string(b)
		for _, dep := range []string{"socket.io", "ws\"", "@fastify/websocket", "sockjs"} {
			if strings.Contains(s, dep) {
				return true
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		s := string(b)
		for _, dep := range []string{"gorilla/websocket", "nhooyr.io/websocket", "coder/websocket", "olahol/melody"} {
			if strings.Contains(s, dep) {
				return true
			}
		}
	}
	return false
}

var remoteURLRe = regexp.MustCompile(`(?:github\.com[:/])([^/\s]+)/([^/\s]+?)(?:\.git)?\s*$`)

// gitRemote は .git/config の [remote "origin"] url を読む(exec なしで済ませる)。
func gitRemote(gitConfig string) (owner, repo string) {
	b, err := os.ReadFile(gitConfig)
	if err != nil {
		return "", ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			inOrigin = t == `[remote "origin"]`
			continue
		}
		if inOrigin && strings.HasPrefix(t, "url") {
			if m := remoteURLRe.FindStringSubmatch(t); m != nil {
				return m[1], m[2]
			}
		}
	}
	return "", ""
}

var samconfigRegionRe = regexp.MustCompile(`(?m)^\s*region\s*=\s*"([^"]+)"`)

func samconfigRegion(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "samconfig.toml")); err == nil {
		if m := samconfigRegionRe.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

// 依存名 → 何を使うか。フレームワーク・DB と違い「あるだけ全部」拾う。
var (
	nodeFrameworks = []string{"next", "@remix-run/node", "react-router", "astro", "nuxt", "@sveltejs/kit"}
	// composer の require / require-dev から拾う。上から順に見て最初の一致を採る。
	phpFrameworks = [][2]string{
		{"yiisoft/yii2", "yii"},
		{"yiisoft/yii", "yii"},
		{"laravel/framework", "laravel"},
		{"symfony/framework-bundle", "symfony"},
		{"cakephp/cakephp", "cakephp"},
		{"codeigniter4/framework", "codeigniter"},
		{"slim/slim", "slim"},
	}
	nodeDBs = []string{"mysql2", "mysql", "pg", "postgres", "@prisma/client", "drizzle-orm"}
	goDBs   = []string{"go-sql-driver/mysql", "jackc/pgx", "lib/pq"}

	// Maven / Gradle の依存(groupId:artifactId のどこかに一致)から拾う。
	// 上から順に見て最初の一致を採る(composer と同じ流儀)。
	javaFrameworks = [][2]string{
		{"spring-boot", "spring-boot"}, // starter 群も parent もこの語を含む
		{"io.quarkus", "quarkus"},
		{"io.micronaut", "micronaut"},
	}
	javaDBs = []string{"mysql-connector-j", "mysql-connector-java", "postgresql", "mariadb-java-client"}
	// AWS SDK は v2(software.amazon.awssdk:dynamodb)と v1(aws-java-sdk-dynamodb)の
	// 両方の artifact 名に一致するよう、サービス名の語で引っ掛ける
	javaWants = map[string]func(*Wants){
		"dynamodb":                  func(w *Wants) { w.DynamoDB = true },
		"aws-sdk-java-sns":          func(w *Wants) { w.SNS = true },
		":sns":                      func(w *Wants) { w.SNS = true },
		"aws-java-sdk-sns":          func(w *Wants) { w.SNS = true },
		":sqs":                      func(w *Wants) { w.SQS = true },
		"aws-java-sdk-sqs":          func(w *Wants) { w.SQS = true },
		"spring-cloud-aws-sqs":      func(w *Wants) { w.SQS = true },
		":s3":                       func(w *Wants) { w.S3 = true },
		"aws-java-sdk-s3":           func(w *Wants) { w.S3 = true },
		"data-redis":                func(w *Wants) { w.Redis = true },
		"jedis":                     func(w *Wants) { w.Redis = true },
		"lettuce-core":              func(w *Wants) { w.Redis = true },
		"opensearch":                func(w *Wants) { w.OpenSearch = true },
		"spring-data-elasticsearch": func(w *Wants) { w.OpenSearch = true },
	}

	nodeWants = map[string]func(*Wants){
		"@aws-sdk/client-dynamodb":       func(w *Wants) { w.DynamoDB = true },
		"dynamoose":                      func(w *Wants) { w.DynamoDB = true },
		"@aws-sdk/client-sns":            func(w *Wants) { w.SNS = true },
		"@aws-sdk/client-sqs":            func(w *Wants) { w.SQS = true },
		"sqs-consumer":                   func(w *Wants) { w.SQS = true },
		"@aws-sdk/client-s3":             func(w *Wants) { w.S3 = true },
		"redis":                          func(w *Wants) { w.Redis = true },
		"ioredis":                        func(w *Wants) { w.Redis = true },
		"bullmq":                         func(w *Wants) { w.Redis = true },
		"@opensearch-project/opensearch": func(w *Wants) { w.OpenSearch = true },
		"@elastic/elasticsearch":         func(w *Wants) { w.OpenSearch = true },
	}
	goWants = map[string]func(*Wants){
		"service/dynamodb": func(w *Wants) { w.DynamoDB = true },
		"service/sns":      func(w *Wants) { w.SNS = true },
		"service/sqs":      func(w *Wants) { w.SQS = true },
		"service/s3":       func(w *Wants) { w.S3 = true },
		"go-redis":         func(w *Wants) { w.Redis = true },
		"gomodule/redigo":  func(w *Wants) { w.Redis = true },
		"opensearch-go":    func(w *Wants) { w.OpenSearch = true },
		"go-elasticsearch": func(w *Wants) { w.OpenSearch = true },
	}
)

// javaCoordinates は pom.xml / build.gradle(.kts) から依存の座標
// (groupId:artifactId)を集める。バージョン解決はしない — 何に依存して
// いるかが分かれば、フレームワークと周辺リソースの推定には足りる。
func javaCoordinates(dir string) []string {
	var out []string
	if b, err := os.ReadFile(filepath.Join(dir, "pom.xml")); err == nil {
		var pom struct {
			Packaging string `xml:"packaging"`
			Parent    struct {
				GroupID    string `xml:"groupId"`
				ArtifactID string `xml:"artifactId"`
			} `xml:"parent"`
			Dependencies struct {
				Dependency []struct {
					GroupID    string `xml:"groupId"`
					ArtifactID string `xml:"artifactId"`
				} `xml:"dependency"`
			} `xml:"dependencies"`
		}
		if xml.Unmarshal(b, &pom) == nil {
			if pom.Parent.ArtifactID != "" {
				out = append(out, pom.Parent.GroupID+":"+pom.Parent.ArtifactID)
			}
			for _, d := range pom.Dependencies.Dependency {
				out = append(out, d.GroupID+":"+d.ArtifactID)
			}
			// 依存が 1 つも無い pom でも「Java のアプリ」ではある
			if len(out) == 0 {
				out = append(out, "pom:"+pom.Packaging)
			}
		}
	}
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			// gradle は構文が広すぎるので go.mod と同じテキスト扱い。
			// 行ごとに残すのは、部分一致の対象を依存宣言の粒度に近づけるため
			for _, line := range strings.Split(string(b), "\n") {
				if t := strings.TrimSpace(line); t != "" {
					out = append(out, t)
				}
			}
		}
	}
	return out
}

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// depsOf は 1 ディレクトリの package.json / go.mod / compose から
// Framework / DBDriver / Wants を読む。
func depsOf(dir string) (framework, dbDriver string, wants Wants) {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		deps := map[string]bool{}
		if json.Unmarshal(b, &pkg) == nil {
			for k := range pkg.Dependencies {
				deps[k] = true
			}
			for k := range pkg.DevDependencies {
				deps[k] = true
			}
		}
		for _, fw := range nodeFrameworks {
			if deps[fw] {
				framework = strings.TrimPrefix(strings.Split(fw, "/")[0], "@")
				break
			}
		}
		if framework == "" && len(deps) > 0 {
			framework = "node"
		}
		for _, db := range nodeDBs {
			if deps[db] {
				dbDriver = db
				break
			}
		}
		for name, mark := range nodeWants {
			if deps[name] {
				mark(&wants)
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "composer.json")); err == nil {
		var pkg struct {
			Require    map[string]string `json:"require"`
			RequireDev map[string]string `json:"require-dev"`
		}
		req := map[string]bool{}
		if json.Unmarshal(b, &pkg) == nil {
			for k := range pkg.Require {
				req[k] = true
			}
			for k := range pkg.RequireDev {
				req[k] = true
			}
		}
		fw := ""
		for _, f := range phpFrameworks {
			if req[f[0]] {
				fw = f[1]
				break
			}
		}
		if fw == "" && len(req) > 0 {
			fw = "php"
		}
		if frameworkRank(fw) > frameworkRank(framework) {
			framework = fw
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if frameworkRank("go") > frameworkRank(framework) {
			framework = "go"
		}
		s := string(b)
		for _, db := range goDBs {
			if strings.Contains(s, db) {
				if dbDriver == "" {
					dbDriver = db
				}
				break
			}
		}
		for frag, mark := range goWants {
			if strings.Contains(s, frag) {
				mark(&wants)
			}
		}
	}
	// Maven / Gradle。coordinates(groupId:artifactId)の文字列に対して部分一致で
	// 引く。pom は XML(stdlib)で読み、gradle はテキスト走査(go.mod と同じ扱い)。
	if coords := javaCoordinates(dir); len(coords) > 0 {
		all := strings.Join(coords, "\n")
		fw := "java"
		for _, f := range javaFrameworks {
			if strings.Contains(all, f[0]) {
				fw = f[1]
				break
			}
		}
		if frameworkRank(fw) > frameworkRank(framework) {
			framework = fw
		}
		if dbDriver == "" {
			for _, db := range javaDBs {
				if strings.Contains(all, db) {
					dbDriver = db
					break
				}
			}
		}
		for frag, mark := range javaWants {
			if strings.Contains(all, frag) {
				mark(&wants)
			}
		}
	}
	// compose のサービスは傍証: DB 種別と、redis / opensearch の利用
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		if dbDriver == "" {
			if strings.Contains(s, "image: mysql") || strings.Contains(s, "image: mariadb") {
				dbDriver = "mysql (compose)"
			} else if strings.Contains(s, "image: postgres") {
				dbDriver = "postgres (compose)"
			}
		}
		if strings.Contains(s, "image: redis") || strings.Contains(s, "image: valkey") {
			wants.Redis = true
		}
		if strings.Contains(s, "opensearchproject/opensearch") || strings.Contains(s, "image: elasticsearch") ||
			strings.Contains(s, "docker.elastic.co") {
			wants.OpenSearch = true
		}
		break // 最初に見つかった compose だけ見る(ポート検出と同じ流儀)
	}
	return framework, dbDriver, wants
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var (
	exposeRe = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

	composeServiceHeadRe = regexp.MustCompile(`^  ([A-Za-z0-9_.-]+):\s*$`)
	composeServiceKeyRe  = regexp.MustCompile(`^    ([A-Za-z0-9_.-]+):\s*(.*)$`)
	composeListItemRe    = regexp.MustCompile(`^\s+-\s*(.+?)\s*$`)
)

// composeService は compose の 1 サービスのうち、ポート検出に要る分だけ。
type composeService struct {
	name   string
	image  string
	build  bool
	ports  []string
	expose []string
}

// parseComposeServices は services: 直下を読む。YAML パーサは持ち込まない
// (このパッケージは標準ライブラリだけで閉じる)。読めない形は黙って捨てる。
func parseComposeServices(s string) []composeService {
	if i := strings.Index(s, "\nservices:"); i >= 0 {
		s = s[i+1:]
	} else if !strings.HasPrefix(s, "services:") {
		return nil
	}
	var out []composeService
	cur := -1
	mode := ""
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") {
			if strings.HasPrefix(line, "services:") {
				continue
			}
			break // 次のトップレベルキー(volumes: など)
		}
		if m := composeServiceHeadRe.FindStringSubmatch(line); m != nil {
			out = append(out, composeService{name: m[1]})
			cur, mode = len(out)-1, ""
			continue
		}
		if cur < 0 {
			continue
		}
		if m := composeServiceKeyRe.FindStringSubmatch(line); m != nil {
			mode = ""
			switch m[1] {
			case "image":
				out[cur].image = strings.TrimSpace(m[2])
			case "build":
				out[cur].build = true
			case "ports", "expose":
				mode = m[1]
			}
			continue
		}
		if mode == "" {
			continue
		}
		if m := composeListItemRe.FindStringSubmatch(line); m != nil {
			if mode == "ports" {
				out[cur].ports = append(out[cur].ports, m[1])
			} else {
				out[cur].expose = append(out[cur].expose, m[1])
			}
		}
	}
	return out
}

// ポートの持ち主として当てにしない image(DB・キャッシュ・検索・エミュレータ等)。
var infraImageMarks = []string{
	"mysql", "mariadb", "postgres", "redis", "valkey", "opensearch", "elasticsearch",
	"kibana", "localstack", "traefik", "mailcatcher", "mailhog", "mailpit", "minio",
	"rabbitmq", "memcached", "dynamodb-local", "adminer", "selenium", "ftp",
}

func isInfraImage(image string) bool {
	l := strings.ToLower(image)
	for _, m := range infraImageMarks {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// containerPort は ports / expose の 1 エントリからコンテナ側のポートを取る。
// "8080:3000" は 3000、"9200" は 9200。変数展開など数値にならないものは捨てる。
func containerPort(entry string) string {
	e := strings.Trim(strings.TrimSpace(entry), `"'`)
	if i := strings.Index(e, "/"); i >= 0 {
		e = e[:i]
	}
	if i := strings.LastIndex(e, ":"); i >= 0 {
		e = e[i+1:]
	}
	if e == "" {
		return ""
	}
	for _, r := range e {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return e
}

// composePort は compose から listen ポートを拾う。アプリのサービス
// (build: がある > DB 等の既製 image でない)を先に見て、そのサービスの
// ports / expose の中だけを読む。ブロックの外まで走査すると、変数展開で
// 読めない行を飛び越えて別サービスのポート(DB や検索)を拾ってしまう。
func composePort(dir string) string {
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		svcs := parseComposeServices(string(b))
		for _, pick := range []func(composeService) bool{
			func(s composeService) bool { return s.build },
			func(s composeService) bool { return !isInfraImage(s.image) },
			func(composeService) bool { return true },
		} {
			for _, svc := range svcs {
				if !pick(svc) {
					continue
				}
				for _, entries := range [][]string{svc.ports, svc.expose} {
					for _, e := range entries {
						if p := containerPort(e); p != "" {
							return p
						}
					}
				}
			}
		}
	}
	return ""
}

// --- URL 構成の検出(kagerou#109 v1)-----------------------------------------

var (
	traefikHostRe = regexp.MustCompile("Host\\(`([^`]+)`\\)")
	serverNameRe  = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	apiURLEnvRe   = regexp.MustCompile(`(?m)[A-Z][A-Z0-9_]*(API|BACKEND)[A-Z0-9_]*URL\s*[:=]`)
)

// corsFiles は「CORS を自前で実装している」ことが名前から分かるファイル。
// 依存の CORS ミドルウェアを使わず手で書く構成は普通にあり(net/http に
// ヘッダを足すだけで済む)、依存だけ見ていると素通りする。
//
// 走査は **この名前のファイルだけ** に限る。全ソースを grep すると走査量が
// 跳ね上がり、「ファイルしか読まない・ベストエフォート」の釣り合いが崩れる。
var corsFiles = []string{
	"cors.go", "cors.ts", "cors.js", "cors.mjs", "cors.py", "cors.rb",
	"middleware/cors.go", "middleware/cors.ts", "middleware/cors.js",
}

// scanURLShape は設定ファイルから URL 構成の信号を拾う。
// コードのリテラル(URL など)は読まない。読むのは CORS ヘッダ名という
// 固定文字列だけで、これはノイズにならず信号が強い。
func scanURLShape(dir string, f *Facts) {
	// 依存の CORS ミドルウェア = クロスオリジン前提の傍証
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		if strings.Contains(string(b), `"cors"`) {
			f.crossHint = true
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		s := string(b)
		if strings.Contains(s, "rs/cors") || strings.Contains(s, "gin-contrib/cors") {
			f.crossHint = true
		}
	}
	// 手書きの CORS。名前で当たりを付けてから中身で確認する(cors.go という
	// 名前だけで決めると、CORS を「無効にする」コードまで cross と読んでしまう)
	if !f.crossHint {
		for _, name := range corsFiles {
			b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			if err == nil && strings.Contains(string(b), "Access-Control-Allow-Origin") {
				f.crossHint = true
				break
			}
		}
	}
	// vite の dev proxy = 開発時だけのパス分割。本番の形は何も語らない
	for _, name := range []string{"vite.config.ts", "vite.config.js", "vite.config.mjs"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if s := string(b); strings.Contains(s, "proxy") && strings.Contains(s, "/api") {
				f.devProxyHint = true
			}
		}
	}
	// next の rewrites = 本番でもパスで分ける意図
	for _, name := range []string{"next.config.js", "next.config.mjs", "next.config.ts"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if strings.Contains(string(b), "rewrites") {
				f.pathHint = true
			}
		}
	}
	// nginx / Caddy の server 定義 = リバプロで組んだマルチドメイン(実ホスト名つき)
	for _, name := range []string{"nginx.conf", "default.conf", "Caddyfile"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, m := range serverNameRe.FindAllStringSubmatch(string(b), -1) {
			for _, h := range strings.Fields(m[1]) {
				addHost(f, h)
			}
		}
	}
	// compose: traefik の Host ラベル(確定信号)と *_API_URL 系 env 名(傍証)
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		for _, m := range traefikHostRe.FindAllStringSubmatch(s, -1) {
			addHost(f, m[1])
		}
		if apiURLEnvRe.MatchString(s) {
			f.crossHint = true
		}
		break
	}
}

// addHost は参考表示用のホスト名を集める(ノイズ除外・重複排除・最大 4)。
func addHost(f *Facts, h string) {
	h = strings.TrimSpace(h)
	if h == "" || h == "_" || h == "localhost" || strings.HasPrefix(h, "127.") || len(f.Hosts) >= 4 {
		return
	}
	for _, e := range f.Hosts {
		if e == h {
			return
		}
	}
	f.Hosts = append(f.Hosts, h)
}

// dockerfilesIn は dir 直下の Dockerfile 一式を返す。"Dockerfile" を先頭に、
// 残り("Dockerfile.lambda" 等)を名前順で続ける。見つからなければ nil。
func dockerfilesIn(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var plain bool
	var rest []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		switch {
		case n == "Dockerfile":
			plain = true
		case strings.HasPrefix(n, "Dockerfile."):
			// .dockerignore 等は拾わない
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	if plain {
		return append([]string{"Dockerfile"}, rest...)
	}
	return rest
}

// --- ヘルスチェックのパス(#165)------------------------------------------
//
// readiness の既定は "/" だが、ルートが重い SSR では無駄に遅く、ルートが
// リダイレクトするアプリでは誤判定する。アプリが専用のパスを持っている事実は
// 複数の場所にファイルとして書かれているので、それを拾う。

// healthPathRe は URL か、パスだけの記述からパス部分を取る。
// クエリは落とす(readiness は素のパスを叩く)。
var healthPathRe = regexp.MustCompile(`https?://[^\s"']*?(/[A-Za-z0-9._~%!$&'()*+,;=:@/-]*)|(?:^|\s)(/[A-Za-z0-9._~%!$&'()*+,;=:@/-]+)`)

// lwaHealthRe は LWA の readiness パス(Dockerfile の ENV)。
var lwaHealthRe = regexp.MustCompile(`(?i)AWS_LWA_READINESS_CHECK_PATH[=\s]+["']?([^\s"']+)`)

// healthcheckPath は dir から ヘルスチェック用のパスを探す。
// 優先順は「kagerou と同じ土俵のもの」から:
//  1. LWA の readiness パス(Lambda で実際に使われる値)
//  2. Dockerfile の HEALTHCHECK
//  3. compose の healthcheck
//
// "/" しか出てこなければ空を返す(既定と同じなので言う価値がない)。
func healthcheckPath(dir string, dockerfiles []string) string {
	for _, name := range dockerfiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		src := string(b)
		if m := lwaHealthRe.FindStringSubmatch(src); len(m) > 1 {
			if p := cleanHealthPath(m[1]); p != "" {
				return p
			}
		}
		if p := healthPathIn(src, "HEALTHCHECK"); p != "" {
			return p
		}
	}
	for _, name := range composeFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if p := healthPathIn(string(b), "healthcheck"); p != "" {
			return p
		}
	}
	return ""
}

// healthPathIn は marker を含む行(と続く数行)からパスらしきものを拾う。
// compose の healthcheck は test: が次の行に来るので、少し先まで見る。
func healthPathIn(src, marker string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if !strings.Contains(l, marker) {
			continue
		}
		end := i + 4
		if end > len(lines) {
			end = len(lines)
		}
		for _, cand := range lines[i:end] {
			// 別のトップレベルキーに入ったら打ち切る(compose の取り違え防止)
			if cand != lines[i] && healthBlockEnded(cand, marker) {
				break
			}
			for _, m := range healthPathRe.FindAllStringSubmatch(cand, -1) {
				for _, g := range m[1:] {
					if p := cleanHealthPath(g); p != "" {
						return p
					}
				}
			}
		}
	}
	return ""
}

// healthBlockEnded は compose で healthcheck ブロックを抜けたかを見る。
// Dockerfile(marker が大文字)では行をまたがないので常に打ち切る。
func healthBlockEnded(line, marker string) bool {
	if marker == "HEALTHCHECK" {
		return !strings.HasSuffix(strings.TrimSpace(line), "\\")
	}
	t := strings.TrimSpace(line)
	return t != "" && !strings.HasPrefix(t, "-") && strings.HasSuffix(strings.SplitN(t, " ", 2)[0], ":") &&
		!strings.HasPrefix(t, "test:") && !strings.HasPrefix(t, "interval:") &&
		!strings.HasPrefix(t, "timeout:") && !strings.HasPrefix(t, "retries:")
}

// cleanHealthPath はクエリ・フラグメントを落とし、"/" だけなら空にする。
func cleanHealthPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimSuffix(p, "\\")
	p = strings.TrimRight(p, `"',`)
	if p == "" || p == "/" || !strings.HasPrefix(p, "/") {
		return ""
	}
	return p
}

// --- CI がイメージを公開しているか(#165)---------------------------------
//
// 既にイメージを作って公開しているリポジトリに対して、kagerou の雛形は
// `sam build` で作り直す前提で workflow を出す。二度手間になっていないか、
// 既存レジストリを使えないかは、導入時に判断すべき点なので事実として出す。

// imagePushMarkers は「イメージを push している」と読める痕跡。
// 実行するのは CI なので .github/workflows だけを見る(compose の build は別物)。
var imagePushMarkers = []struct {
	marker   string
	registry string
}{
	{"ghcr.io", "ghcr.io"},
	{"docker/build-push-action", ""},
	{"amazon-ecr-login", "ecr"},
	{"docker push", ""},
}

// scanWorkflows は .github/workflows/*.yml を読み、イメージ公開の痕跡を返す。
// registry は分かれば("ghcr.io" / "ecr")、分からなければ空。
func scanWorkflows(dir string) (pushes bool, registry string) {
	wf := filepath.Join(dir, ".github", "workflows")
	ents, err := os.ReadDir(wf)
	if err != nil {
		return false, ""
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || (!strings.HasSuffix(n, ".yml") && !strings.HasSuffix(n, ".yaml")) {
			continue
		}
		// kagerou 自身が出した workflow は「既存の CI」ではない
		if strings.HasPrefix(n, "kagerou-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(wf, n))
		if err != nil {
			continue
		}
		src := string(b)
		for _, m := range imagePushMarkers {
			if strings.Contains(src, m.marker) {
				pushes = true
				if registry == "" {
					registry = m.registry
				}
			}
		}
	}
	return pushes, registry
}
