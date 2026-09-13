package stack

// peer 連動の解決(#99)。「同じ環境名の env 同士が組む」を kagerou 側で解決し、
// 7 分かかる CREATE_FAILED(相手 Topic 不在で rollback)を up 前の即決に変える。

// ResolvePeer は相手プロジェクトの env を 同名 > fallback 名 の順で探す。
// ready な env だけを相手にする(creating/failed の相手に繋いでも壊れるだけ)。
// 見つかったら (env名, その URL, true)。どちらも居なければ (fallback, "", false)
// — 呼び出し側は警告した上で fallback 名のまま進める(相手が後から立つ運用もある)。
func ResolvePeer(infos []*Info, peerProject, name, fallback string) (env, url string, found bool) {
	pick := func(n string) *Info {
		for _, i := range infos {
			if i.Tags[TagProject] == peerProject && i.Tags[TagName] == n && i.State() == "ready" {
				return i
			}
		}
		return nil
	}
	if i := pick(name); i != nil {
		u, _ := i.EnvironmentURL()
		return name, u, true
	}
	if i := pick(fallback); i != nil {
		u, _ := i.EnvironmentURL()
		return fallback, u, true
	}
	return fallback, "", false
}
